package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// runObserveHelperEnv carries runObserve's arguments to the re-executed test
// binary, separated by \x1f, so a test can observe its exit status.
const runObserveHelperEnv = "FLYWHEEL_TEST_RUNOBSERVE_ARGS"

// runObserveProcess runs runObserve(args) in a child copy of the test binary
// and returns its stderr and exit code.
func runObserveProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestObserveCmdExitCodes$")
	cmd.Env = append(os.Environ(), runObserveHelperEnv+"="+strings.Join(args, "\x1f"))
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

// TestObserveCmdExitCodes checks observe's usage errors exit 2 and a
// poka-yoke refusal (no --session, T4) exits 6 (issue #842).
func TestObserveCmdExitCodes(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runObserveHelperEnv); ok {
		runObserve(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	cases := []struct {
		args []string
		code int
		want string
	}{
		{[]string{"T1", "--result", "pass", "--evidence", "E", "--session", "lead", "--dir", dir}, 2, "--check"},
		{[]string{"T1", "--check", "x", "--result", "pass", "--evidence", "E", "--session", "lead", "--dir", dir}, 2, "check"},
		{[]string{"T1", "--check", "1", "--result", "maybe", "--evidence", "E", "--session", "lead", "--dir", dir}, 2, "not pass or fail"},
		{[]string{"T1", "--check", "1", "--result", "pass", "--session", "lead", "--dir", dir}, 2, "--evidence is required"},
		{[]string{"--check", "1", "--result", "pass", "--evidence", "E", "--dir", dir}, 2, "exactly one task id"},
		{[]string{"T1", "--check", "1", "--result", "pass", "--evidence", "E", "--dir", dir}, 6, "--session"},
	}
	for _, c := range cases {
		stderr, code := runObserveProcess(t, c.args...)
		if code != c.code || !strings.Contains(stderr, c.want) {
			t.Errorf("observe %v: exit %d, stderr %q; want exit %d naming %q", c.args, code, stderr, c.code, c.want)
		}
	}
}

// TestObserveCmdFlagsInterleaved checks the task id may come before, after or
// between the flags.
func TestObserveCmdFlagsInterleaved(t *testing.T) {
	t.Parallel()
	forms := [][]string{
		{"T1", "--check", "2", "--result", "fail", "--evidence", "E", "--session", "S"},
		{"--check", "2", "--result", "fail", "--evidence", "E", "--session", "S", "T1"},
		{"--check", "2", "T1", "--result", "fail", "--evidence", "E", "--session", "S"},
	}
	for _, args := range forms {
		fs, o := observeFlags()
		pos, err := parseArgs(fs, args)
		if err != nil {
			t.Errorf("observeFlags parseArgs(%v): %v", args, err)
			continue
		}
		want := observeOptions{dir: ".", check: 2, result: "fail", evidence: "E", session: "S"}
		if len(pos) != 1 || pos[0] != "T1" || *o != want {
			t.Errorf("observeFlags parseArgs(%v) = %v, %#v; want [T1], %#v", args, pos, *o, want)
		}
	}
}
