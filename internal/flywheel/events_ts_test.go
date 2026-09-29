package flywheel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// frozen is a clock reading every append in these tests shares: the clock has
// not moved between them (issue #650).
var frozen = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

func mustTS(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatalf("parse ts %q: %v", s, err)
	}
	return ts
}

// replanInOneTick appends a finished T1 then a planned T1 at the same clock
// reading and checks the planned sorts after the finish.
func replanInOneTick(t *testing.T, dir string) {
	t.Helper()
	for _, e := range []Event{
		{Task: "T1", Kind: "planned", Brief: "b.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{Task: "T1", Kind: "finished", Attempt: "r1"},
		{Task: "T1", Kind: "planned", Brief: "b.txt"},
	} {
		if err := appendEvents(dir, []Event{e}, frozen); err != nil {
			t.Fatalf("appendEvents(%s): %v", e.Kind, err)
		}
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}
	for i := 1; i < len(events); i++ {
		a, b := mustTS(t, events[i-1].TS), mustTS(t, events[i].TS)
		if !b.After(a) {
			t.Errorf("event %d (%s) ts %s is not after event %d ts %s", i, events[i].Kind, events[i].TS, i-1, events[i-1].TS)
		}
	}
	if ts, _ := findTask(Derive(events), "T1"); ts.Status != "planned" {
		t.Errorf("T1 status = %q after a re-plan in the finish's clock tick, want planned", ts.Status)
	}
}

func TestAppendEventsMonotonicTSReplan(t *testing.T) {
	t.Parallel()
	replanInOneTick(t, t.TempDir())
}

func TestAppendEventsMonotonicTSShardReplan(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	replanInOneTick(t, dir)
}

func TestAppendEventsMonotonicTSBatchSharesInstant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := appendEvents(dir, []Event{{Task: "T1", Kind: "finished", Attempt: "r1"}}, frozen); err != nil {
		t.Fatalf("appendEvents finished: %v", err)
	}
	batch := []Event{
		{Task: "T1", Kind: "note", Note: "one"},
		{Task: "T1", Kind: "note", Note: "two"},
	}
	if err := appendEvents(dir, batch, frozen); err != nil {
		t.Fatalf("appendEvents batch: %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[1].TS != events[2].TS {
		t.Errorf("batch ts = %s, %s, want one shared instant", events[1].TS, events[2].TS)
	}
	if first, batchTS := mustTS(t, events[0].TS), mustTS(t, events[1].TS); !batchTS.After(first) {
		t.Errorf("batch ts %s is not after the previous line's ts %s", events[1].TS, events[0].TS)
	}
}

func TestAppendEventsMonotonicTSCallerTSKept(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := appendEvents(dir, []Event{{Task: "T1", Kind: "finished", Attempt: "r1"}}, frozen); err != nil {
		t.Fatalf("appendEvents finished: %v", err)
	}
	early := frozen.Add(-time.Hour).Format(time.RFC3339Nano)
	if err := appendEvents(dir, []Event{{TS: early, Task: "T1", Kind: "note", Note: "backdated"}}, frozen); err != nil {
		t.Fatalf("appendEvents note: %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 2 || events[1].TS != early {
		t.Fatalf("events = %+v, want the note's ts %s kept", events, early)
	}
}
