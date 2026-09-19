package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const maxClockSkew = time.Minute

// shardGenesis is the prev of every shard's first line.
var shardGenesis = lineHash([]byte("flywheel shard v1"))

// shardLockName is the repo lock of one shard: "locks/<base without .jsonl>.lock"
// (under .flywheel/locks/, never under .flywheel/events/ — acquireRepoLock's
// MkdirAll would otherwise create the events directory and flip the layout).
func shardLockName(base string) string {
	// Remove .jsonl suffix if present
	if len(base) > 6 && base[len(base)-6:] == ".jsonl" {
		base = base[:len(base)-6]
	}
	return filepath.Join("locks", base+".lock")
}

// nextStamp is the timestamp a sharded append gives events without one:
// the latest of now, shardLast+1ns and observed+1ns, ignoring a candidate
// more than maxClockSkew ahead of now (a bad clock or a hand-written future
// line must not drag every later stamp). Pure.
func nextStamp(now, shardLast, observed time.Time) time.Time {
	stamp := now
	limit := now.Add(maxClockSkew)
	for _, seen := range []time.Time{shardLast, observed} {
		if seen.IsZero() {
			continue
		}
		// Each candidate is judged on its own: one beyond the skew limit is
		// ignored without discarding a sane one (the shard's own last stamp
		// keeps stamps increasing within the shard).
		if c := seen.Add(time.Nanosecond); c.After(stamp) && !c.After(limit) {
			stamp = c
		}
	}
	return stamp
}

// appendSharded appends events to their shard. Every event must have the
// same shardOf, else an error before anything is written (a batch is atomic
// only within one file). Then appendShardLines.
func appendSharded(dir string, events []Event, now time.Time) error {
	if len(events) == 0 {
		return nil
	}

	// Verify all events belong to the same shard
	base := shardOf(events[0])
	for i := 1; i < len(events); i++ {
		if shardOf(events[i]) != base {
			return fmt.Errorf("batch mixes shards %q and %q (cross-shard batches not allowed)", base, shardOf(events[i]))
		}
	}

	return appendShardLines(dir, base, events, now)
}

// appendShardLines, under acquireRepoLock(dir, shardLockName(base),
// eventsLockTimings()): read the shard's last complete line
// (lastCompleteLineOf on .flywheel/events/<base>) and parse its ts (the
// shard's last TS; zero if none/unparseable); T := nextStamp(now, thatTS,
// observedLog(dir)); every event with an empty TS gets T formatted
// RFC3339Nano (explicit TS values are kept); chain prev: the first event's
// prev is lineHash(last line) or shardGenesis when the shard has no line,
// each next one the hash of the line before it; prefix a "\n" when the file
// ends in a torn byte (needsNewlinePrefix); ONE O_APPEND|O_CREATE write;
// close; then observeLog(dir, T) and release.
func appendShardLines(dir, base string, events []Event, now time.Time) error {
	release, err := acquireRepoLock(dir, shardLockName(base), eventsLockTimings())
	if err != nil {
		return err
	}
	defer release()

	// Read the shard's last complete line
	shardPath := filepath.Join(dir, ".flywheel", "events", base)
	lastLine, found, err := lastCompleteLineOf(shardPath)
	if err != nil {
		return err
	}

	// Parse the last line's TS to get the shard's last timestamp
	var shardLastTS time.Time
	if found {
		var lastRecord Event
		if err := json.Unmarshal(lastLine, &lastRecord); err == nil && lastRecord.TS != "" {
			if ts, err := time.Parse(time.RFC3339Nano, lastRecord.TS); err == nil {
				shardLastTS = ts
			}
		}
	}

	// Compute the timestamp for events without one
	T := nextStamp(now, shardLastTS, observedLog(dir))

	// Build the lines to append, setting TS and chaining prev
	var prev string
	if found {
		prev = lineHash(lastLine)
	} else {
		prev = shardGenesis
	}

	var lines [][]byte
	for i := range events {
		// Set TS if empty
		if events[i].TS == "" {
			events[i].TS = T.Format(time.RFC3339Nano)
		}
		// Set prev
		events[i].Prev = prev
		// Marshal the event
		line, err := marshalEvent(events[i])
		if err != nil {
			return err
		}
		lines = append(lines, line)
		// Next event's prev is this line's hash
		prev = lineHash(line)
	}

	// Check if we need a newline prefix
	prefix, err := needsNewlinePrefix(shardPath)
	if err != nil {
		return err
	}

	// Build the write buffer
	var buf bytes.Buffer
	if prefix {
		if _, err := buf.WriteString("\n"); err != nil {
			return fmt.Errorf("buffer: %w", err)
		}
	}
	for _, line := range lines {
		if _, err := buf.Write(line); err != nil {
			return fmt.Errorf("buffer: %w", err)
		}
		if _, err := buf.WriteString("\n"); err != nil {
			return fmt.Errorf("buffer: %w", err)
		}
	}

	// Ensure the directory exists
	eventsDir := filepath.Join(dir, ".flywheel", "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", eventsDir, err)
	}

	// One O_APPEND|O_CREATE write
	f, err := os.OpenFile(shardPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", shardPath, err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("append %s: %w", shardPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", shardPath, err)
	}

	// Observe the timestamp
	observeLog(dir, T)

	return nil
}
