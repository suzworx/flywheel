package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runLandPRHelperEnv carries runLand's arguments to the re-executed test
// binary, separated by \x1f, so a test can observe its exit status.
const runLandPRHelperEnv = "FLYWHEEL_TEST_RUNLANDPR_ARGS"

// runLandPRProcess runs runLand(args) in a child copy of the test binary and
// returns its stderr and exit code.
func runLandPRProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLandPRCmdUsage$")
	cmd.Env = append(os.Environ(), runLandPRHelperEnv+"="+strings.Join(args, "\x1f"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var e *exec.ExitError
	if errors.As(err, &e) {
		return stderr.String(), e.ExitCode()
	}
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	return stderr.String(), 0
}

// TestLandPRCmdUsage checks land --pr's usage refusals exit 2 (issue #831).
func TestLandPRCmdUsage(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runLandPRHelperEnv); ok {
		runLand(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"T1", "--pr", "9", "--session", "lead-1", "--dir", dir}, "--pr takes no task id"},
		{[]string{"--pr", "9", "--merge", "--session", "lead-1", "--dir", dir}, "--pr and --merge"},
		{[]string{"--pr", "9", "--dir", dir}, "--pr requires --session"},
	}
	for _, c := range cases {
		stderr, code := runLandPRProcess(t, c.args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("land %v: exit %d, stderr %q; want exit 2 naming %q", c.args, code, stderr, c.want)
		}
	}
}
