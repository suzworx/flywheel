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

// runLedgerHelperEnv carries runLedger's arguments to the re-executed test
// binary, separated by \x1f, so a test can observe its output and exit status.
const runLedgerHelperEnv = "FLYWHEEL_TEST_RUNLEDGER_ARGS"

// runLedgerProcess runs runLedger(args) in a child copy of the test binary and
// returns its stdout, stderr and exit code.
func runLedgerProcess(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLedgerCmdUsage$")
	cmd.Env = append(os.Environ(), runLedgerHelperEnv+"="+strings.Join(args, "\x1f"))
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

// TestLedgerCmdUsage checks ledger's usage errors exit 2 and a real backup
// exits 0 with the "backed up" line.
func TestLedgerCmdUsage(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runLedgerHelperEnv); ok {
		var args []string
		if v != "" {
			args = strings.Split(v, "\x1f")
		}
		runLedger(args)
		os.Exit(0)
	}
	dir := t.TempDir()
	for _, n := range []string{"one", "two"} {
		if err := flywheel.AppendEvent(dir, flywheel.Event{Kind: "note", Note: n}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	for _, args := range [][]string{{}, {"nope"}, {"backup"}, {"backup", "a", "b"}} {
		if _, stderr, code := runLedgerProcess(t, args...); code != 2 || !strings.Contains(stderr, "usage: flywheel ledger backup") {
			t.Errorf("ledger %q: exit %d, stderr %q; want exit 2 and the usage", args, code, stderr)
		}
	}
	dest := filepath.Join(t.TempDir(), "bak")
	stdout, stderr, code := runLedgerProcess(t, "backup", dest, "--dir", dir)
	if code != 0 || !strings.Contains(stdout, "backed up ") || !strings.Contains(stdout, "; chain ok (2 lines)") {
		t.Fatalf("ledger backup: exit %d, stdout %q, stderr %q; want exit 0 and the backed up line", code, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(dest, ".flywheel", "events.jsonl")); err != nil {
		t.Fatalf("backup copy missing: %v", err)
	}
	if _, stderr, code := runLedgerProcess(t, "backup", dest, "--dir", dir); code != 1 || !strings.Contains(stderr, "flywheel ledger backup: ") {
		t.Errorf("backup into a non-empty dest: exit %d, stderr %q; want exit 1", code, stderr)
	}
}
