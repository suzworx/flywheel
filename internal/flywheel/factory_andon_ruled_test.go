package flywheel

import (
	"slices"
	"testing"
	"time"
)

// TestAndonRuledUnits checks buildAndon drops the run-state entry of a unit
// the lead already ruled on or that left the line, keeps it for an unruled
// unit, and keeps a ruled unit's needs-owner entry (issue #860).
func TestAndonRuledUnits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		unit Unit
		want []string
	}{
		{"landed failed-dirty", Unit{Task: "T1", status: "landed", RunState: "failed-dirty"}, nil},
		{"passed failed", Unit{Task: "T1", status: "passed", RunState: "failed"}, nil},
		{"needs-correction capped", Unit{Task: "T1", status: "needs-correction", RunState: "capped"}, nil},
		{"rejected failed", Unit{Task: "T1", status: "rejected", RunState: "failed"}, nil},
		{"withdrawn silent", Unit{Task: "T1", status: "withdrawn", RunState: "silent"}, nil},
		{"finished failed-dirty", Unit{Task: "T1", status: "finished", RunState: "failed-dirty"}, []string{"failed-dirty"}},
		{"running stalled", Unit{Task: "T1", status: "running", RunState: "stalled"}, []string{"stalled"}},
		{"blocked blocked", Unit{Task: "T1", status: "blocked", RunState: "blocked"}, []string{"blocked"}},
		{"empty status failed", Unit{Task: "T1", RunState: "failed"}, []string{"failed"}},
		{"landed failed-dirty needs owner", Unit{Task: "T1", status: "landed", RunState: "failed-dirty", NeedsOwner: 1, Open: 1}, []string{"needs-owner (1)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, a := range buildAndon([]Unit{tc.unit}, nil, nil) {
				got = append(got, a.State)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("buildAndon(%+v) states = %q, want %q", tc.unit, got, tc.want)
			}
		})
	}
}

// TestAndonRuledLandedFromLedger checks a unit whose last attempt finished
// unclean with written files (failed-dirty) leaves the floor's andon once it
// is landed (issue #860).
func TestAndonRuledLandedFromLedger(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "old", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "old", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:01Z", Task: "old", Kind: "finished", Attempt: "r1", Reason: "failed", Wrote: []string{"a.go"}},
		{TS: "2026-09-15T00:00:02Z", Task: "old", Kind: "landed"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-10-20T00:00:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "old")
	if !ok {
		t.Fatal("unit old missing")
	}
	if u.RunState != "failed-dirty" {
		t.Errorf("run state = %q, want failed-dirty", u.RunState)
	}
	for _, a := range fl.Andon {
		if a.Task == "old" {
			t.Errorf("landed unit on the andon: %+v", fl.Andon)
		}
	}
}
