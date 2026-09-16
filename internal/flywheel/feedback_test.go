package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLearningsAssignsSequentialIDs(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:00Z", Task: "t1", Kind: "learning",
		Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "runs/t1.r1.jsonl", Ask: "repeat",
		Signals: []string{"slow", "terse"}}); err != nil {
		t.Fatalf("AppendEvent() learning 1 error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:01Z", Task: "t1", Kind: "learning",
		Severity: "P2", Title: "Cache", Observed: "misses", Evidence: "runs", Ask: "warm"}); err != nil {
		t.Fatalf("AppendEvent() learning 2 error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	views := Learnings(events)
	if len(views) != 2 {
		t.Fatalf("Learnings() = %d views, want 2", len(views))
	}
	if views[0].ID != "L-01" || views[0].Severity != "P1" || views[0].Title != "Terse" {
		t.Errorf("Learnings()[0] = %+v, want L-01 P1 Terse", views[0])
	}
	if views[1].ID != "L-02" || views[1].Severity != "P2" || views[1].Title != "Cache" {
		t.Errorf("Learnings()[1] = %+v, want L-02 P2 Cache", views[1])
	}
	if got := NextLearningID(events); got != "L-03" {
		t.Errorf("NextLearningID() = %q, want L-03", got)
	}
}

func TestLearningsDismissMarksWithoutRenumbering(t *testing.T) {
	dir := t.TempDir()
	for i, e := range []Event{
		{TS: "2026-09-16T00:00:00Z", Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"},
		{TS: "2026-09-16T00:00:01Z", Task: "t1", Kind: "learning", Severity: "P2", Title: "Cache", Observed: "misses", Evidence: "e2", Ask: "a2"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() learning %d error = %v", i, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:02Z", Task: "t1", Kind: "dismissed", ID: "L-01", Note: "fixed"}); err != nil {
		t.Fatalf("AppendEvent() dismissed error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	views := Learnings(events)
	if len(views) != 2 {
		t.Fatalf("Learnings() = %d views, want 2 (dismiss must not renumber)", len(views))
	}
	if !views[0].Dismissed || views[0].Reason != "fixed" {
		t.Errorf("Learnings()[0] = %+v, want dismissed with reason fixed", views[0])
	}
	if views[1].ID != "L-02" || views[1].Dismissed {
		t.Errorf("Learnings()[1] = %+v, want L-02 untouched", views[1])
	}
}

func TestLearningsDismissUnknownIDIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:00Z", Task: "t1", Kind: "learning",
		Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"}); err != nil {
		t.Fatalf("AppendEvent() learning error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:01Z", Task: "t1", Kind: "dismissed", ID: "L-09", Note: "x"}); err != nil {
		t.Fatalf("AppendEvent() dismissed error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	views := Learnings(events)
	if len(views) != 1 || views[0].Dismissed {
		t.Errorf("Learnings() = %+v, want the sole learning untouched by an unknown dismiss target", views)
	}
}

func TestWriteLearningsFile(t *testing.T) {
	dir := t.TempDir()
	views := []LearningView{
		{ID: "L-01", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "runs/t1.r1.jsonl",
			Ask: "repeat", Signals: []string{"slow", "terse"}, Dismissed: true, Reason: "fixed"},
		{ID: "L-02", Severity: "P2", Title: "Cache", Observed: "misses", Evidence: "runs", Ask: "warm"},
	}
	if err := WriteLearningsFile(dir, views); err != nil {
		t.Fatalf("WriteLearningsFile() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "learnings.md"))
	if err != nil {
		t.Fatalf("read learnings.md: %v", err)
	}
	want := "# Learnings\n" +
		"\n## L-01 — Terse\n" +
		"severity: P1\n" +
		"observed: slow\n" +
		"evidence: runs/t1.r1.jsonl\n" +
		"ask: repeat\n" +
		"signals: slow, terse\n" +
		"dismissed: fixed\n" +
		"\n## L-02 — Cache\n" +
		"severity: P2\n" +
		"observed: misses\n" +
		"evidence: runs\n" +
		"ask: warm\n"
	if string(got) != want {
		t.Errorf("learnings.md =\n%s\nwant\n%s", got, want)
	}
}
