package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	legacyLogName = "events.jsonl"
	shardDirName  = "events"
	floorShard    = "@floor.jsonl"
)

// ShardedLayout reports whether dir uses the sharded log: .flywheel/events
// is a directory. A regular file at that path is an error naming it.
func ShardedLayout(dir string) (bool, error) {
	eventsPath := filepath.Join(dir, ".flywheel", shardDirName)
	info, err := os.Stat(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", eventsPath, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory", eventsPath)
	}
	return true, nil
}

// shardFileName is the shard file of a task: "<task>.jsonl", except
// "@task-<task>.jsonl" when the part of task before its first '.' is a
// Windows device name (CON, PRN, AUX, NUL, COM0-COM9, LPT0-LPT9, any case),
// and "@task-h<first 32 hex of sha256(task)>.jsonl" when task is longer
// than 100 bytes.
func shardFileName(task string) string {
	if len(task) > 100 {
		h := sha256.Sum256([]byte(task))
		return "@task-h" + hex.EncodeToString(h[:16]) + ".jsonl"
	}

	// Get the part before the first '.'
	prefix := task
	if i := strings.IndexByte(task, '.'); i >= 0 {
		prefix = task[:i]
	}

	// Check if prefix is a reserved device name (case-insensitive)
	if windowsDeviceNames[strings.ToUpper(prefix)] {
		return "@task-" + task + ".jsonl"
	}

	return task + ".jsonl"
}

// windowsDeviceNames are the names Windows reserves for devices in every
// directory: a shard named after one would write to the device.
var windowsDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM0": true, "COM1": true, "COM2": true, "COM3": true, "COM4": true,
	"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT0": true, "LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
	"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sessionShardName is "@session-<s>.jsonl" when taskOK(s) and len(s) <= 64,
// else "@session-h<first 32 hex of sha256(s)>.jsonl".
func sessionShardName(session string) string {
	if taskOK(session) && len(session) <= 64 {
		return "@session-" + session + ".jsonl"
	}
	h := sha256.Sum256([]byte(session))
	return "@session-h" + hex.EncodeToString(h[:16]) + ".jsonl"
}

// shardOf is the shard file an event belongs to: floor-level kinds
// (floorLevel), learning, dismissed and sharded → floorShard; session_start,
// session_command, session_end → sessionShardName(e.Session); every other
// kind → shardFileName(e.Task).
func shardOf(e Event) string {
	// Check session kinds first before floorLevel (which also includes them)
	if e.Kind == "session_start" || e.Kind == "session_command" || e.Kind == "session_end" {
		return sessionShardName(e.Session)
	}
	if floorLevel(e.Kind) || e.Kind == "learning" || e.Kind == "dismissed" || e.Kind == "sharded" {
		return floorShard
	}
	return shardFileName(e.Task)
}

// logFile is one file of the log: Rel is "events.jsonl" or "events/<base>"
// (slash-separated), Path the file's path on disk.
type logFile struct {
	Rel  string
	Path string
}

// logFilesOf lists the log's files: the legacy .flywheel/events.jsonl first
// when it exists, then every regular *.jsonl file directly in
// .flywheel/events/, sorted by name; anything else there (sub-directories,
// *.lock, *.tmp, names not ending in .jsonl) is ignored.
func logFilesOf(dir string) ([]logFile, error) {
	var files []logFile

	// Legacy file first
	legacyPath := filepath.Join(dir, ".flywheel", legacyLogName)
	if info, err := os.Stat(legacyPath); err == nil && !info.IsDir() {
		files = append(files, logFile{Rel: "events.jsonl", Path: legacyPath})
	}

	// Shard files in .flywheel/events/
	eventsDir := filepath.Join(dir, ".flywheel", shardDirName)
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return files, nil
		}
		return nil, fmt.Errorf("read %s: %w", eventsDir, err)
	}

	var shardNames []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		shardNames = append(shardNames, entry.Name())
	}

	sort.Strings(shardNames)
	for _, name := range shardNames {
		rel := "events/" + name
		path := filepath.Join(eventsDir, name)
		files = append(files, logFile{Rel: rel, Path: path})
	}

	return files, nil
}

// fileState is what a logReader knows about one file.
type fileState struct {
	off    int64
	mtime  time.Time
	last   time.Time
	events []Event
	keys   []time.Time
}

// logReader reads the log incrementally, file by file.
type logReader struct {
	files map[string]*fileState
	Bytes int64
}

func newLogReader() *logReader {
	return &logReader{
		files: make(map[string]*fileState),
	}
}

// refresh reads what each file gained since the last refresh. Per file:
// open, fstat, and re-read it from zero when its size shrank, the byte at
// off-1 is not '\n', or its size is unchanged but its mtime changed; else
// read [off, size), keep only up to the last '\n' (an unterminated tail is
// still being appended), ParseEvents(…, false) it, append the events and
// extend keys with the running max ts (time.RFC3339Nano; a ts that does not
// parse leaves the running max as it was). Files that vanished are dropped.
// changed reports whether any file gained, lost or re-read anything. Errors
// name the file's Rel.
func (r *logReader) refresh(dir string) (changed bool, err error) {
	files, err := logFilesOf(dir)
	if err != nil {
		return false, err
	}

	// Track which files currently exist
	currentFiles := make(map[string]bool)
	for _, f := range files {
		currentFiles[f.Rel] = true
	}

	// Remove files that no longer exist
	for rel := range r.files {
		if !currentFiles[rel] {
			delete(r.files, rel)
			changed = true
		}
	}

	// Process each file
	for _, lf := range files {
		gained, err := r.refreshFile(lf)
		if err != nil {
			return changed, err
		}
		changed = changed || gained
	}

	return changed, nil
}

