package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// runVerifyHelperEnv carries runVerify's arguments to the re-executed test
// binary, separated by \x1f, so a test can observe its stdout and exit status.
const runVerifyHelperEnv = "FLYWHEEL_TEST_RUNVERIFY_ARGS"

// runVerifyProcess runs runVerify(args) in a child copy of the test binary and
// returns its stdout and exit code.
func runVerifyProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestVerifyLogReorderedText$")
	cmd.Env = append(os.Environ(), runVerifyHelperEnv+"="+strings.Join(args, "\x1f"))
	var stdout strings.Builder
	cmd.Stdout = &stdout
	err := cmd.Run()
	var e *exec.ExitError
	if errors.As(err, &e) {
		return stdout.String(), e.ExitCode()
	}
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	return stdout.String(), 0
}

// TestVerifyLogReorderedText checks verify --log on a legacy log whose lines 2
// and 3 were swapped names the reorder, not an edit (issue #436), and that an
// acknowledged break passes and is named.
func TestVerifyLogReorderedText(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runVerifyHelperEnv); ok {
		runVerify(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	for _, n := range []string{"one", "two", "three"} {
		if err := flywheel.AppendEvent(dir, flywheel.Event{Kind: "note", Note: n}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	lines[1], lines[2] = lines[2], lines[1]
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := runVerifyProcess(t, "--log", "--dir", dir)
	if code != 6 || !strings.Contains(out, "events.jsonl line 2: reordered") || strings.Contains(out, "edited or removed") || !strings.Contains(out, "(prev ") {
		t.Fatalf("verify --log = %d %q, want exit 6 naming the reorder", code, out)
	}
	if _, err := flywheel.Reanchor(dir, "merge reordered one batch", "S1", false); err != nil {
		t.Fatalf("Reanchor: %v", err)
	}
	out, code = runVerifyProcess(t, "--log", "--dir", dir)
	if code != 0 || !strings.Contains(out, "acknowledged break at events.jsonl line 2 (reordered), by S1: merge reordered one batch") {
		t.Errorf("verify --log after reanchor = %d %q, want a pass naming the acknowledgement", code, out)
	}
}
