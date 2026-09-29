package main

import (
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestStatusPrintRefused: a unit whose dispatch was refused gets one line
// naming its status and the refusing rule; no refused unit prints nothing
// (issue #651).
func TestStatusPrintRefused(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	printRefused(&out, []flywheel.TaskState{
		{ID: "T1", Status: "planned", Refused: "dispatch-lock: lock .flywheel/dispatch.lock is held by run o12 (pid 4242)"},
		{ID: "T2", Status: "finished"},
	})
	if want := "Refused: 1\n  T1 planned refused: dispatch-lock\n"; out.String() != want {
		t.Errorf("printRefused() = %q, want %q", out.String(), want)
	}
	out.Reset()
	printRefused(&out, []flywheel.TaskState{{ID: "T2", Status: "finished"}})
	if out.String() != "" {
		t.Errorf("printRefused(no refusals) = %q, want empty", out.String())
	}
}
