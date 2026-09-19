package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func TestClockForEmptyReturnsAdvancingClock(t *testing.T) {
	clock, err := clockFor("")
	if err != nil {
		t.Fatalf("clockFor(\"\"): unexpected error: %v", err)
	}
	first := clock()
	time.Sleep(2 * time.Millisecond)
	second := clock()
	if !second.After(first) {
		t.Fatalf("expected clock to advance, got %v then %v", first, second)
	}
}

func TestClockForRFC3339ReturnsFixedInstant(t *testing.T) {
	want := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	clock, err := clockFor("2026-09-13T09:00:00Z")
	if err != nil {
		t.Fatalf("clockFor(\"2026-09-13T09:00:00Z\"): unexpected error: %v", err)
	}
	if got := clock(); !got.Equal(want) {
		t.Fatalf("first reading: got %v, want %v", got, want)
	}
	if got := clock(); !got.Equal(want) {
		t.Fatalf("second reading: got %v, want %v", got, want)
	}
}

func TestClockForRejectsBadFlag(t *testing.T) {
	if _, err := clockFor("nope"); err == nil {
		t.Fatal("clockFor(\"nope\"): expected an error, got nil")
	}
}

// TestFactoryPipedRendersOnce checks that `flywheel factory` with stdout not
// a terminal renders the floor once and returns instead of starting a live
// loop that would hang an automated caller (#336 review: the interactive
// wiring briefly sent this case to the redraw loop).
func TestFactoryPipedRendersOnce(t *testing.T) {
	dir := t.TempDir()
	if _, err := flywheel.Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	stdout := os.Stdout
	os.Stdout = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		runFactory([]string{"--dir", dir})
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		os.Stdout = stdout
		t.Fatal("flywheel factory with a piped stdout did not return")
	}
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "flywheel factory") {
		t.Errorf("output = %q, want one rendered floor", out)
	}
}
