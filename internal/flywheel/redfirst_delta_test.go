package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRedFirstDelta: a correction whose merged gates drop the gate that
// failed on the base tree before the first dispatch is refused with rule
// red-first before dispatch, since inspect could never pass it; a delta that
// keeps it, adds gates or declares none is not, nor is one over an attempt
// that already fails red-first (issue #825).
func TestRedFirstDelta(t *testing.T) {
	t.Parallel()
	const red = "exit 0"
	off := false
	cases := []struct {
		name    string
		delta   string
		probe   bool  // record a red pre-dispatch probe on red
		base    *bool // lint.red_first for the first dispatch
		final   *bool // lint.red_first for the correction
		refused bool
	}{
		{"gate replaced", "gate: exit 1\n\n# TASK: delta\n", true, nil, nil, true},
		{"gate kept and one added", "gate: " + red + "\ngate: go version\n\n# TASK: delta\n", true, nil, nil, false},
		{"no gate lines", "# TASK: delta\n", true, nil, nil, false},
		{"rule off", "gate: exit 1\n\n# TASK: delta\n", true, nil, &off, false},
		{"replaced attempt already fails", "gate: exit 1\n\n# TASK: delta\n", false, &off, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := refusedTask(t, "kind: fix\ngate: "+red+"\n\n# TASK x\n")
			setRedFirst := func(v *bool) {
				cfg := simConfig(fixturePath("clean.jsonl", t))
				cfg.Lint = &LintConfig{RedFirst: v}
				if err := WriteConfig(dir, cfg); err != nil {
					t.Fatalf("WriteConfig() error = %v", err)
				}
			}
			setRedFirst(tc.base)
			if tc.probe {
				if err := AppendEvent(dir, redFirstProbe("2026-09-26T00:01:00Z", red, 1)); err != nil {
					t.Fatalf("AppendEvent() error = %v", err)
				}
			}
			if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
				t.Fatalf("fresh Run() error = %v", err)
			}
			setRedFirst(tc.final)
			delta := filepath.Join(dir, "delta.txt")
			if err := os.WriteFile(delta, []byte(tc.delta), 0o644); err != nil {
				t.Fatal(err)
			}
			before, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			_, err = Run(dir, RunOptions{Task: "T1", DeltaPath: delta})
			var rf *RuleRefusal
			isRedFirst := errors.As(err, &rf) && rf.Rule == "red-first"
			if !tc.refused {
				if isRedFirst {
					t.Fatalf("Run(delta) error = %v, want no red-first refusal", err)
				}
				return
			}
			if !isRedFirst {
				t.Fatalf("Run(delta) error = %v, want a red-first refusal", err)
			}
			if !strings.Contains(rf.Fix, red) {
				t.Errorf("Fix = %q, want it to name the red-probed gate %q", rf.Fix, red)
			}
			wantRefusedAppended(t, dir, before, "T1", "red-first")
		})
	}
}
