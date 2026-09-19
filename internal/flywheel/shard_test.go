package flywheel

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestShardedLayoutAbsentIsLegacy(t *testing.T) {
	dir := t.TempDir()
	// No events/ dir created
	sharded, err := ShardedLayout(dir)
	if err != nil {
		t.Fatalf("ShardedLayout: %v", err)
	}
	if sharded {
		t.Fatal("expected false without events/ dir")
	}

	// Verify ReadEvents returns legacy behavior
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestShardedLayoutRegularFileErrors(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create a regular file at .flywheel/events
	eventsPath := filepath.Join(dotDir, "events")
	if err := os.WriteFile(eventsPath, []byte("test"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	sharded, err := ShardedLayout(dir)
	if err == nil {
		t.Fatal("expected error for regular file at events path")
	}
	if sharded {
		t.Fatal("expected sharded=false on error")
	}
}

func TestShardFileNameEscapes(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"L1", "L1.jsonl"},
		{"con", "@task-con.jsonl"},
		{"NUL.x", "@task-NUL.x.jsonl"},
		{"lpt9", "@task-lpt9.jsonl"},
		{"PRN", "@task-PRN.jsonl"},
		{"COM5", "@task-COM5.jsonl"},
		{"AUX", "@task-AUX.jsonl"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shardFileName(tt.input)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}

	// Test 101-byte id
	longTask := string(bytes.Repeat([]byte("a"), 101))
	got := shardFileName(longTask)
	if !bytes.HasPrefix([]byte(got), []byte("@task-h")) || !bytes.HasSuffix([]byte(got), []byte(".jsonl")) {
		t.Errorf("long task expected @task-h<hex>.jsonl, got %q", got)
	}
}

func TestShardOfRouting(t *testing.T) {
	tests := []struct {
		name     string
		event    Event
		expected string
	}{
		{"staffed", Event{Kind: "staffed", Task: "", Session: "s1"}, "@floor.jsonl"},
		{"lead_edit", Event{Kind: "lead_edit", Task: "", Session: "s1"}, "@floor.jsonl"},
		{"goal", Event{Kind: "goal", Task: ""}, "@floor.jsonl"},
		{"learning", Event{Kind: "learning", Task: ""}, "@floor.jsonl"},
		{"dismissed", Event{Kind: "dismissed", Task: ""}, "@floor.jsonl"},
		{"sharded", Event{Kind: "sharded", Task: ""}, "@floor.jsonl"},
		{"probed", Event{Kind: "probed", Task: ""}, "@floor.jsonl"},
		{"session_start", Event{Kind: "session_start", Session: "s1"}, "@session-s1.jsonl"},
		{"session_command", Event{Kind: "session_command", Session: "s1"}, "@session-s1.jsonl"},
		{"session_end", Event{Kind: "session_end", Session: "s1"}, "@session-s1.jsonl"},
		{"task T1", Event{Kind: "planned", Task: "T1"}, "T1.jsonl"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shardOf(tt.event)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSessionShardNameHashesUnsafe(t *testing.T) {
	// Safe name: taskOK and len <= 64
	safe := "s1"
	got := sessionShardName(safe)
	if got != "@session-s1.jsonl" {
		t.Errorf("safe session: got %q, want @session-s1.jsonl", got)
	}

	// Unsafe: contains /
	unsafe := "s/1"
	got = sessionShardName(unsafe)
	if !bytes.HasPrefix([]byte(got), []byte("@session-h")) {
		t.Errorf("unsafe session with /: got %q, should be @session-h...", got)
	}

	// Unsafe: longer than 64
	longSession := string(bytes.Repeat([]byte("a"), 65))
	got = sessionShardName(longSession)
	if !bytes.HasPrefix([]byte(got), []byte("@session-h")) {
		t.Errorf("long session: got %q, should be @session-h...", got)
	}
}

func TestLogFilesOfOrderAndFilter(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create legacy file
	legacyPath := filepath.Join(dotDir, "events.jsonl")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// Create shard files
	files := []string{"b.jsonl", "a.jsonl", "x.lock", "y.tmp"}
	for _, name := range files {
		path := filepath.Join(eventsDir, name)
		if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Create a subdirectory (should be ignored)
	subdir := filepath.Join(eventsDir, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	logfiles, err := logFilesOf(dir)
	if err != nil {
		t.Fatalf("logFilesOf: %v", err)
	}

	// Expect: legacy first, then a.jsonl, b.jsonl (sorted); x.lock and y.tmp ignored
	if len(logfiles) != 3 {
		t.Fatalf("expected 3 files, got %d", len(logfiles))
	}

	if logfiles[0].Rel != "events.jsonl" {
		t.Errorf("first file should be events.jsonl, got %s", logfiles[0].Rel)
	}
	if logfiles[1].Rel != "events/a.jsonl" {
		t.Errorf("second file should be events/a.jsonl, got %s", logfiles[1].Rel)
	}
	if logfiles[2].Rel != "events/b.jsonl" {
		t.Errorf("third file should be events/b.jsonl, got %s", logfiles[2].Rel)
	}
}

func TestMergeLogLegacyFirst(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Legacy event with later TS
	legacyPath := filepath.Join(dotDir, "events.jsonl")
	legacyContent := `{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"planned"}` + "\n"
	if err := os.WriteFile(legacyPath, []byte(legacyContent), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// Shard event with earlier TS
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	shardContent := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n"
	if err := os.WriteFile(shardPath, []byte(shardContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	events, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("readShardedEvents: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Legacy should come first despite later TS
	if events[0].TS != "2026-09-19T00:00:02Z" {
		t.Errorf("first event TS should be 2026-09-19T00:00:02Z, got %s", events[0].TS)
	}
}

func TestMergeLogByKeyThenName(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Two shards with events at same TS, should be sorted by filename
	shard1Path := filepath.Join(eventsDir, "b.jsonl")
	shard1Content := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n"
	if err := os.WriteFile(shard1Path, []byte(shard1Content), 0o644); err != nil {
		t.Fatalf("write shard b: %v", err)
	}

	shard2Path := filepath.Join(eventsDir, "a.jsonl")
	shard2Content := `{"ts":"2026-09-19T00:00:01Z","task":"T2","kind":"planned"}` + "\n"
	if err := os.WriteFile(shard2Path, []byte(shard2Content), 0o644); err != nil {
		t.Fatalf("write shard a: %v", err)
	}

	events, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("readShardedEvents: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// a.jsonl should come before b.jsonl
	if events[0].Task != "T2" {
		t.Errorf("first event task should be T2 (from a.jsonl), got %s", events[0].Task)
	}
	if events[1].Task != "T1" {
		t.Errorf("second event task should be T1 (from b.jsonl), got %s", events[1].Task)
	}
}

func TestMergeLogKeepsFileOrderWhenTSGoesBackwards(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Shard with TS going backwards - should keep file order
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	shardContent := `{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"planned"}` + "\n" +
		`{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"dispatched"}` + "\n"
	if err := os.WriteFile(shardPath, []byte(shardContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	events, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("readShardedEvents: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Should maintain file order: planned first, then dispatched
	if events[0].Kind != "planned" {
		t.Errorf("first event should be planned, got %s", events[0].Kind)
	}
	if events[1].Kind != "dispatched" {
		t.Errorf("second event should be dispatched, got %s", events[1].Kind)
	}
}

func TestMergeLogUnparseableTSStaysInPlace(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Shard with unparseable TS
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	shardContent := `{"ts":"invalid","task":"T1","kind":"planned"}` + "\n" +
		`{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"dispatched"}` + "\n"
	if err := os.WriteFile(shardPath, []byte(shardContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	events, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("readShardedEvents: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// Should maintain file order despite unparseable TS
	if events[0].Kind != "planned" {
		t.Errorf("first event should be planned, got %s", events[0].Kind)
	}
}

func TestMergeLogDeterministicUnderListingOrder(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create multiple shards
	shardFiles := map[string]string{
		"z.jsonl": `{"ts":"2026-09-19T00:00:01Z","task":"Z","kind":"planned"}` + "\n",
		"a.jsonl": `{"ts":"2026-09-19T00:00:01Z","task":"A","kind":"planned"}` + "\n",
		"m.jsonl": `{"ts":"2026-09-19T00:00:01Z","task":"M","kind":"planned"}` + "\n",
	}

	for name, content := range shardFiles {
		path := filepath.Join(eventsDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// Read twice and compare
	events1, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("first readShardedEvents: %v", err)
	}

	events2, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("second readShardedEvents: %v", err)
	}

	if !reflect.DeepEqual(events1, events2) {
		t.Fatal("merged events differ between reads (not deterministic)")
	}
}

func TestReadEventsShardedSkipsUnterminatedTailPerFile(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Shard with unterminated tail
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	shardContent := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n" +
		`{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"dispatched"}` // no newline
	if err := os.WriteFile(shardPath, []byte(shardContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	events, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("readShardedEvents: %v", err)
	}

	// Should have only 1 event (the one with newline)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].Kind != "planned" {
		t.Errorf("event should be planned, got %s", events[0].Kind)
	}

	// Now add newline and read again
	withNewline := shardContent + "\n"
	if err := os.WriteFile(shardPath, []byte(withNewline), 0o644); err != nil {
		t.Fatalf("rewrite shard: %v", err)
	}

	events, err = readShardedEvents(dir)
	if err != nil {
		t.Fatalf("readShardedEvents after newline: %v", err)
	}

	// Should have 2 events now
	if len(events) != 2 {
		t.Fatalf("expected 2 events after adding newline, got %d", len(events))
	}
}

func TestReadEventsShardedNamesFileOnMalformedLine(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Shard with malformed JSON
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	shardContent := `{malformed json}` + "\n"
	if err := os.WriteFile(shardPath, []byte(shardContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	_, err := readShardedEvents(dir)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}

	// Error should mention the file path
	if !bytes.Contains([]byte(err.Error()), []byte("events/T1.jsonl")) {
		t.Errorf("error should mention events/T1.jsonl, got: %v", err)
	}
}

func TestReadEventsShardedPerTaskSubsequenceIsFileOrder(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Shard with T1 events in specific order
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	shardContent := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n" +
		`{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"dispatched"}` + "\n" +
		`{"ts":"2026-09-19T00:00:03Z","task":"T1","kind":"finished"}` + "\n"
	if err := os.WriteFile(shardPath, []byte(shardContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	events, err := readShardedEvents(dir)
	if err != nil {
		t.Fatalf("readShardedEvents: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Check order: planned, dispatched, finished
	expectedKinds := []string{"planned", "dispatched", "finished"}
	for i, expected := range expectedKinds {
		if events[i].Kind != expected {
			t.Errorf("event %d kind: got %s, want %s", i, events[i].Kind, expected)
		}
	}
}

func TestLogReaderRefreshReadsOnlyAppended(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create initial shard
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	initialContent := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n"
	if err := os.WriteFile(shardPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	reader := newLogReader()
	_, err := reader.refresh(dir)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	initialBytes := reader.Bytes

	// Append one more line
	appendContent := `{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"dispatched"}` + "\n"
	f, err := os.OpenFile(shardPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(appendContent); err != nil {
		f.Close()
		t.Fatalf("append: %v", err)
	}
	f.Close()

	_, err = reader.refresh(dir)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	// Should have added exactly len(appendContent) bytes
	expectedBytes := initialBytes + int64(len(appendContent))
	if reader.Bytes != expectedBytes {
		t.Errorf("Bytes: got %d, want %d", reader.Bytes, expectedBytes)
	}
}

func TestLogReaderResetsOnShrinkAndBoundaryMismatch(t *testing.T) {
	dir := t.TempDir()
	dotDir := filepath.Join(dir, ".flywheel")
	eventsDir := filepath.Join(dotDir, "events")

	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create initial shard
	shardPath := filepath.Join(eventsDir, "T1.jsonl")
	initialContent := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n" +
		`{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"dispatched"}` + "\n"
	if err := os.WriteFile(shardPath, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	reader := newLogReader()
	_, err := reader.refresh(dir)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	initialEvents := len(reader.merged())

	// Truncate the file (shrink)
	truncContent := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n"
	if err := os.WriteFile(shardPath, []byte(truncContent), 0o644); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	_, err = reader.refresh(dir)
	if err != nil {
		t.Fatalf("refresh after shrink: %v", err)
	}

	finalEvents := len(reader.merged())

	// Should have reset and re-read, resulting in fewer events
	if finalEvents >= initialEvents {
		t.Errorf("events after shrink: got %d, want fewer than %d", finalEvents, initialEvents)
	}
}

func TestDeriveSameOverLegacyAndSharded(t *testing.T) {
	// Create legacy layout
	legacyDir := t.TempDir()
	legacyDotDir := filepath.Join(legacyDir, ".flywheel")
	if err := os.MkdirAll(legacyDotDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}

	legacyPath := filepath.Join(legacyDotDir, "events.jsonl")
	content := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"planned"}` + "\n" +
		`{"ts":"2026-09-19T00:00:02Z","task":"T1","kind":"dispatched"}` + "\n"
	if err := os.WriteFile(legacyPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// Create sharded layout with same events
	shardedDir := t.TempDir()
	shardedDotDir := filepath.Join(shardedDir, ".flywheel")
	shardedEventsDir := filepath.Join(shardedDotDir, "events")
	if err := os.MkdirAll(shardedEventsDir, 0o755); err != nil {
		t.Fatalf("mkdir sharded: %v", err)
	}

	shardPath := filepath.Join(shardedEventsDir, "T1.jsonl")
	if err := os.WriteFile(shardPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write shard: %v", err)
	}

	// Read both
	legacyEvents, err := ReadEvents(legacyDir)
	if err != nil {
		t.Fatalf("ReadEvents legacy: %v", err)
	}

	shardedEvents, err := ReadEvents(shardedDir)
	if err != nil {
		t.Fatalf("ReadEvents sharded: %v", err)
	}

	// Derive from both
	legacyState := Derive(legacyEvents)
	shardedState := Derive(shardedEvents)

	// Should be equal
	if !reflect.DeepEqual(legacyState, shardedState) {
		t.Fatal("Derive states differ between legacy and sharded layouts")
	}
}

func TestObserveLogIsMaxPerDir(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	t1, _ := time.Parse(time.RFC3339Nano, "2026-09-19T00:00:01Z")
	t2, _ := time.Parse(time.RFC3339Nano, "2026-09-19T00:00:02Z")

	// Observe t2 for dir1
	observeLog(dir1, t2)
	if observedLog(dir1) != t2 {
		t.Errorf("observedLog(dir1): got %v, want %v", observedLog(dir1), t2)
	}

	// Observe t1 for dir1 (earlier)
	observeLog(dir1, t1)
	if observedLog(dir1) != t2 {
		t.Errorf("observedLog(dir1) after earlier: got %v, want %v", observedLog(dir1), t2)
	}

	// Observe t1 for dir2 (independent)
	observeLog(dir2, t1)
	if observedLog(dir2) != t1 {
		t.Errorf("observedLog(dir2): got %v, want %v", observedLog(dir2), t1)
	}
	if observedLog(dir1) != t2 {
		t.Errorf("observedLog(dir1) unchanged: got %v, want %v", observedLog(dir1), t2)
	}
}

// TestLogReaderDetectsSameSizeRewriteWithPartialTail checks that a shard
// rewritten to the same size while it ends in an unterminated tail is read
// again (#347 review: off lags the size then).
func TestLogReaderDetectsSameSizeRewriteWithPartialTail(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, ".flywheel", "events")
	if err := os.MkdirAll(events, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shard := filepath.Join(events, "T1.jsonl")
	write := func(kind string, mtime time.Time) {
		t.Helper()
		body := `{"ts":"2026-09-19T00:00:01Z","task":"T1","kind":"` + kind + `"}` + "\n" + `{"partial`
		if err := os.WriteFile(shard, []byte(body), 0o644); err != nil {
			t.Fatalf("write shard: %v", err)
		}
		if err := os.Chtimes(shard, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	write("planned", time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC))
	r := newLogReader()
	if _, err := r.refresh(dir); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	write("blocked", time.Date(2026, 9, 19, 2, 0, 0, 0, time.UTC)) // "blocked" and "planned" have the same length
	if _, err := r.refresh(dir); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got := r.merged()
	if len(got) != 1 || got[0].Kind != "blocked" {
		t.Errorf("merged = %+v, want the rewritten blocked event", got)
	}
}
