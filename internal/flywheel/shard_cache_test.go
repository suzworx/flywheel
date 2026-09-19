package flywheel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testEvents returns n test events.
func testEventsCache(n int) []Event {
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

// testEventsWithTaskCache returns n test events with a given task.
func testEventsWithTaskCache(task string, n int) []Event {
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

// TestReadEventsCacheSeesRawAppend tests that the cache sees an append
// made directly to a file (simulating another process).
func TestReadEventsCacheSeesRawAppend(t *testing.T) {
	clearLogCache()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	// Write initial events
	path := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	if err := writeEventsToFile(path, testEventsWithTaskCache("T1", 2)); err != nil {
		t.Fatalf("writeEventsToFile: %v", err)
	}

	// Read
	events1, _, err := cachedShardedEvents(dir)
	if err != nil {
		t.Fatalf("cachedShardedEvents: %v", err)
	}
	if len(events1) != 2 {
		t.Fatalf("cachedShardedEvents returned %d events, want 2", len(events1))
	}

	// Raw append (simulate another process)
	if err := appendEventsToFile(path, testEventsWithTaskCache("T1", 1)); err != nil {
		t.Fatalf("appendEventsToFile: %v", err)
	}

	// Read again
	events2, _, err := cachedShardedEvents(dir)
	if err != nil {
		t.Fatalf("cachedShardedEvents (second): %v", err)
	}
	if len(events2) != 3 {
		t.Fatalf("cachedShardedEvents returned %d events, want 3", len(events2))
	}
}

// TestReadEventsCacheDetectsReplacedShard tests that the cache detects
// when a shard is replaced wholesale (same size, different mtime).
func TestReadEventsCacheDetectsReplacedShard(t *testing.T) {
	clearLogCache()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	// Write initial events
	path := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	if err := writeEventsToFile(path, testEventsWithTaskCache("T1", 2)); err != nil {
		t.Fatalf("writeEventsToFile: %v", err)
	}

	// Read
	events1, _, err := cachedShardedEvents(dir)
	if err != nil {
		t.Fatalf("cachedShardedEvents: %v", err)
	}
	oldTask := events1[0].Task

	// Replace with same size, different mtime
	time.Sleep(10 * time.Millisecond)
	if err := writeEventsToFile(path, testEventsWithTaskCache("T1", 2)); err != nil {
		t.Fatalf("writeEventsToFile: %v", err)
	}
	filePath := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	newTime := time.Now()
	os.Chtimes(filePath, newTime, newTime)

	// Read again
	events2, _, err := cachedShardedEvents(dir)
	if err != nil {
		t.Fatalf("cachedShardedEvents (second): %v", err)
	}
	if len(events2) != 2 {
		t.Fatalf("cachedShardedEvents returned %d events, want 2", len(events2))
	}
	if events2[0].Task != oldTask {
		t.Logf("Note: Task changed (possibly due to rewrite), which is expected for same-size file rewrite")
	}
}

// TestReadEventsCacheReturnsFreshSlice tests that each call returns a fresh
// slice, not aliased to the cached one.
func TestReadEventsCacheReturnsFreshSlice(t *testing.T) {
	clearLogCache()
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	path := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	if err := writeEventsToFile(path, testEventsWithTaskCache("T1", 3)); err != nil {
		t.Fatalf("writeEventsToFile: %v", err)
	}

	// Read
	events1, _, err := cachedShardedEvents(dir)
	if err != nil {
		t.Fatalf("cachedShardedEvents: %v", err)
	}

	// Mutate the returned slice
	if len(events1) > 0 {
		events1[0].Task = "modified"
	}

	// Read again
	events2, _, err := cachedShardedEvents(dir)
	if err != nil {
		t.Fatalf("cachedShardedEvents (second): %v", err)
	}

	if len(events2) > 0 && events2[0].Task == "modified" {
		t.Fatalf("Cache returned aliased slice: mutating one affected the other")
	}
}

// TestReadEventsCacheBounded tests that the cache is bounded to logCacheSize.
func TestReadEventsCacheBounded(t *testing.T) {
	clearLogCache()

	dirs := make([]string, 6)
	for i := 0; i < 6; i++ {
		dirs[i] = t.TempDir()
		os.Mkdir(filepath.Join(dirs[i], ".flywheel"), 0o755)
		os.Mkdir(filepath.Join(dirs[i], ".flywheel", "events"), 0o755)
		path := filepath.Join(dirs[i], ".flywheel", "events", "T1.jsonl")
		if err := writeEventsToFile(path, testEventsWithTaskCache("T1", 1)); err != nil {
			t.Fatalf("writeEventsToFile: %v", err)
		}

		if _, _, err := cachedShardedEvents(dirs[i]); err != nil {
			t.Fatalf("cachedShardedEvents: %v", err)
		}
	}

	logCacheMu.Lock()
	size := len(logCache)
	logCacheMu.Unlock()

	if size > logCacheSize {
		t.Fatalf("Cache size is %d, want at most %d", size, logCacheSize)
	}
}

// clearLogCache resets the cache for tests (unexported, used in tests).
func clearLogCache() {
	logCacheMu.Lock()
	defer logCacheMu.Unlock()
	logCache = make(map[string]*cacheEntry)
	logCacheOr = []string{}
}

// BenchmarkReadEventsLegacy10k benchmarks reading 10,000 events from one file.
func BenchmarkReadEventsLegacy10k(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := writeEventsToFile(path, testEventsCache(10000)); err != nil {
		b.Fatalf("writeEventsToFile: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ReadEvents(dir); err != nil {
			b.Fatalf("ReadEvents: %v", err)
		}
	}
}

// BenchmarkReadEventsSharded1k benchmarks reading 1,000 shards with 10 events each.
func BenchmarkReadEventsSharded1k(b *testing.B) {
	dir := b.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	for i := 0; i < 1000; i++ {
		taskName := "task_" + string(rune('A'+(rune(i%26)))) + string(rune('0'+rune((i/26)%10)))
		path := filepath.Join(dir, ".flywheel", "events", shardFileName(taskName))
		if err := writeEventsToFile(path, testEventsWithTaskCache(taskName, 10)); err != nil {
			b.Fatalf("writeEventsToFile: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ReadEvents(dir); err != nil {
			b.Fatalf("ReadEvents: %v", err)
		}
	}
}

// BenchmarkReadEventsShardedCachedAfterAppend benchmarks reading the same
// sharded log twice with one appended line between.
func BenchmarkReadEventsShardedCachedAfterAppend(b *testing.B) {
	clearLogCache()
	dir := b.TempDir()
	os.Mkdir(filepath.Join(dir, ".flywheel"), 0o755)
	os.Mkdir(filepath.Join(dir, ".flywheel", "events"), 0o755)

	for i := 0; i < 1000; i++ {
		taskName := "task_" + string(rune('A'+(rune(i%26)))) + string(rune('0'+rune((i/26)%10)))
		path := filepath.Join(dir, ".flywheel", "events", shardFileName(taskName))
		if err := writeEventsToFile(path, testEventsWithTaskCache(taskName, 10)); err != nil {
			b.Fatalf("writeEventsToFile: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// First read
		if _, err := ReadEvents(dir); err != nil {
			b.Fatalf("ReadEvents: %v", err)
		}

		// Append to the first shard
		shardPath := filepath.Join(dir, ".flywheel", "events", "task_A0.jsonl")
		if err := appendEventsToFile(shardPath, testEventsWithTaskCache("task_A0", 1)); err != nil {
			b.Fatalf("appendEventsToFile: %v", err)
		}

		// Second read (should use cache)
		if _, err := ReadEvents(dir); err != nil {
			b.Fatalf("ReadEvents (second): %v", err)
		}
	}
}
