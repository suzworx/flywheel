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

// runInitHelperEnv carries runInit's arguments to the re-executed test
// binary, separated by \x1f, so a test can observe its output and exit status.
const runInitHelperEnv = "FLYWHEEL_TEST_RUNINIT_ARGS"

// runInitProcess runs runInit(args) in a child copy of the test binary with
// an empty PATH (so detection finds no agent CLI) and returns its combined
// output and exit code.
func runInitProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInitAdapterFlag$")
	cmd.Env = append(os.Environ(), runInitHelperEnv+"="+strings.Join(args, "\x1f"), "PATH=")
	out, err := cmd.CombinedOutput()
	var e *exec.ExitError
	if errors.As(err, &e) {
		return string(out), e.ExitCode()
	}
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	return string(out), 0
}

// TestInitAdapterFlag checks `init --adapter claude` writes a claude worker
// without detecting (no agent CLI is on the child's PATH) and says so, and
// `--adapter nope` is a usage error (exit 2) that writes nothing (issue #275).
func TestInitAdapterFlag(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runInitHelperEnv); ok {
		runInit(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	out, code := runInitProcess(t, "--dir", dir, "--adapter", "claude")
	if code != 0 {
		t.Fatalf("init --adapter claude = %d\n%s", code, out)
	}
	cfg, exists, err := flywheel.LoadConfig(dir)
	if err != nil || !exists {
		t.Fatalf("LoadConfig = %v (exists %v)", err, exists)
	}
	if w := cfg.Workers[0]; w.Adapter != "claude" || w.Model != "claude-sonnet-5" {
		t.Errorf("default worker = %s %s, want claude claude-sonnet-5", w.Adapter, w.Model)
	}
	if want := "agents: not detected (--adapter claude); worker default uses claude (claude-sonnet-5)"; !strings.Contains(out, want) {
		t.Errorf("init output lacks %q:\n%s", want, out)
	}

	bad := t.TempDir()
	out, code = runInitProcess(t, "--dir", bad, "--adapter", "nope")
	if code != 2 || !strings.Contains(out, `--adapter "nope" must be one of claude, opencode, codex`) {
		t.Errorf("init --adapter nope = %d, want 2 naming the adapters\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(bad, ".flywheel", "config.json")); err == nil {
		t.Error("init --adapter nope wrote config.json")
	}
}
