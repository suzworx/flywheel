package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func TestClockForEmptyReturnsAdvancingClock(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	if _, err := clockFor("nope"); err == nil {
		t.Fatal("clockFor(\"nope\"): expected an error, got nil")
	}
}

// factoryHelperEnv carries runFactory's arguments to the re-executed test
// binary, separated by \x1f.
const factoryHelperEnv = "FLYWHEEL_TEST_RUNFACTORY_ARGS"

// TestFactoryCtxFlag checks --ctx (issue #585 f4): `factory --once --ctx
// beta` renders beta's ledger, and an unknown name exits 2 naming the known
// ones. Each run is a child with its own FLYWHEEL_FLEET.
func TestFactoryCtxFlag(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(factoryHelperEnv); ok {
		runFactory(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	var fleet flywheel.Fleet
	for _, r := range []struct{ name, task string }{{"alpha", "A1"}, {"beta", "B1"}} {
		dir := t.TempDir()
		if _, err := flywheel.Init(dir, false); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if err := flywheel.AppendEvent(dir, flywheel.Event{TS: "2026-09-27T12:00:00Z", Task: r.task, Kind: "planned"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		fleet.Roots = append(fleet.Roots, flywheel.FleetRoot{Name: r.name, Path: dir})
	}
	file := filepath.Join(t.TempDir(), "fleet.json")
	if err := flywheel.SaveFleet(file, fleet); err != nil {
		t.Fatalf("SaveFleet: %v", err)
	}
	run := func(args ...string) (string, string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFactoryCtxFlag$")
		cmd.Env = append(os.Environ(), factoryHelperEnv+"="+strings.Join(args, "\x1f"), "FLYWHEEL_FLEET="+file)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var e *exec.ExitError
		if errors.As(err, &e) {
			return stdout.String(), stderr.String(), e.ExitCode()
		}
		if err != nil {
			t.Fatalf("run child: %v", err)
		}
		return stdout.String(), stderr.String(), 0
	}
	out, errOut, code := run("--once", "--ctx", "beta", "--width", "120")
	if code != 0 || !strings.Contains(out, "B1") || strings.Contains(out, "A1") {
		t.Errorf("factory --once --ctx beta: exit %d, stdout %q, stderr %q; want beta's B1 only", code, out, errOut)
	}
	_, errOut, code = run("--once", "--ctx", "gamma")
	if code != 2 || !strings.Contains(errOut, `--ctx "gamma"`) || !strings.Contains(errOut, "alpha, beta") {
		t.Errorf("factory --ctx gamma: exit %d, stderr %q; want 2 naming alpha, beta", code, errOut)
	}
}

// TestFactoryKeysFlag checks --keys/--frames: `factory --keys j --frames`
// prints the first frame and one after j as JSON, and an unknown key token is
// a usage error. Each run is a child, since runFactory exits.
func TestFactoryKeysFlag(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(factoryHelperEnv); ok {
		runFactory(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	if _, err := flywheel.Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := flywheel.AppendEvent(dir, flywheel.Event{TS: "2026-09-27T12:00:00Z", Task: "K1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	run := func(args ...string) (string, string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestFactoryKeysFlag$")
		cmd.Env = append(os.Environ(), factoryHelperEnv+"="+strings.Join(args, "\x1f"))
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var e *exec.ExitError
		if errors.As(err, &e) {
			return stdout.String(), stderr.String(), e.ExitCode()
		}
		if err != nil {
			t.Fatalf("run child: %v", err)
		}
		return stdout.String(), stderr.String(), 0
	}
	out, errOut, code := run("--keys", "j", "--frames", "--width", "80", "--height", "20", "--now", "2026-09-27T12:05:00Z", "--dir", dir)
	var frames []flywheel.Frame
	if code != 0 {
		t.Fatalf("factory --keys j --frames: exit %d, stderr %q", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &frames); err != nil {
		t.Fatalf("stdout is not JSON frames: %v\n%s", err, out)
	}
	if len(frames) != 2 || frames[0].Key != "" || frames[1].Key != "j" || !strings.Contains(frames[0].Frame, "K1") {
		t.Errorf("frames = %+v, want 2 (\"\" then j) showing K1", frames)
	}
	_, errOut, code = run("--keys", "<nope>", "--frames", "--dir", dir)
	if code != 2 || !strings.Contains(errOut, "<nope>") {
		t.Errorf("factory --keys <nope>: exit %d, stderr %q; want 2 naming the token", code, errOut)
	}
}

// TestFactoryPipedRendersOnce checks that `flywheel factory` with stdout not
// a terminal renders the floor once and returns instead of starting a live
// loop that would hang an automated caller (#336 review: the interactive
// wiring briefly sent this case to the redraw loop).
func TestFactoryPipedRendersOnce(t *testing.T) {
	// not parallel: swaps os.Stdout
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
