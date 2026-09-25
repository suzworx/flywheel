package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testEvents returns n test events.
func testEvents(n int) []Event {
	var events []Event
	for i := 0; i < n; i++ {
		events = append(events, Event{
			TS:   time.Now().Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano),
			Task: "test",
			Kind: "planned",
		})
	}
	return events
}

// testEventsWithTask returns n test events with a given task.
func testEventsWithTask(task string, n int) []Event {
	var events []Event
	for i := 0; i < n; i++ {
		events = append(events, Event{
			TS:   time.Now().Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano),
			Task: task,
			Kind: "planned",
		})
	}
	return events
}

// writeTestLog writes test events to a file (creates .flywheel if needed).
func writeTestLog(dir string, filename string, events []Event) error {
	os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755)
	path := filepath.Join(dir, ".flywheel", filename)
	return writeEventsToFile(path, events)
}

// writeTestLogWithTask writes test events with task to a file.
func writeTestLogWithTask(dir string, filename string, events []Event) error {
	os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755)
	path := filepath.Join(dir, ".flywheel", filename)
	os.MkdirAll(filepath.Dir(path), 0o755)
	return writeEventsToFile(path, events)
}

// appendTestLog appends test events to a file.
func appendTestLog(dir string, filename string, events []Event) error {
	path := filepath.Join(dir, ".flywheel", filename)
	return appendEventsToFile(path, events)
}

// appendTestLogWithTask appends test events with task to a file.
func appendTestLogWithTask(dir string, filename string, events []Event) error {
	path := filepath.Join(dir, ".flywheel", filename)
	return appendEventsToFile(path, events)
}

// getLogBytes returns the total bytes of a log file.
func getLogBytes(dir string, filename string) int64 {
	path := filepath.Join(dir, ".flywheel", filename)
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// writeEventsToFile writes events to a file as JSONL.
func writeEventsToFile(path string, events []Event) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		f.Write(b)
		f.WriteString("\n")
	}
	return nil
}

// appendEventsToFile appends events to a file as JSONL.
func appendEventsToFile(path string, events []Event) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		f.Write(b)
		f.WriteString("\n")
	}
	return nil
}

// TestTailLogLegacyMatchesTailEvents tests that TailLog in a legacy repo
// returns the same events as TailEvents.
func TestTailLogLegacyMatchesTailEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeTestLog(dir, "events.jsonl", testEvents(3)); err != nil {
		t.Fatalf("writeTestLog: %v", err)
	}

	// TailEvents from the start
	events1, off1, err := TailEvents(dir, 0)
	if err != nil {
		t.Fatalf("TailEvents: %v", err)
	}
	if len(events1) != 3 {
		t.Fatalf("TailEvents returned %d events, want 3", len(events1))
	}

	// TailLog from the start
	events2, cur2, err := TailLog(dir, LogCursor{})
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(events2) != 3 {
		t.Fatalf("TailLog returned %d events, want 3", len(events2))
	}

	// Append one event
	if err := appendTestLog(dir, "events.jsonl", testEvents(1)); err != nil {
		t.Fatalf("appendTestLog: %v", err)
	}

	// TailEvents from previous offset
	events3, _, err := TailEvents(dir, off1)
	if err != nil {
		t.Fatalf("TailEvents (second): %v", err)
	}
	if len(events3) != 1 {
		t.Fatalf("TailEvents returned %d events, want 1", len(events3))
	}

	// TailLog from previous cursor
	events4, _, err := TailLog(dir, cur2)
	if err != nil {
		t.Fatalf("TailLog (second): %v", err)
	}
	if len(events4) != 1 {
		t.Fatalf("TailLog returned %d events, want 1", len(events4))
	}
}

