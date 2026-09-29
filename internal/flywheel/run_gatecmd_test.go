package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// placeholderGate is placeholder text by its phrase, so it is refused on every
// host, even one with an assembler named as (issue #662).
const placeholderGate = "(as the brief)"

// wantGateCommandRefusal fails t unless run refuses with rule gate-command
// naming placeholderGate, appending only one dispatch_refused and no
// dispatched.
func wantGateCommandRefusal(t *testing.T, dir string, o RunOptions) {
	t.Helper()
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, o); !errors.As(err, &rf) || rf.Rule != "gate-command" || !strings.Contains(rf.Fix, `"`+placeholderGate+`" looks like placeholder text`) {
		t.Fatalf("Run() error = %v, want a gate-command refusal naming %q", err, placeholderGate)
	}
	wantRefusedAppended(t, dir, before, "T1", "gate-command")
}

// TestRunGateCommandRefusedFresh: a planned brief whose gate is placeholder
// text is refused before dispatched (issue #662).
func TestRunGateCommandRefusedFresh(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go\nneeds: none\ngate: "+placeholderGate+"\n\n# TASK x\n")
	wantGateCommandRefusal(t, dir, RunOptions{Task: "T1"})
}

// TestRunGateCommandDelta: over a valid base brief, a --delta with a
// placeholder gate is refused, and one declaring no gate: lines dispatches
// with the base brief's gates (issue #662).
func TestRunGateCommandDelta(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go\nneeds: none\ngate: go version\n\n# TASK x\n")
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("fresh Run() error = %v", err)
	}
	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("owns: a.go\nneeds: none\ngate: "+placeholderGate+"\n\n# TASK: delta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantGateCommandRefusal(t, dir, RunOptions{Task: "T1", DeltaPath: bad})
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(good, []byte("owns: a.go\nneeds: none\n\n# TASK: delta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: good}); err != nil {
		t.Fatalf("delta Run() without gates error = %v, want a dispatch", err)
	}
	if n := len(kindEvents(t, dir, "dispatched")); n != 2 {
		t.Errorf("dispatched events = %d, want 2 (fresh and the gate-less delta)", n)
	}
}
