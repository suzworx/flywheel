package flywheel

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeniedWritePaths(t *testing.T) {
	t.Parallel()
	wt := t.TempDir()
	outside := filepath.Join(filepath.Dir(wt), "other.go")
	got := deniedWritePaths(wt, []string{
		"Write " + filepath.Join(wt, "sub", "b.go"),
		"Edit sub/b.go",
		`MultiEdit sub\c.go`,
		"NotebookEdit n.ipynb",
		"Bash" + bashDenialSep + "node -e x",
		"Read " + filepath.Join(wt, "r.go"),
		"Write " + outside,
		"Edit a.go",
	})
	want := []string{"a.go", "n.ipynb", "sub/b.go", "sub/c.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deniedWritePaths() = %q, want %q", got, want)
	}
	if got := deniedWritePaths(wt, nil); got != nil {
		t.Errorf("deniedWritePaths(nil) = %q, want nil", got)
	}
}

// TestDeniedWriteInspect checks inspect refuses a pass (rule denied-write)
// when a path whose write was denied changed anyway, unless a lead claim
// covers it, and never for a denied path the tree did not change (#757).
func TestDeniedWriteInspect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		denied []string
		claim  bool
		want   bool
	}{
		{"routed around", []string{"a.go"}, false, true},
		{"lead claimed", []string{"a.go"}, true, false},
		{"unchanged path", []string{"b.go"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir, err := initTask(t, []string{"exit 0"})
			if err != nil {
				t.Fatalf("initTask() error = %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n\nvar routed = 1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			rc := 0
			if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1", RC: &rc, DeniedWrites: c.denied}); err != nil {
				t.Fatal(err)
			}
			if c.claim {
				if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "lead_edit", Session: "lead1", Owns: []string{"a.go"}, Baseline: map[string]string{"a.go": fileSHA(dir, "a.go")}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
				t.Fatalf("ValidateTask() error = %v", err)
			}
			err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
			if !c.want {
				if err != nil && refusalRule(t, err) == "denied-write" {
					t.Errorf("InspectTask() = %v, want no denied-write refusal", err)
				}
				return
			}
			if err == nil {
				t.Fatal("InspectTask() accepted a pass whose denied write changed anyway")
			}
			if got := refusalRule(t, err); got != "denied-write" || !strings.Contains(err.Error(), "flywheel claim-edit --paths a.go --session <you>") {
				t.Errorf("InspectTask() = %v (rule %q), want rule denied-write naming a.go and claim-edit", err, got)
			}
		})
	}
}

// TestDeniedWriteRunRecorded checks a run records the result line's denied
// writes on the finished event whatever the finish reason, Bash ignored.
func TestDeniedWriteRunRecorded(t *testing.T) {
	t.Parallel()
	session := "ses_test_deniedwrite_001"
	start := `{"type":"step_start","sessionID":"` + session + `","part":{"type":"step_start"}}` + "\n"
	denied := `{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","session_id":"` + session +
		`","permission_denials":[{"tool_name":"Write","tool_input":{"file_path":"x.json"}},{"tool_name":"Bash","tool_input":{"command":"node -e x"}}]}` + "\n"
	dir := setupTask(t)
	model := filepath.Join(t.TempDir(), "f.jsonl")
	if err := os.WriteFile(model, []byte(start+denied), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := WriteConfig(dir, simConfig(model)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if f := lastFinished(t, dir, "T1"); !reflect.DeepEqual(f.DeniedWrites, []string{"x.json"}) {
		t.Errorf("finished denied_writes = %q, want [x.json]", f.DeniedWrites)
	}
}
