package flywheel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedFirstLintProblem(t *testing.T) {
	t.Parallel()
	green := []GateProbe{{N: 1, RC: 0}, {N: 2, RC: 0}}
	cases := []struct {
		name   string
		kind   string
		on     bool
		probes []GateProbe
		want   bool
	}{
		{"fix all green", "fix", true, green, true},
		{"fix one red", "fix", true, []GateProbe{{N: 1, RC: 0}, {N: 2, RC: 1}}, false},
		{"fix only 127", "fix", true, []GateProbe{{N: 1, RC: 127, CannotStart: true}}, true},
		{"fix only 126", "fix", true, []GateProbe{{N: 1, RC: 126, CannotStart: true}}, true},
		{"fix spawn error", "fix", true, []GateProbe{{N: 1, RC: 1, Err: errors.New("spawn"), CannotStart: true}}, true},
		{"feature all green", "feature", true, green, false},
		{"fix off", "fix", false, green, false},
		{"fix no probes", "fix", true, nil, false},
	}
	for _, c := range cases {
		got := RedFirstLintProblem(c.kind, c.on, c.probes)
		if c.want {
			if !strings.Contains(got, "kind: fix needs a gate that fails before the fix") ||
				!strings.Contains(got, "rule red-first") || !strings.Contains(got, "lint.red_first false") {
				t.Errorf("%s: RedFirstLintProblem() = %q, want the red-first problem", c.name, got)
			}
		} else if got != "" {
			t.Errorf("%s: RedFirstLintProblem() = %q, want \"\"", c.name, got)
		}
	}
}

// redFirstProbe is a gate_probed event for T1 at ts with command cmd and rc.
func redFirstProbe(ts, cmd string, rc int) Event {
	return Event{TS: ts, Task: "T1", Kind: "gate_probed", Gate: "1", Command: cmd, RC: &rc}
}

func TestRedFirstRefusal(t *testing.T) {
	t.Parallel()
	const gate = "go test ./x"
	fix := BriefHeader{Kind: "fix", Gates: []string{gate, "go vet ./..."}}
	dispatched := Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched"}
	cases := []struct {
		name   string
		events []Event
		header BriefHeader
		on     bool
		want   string // "" means nil; otherwise a substring of Fix
	}{
		{"red before dispatch", []Event{redFirstProbe("2026-09-12T01:00:00Z", gate, 1), dispatched}, fix, true, ""},
		{"red with no dispatch", []Event{redFirstProbe("2026-09-12T01:00:00Z", gate, 1)}, fix, true, ""},
		{"red only after dispatch", []Event{dispatched, redFirstProbe("2026-09-12T03:00:00Z", gate, 1)}, fix, true, "no gate probe recorded before dispatch"},
		{"red on a dropped gate", []Event{redFirstProbe("2026-09-12T01:00:00Z", "go test ./old", 1), dispatched}, fix, true, "every pre-dispatch probe"},
		{"newest green after red", []Event{redFirstProbe("2026-09-12T01:00:00Z", gate, 1), redFirstProbe("2026-09-12T01:30:00Z", gate, 0), dispatched}, fix, true, "every pre-dispatch probe"},
		{"only 127", []Event{redFirstProbe("2026-09-12T01:00:00Z", gate, 127), dispatched}, fix, true, "every pre-dispatch probe"},
		{"other task red", []Event{{TS: "2026-09-12T01:00:00Z", Task: "T2", Kind: "gate_probed", Command: gate, RC: new(1)}}, fix, true, "no gate probe recorded before dispatch"},
		{"no probes", nil, fix, true, "no gate probe recorded before dispatch"},
		{"feature", nil, BriefHeader{Kind: "feature", Gates: fix.Gates}, true, ""},
		{"off", nil, fix, false, ""},
	}
	for _, c := range cases {
		r := redFirstRefusal(c.events, "T1", c.header, c.on)
		if c.want == "" {
			if r != nil {
				t.Errorf("%s: redFirstRefusal() = %v, want nil", c.name, r)
			}
			continue
		}
		if r == nil {
			t.Errorf("%s: redFirstRefusal() = nil, want a red-first refusal", c.name)
			continue
		}
		if r.Rule != "red-first" || !strings.Contains(r.Fix, c.want) ||
			!strings.Contains(r.Fix, "flywheel lint <brief> --probe --task T1") || !strings.Contains(r.Fix, "lint.red_first false") {
			t.Errorf("%s: redFirstRefusal() = %s: %s, want rule red-first with %q", c.name, r.Rule, r.Fix, c.want)
		}
	}
}

func TestRedFirstInspect(t *testing.T) {
	t.Parallel()
	for _, red := range []bool{false, true} {
		dir, err := initTask(t, []string{"exit 0"})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		brief := "owns: a.go\nneeds: none\nkind: fix\ngate: exit 0\n\n# TASK: gauges\n"
		if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
			t.Fatalf("write brief: %v", err)
		}
		git(t, dir, []string{"commit", "-am", "kind fix"})
		if red {
			if err := AppendEvent(dir, redFirstProbe("2026-09-12T00:30:00Z", "exit 0", 1)); err != nil {
				t.Fatalf("AppendEvent() error = %v", err)
			}
		}
		logFinished(t, dir, "T1", "w1")
		if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
		if red {
			if err != nil {
				t.Errorf("InspectTask() with a red pre-dispatch probe error = %v, want nil", err)
			}
			continue
		}
		if err == nil {
			t.Fatal("InspectTask() accepted a kind: fix pass with no base probe")
		}
		if got := refusalRule(t, err); got != "red-first" {
			t.Errorf("rule = %q, want red-first (%v)", got, err)
		}
		if !strings.Contains(err.Error(), "no gate probe recorded before dispatch") {
			t.Errorf("error = %v, want the no-probe text", err)
		}
	}
}

func TestRedFirstConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	path := filepath.Join(dir, ".flywheel", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["lint"] = map[string]any{"red_first": false}
	if data, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	off, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	var raw Config
	if err := json.Unmarshal([]byte(`{"lint":{}}`), &raw); err != nil || !raw.LintRedFirst() {
		t.Errorf("LintRedFirst() with red_first absent = false (err %v), want true", err)
	}
	if off.LintRedFirst() {
		t.Error("LintRedFirst() = true with red_first false, want false")
	}
	if !(Config{}).LintRedFirst() {
		t.Error("LintRedFirst() = false with no lint config, want true")
	}
	if !(Config{Lint: &LintConfig{}}).LintRedFirst() {
		t.Error("LintRedFirst() = false with red_first unset, want true")
	}
}
