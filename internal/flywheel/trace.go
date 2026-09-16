package flywheel

import (
	"fmt"
	"strings"
)

// TraceLine is one event in a session's trace: enough to print
// "<ts> <task|-> <kind> (detail)" without the caller touching the raw Event.
// Detail is already formatted (its distinctive fields joined by ", ") and
// empty when the event has none worth showing.
type TraceLine struct {
	TS     string
	Task   string
	Kind   string
	Detail string
}

// Trace returns, in log order, every event in dir's event log whose Session
// equals session. It is read-only: unlike Status or Stats it never calls
// Derive and it never writes — a trace exists purely to answer "what did this
// session do", across every task it touched. An unknown session simply
// yields no lines, not an error.
func Trace(dir, session string) ([]TraceLine, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, fmt.Errorf("trace %s: %w", dir, err)
	}
	var lines []TraceLine
	for _, e := range events {
		if session == "" || e.Session != session {
			continue
		}
		lines = append(lines, TraceLine{TS: e.TS, Task: e.Task, Kind: e.Kind, Detail: traceDetail(e)})
	}
	return lines, nil
}

// traceDetail renders an event's distinctive fields, in a fixed order, for
// one trace line's parenthetical: attempt, verdict and model when present;
// role for a staffed event's persona; note for a session_command event.
func traceDetail(e Event) string {
	var parts []string
	if e.Attempt != "" {
		parts = append(parts, "attempt="+e.Attempt)
	}
	if e.Verdict != "" {
		parts = append(parts, "verdict="+e.Verdict)
	}
	if e.Model != "" {
		parts = append(parts, "model="+e.Model)
	}
	if e.Kind == "staffed" && e.Persona != "" {
		parts = append(parts, "role="+e.Persona)
	}
	if e.Kind == "session_command" && e.Note != "" {
		parts = append(parts, "note="+e.Note)
	}
	return strings.Join(parts, ", ")
}