// TestTailLogShardedReadsOnlyAppended tests that TailLog in a sharded repo
// returns only appended events on each tail.
func TestTailLogShardedReadsOnlyAppended(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	// Write initial events to T1 and T2
	if err := writeTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 2)); err != nil {
		t.Fatalf("writeTestLogWithTask: %v", err)
	}
	if err := writeTestLogWithTask(dir, "events/T2.jsonl", testEventsWithTask("T2", 2)); err != nil {
		t.Fatalf("writeTestLogWithTask: %v", err)
	}

	// First TailLog
	events1, cur1, err := TailLog(dir, LogCursor{})
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(events1) != 4 {
		t.Fatalf("TailLog returned %d events, want 4", len(events1))
	}

	// Append to T1 and T2
	if err := appendTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 1)); err != nil {
		t.Fatalf("appendTestLogWithTask: %v", err)
	}
	if err := appendTestLogWithTask(dir, "events/T2.jsonl", testEventsWithTask("T2", 1)); err != nil {
		t.Fatalf("appendTestLogWithTask: %v", err)
	}

	// Second TailLog
	events2, cur2, err := TailLog(dir, cur1)
	if err != nil {
		t.Fatalf("TailLog (second): %v", err)
	}
	if len(events2) != 2 {
		t.Fatalf("TailLog returned %d events, want 2", len(events2))
	}

	// Append once more to T1
	if err := appendTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 1)); err != nil {
		t.Fatalf("appendTestLogWithTask: %v", err)
	}

	// Third TailLog
	events3, _, err := TailLog(dir, cur2)
	if err != nil {
		t.Fatalf("TailLog (third): %v", err)
	}
	if len(events3) != 1 {
		t.Fatalf("TailLog returned %d events, want 1", len(events3))
	}
}

// TestTailLogNewShardAppears tests that TailLog picks up a new shard file.
func TestTailLogNewShardAppears(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	// Write to T1
	if err := writeTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 2)); err != nil {
		t.Fatalf("writeTestLogWithTask: %v", err)
	}

	// First TailLog
	events1, cur1, err := TailLog(dir, LogCursor{})
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(events1) != 2 {
		t.Fatalf("TailLog returned %d events, want 2", len(events1))
	}

	// Create T2 (new shard)
	if err := writeTestLogWithTask(dir, "events/T2.jsonl", testEventsWithTask("T2", 2)); err != nil {
		t.Fatalf("writeTestLogWithTask: %v", err)
	}

	// Second TailLog
	events2, _, err := TailLog(dir, cur1)
	if err != nil {
		t.Fatalf("TailLog (second): %v", err)
	}
	if len(events2) != 2 {
		t.Fatalf("TailLog returned %d events, want 2", len(events2))
	}
}

// TestTailLogCursorNotMutated tests that the input cursor is not mutated.
func TestTailLogCursorNotMutated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeTestLog(dir, "events.jsonl", testEvents(3)); err != nil {
		t.Fatalf("writeTestLog: %v", err)
	}

	cur := LogCursor{}

	// First TailLog
	events1, _, err := TailLog(dir, cur)
	if err != nil {
		t.Fatalf("TailLog: %v", err)
	}
	if len(events1) != 3 {
		t.Fatalf("TailLog returned %d events, want 3", len(events1))
	}

	// cur should still be zero
	if cur.r != nil {
		t.Fatalf("TailLog mutated the input cursor")
	}

	// Append and tail again with the same cursor
	if err := appendTestLog(dir, "events.jsonl", testEvents(1)); err != nil {
		t.Fatalf("appendTestLog: %v", err)
	}
	events2, _, err := TailLog(dir, cur)
	if err != nil {
		t.Fatalf("TailLog (second): %v", err)
	}
	if len(events2) != 4 {
		t.Fatalf("TailLog returned %d events, want 4", len(events2))
	}
}

// TestTailEventsShardedLayoutErrors tests that TailEvents errors in a sharded
// layout.
func TestTailEventsShardedLayoutErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	_, _, err := TailEvents(dir, 0)
	if err == nil || err.Error() != "sharded log: use TailLog" {
		t.Fatalf("TailEvents: want 'sharded log: use TailLog', got %v", err)
	}
}

// TestWatcherShardedEqualsReadEvents tests that a Watcher in a sharded repo
// accumulates the same events as ReadEvents.
func TestWatcherShardedEqualsReadEvents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	// Write initial events
	if err := writeTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 2)); err != nil {
		t.Fatalf("writeTestLogWithTask: %v", err)
	}

	// First refresh
	w := NewWatcher()
	if err := readEvents(dir, &w); err != nil {
		t.Fatalf("readEvents: %v", err)
	}

	allEvents, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	if len(w.events) != len(allEvents) || len(w.events) != 2 {
		t.Fatalf("Watcher has %d events, ReadEvents has %d, want both 2", len(w.events), len(allEvents))
	}

	// Append to T1 and create T2
	if err := appendTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 1)); err != nil {
		t.Fatalf("appendTestLogWithTask: %v", err)
	}
	if err := writeTestLogWithTask(dir, "events/T2.jsonl", testEventsWithTask("T2", 1)); err != nil {
		t.Fatalf("writeTestLogWithTask: %v", err)
	}

	// Second refresh
	if err := readEvents(dir, &w); err != nil {
		t.Fatalf("readEvents (second): %v", err)
	}

	allEvents, err = ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents (second): %v", err)
	}

	if len(w.events) != len(allEvents) || len(w.events) != 4 {
		t.Fatalf("Watcher has %d events, ReadEvents has %d, want both 4", len(w.events), len(allEvents))
	}

	for i, we := range w.events {
		if we.Task != allEvents[i].Task {
			t.Fatalf("Event %d: Watcher task %q, ReadEvents task %q", i, we.Task, allEvents[i].Task)
		}
	}
}

