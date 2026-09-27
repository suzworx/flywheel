package flywheel

import (
	"errors"
	"strings"
	"testing"
)

// TestPreflightRefusal checks preflightRefusal with a fake runner: all pass
// gives nil; the second failing refuses with Rule preflight naming that
// command and its first output line, and the third never runs (issue #635).
// Not parallel: it swaps runPreflight.
func TestPreflightRefusal(t *testing.T) {
	orig := runPreflight
	t.Cleanup(func() { runPreflight = orig })
	var ran []string
	runPreflight = func(wd, cmd string) (int, int64, []byte, error) {
		ran = append(ran, cmd)
		if cmd == "check-budget" {
			return 2, 0, []byte("\n  balance 1.20 below 5.00  \nsecond line\n"), nil
		}
		return 0, 0, nil, nil
	}

	if r := preflightRefusal("d", []string{"a", "b"}); r != nil {
		t.Fatalf("preflightRefusal(all pass) = %v, want nil", r)
	}
	ran = nil
	r := preflightRefusal("d", []string{"a", "check-budget", "c"})
	if r == nil || r.Rule != "preflight" {
		t.Fatalf("preflightRefusal() = %v, want a preflight RuleRefusal", r)
	}
	for _, want := range []string{`"check-budget"`, "exited 2", "balance 1.20 below 5.00", "flywheel run <task>"} {
		if !strings.Contains(r.Fix, want) {
			t.Errorf("Fix = %q, want it to contain %q", r.Fix, want)
		}
	}
	if strings.Contains(r.Fix, "second line") {
		t.Errorf("Fix = %q, want only the first output line", r.Fix)
	}
	if want := []string{"a", "check-budget"}; strings.Join(ran, ",") != strings.Join(want, ",") {
		t.Errorf("ran %q, want %q (the third command must not run)", ran, want)
	}

	runPreflight = func(string, string) (int, int64, []byte, error) {
		return 0, 0, nil, errors.New("no shell")
	}
	if r := preflightRefusal("d", []string{"x"}); r == nil || !strings.Contains(r.Fix, "no shell") {
		t.Errorf("preflightRefusal(spawn error) = %v, want a refusal naming the error", r)
	}
}

// TestPreflightFirstOutputLineCap checks the quoted output line is cut to 200
// characters (issue #635).
func TestPreflightFirstOutputLineCap(t *testing.T) {
	t.Parallel()
	if got := firstOutputLine([]byte(strings.Repeat("x", 300))); len(got) != 200 {
		t.Errorf("len(firstOutputLine) = %d, want 200", len(got))
	}
	if got := firstOutputLine([]byte(" \n\t\n")); got != "" {
		t.Errorf("firstOutputLine(blank) = %q, want empty", got)
	}
}

// TestPreflightRunRefusal checks run refuses a brief whose preflight: command
// exits non-zero with a preflight RuleRefusal and no dispatched event, and
// dispatches when the command exits 0 (issue #635). Real commands: exit 0 and
// exit 3 work under bash, sh and cmd /C.
func TestPreflightRunRefusal(t *testing.T) {
	t.Parallel()
	dir := needsEnvTask(t, "preflight: exit 3\n\n# TASK x\n")
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T1"})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "preflight" || !strings.Contains(rf.Error(), "exit 3") {
		t.Fatalf("Run() error = %v, want a preflight RuleRefusal naming exit 3", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if hasEventKind(evs, "dispatched") {
		t.Fatal("dispatched event recorded despite the preflight refusal")
	}

	ok := needsEnvTask(t, "preflight: exit 0\n\n# TASK x\n")
	if err := WriteConfig(ok, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var out strings.Builder
	if _, err := Run(ok, RunOptions{Task: "T1", Progress: &out}); errors.As(err, &rf) && rf.Rule == "preflight" {
		t.Fatalf("Run() with preflight: exit 0 refused: %v", err)
	}
	if evs, _ = ReadEvents(ok); !hasEventKind(evs, "dispatched") {
		t.Fatal("no dispatched event recorded with a passing preflight")
	}
	if !strings.Contains(out.String(), "T1 preflight ok: exit 0") {
		t.Errorf("progress = %q, want a preflight ok line", out.String())
	}
}
