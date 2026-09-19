package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
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

// TestFeedbackAddReadFailureRecordedNotLost checks that when the reread of
// the log fails after the learning event was appended, the learning is still
// durably in the log and the failure names the recorded id: the append is
// durable the moment it returns, so a later read failure must never invite a
// re-add either.
func TestFeedbackAddReadFailureRecordedNotLost(t *testing.T) {
	dir := t.TempDir()
	// A title larger than the log parser's 16 MiB line budget makes the
	// post-append reread fail ("token too long") while the append itself
	// succeeds, deterministically and on every platform.
	huge := strings.Repeat("x", 17*1024*1024)
	id, _, aerr, rerr := addLearning(dir, "t1", "P1", huge, "slow", "e1", "repeat", nil)
	if aerr != nil {
		t.Fatalf("addLearning() append error = %v, want the learning recorded", aerr)
	}
	if rerr == nil {
		t.Fatal("addLearning() succeeded, want a read failure on the reread")
	}
	if id != "L-01" {
		t.Errorf("addLearning() id = %q, want L-01", id)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(b), huge) {
		t.Error("learning not durably in the log: the appended line is missing")
	}
	msg := feedbackRenderFailure(id, rerr)
	for _, want := range []string{"was recorded", "L-01", "derived from the event log", "do not re-add it"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message %q does not contain %q", msg, want)
		}
	}
}

// TestPrintUntriaged checks the untriaged section of `flywheel feedback`:
// "none" when there are none; otherwise the count, one line per signal (the
// run file only when recorded) and a triage hint naming the first signal.
func TestPrintUntriaged(t *testing.T) {
	var none bytes.Buffer
	printUntriaged(&none, nil)
	if got := none.String(); got != "untriaged signals: none\n" {
		t.Errorf("printUntriaged(nil) = %q, want the none line", got)
	}
	var two bytes.Buffer
	printUntriaged(&two, []flywheel.SignalView{
		{Task: "T1", Attempt: "r1", Signal: "no-plan", Path: ".flywheel/runs/T1.r1.jsonl"},
		{Task: "T2", Attempt: "c1", Signal: "stalled"},
	})
	want := "untriaged signals: 2\n" +
		"  T1 r1 no-plan (.flywheel/runs/T1.r1.jsonl)\n" +
		"  T2 c1 stalled\n" +
		"  triage: flywheel feedback add --task T1 ... --signals no-plan\n"
	if got := two.String(); got != want {
		t.Errorf("printUntriaged() =\n%s\nwant\n%s", got, want)
	}
}