// TestWatcherShardedReadsOnlyAppendedBytes tests that EventsBytes grows only
// by bytes appended.
func TestWatcherShardedReadsOnlyAppendedBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	// Write initial events
	if err := writeTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 2)); err != nil {
		t.Fatalf("writeTestLogWithTask: %v", err)
	}
	bytes1 := getLogBytes(dir, "events/T1.jsonl")

	w := NewWatcher()
	if err := readEvents(dir, &w); err != nil {
		t.Fatalf("readEvents: %v", err)
	}

	if w.EventsBytes != bytes1 {
		t.Fatalf("EventsBytes is %d, want %d", w.EventsBytes, bytes1)
	}

	// Append to T1
	if err := appendTestLogWithTask(dir, "events/T1.jsonl", testEventsWithTask("T1", 1)); err != nil {
		t.Fatalf("appendTestLogWithTask: %v", err)
	}
	newBytes := getLogBytes(dir, "events/T1.jsonl")
	appended := newBytes - bytes1

	before := w.EventsBytes
	if err := readEvents(dir, &w); err != nil {
		t.Fatalf("readEvents (second): %v", err)
	}

	if w.EventsBytes != before+appended {
		t.Fatalf("EventsBytes grew by %d, want %d", w.EventsBytes-before, appended)
	}
}

// TestTailLogReReadsReplacedShard checks that a shard rewritten wholesale is
// re-emitted from the start instead of slicing past its events (the count a
// cursor remembers can exceed what the file now holds).
func TestTailLogReReadsReplacedShard(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := filepath.Join(dir, ".flywheel", "events")
	if err := os.MkdirAll(events, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shard := filepath.Join(events, "T1.jsonl")
	two := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned","brief":"b.txt"}` + "\n" +
		`{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"started","attempt":"r1","session":"s1"}` + "\n"
	if err := os.WriteFile(shard, []byte(two), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}
	got, cur, err := TailLog(dir, LogCursor{})
	if err != nil || len(got) != 2 {
		t.Fatalf("TailLog() = %d events, %v; want 2", len(got), err)
	}
	one := `{"ts":"2026-09-19T00:00:03Z","task":"T1","kind":"blocked"}` + "\n"
	if err := os.WriteFile(shard, []byte(one), 0o644); err != nil {
		t.Fatalf("rewrite shard: %v", err)
	}
	got, _, err = TailLog(dir, cur)
	if err != nil {
		t.Fatalf("TailLog() after the rewrite: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "blocked" {
		t.Errorf("TailLog() = %+v, want the rewritten file's single event", got)
	}
}

// TestTailLogReEmitsSameSizeReplacement checks that a shard replaced by the
// same number of bytes and events is still re-emitted: nothing in the offset
// or the count shows the replacement, only the file's generation (#350 review).
func TestTailLogReEmitsSameSizeReplacement(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := filepath.Join(dir, ".flywheel", "events")
	if err := os.MkdirAll(events, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shard := filepath.Join(events, "T1.jsonl")
	write := func(kind string, mtime time.Time) {
		t.Helper()
		body := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"` + kind + `"}` + "\n" +
			`{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"` + kind + `"}` + "\n"
		if err := os.WriteFile(shard, []byte(body), 0o644); err != nil {
			t.Fatalf("write shard: %v", err)
		}
		if err := os.Chtimes(shard, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	write("planned", time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC))
	got, cur, err := TailLog(dir, LogCursor{})
	if err != nil || len(got) != 2 {
		t.Fatalf("TailLog() = %d events, %v; want 2", len(got), err)
	}
	write("blocked", time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)) // same size, same count
	got, _, err = TailLog(dir, cur)
	if err != nil {
		t.Fatalf("TailLog() after the replacement: %v", err)
	}
	if len(got) != 2 || got[0].Kind != "blocked" {
		t.Errorf("TailLog() = %+v, want both replacement events", got)
	}
}
