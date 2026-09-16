package flywheel

import (
	"strings"
	"testing"
)

// TestTraceOrdersBySessionAcrossTasks matches the issue #62 acceptance
// fixture: lines come back in log order, a session's lines never leak into
// another session's trace, and a session_command's note is visible.
func TestTraceOrdersBySessionAcrossTasks(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Kind: "session_start", Session: "ses_1"},
		{TS: "2026-09-14T10:01:00Z", Task: "t1", Kind: "dispatched", Attempt: "r1", Session: "ses_1", Model: "m1"},
		{TS: "2026-09-14T10:02:00Z", Kind: "session_command", Session: "ses_1", Note: "flywheel inspect t1"},
		{TS: "2026-09-14T10:03:00Z", Task: "t2", Kind: "started", Session: "ses_2"},
		{TS: "2026-09-14T10:04:00Z", Kind: "session_end", Session: "ses_1"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent(%+v) error = %v", e, err)
		}
	}

	lines, err := Trace(dir, "ses_1")
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if len(lines) != 4 {
		t.Fatalf("Trace() = %d lines, want 4: %+v", len(lines), lines)
	}
	wantKinds := []string{"session_start", "dispatched", "session_command", "session_end"}
	for i, l := range lines {
		if l.Kind != wantKinds[i] {
			t.Errorf("lines[%d].Kind = %q, want %q", i, l.Kind, wantKinds[i])
		}
	}
	if lines[1].Task != "t1" || lines[1].Detail != "attempt=r1, model=m1" {
		t.Errorf("dispatched line = %+v, want task t1, detail 'attempt=r1, model=m1'", lines[1])
	}
	if !strings.Contains(lines[2].Detail, "flywheel inspect t1") {
		t.Errorf("session_command detail = %q, want it to contain the note", lines[2].Detail)
	}
	for _, l := range lines {
		if l.Task == "t2" || strings.Contains(l.Detail, "t2") {
			t.Errorf("ses_2's task t2 leaked into ses_1's trace: %+v", l)
		}
	}
}

// TestTraceUnknownSessionIsEmpty checks a session with no matching events
// yields an empty (not nil-error) result rather than failing.
func TestTraceUnknownSessionIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Kind: "session_start", Session: "ses_1"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	lines, err := Trace(dir, "ses_9")
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("Trace() = %d lines, want 0 for an unknown session", len(lines))
	}
}

// TestTraceShowsStaffedRole checks a staffed event's persona surfaces as its
// role in the trace detail.
func TestTraceShowsStaffedRole(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Kind: "staffed", Session: "ses_1", Persona: "lead"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	lines, err := Trace(dir, "ses_1")
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if len(lines) != 1 || lines[0].Detail != "role=lead" {
		t.Fatalf("Trace() = %+v, want one line with detail 'role=lead'", lines)
	}
}

// TestTraceEmptyLogIsEmpty checks a directory with no event log at all
// (ReadEvents' missing-file case) yields an empty result, not an error.
func TestTraceEmptyLogIsEmpty(t *testing.T) {
	dir := t.TempDir()
	lines, err := Trace(dir, "ses_1")
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if len(lines) != 0 {
		t.Errorf("Trace() = %d lines, want 0 for an empty log", len(lines))
	}
}
