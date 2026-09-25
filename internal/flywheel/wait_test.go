package flywheel

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitDir returns a fresh target dir whose log holds evs.
func waitDir(t *testing.T, evs ...Event) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent(%s %s): %v", e.Task, e.Kind, err)
		}
	}
	return dir
}

// fakeClock is an injected now/sleep pair: sleep advances now and runs hook.
type fakeClock struct {
	t      time.Time
	sleeps int
	hook   func(n int)
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(d time.Duration) {
	c.t = c.t.Add(d)
	c.sleeps++
	if c.hook != nil {
		c.hook(c.sleeps)
	}
}

// TestWaitFor covers WaitFor (issue #393): finished tasks return at once, a
// finish appended between ticks is seen, a timeout is a *WaitTimeout, and an
// unclean reason gives clean=false.
func TestWaitFor(t *testing.T) {
	t.Parallel()
	disp := func(task, a string) Event { return Event{Task: task, Kind: "dispatched", Attempt: a} }
	fin := func(task, a, r string) Event { return Event{Task: task, Kind: "finished", Attempt: a, Reason: r} }

	t.Run("already finished", func(t *testing.T) {
		dir := waitDir(t, disp("T1", "r1"), fin("T1", "r1", "stop"), disp("T2", "r1"), fin("T2", "r1", "stop"))
		c := &fakeClock{t: time.Unix(0, 0)}
		var out bytes.Buffer
		clean, err := WaitFor(dir, []string{"T1", "T2"}, time.Minute, time.Second, c.now, c.sleep, &out)
		if err != nil || !clean || c.sleeps != 0 {
			t.Fatalf("WaitFor = %v, %v after %d sleeps; want clean at once", clean, err, c.sleeps)
		}
		if got := out.String(); got != "T1 r1 finished reason=stop\nT2 r1 finished reason=stop\n" {
			t.Errorf("output = %q", got)
		}
	})

	t.Run("appended between ticks", func(t *testing.T) {
		// T1's r1 finished before the wait; its r2 is running, so only r2's finish counts.
		dir := waitDir(t, disp("T1", "r1"), fin("T1", "r1", "error"), disp("T1", "r2"))
		c := &fakeClock{t: time.Unix(0, 0)}
		c.hook = func(n int) {
			if n == 2 {
				if err := AppendEvent(dir, fin("T1", "r2", "stop")); err != nil {
					t.Fatal(err)
				}
			}
		}
		var out bytes.Buffer
		clean, err := WaitFor(dir, []string{"T1"}, 0, time.Second, c.now, c.sleep, &out)
		if err != nil || !clean || c.sleeps != 2 {
			t.Fatalf("WaitFor = %v, %v after %d sleeps; want clean after 2", clean, err, c.sleeps)
		}
		if got := out.String(); got != "T1 r2 finished reason=stop\n" {
			t.Errorf("output = %q", got)
		}
	})

	t.Run("no attempt yet waits for the first", func(t *testing.T) {
		dir := waitDir(t, Event{Task: "T1", Kind: "planned", Brief: "b.txt"})
		c := &fakeClock{t: time.Unix(0, 0)}
		c.hook = func(n int) {
			if n == 1 {
				if err := AppendEvents(dir, []Event{disp("T1", "r1"), fin("T1", "r1", "stop")}); err != nil {
					t.Fatal(err)
				}
			}
		}
		clean, err := WaitFor(dir, []string{"T1"}, 0, time.Second, c.now, c.sleep, &bytes.Buffer{})
		if err != nil || !clean {
			t.Fatalf("WaitFor = %v, %v; want clean", clean, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		dir := waitDir(t, disp("T1", "r1"))
		c := &fakeClock{t: time.Unix(0, 0)}
		_, err := WaitFor(dir, []string{"T1"}, 5*time.Second, time.Second, c.now, c.sleep, &bytes.Buffer{})
		var wt *WaitTimeout
		if !errors.As(err, &wt) {
			t.Fatalf("err = %v, want *WaitTimeout", err)
		}
		if len(wt.Pending) != 1 || wt.Pending[0] != "T1" || !strings.Contains(err.Error(), "T1") {
			t.Errorf("timeout = %+v", wt)
		}
	})

	t.Run("unclean reason", func(t *testing.T) {
		dir := waitDir(t, disp("T1", "r1"), fin("T1", "r1", "stop"), disp("T2", "r1"), fin("T2", "r1", "stalled"))
		c := &fakeClock{t: time.Unix(0, 0)}
		clean, err := WaitFor(dir, []string{"T1", "T2"}, 0, time.Second, c.now, c.sleep, &bytes.Buffer{})
		if err != nil || clean {
			t.Fatalf("WaitFor = %v, %v; want clean=false, nil", clean, err)
		}
	})
}
