package flywheel

import (
	"testing"
)

// redFirstLateEvents is T1's ledger with a green pre-dispatch probe of gate,
// a dispatched event on tree dTree, then late, a probe of gate with rc on
// tree pTree, appended after a finished event when afterFinished.
func redFirstLateEvents(gate, dTree, pTree string, rc int, afterFinished bool) []Event {
	late := redFirstProbe("2026-09-12T03:00:00Z", gate, rc)
	late.Tree = pTree
	events := []Event{
		redFirstProbe("2026-09-12T01:00:00Z", gate, 0),
		{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Tree: dTree},
	}
	if afterFinished {
		events = append(events, Event{TS: "2026-09-12T02:30:00Z", Task: "T1", Kind: "finished", Attempt: "r1"})
	}
	return append(events, late)
}

func TestRedFirstLateProbe(t *testing.T) {
	t.Parallel()
	const gate = "go test ./x"
	fix := BriefHeader{Kind: "fix", Gates: []string{gate, "go vet ./..."}}
	cases := []struct {
		name   string
		events []Event
		pass   bool
	}{
		{"matching tree before finished", redFirstLateEvents(gate, "tree1", "tree1", 1, false), true},
		{"different tree", redFirstLateEvents(gate, "tree1", "tree2", 1, false), false},
		{"empty probe tree", redFirstLateEvents(gate, "tree1", "", 1, false), false},
		{"empty dispatched tree", redFirstLateEvents(gate, "", "", 1, false), false},
		{"empty dispatched tree, probe tree set", redFirstLateEvents(gate, "", "tree1", 1, false), false},
		{"matching tree after finished", redFirstLateEvents(gate, "tree1", "tree1", 1, true), false},
	}
	for _, c := range cases {
		r := redFirstRefusal(c.events, "T1", fix, true)
		if c.pass && r != nil {
			t.Errorf("%s: redFirstRefusal() = %v, want nil", c.name, r)
		}
		if !c.pass && (r == nil || r.Rule != "red-first") {
			t.Errorf("%s: redFirstRefusal() = %v, want a red-first refusal", c.name, r)
		}
	}
}

func TestRedFirstLateNewestWins(t *testing.T) {
	t.Parallel()
	const gate = "go test ./x"
	fix := BriefHeader{Kind: "fix", Gates: []string{gate}}
	dispatched := Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Tree: "tree1"}
	red := []Event{redFirstProbe("2026-09-12T01:00:00Z", gate, 1), dispatched}
	if r := redFirstRefusal(red, "T1", fix, true); r != nil {
		t.Errorf("red pre-dispatch probe alone: redFirstRefusal() = %v, want nil", r)
	}
	green := redFirstProbe("2026-09-12T03:00:00Z", gate, 0)
	green.Tree = "tree1"
	if r := redFirstRefusal(append(red, green), "T1", fix, true); r == nil || r.Rule != "red-first" {
		t.Errorf("late green matching probe: redFirstRefusal() = %v, want a red-first refusal", r)
	}
}

func TestRedFirstLateDelta(t *testing.T) {
	t.Parallel()
	const gate = "go test ./x"
	before := BriefHeader{Kind: "fix", Gates: []string{gate, "go vet ./..."}}
	after := BriefHeader{Kind: "fix", Gates: []string{"go vet ./..."}}
	events := redFirstLateEvents(gate, "tree1", "tree1", 1, false)
	r := redFirstDeltaRefusal(events, "T1", before, after, true)
	if r == nil || r.Rule != "red-first" {
		t.Fatalf("redFirstDeltaRefusal() = %v, want a red-first refusal for dropping the late red gate", r)
	}
	if r := redFirstDeltaRefusal(events, "T1", before, before, true); r != nil {
		t.Errorf("redFirstDeltaRefusal() keeping the gate = %v, want nil", r)
	}
	other := redFirstLateEvents(gate, "tree1", "tree2", 1, false)
	if r := redFirstDeltaRefusal(other, "T1", before, after, true); r != nil {
		t.Errorf("redFirstDeltaRefusal() with a late probe on another tree = %v, want nil (before already fails)", r)
	}
}

func TestRecordGateProbesTree(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := treeHash(dir)
	if err != nil || want == "" {
		t.Fatalf("treeHash() = %q, %v", want, err)
	}
	probes := []GateProbe{{N: 1, RC: 1}}
	if err := RecordGateProbes(dir, "T1", []string{"exit 0"}, probes); err != nil {
		t.Fatalf("RecordGateProbes() error = %v", err)
	}
	if got := lastProbeTree(t, dir); got != want {
		t.Errorf("gate_probed tree = %q, want treeHash %q", got, want)
	}

	plain := t.TempDir()
	if _, err := Init(plain, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := RecordGateProbes(plain, "T1", []string{"exit 0"}, probes); err != nil {
		t.Fatalf("RecordGateProbes() on a non-git dir error = %v", err)
	}
	if got := lastProbeTree(t, plain); got != "" {
		t.Errorf("gate_probed tree on a non-git dir = %q, want empty", got)
	}
}

// lastProbeTree returns the Tree of dir's newest gate_probed event.
func lastProbeTree(t *testing.T, dir string) string {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "gate_probed" {
			return events[i].Tree
		}
	}
	t.Fatal("no gate_probed event")
	return ""
}
