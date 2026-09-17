package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flywheel/internal/flywheel"
)

// TestFeedbackAddRenderFailureRecordedNotLost checks that when the artifact
// write refuses after the learning event was appended, the learning is still
// durably in the log, the failure names the recorded id, and the message
// never invites a re-add.
func TestFeedbackAddRenderFailureRecordedNotLost(t *testing.T) {
	dir := t.TempDir()
	dot := filepath.Join(dir, ".flywheel", "learnings.md")
	if err := os.MkdirAll(filepath.Dir(dot), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	curated := []byte("# Learnings\n\n## Hand written, precious\ndo not lose me\n")
	if err := os.WriteFile(dot, curated, 0o644); err != nil {
		t.Fatalf("write curated dot file: %v", err)
	}
	id, _, aerr, rerr := addLearning(dir, "t1", "P1", "Terse", "slow", "e1", "repeat", nil)
	if aerr != nil {
		t.Fatalf("addLearning() append error = %v, want the learning recorded", aerr)
	}
	if rerr == nil {
		t.Fatal("addLearning() succeeded, want a render failure on the curated dot file")
	}
	if id != "L-01" {
		t.Errorf("addLearning() id = %q, want L-01", id)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].Kind != "learning" || events[0].Title != "Terse" {
		t.Errorf("learning not durably recorded, events = %+v", events)
	}
	got, err := os.ReadFile(dot)
	if err != nil {
		t.Fatalf("read curated dot file: %v", err)
	}
	if string(got) != string(curated) {
		t.Errorf("curated dot file changed to %q, want %q untouched", got, curated)
	}
	msg := feedbackRenderFailure(id, rerr)
	for _, want := range []string{"was recorded", "L-01", ".flywheel/learnings.md", "derived from the event log", "do not re-add it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message %q does not contain %q", msg, want)
		}
	}
}
