package flywheel

import (
	"errors"
	"testing"
)

// TestRedFirstDispatch: flywheel run refuses a kind: fix task's first dispatch
// with rule red-first unless a gate still in the brief failed on the base tree
// before dispatch, so a missed probe costs no paid attempt (issue #812).
func TestRedFirstDispatch(t *testing.T) {
	t.Parallel()
	const fix = "kind: fix\ngate: exit 0\n\n# TASK x\n"
	off := false
	cases := []struct {
		name     string
		brief    string
		probes   []Event
		redFirst *bool
		refused  bool
	}{
		{"no probe", fix, nil, nil, true},
		{"green probe only", fix, []Event{redFirstProbe("2026-09-26T00:01:00Z", "exit 0", 0)}, nil, true},
		{"red probe on a gate", fix, []Event{redFirstProbe("2026-09-26T00:01:00Z", "exit 0", 1)}, nil, false},
		{"rule off", fix, nil, &off, false},
		{"not kind fix", "gate: exit 0\n\n# TASK x\n", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := refusedTask(t, tc.brief)
			if tc.redFirst != nil {
				cfg := simConfig(fixturePath("clean.jsonl", t))
				cfg.Lint = &LintConfig{RedFirst: tc.redFirst}
				if err := WriteConfig(dir, cfg); err != nil {
					t.Fatalf("WriteConfig() error = %v", err)
				}
			}
			for _, e := range tc.probes {
				if err := AppendEvent(dir, e); err != nil {
					t.Fatalf("AppendEvent() error = %v", err)
				}
			}
			before, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			_, err = Run(dir, RunOptions{Task: "T1"})
			var rf *RuleRefusal
			isRedFirst := errors.As(err, &rf) && rf.Rule == "red-first"
			if !tc.refused {
				if isRedFirst {
					t.Fatalf("Run() error = %v, want no red-first refusal", err)
				}
				return
			}
			if !isRedFirst {
				t.Fatalf("Run() error = %v, want a red-first refusal", err)
			}
			if evs, _ := ReadEvents(dir); hasEventKind(evs, "dispatched") {
				t.Error("a dispatched event was recorded, want none")
			}
			wantRefusedAppended(t, dir, before, "T1", "red-first")
		})
	}
}