// refreshFile brings one file's state up to date with a single open: fstat
// the open file, re-read from zero on the reset rule (size shrank, the byte
// at off-1 is not '\n', or same size with a new mtime), then read only
// [off, size) — never the bytes already read, so a refresh costs what the
// file gained. A file gone since it was listed is dropped.
func (r *logReader) refreshFile(lf logFile) (changed bool, err error) {
	f, err := os.Open(lf.Path)
	if err != nil {
		if os.IsNotExist(err) {
			if _, ok := r.files[lf.Rel]; ok {
				delete(r.files, lf.Rel)
				return true, nil
			}
			return false, nil
		}
		return false, fmt.Errorf("open %s: %w", lf.Rel, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", lf.Rel, err)
	}
	size, mtime := info.Size(), info.ModTime()
	state, ok := r.files[lf.Rel]
	if !ok {
		state = &fileState{}
		r.files[lf.Rel] = state
	}

	reread := size < state.off || (state.off > 0 && size == state.off && !mtime.Equal(state.mtime))
	if !reread && state.off > 0 && size > state.off {
		var b [1]byte
		if _, err := f.ReadAt(b[:], state.off-1); err != nil {
			return false, fmt.Errorf("read %s: %w", lf.Rel, err)
		}
		reread = b[0] != '\n'
	}
	if reread {
		r.Bytes -= state.off
		*state = fileState{}
		changed = true
	}
	state.mtime = mtime
	if size <= state.off {
		return changed, nil
	}

	data := make([]byte, size-state.off)
	n, err := f.ReadAt(data, state.off)
	if err != nil && err != io.EOF {
		return changed, fmt.Errorf("read %s: %w", lf.Rel, err)
	}
	data = data[:n]
	// Only complete lines: an unterminated tail is still being appended.
	nl := bytes.LastIndexByte(data, '\n')
	if nl < 0 {
		return changed, nil
	}
	data = data[:nl+1]
	events, err := ParseEvents(bytes.NewReader(data), false)
	if err != nil {
		return changed, fmt.Errorf("%s: %w", lf.Rel, err)
	}
	for _, e := range events {
		if ts, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && ts.After(state.last) {
			state.last = ts
		}
		state.keys = append(state.keys, state.last)
		state.events = append(state.events, e)
	}
	state.off += int64(len(data))
	r.Bytes += int64(len(data))
	return true, nil
}

// merged is the merged order: the legacy file's events in file order, then
// the events of every other file sorted STABLY by (key, Rel, index in file).
func (r *logReader) merged() []Event {
	if len(r.files) == 0 {
		return []Event{}
	}

	// Separate legacy and shards
	var legacyEvents []Event
	var shardEntries []struct {
		rel    string
		state  *fileState
		events []Event
		keys   []time.Time
	}

	for rel, state := range r.files {
		if rel == "events.jsonl" {
			legacyEvents = append(legacyEvents, state.events...)
		} else {
			shardEntries = append(shardEntries, struct {
				rel    string
				state  *fileState
				events []Event
				keys   []time.Time
			}{rel, state, state.events, state.keys})
		}
	}

	// Sort shards by (key, Rel, index) using a stable sort
	type item struct {
		key   time.Time
		rel   string
		index int
		event Event
	}

	var items []item
	for _, s := range shardEntries {
		for i, e := range s.events {
			key := time.Time{}
			if i < len(s.keys) {
				key = s.keys[i]
			}
			items = append(items, item{key: key, rel: s.rel, index: i, event: e})
		}
	}

	// Stable sort by (key, rel, index)
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].key.Equal(items[j].key) {
			return items[i].key.Before(items[j].key)
		}
		if items[i].rel != items[j].rel {
			return items[i].rel < items[j].rel
		}
		return items[i].index < items[j].index
	})

	// Build result: legacy first, then sorted shards
	var result []Event
	result = append(result, legacyEvents...)
	for _, item := range items {
		result = append(result, item.event)
	}

	return result
}

// maxKey is the largest key over every file.
func (r *logReader) maxKey() time.Time {
	var max time.Time
	for _, state := range r.files {
		if state.last.After(max) {
			max = state.last
		}
	}
	return max
}

// observedLog per directory logical clock (protected by a mutex).
var (
	observedLogMu sync.Mutex
	observedLogs  = make(map[string]time.Time)
)

// observeLog raises the process-level logical clock of dir (keyed by
// filepath.Abs(dir), guarded by a mutex) to t; observedLog reads it (zero
// when never observed).
func observeLog(dir string, t time.Time) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	observedLogMu.Lock()
	defer observedLogMu.Unlock()

	if t.After(observedLogs[absDir]) {
		observedLogs[absDir] = t
	}
}

func observedLog(dir string) time.Time {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}

	observedLogMu.Lock()
	defer observedLogMu.Unlock()

	return observedLogs[absDir]
}

// readShardedEvents reads the whole sharded log in merged order:
// newLogReader().refresh(dir), observeLog(dir, maxKey()), merged().
func readShardedEvents(dir string) ([]Event, error) {
	reader := newLogReader()
	_, err := reader.refresh(dir)
	if err != nil {
		return nil, err
	}

	maxKey := reader.maxKey()
	observeLog(dir, maxKey)

	return reader.merged(), nil
}
