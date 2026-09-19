package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// EnableShards switches dir to the sharded log layout (issue #47), once,
// without copying or rewriting .flywheel/events.jsonl. Under dispatch.lock,
// then the feedback lock (acquireFeedbackLock), then events.lock:
//   - if .flywheel/events already is a directory: compare the latest seal's
//     sha256 in events/@floor.jsonl with the hash of the legacy file's
//     current last complete line (lastCompleteLineOf; "" when none); equal →
//     nothing to do (sealed false); different (a binary without shard
//     support appended to the legacy file) → append a new seal to @floor
//     through appendShardLines (sealed true);
//   - else build the seal Event{Kind: "sharded", Path: ".flywheel/events.jsonl",
//     SHA256: <legacy last-line hash or "">, Note: fmt.Sprintf("legacy log sealed at %d lines", n),
//     Prev: shardGenesis, TS: max(now, the legacy log's latest ts + 1ns) as RFC3339Nano},
//     write it (marshalEvent + "\n") to <tmp>/@floor.jsonl where <tmp> is
//     os.MkdirTemp(".flywheel", "events.tmp-*"), then os.Rename(<tmp>, .flywheel/events)
//     — the layout flips atomically, already sealed. On Windows a rename can
//     fail transiently while another process holds a handle: retry up to 20
//     times, 50 ms apart, before returning the error. Remove <tmp> on failure.
//
// legacyLines is the number of complete, non-blank lines in events.jsonl.
func EnableShards(dir string, now time.Time) (sealed bool, legacyLines int, err error) {
	// Take dispatch.lock first
	releaseDispatch, err := acquireDispatchLock(dir)
	if err != nil {
		return false, 0, err
	}
	defer releaseDispatch()

	// Then feedback.lock
	releaseFeedback, err := acquireFeedbackLock(dir)
	if err != nil {
		return false, 0, err
	}
	defer releaseFeedback()

	// Then events.lock
	releaseEvents, err := acquireRepoLock(dir, "events.lock", eventsLockTimings())
	if err != nil {
		return false, 0, err
	}
	defer releaseEvents()

	sharded, err := ShardedLayout(dir)
	if err != nil {
		return false, 0, err
	}

	legacyPath := filepath.Join(dir, ".flywheel", legacyLogName)

	if sharded {
		// Layout is already sharded. Check if we need to reseal.
		// Read the legacy file's last complete line hash
		legacyHash, err := lastLineHash(legacyPath)
		if err != nil {
			return false, 0, err
		}

		// Count complete lines in legacy file
		data, err := os.ReadFile(legacyPath)
		if err != nil {
			if os.IsNotExist(err) {
				legacyLines = 0
			} else {
				return false, 0, fmt.Errorf("read %s: %w", legacyPath, err)
			}
		} else {
			legacyLines = countCompleteLines(data)
		}

		// Get the last seal from @floor.jsonl
		floorPath := filepath.Join(dir, ".flywheel", shardDirName, floorShard)
		lastSeal, err := lastSealEvent(floorPath)
		if err != nil {
			return false, 0, err
		}

		// A seal that still matches: nothing to do. No seal at all (a layout
		// created by hand) is repaired rather than refused — the seal is what
		// verification needs, and this is the only command that writes one.
		if lastSeal != nil && lastSeal.SHA256 == legacyHash {
			return false, legacyLines, nil
		}

		// The legacy file grew after the seal, or there is none: seal it
		// again. The timestamp is left empty so appendShardLines stamps it
		// after @floor's own last line.
		seal := Event{
			Kind:   "sharded",
			Path:   ".flywheel/events.jsonl",
			SHA256: legacyHash,
			Note:   fmt.Sprintf("legacy log sealed at %d lines", legacyLines),
		}
		if err := appendShardLines(dir, floorShard, []Event{seal}, now); err != nil {
			return false, legacyLines, err
		}
		return true, legacyLines, nil
	}

	// Legacy layout. Migrate to sharded.
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, 0, fmt.Errorf("read %s: %w", legacyPath, err)
		}
		// No legacy file; count lines as 0
		legacyLines = 0
	} else {
		legacyLines = countCompleteLines(data)
	}

	// Get the hash of the last complete line
	legacyHash, err := lastLineHash(legacyPath)
	if err != nil {
		return false, 0, err
	}

	// Determine the seal's timestamp: max(now, latest legacy TS + 1ns)
	sealTime := computeSealTime(now, legacyLines, legacyPath)

	// Build the seal event
	seal := Event{
		Kind:   "sharded",
		Path:   ".flywheel/events.jsonl",
		SHA256: legacyHash,
		Prev:   shardGenesis,
		Note:   fmt.Sprintf("legacy log sealed at %d lines", legacyLines),
		TS:     sealTime.Format(time.RFC3339Nano),
	}

	// Create a temporary directory for atomicity
	tmpDir, err := os.MkdirTemp(filepath.Join(dir, ".flywheel"), "events.tmp-*")
	if err != nil {
		return false, 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	// Write the seal to tmpDir/@floor.jsonl
	floorPath := filepath.Join(tmpDir, floorShard)
	sealBytes, err := marshalEvent(seal)
	if err != nil {
		return false, 0, fmt.Errorf("marshal seal: %w", err)
	}
	if err := os.WriteFile(floorPath, append(sealBytes, '\n'), 0o644); err != nil {
		return false, 0, fmt.Errorf("write seal: %w", err)
	}

	// Atomically rename tmpDir to .flywheel/events, with retry on Windows
	targetPath := filepath.Join(dir, ".flywheel", shardDirName)
	if err := renameWithRetry(tmpDir, targetPath); err != nil {
		return false, 0, fmt.Errorf("rename %s to %s: %w", tmpDir, targetPath, err)
	}

	return true, legacyLines, nil
}

