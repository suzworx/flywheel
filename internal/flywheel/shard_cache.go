package flywheel

import (
	"path/filepath"
	"sync"
	"time"
)

// logCacheSize is how many repositories the read cache keeps.
const logCacheSize = 4

// cacheEntry holds a logReader and its merged slice for a repository.
type cacheEntry struct {
	reader *logReader
	merged []Event
}

// logCache is a process-level LRU cache of logReaders, one per repository.
var (
	logCacheMu sync.Mutex
	logCache   = make(map[string]*cacheEntry)
	logCacheOr []string // order for LRU eviction
)

// cachedShardedEvents is readShardedEvents' reader with a process-level cache: one
// logReader per repository (keyed by its absolute path, at most
// logCacheSize of them, least-recently-used evicted), so a repeated read
// costs one open and fstat per shard instead of decoding the whole log.
// Every call returns a fresh slice: callers sort and filter what they get.
// The legacy layout is never cached (files there are rewritten in place).
// It also reports the latest timestamp in the log, which the reader already
// tracks per file.
func cachedShardedEvents(dir string) ([]Event, time.Time, error) {
	logCacheMu.Lock()
	defer logCacheMu.Unlock()

	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	entry, ok := logCache[absDir]
	if ok {
		// Move to end for LRU
		newOr := make([]string, 0, len(logCacheOr))
		for _, d := range logCacheOr {
			if d != absDir {
				newOr = append(newOr, d)
			}
		}
		newOr = append(newOr, absDir)
		logCacheOr = newOr

		// Refresh the reader
		changed, err := entry.reader.refresh(dir)
		if err != nil {
			return nil, time.Time{}, err
		}
		if changed {
			entry.merged = entry.reader.merged()
		}
		return append([]Event{}, entry.merged...), entry.reader.maxKey(), nil
	}

	// Not in cache: create new reader
	reader := newLogReader()
	_, err = reader.refresh(dir)
	if err != nil {
		return nil, time.Time{}, err
	}

	merged := reader.merged()
	entry = &cacheEntry{
		reader: reader,
		merged: merged,
	}
	logCache[absDir] = entry
	logCacheOr = append(logCacheOr, absDir)

	// Evict if over capacity
	if len(logCache) > logCacheSize {
		evict := logCacheOr[0]
		logCacheOr = logCacheOr[1:]
		delete(logCache, evict)
	}

	return append([]Event{}, merged...), reader.maxKey(), nil
}
