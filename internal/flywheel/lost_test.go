package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lostFixture initialises a factory with task T1 dispatched (attempt r1) at
// dispatchedAt, no lease, and returns the directory.
func lostFixture(t *testing.T, dispatchedAt time.Time) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	for _, e := range []Event{
		{TS: dispatchedAt.Add(-time.Minute).Format(time.RFC3339Nano), Task: "T1", Kind: "planned", Brief: "b1.md"},
		{TS: dispatchedAt.Format(time.RFC3339Nano), Task: "T1", Kind: "dispatched", Attempt: "r1"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() %s: %v", e.Kind, err)
		}
	}
	return dir
}

// lostEvents returns T1's lost events.
func lostEvents(t *testing.T, dir string) []Event {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var out []Event
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "lost" {
			out = append(out, e)
		}
	}
	return out
}

// TestNextMarksLost checks flywheel next's path (NextActions) records a lost
// event for an attempt idle past limits.lost_after, reports the MARK_LOST,
// and a second call appends nothing (issue #402).
func TestNextMarksLost(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	dir := lostFixture(t, at)
	acts, err := NextActions(dir, at.Add(72*time.Hour))
	if err != nil {
		t.Fatalf("NextActions() error = %v", err)
	}
	if len(acts) == 0 || acts[0].Kind != "MARK_LOST" || acts[0].Task != "T1" || acts[0].Reason != "idle" {
		t.Errorf("NextActions() = %+v, want MARK_LOST T1 (idle) first", acts)
	}
	lost := lostEvents(t, dir)
	if len(lost) != 1 || lost[0].Reason != "idle" || lost[0].Attempt != "r1" {
		t.Fatalf("lost events = %+v, want one idle lost for r1", lost)
	}
	acts, err = NextActions(dir, at.Add(73*time.Hour))
	if err != nil {
		t.Fatalf("NextActions() second error = %v", err)
	}
	for _, a := range acts {
		if a.Kind == "MARK_LOST" {
			t.Errorf("second NextActions() = %+v, want no MARK_LOST", acts)
		}
	}
	if n := len(lostEvents(t, dir)); n != 1 {
		t.Errorf("lost events after second call = %d, want 1", n)
	}
}

// TestMarkLostRunFile checks MarkLost reads the run file's mtime: a fresh run
// file keeps an old dispatch alive, an idle one past lost_after marks it lost
// with the run-file evidence, and a repeat call appends nothing.
func TestMarkLostRunFile(t *testing.T) {
	t.Parallel()
	start := now().UTC().Add(-72 * time.Hour)
	dir := lostFixture(t, start)
	runFile := filepath.Join(dir, ".flywheel", "runs", "T1.r1.jsonl")
	if err := os.MkdirAll(filepath.Dir(runFile), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	if err := os.WriteFile(runFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}
	if n, err := MarkLost(dir, now()); err != nil || n != 0 {
		t.Fatalf("MarkLost() with a fresh run file = %d, %v; want 0, nil", n, err)
	}
	idle := now().Add(-30 * time.Hour)
	if err := os.Chtimes(runFile, idle, idle); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	if n, err := MarkLost(dir, now()); err != nil || n != 1 {
		t.Fatalf("MarkLost() with an idle run file = %d, %v; want 1, nil", n, err)
	}
	lost := lostEvents(t, dir)
	if len(lost) != 1 || lost[0].Reason != "idle" || !strings.Contains(lost[0].Note, "run file idle since") {
		t.Errorf("lost events = %+v, want one idle lost naming the run file", lost)
	}
	if n, err := MarkLost(dir, now()); err != nil || n != 0 {
		t.Errorf("repeat MarkLost() = %d, %v; want 0, nil (idempotent)", n, err)
	}
}
