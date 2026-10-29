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

// TestVerifyLedgerDirInconclusive checks issue #464: with ledger.dir outside
// the checkout and no log there, verify --all --log is inconclusive (exit 8,
// naming ledger.dir), never a vacuous pass; once the dir holds a log it passes.
// not parallel: sets FLYWHEEL_LEDGER_DIR
func TestVerifyLedgerDirInconclusive(t *testing.T) {
	t.Setenv("FLYWHEEL_LEDGER_DIR", "")
	dir, ext := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":1,"ledger":{"dir":"` + filepath.ToSlash(ext) + `"}}`
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := runVerifyProcess(t, "--all", "--log", "--dir", dir); code != 8 || out != "" {
		t.Fatalf("verify --all --log = %d %q, want exit 8 and nothing on stdout", code, out)
	}
	out, code := runVerifyProcess(t, "--all", "--log", "--json", "--dir", dir)
	if code != 8 || !strings.Contains(out, `"rule":"LOG"`) || !strings.Contains(out, "inconclusive: ledger.dir places the event log at") || !strings.Contains(out, `"inconclusive":true`) {
		t.Fatalf("verify --all --log --json = %d %q, want exit 8 naming ledger.dir", code, out)
	}
	if err := flywheel.AppendEvent(dir, flywheel.Event{Kind: "note", Note: "one"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ext, "events.jsonl")); err != nil {
		t.Fatalf("the log is not in ledger.dir: %v", err)
	}
	if out, code := runVerifyProcess(t, "--all", "--log", "--dir", dir); code != 0 || !strings.Contains(out, "PASS - LOG") {
		t.Errorf("verify --all --log with the log present = %d %q, want exit 0", code, out)
	}
}

// TestVerifyShippedPlannedOnlyExits6 checks issue #861: a unit that was only
// planned passes plain verify, but verify --shipped refuses it with exit 6 and
// a failing S1 line.
func TestVerifyShippedPlannedOnlyExits6(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("owns: a.go\nneeds: none\ngate: true\n\n# TASK: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := flywheel.AppendEvent(dir, flywheel.Event{TS: "2026-09-20T00:00:00Z", Task: "T", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatal(err)
	}
	if out, code := runVerifyProcess(t, "--dir", dir, "T"); code != 0 || strings.Contains(out, " S1:") {
		t.Fatalf("verify T = %d %q, want exit 0 and no S1 line", code, out)
	}
	out, code := runVerifyProcess(t, "--shipped", "--dir", dir, "T")
	if code != 6 || !strings.Contains(out, "FAIL T S1: task \"T\" was never judged") {
		t.Errorf("verify --shipped T = %d %q, want exit 6 with a failing S1", code, out)
	}
}
