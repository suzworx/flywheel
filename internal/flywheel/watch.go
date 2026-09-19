package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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

	return fmt.Sprintf("%s %s %s", clock, task, explainLine(e))
}

// TailEvents reads the complete lines of .flywheel/events.jsonl from byte
// offset onward and returns their events and the offset just past the last
// complete line. An unterminated tail (a record still being appended) is left
// for the next call; a missing log returns no events and offset unchanged.
// Malformed complete lines are an error naming the offset.
func TailEvents(dir string, offset int64) ([]Event, int64, error) {
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, offset, nil
		}
		return nil, offset, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(b)) <= offset {
		return []Event{}, offset, nil
	}
	tail := b[offset:]
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
