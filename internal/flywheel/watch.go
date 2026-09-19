package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// oneLine folds line breaks inside an event's text into spaces.
var oneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

// LogCursor remembers how far a tail has read each file of the log. The
// zero value reads from the start.
type LogCursor struct{ r *logReader } // unexported field; never mutated by TailLog

// HumanLine is one event as a readable line: its local time (HH:MM:SS; the raw
// TS when it cannot be parsed), its task ("-" for floor-level events), and
// explainLine's summary.
func HumanLine(e Event) string {
	clock := "-"
	if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
		clock = t.Local().Format("15:04:05")
	} else {
		clock = e.TS
	}

	task := e.Task
	if task == "" {
		task = "-"
	}

	// One event, one physical line: a note or title with line breaks must not
	// split it (#305 review).
	return oneLine.Replace(fmt.Sprintf("%s %s %s", clock, task, explainLine(e)))
}

// TailEvents reads the complete lines of .flywheel/events.jsonl from byte
// offset onward and returns their events and the offset just past the last
// complete line. An unterminated tail (a record still being appended) is left
// for the next call; a missing log returns no events and offset unchanged.
// Malformed complete lines are an error naming the offset.
// In a sharded log, it returns an error directing the caller to TailLog.
func TailEvents(dir string, offset int64) ([]Event, int64, error) {
	sharded, err := ShardedLayout(dir)
	if err != nil {
		return nil, offset, err
	}
	if sharded {
		return nil, offset, fmt.Errorf("sharded log: use TailLog")
	}

	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	// Read only what was appended since offset, never the whole log again
	// (#305 review).
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, offset, nil
		}
		return nil, offset, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, fmt.Errorf("seek %s: %w", path, err)
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return nil, offset, fmt.Errorf("read %s: %w", path, err)
	}
	if len(tail) == 0 {
		return []Event{}, offset, nil
	}
	end := bytes.LastIndexByte(tail, '\n')
	if end < 0 {
		return []Event{}, offset, nil // only a record still being appended
	}
	events := []Event{}
	pos := offset
	for _, line := range bytes.Split(tail[:end], []byte("\n")) {
		if len(bytes.TrimSpace(line)) > 0 {
			var e Event
			if err := json.Unmarshal(line, &e); err != nil {
				return nil, offset, fmt.Errorf("malformed line at offset %d: %w", pos, err)
			}
			events = append(events, e)
		}
		pos += int64(len(line)) + 1
	}
	return events, offset + int64(end) + 1, nil
}

// TailLog returns the events the log gained since cur, merged among
// themselves in the log's order, and the cursor to pass next time. It works
// in both layouts: legacy reads one file, sharded reads every shard. The
// returned cursor is a new value; cur is left untouched, so an unchanged
// cursor can be reused after an error. A file replaced wholesale (shrunk,
// or rewritten to the same size) is re-read from the start and its events
// are returned again.
func TailLog(dir string, cur LogCursor) ([]Event, LogCursor, error) {
	prev := cur.r
	next := newLogReader()
	if prev != nil {
		next = prev.clone()
	}

	changed, err := next.refresh(dir)
	if err != nil {
		return nil, cur, err
	}

	var events []Event
	if changed {
		events = next.mergedSince(prev)
	}

	return events, LogCursor{r: next}, nil
}
