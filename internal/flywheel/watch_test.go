package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWatchHumanLineFormats checks that HumanLine formats events correctly.
func TestWatchHumanLineFormats(t *testing.T) {
	t.Parallel()
	e := Event{
		TS:      "2026-09-18T10:00:00Z",
		Task:    "T1",
		Kind:    "dispatched",
		Attempt: "r1",
		Adapter: "claude",
		Model:   "m1",
	}
	line := HumanLine(e)
	if !containsAll(line, "T1", "dispatched", "r1", "to", "claude", "m1") {
		t.Errorf("HumanLine(%+v) = %q, want it to contain all of T1, dispatched, r1, to, claude, m1", e, line)
	}

	e2 := Event{
		TS:   "2026-09-18T10:00:00Z",
		Task: "",
		Kind: "staffed",
	}
	line2 := HumanLine(e2)
	if !contains(line2, " - ") {
		t.Errorf("HumanLine with empty task = %q, want it to contain ' - ' (task as dash)", line2)
	}
}

// TestWatchTailEventsReadsFromOffset checks that TailEvents reads complete lines from offset.
func TestWatchTailEventsReadsFromOffset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	e1 := Event{Task: "T1", Kind: "planned", Brief: "b1.txt"}
	if err := AppendEvent(dir, e1); err != nil {
		t.Fatalf("AppendEvent e1: %v", err)
	}

	e2 := Event{Task: "T2", Kind: "planned", Brief: "b2.txt"}
	if err := AppendEvent(dir, e2); err != nil {
		t.Fatalf("AppendEvent e2: %v", err)
	}

	events, off, err := TailEvents(dir, 0)
	if err != nil {
		t.Fatalf("TailEvents(0): %v", err)
	}
	if len(events) != 2 {
		t.Errorf("TailEvents(0) returned %d events, want 2", len(events))
	}

	e3 := Event{Task: "T3", Kind: "planned", Brief: "b3.txt"}
	if err := AppendEvent(dir, e3); err != nil {
		t.Fatalf("AppendEvent e3: %v", err)
	}

	newEvents, newOff, err := TailEvents(dir, off)
	if err != nil {
		t.Fatalf("TailEvents(%d): %v", off, err)
	}
	if len(newEvents) != 1 {
		t.Errorf("TailEvents(%d) returned %d events, want 1", off, len(newEvents))
	}
	if len(newEvents) > 0 && newEvents[0].Task != "T3" {
		t.Errorf("TailEvents(%d) returned event with task %q, want T3", off, newEvents[0].Task)
	}
	if newOff <= off {
		t.Errorf("TailEvents returned offset %d, want > %d", newOff, off)
	}
}

// TestWatchTailEventsLeavesPartialTail checks that TailEvents leaves unterminated lines.
func TestWatchTailEventsLeavesPartialTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	e1 := Event{Task: "T1", Kind: "planned", Brief: "b1.txt"}
	if err := AppendEvent(dir, e1); err != nil {
		t.Fatalf("AppendEvent e1: %v", err)
	}

	// Read the length of the first complete line
	events, off, err := TailEvents(dir, 0)
	if err != nil {
		t.Fatalf("TailEvents(0): %v", err)
	}
	if len(events) != 1 {
		t.Errorf("TailEvents(0) returned %d events, want 1", len(events))
	}

	firstOffset := off

	// Append a partial line (no newline)
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString("{\"task\":\"T2\",\"kind\":\"planned\",\"brief\":\"b2.txt\""); err != nil {
		f.Close()
		t.Fatalf("write partial: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Read again from offset 0
	events, off, err = TailEvents(dir, 0)
	if err != nil {
		t.Fatalf("TailEvents(0) after partial: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("TailEvents(0) returned %d events, want 1 (partial line not consumed)", len(events))
	}
	if off != firstOffset {
		t.Errorf("TailEvents offset = %d, want %d (partial line should not advance offset)", off, firstOffset)
	}
}

// TestWatchTailEventsMissingLog checks that TailEvents handles missing log.
func TestWatchTailEventsMissingLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	events, off, err := TailEvents(dir, 0)
	if err != nil {
		t.Errorf("TailEvents on missing log: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("TailEvents on missing log returned %d events, want 0", len(events))
	}
	if off != 0 {
		t.Errorf("TailEvents on missing log returned offset %d, want 0", off)
	}
}

// Helper functions

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestWatchHumanLineKeepsOneLine checks a note with line breaks never splits
// an event across lines (#305 review).
func TestWatchHumanLineKeepsOneLine(t *testing.T) {
	t.Parallel()
	e := Event{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "landed", Commit: "abc1234", Note: "merged\nmanually\r\nby hand"}
	if got := HumanLine(e); strings.ContainsAny(got, "\n\r") {
		t.Errorf("HumanLine() = %q, want a single line", got)
	}
}