// countCompleteLines counts the number of complete, non-blank lines in data.
func countCompleteLines(data []byte) int {
	// Drop unterminated tail
	if len(data) > 0 && data[len(data)-1] != '\n' {
		// Find the last newline
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL == -1 {
			// No complete line at all
			return 0
		}
		data = data[:lastNL+1]
	}

	// Count non-blank lines
	count := 0
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		// Trim carriage return (Windows CRLF)
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(bytes.TrimSpace(line)) > 0 {
			count++
		}
	}
	return count
}

// computeSealTime returns the seal's timestamp: now, or the latest
// timestamp in the legacy log plus a nanosecond when that is later — over
// EVERY line, not only the last one, so no legacy event ever sorts after
// the seal.
func computeSealTime(now time.Time, legacyLines int, legacyPath string) time.Time {
	if legacyLines == 0 {
		return now
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return now
	}
	// Drop an unterminated tail.
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL == -1 {
			return now
		}
		data = data[:lastNL+1]
	}
	latest := time.Time{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil || e.TS == "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && ts.After(latest) {
			latest = ts
		}
	}
	if next := latest.Add(time.Nanosecond); !latest.IsZero() && next.After(now) {
		return next
	}
	return now
}

// lastSealEvent reads the last "sharded" kind event from path, or nil if not found.
func lastSealEvent(path string) (*Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Drop unterminated tail
	if len(data) > 0 && data[len(data)-1] != '\n' {
		lastNL := bytes.LastIndexByte(data, '\n')
		if lastNL == -1 {
			return nil, nil
		}
		data = data[:lastNL+1]
	}

	// Scan backwards for the last sharded event
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSuffix(lines[i], []byte("\r"))
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.Kind == "sharded" {
			return &e, nil
		}
	}
	return nil, nil
}

// renameWithRetry renames oldPath to newPath, retrying up to 20 times on Windows
// when the error looks like a lock contention (file is in use).
func renameWithRetry(oldPath, newPath string) error {
	// First attempt
	err := os.Rename(oldPath, newPath)
	if err == nil {
		return nil
	}

	// On Windows, retry if it looks like a transient lock issue
	if runtime.GOOS == "windows" {
		for i := 0; i < 19; i++ {
			time.Sleep(50 * time.Millisecond)
			if err := os.Rename(oldPath, newPath); err == nil {
				return nil
			}
		}
	}

	return err
}
