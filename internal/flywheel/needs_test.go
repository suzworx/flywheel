package flywheel

import (
	"testing"
	"time"
)

func TestNeedsTargetsDropsNone(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"none", []string{"none"}, nil},
		{"None uppercase", []string{"None"}, nil},
		{"dash", []string{"-"}, nil},
		{"empty", []string{""}, nil},
		{"NONE uppercase", []string{"NONE"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NeedTargets(tt.in...)
			if len(got) != len(tt.want) {
				t.Errorf("NeedTargets(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNeedsTargetsSplitsCommas(t *testing.T) {
	got := NeedTargets("T1, T2", "T3", "T1")
	want := []string{"T1", "T2", "T3"}
	if len(got) != len(want) {
		t.Errorf("NeedTargets = %v, want %v", got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NeedTargets[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNeedsHeaderNoneIsNoDependency(t *testing.T) {
	// Brief with needs: none should have Needs empty but NeedsDeclared true
	path := writeBrief(t, "owns: a.go\nneeds: none\ngate: true\n\n# TASK: x\n")
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(h.Needs) != 0 {
		t.Errorf("needs = %v, want empty", h.Needs)
	}
	if !h.NeedsDeclared {
		t.Errorf("NeedsDeclared = %v, want true", h.NeedsDeclared)
	}

	// Brief with no needs: line should have both empty
	path2 := writeBrief(t, "owns: a.go\ngate: true\n\n# TASK: x\n")
	h2, err := ParseBriefHeader(path2)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(h2.Needs) != 0 {
		t.Errorf("needs = %v, want empty", h2.Needs)
	}
	if h2.NeedsDeclared {
		t.Errorf("NeedsDeclared = %v, want false", h2.NeedsDeclared)
	}
}

func TestNeedsDeriveLegacyNone(t *testing.T) {
	// A planned event with Needs ["none"] should derive to task with no Needs
	events := []Event{{
		TS:    "2026-09-19T00:00:00Z",
		Task:  "T1",
		Kind:  "planned",
		Brief: "b.txt",
		Needs: []string{"none"},
	}}
	st := Derive(events)
	if len(st.Tasks) != 1 {
		t.Fatalf("Derive returned %d tasks, want 1", len(st.Tasks))
	}
	if len(st.Tasks[0].Needs) != 0 {
		t.Errorf("Needs = %v, want empty", st.Tasks[0].Needs)
	}
}

func TestNeedsReconcileDispatchesNone(t *testing.T) {
	// Same planned event with Needs ["none"]; Reconcile should dispatch it
	events := []Event{{
		TS:    "2026-09-19T00:00:00Z",
		Task:  "T1",
		Kind:  "planned",
		Brief: "b.txt",
		Needs: []string{"none"},
	}}
	st := Derive(events)
	actions := Reconcile(st, events, Observed{}, Policy{MaxParallel: 1}, time.Now())
	if len(actions) != 1 {
		t.Fatalf("Reconcile returned %d actions, want 1", len(actions))
	}
	if actions[0].Kind != "DISPATCH" {
		t.Errorf("action Kind = %q, want DISPATCH", actions[0].Kind)
	}
	if actions[0].Task != "T1" {
		t.Errorf("action Task = %q, want T1", actions[0].Task)
	}
}
