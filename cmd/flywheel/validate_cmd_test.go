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

// runValidateHelperEnv carries the validate arguments into the child process
// TestNeedsEnvValidateRefusal re-execs, since runValidate exits the process.
const runValidateHelperEnv = "FLYWHEEL_TEST_RUN_VALIDATE_ARGS"

// TestNeedsEnvValidateRefusal checks flywheel validate exits 6 with a
// needs-env refusal on stderr, and runs no gate, while a variable the brief's
// needs-env: names is unset (issue #534).
func TestNeedsEnvValidateRefusal(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runValidateHelperEnv); ok {
		runValidate(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	const name = "FW_TEST_NEEDS_ENV_X"
	dir := t.TempDir()
	if _, err := flywheel.Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	brief := "needs-env: " + name + "\ngate: echo x > marker.txt\n\n# TASK x\n"
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := flywheel.AppendEvent(dir, flywheel.Event{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestNeedsEnvValidateRefusal$")
	// The child's environment never holds the variable, whatever this one has.
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, name+"=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, runValidateHelperEnv+"="+strings.Join([]string{"T1", "--dir", dir}, "\x1f"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	var e *exec.ExitError
	if errors.As(err, &e) {
		code = e.ExitCode()
	} else if err != nil {
		t.Fatalf("run child: %v", err)
	}
	if code != 6 {
		t.Fatalf("validate exit = %d, want 6; stderr:\n%s", code, stderr.String())
	}
	if got := stderr.String(); !strings.HasPrefix(got, "validate: ") || !strings.Contains(got, "needs-env") || !strings.Contains(got, name) {
		t.Errorf("stderr = %q, want a validate: needs-env refusal naming %s", got, name)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err == nil {
		t.Error("a gate ran despite the refusal")
	}
}
