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

// TestValidateBaseProbeNote checks validate notes a failed gate whose newest
// probe on the base tree also failed, matched by command, and prints nothing
// extra for one whose newest probe passed or that was never probed (issue #544).
func TestValidateBaseProbeNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitInitRepo(t, dir)
	if _, err := flywheel.Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	brief := "owns: out.txt (new)\nneeds: none\ngate: exit 1\ngate: exit 2\ngate: exit 3\n\n# TASK x\n"
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	rebaseGit(t, dir, "add", "-A")
	rebaseGit(t, dir, "commit", "-q", "-m", "base")
	rc := func(n int) *int { return &n }
	// The probes precede planned, as lint --probe --task runs before it; the
	// probe of gate 1 was recorded at index 3, so matching is by command.
	evs := []flywheel.Event{
		{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "gate_probed", Gate: "3", Command: "exit 1", RC: rc(1), Reason: "boom"},
		{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "gate_probed", Gate: "2", Command: "exit 2", RC: rc(2), Reason: "old"},
		{TS: "2026-09-26T00:00:01Z", Task: "T1", Kind: "gate_probed", Gate: "2", Command: "exit 2", RC: rc(0)},
		// A failed probe with no reason ends the note at the exit code.
		{TS: "2026-09-26T00:00:01Z", Task: "T1", Kind: "gate_probed", Gate: "1", Command: "exit 3", RC: rc(3)},
		{TS: "2026-09-26T00:00:02Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
	}
	if err := flywheel.AppendEvents(dir, evs); err != nil {
		t.Fatalf("AppendEvents() error = %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNeedsEnvValidateRefusal$")
	cmd.Env = append(os.Environ(), runValidateHelperEnv+"="+strings.Join([]string{"T1", "--dir", dir}, "\x1f"))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var e *exec.ExitError
	if errors.As(err, &e) {
		code = e.ExitCode()
	} else if err != nil {
		t.Fatalf("run child: %v", err)
	}
	if code != 5 {
		t.Fatalf("validate exit = %d, want 5; stdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	want := "T1 gate 1: failed (rc=1)\n" +
		"T1 gate 1: note: this gate already failed on the base tree before dispatch (exit 1): boom\n" +
		"T1 gate 2: failed (rc=2)\n" +
		"T1 gate 3: failed (rc=3)\n" +
		"T1 gate 3: note: this gate already failed on the base tree before dispatch (exit 3)\n"
	if !strings.Contains(out, want) {
		t.Errorf("validate stdout lacks\n%s\ngot:\n%s", want, out)
	}
	if n := strings.Count(out, "note: this gate already failed"); n != 2 {
		t.Errorf("validate printed %d base-tree notes, want 2:\n%s", n, out)
	}
}

// TestChurnHints checks the issue #647 hint: one line per churn class, sorted,
// naming the paths and the restore command; none when nothing is labelled.
func TestChurnHints(t *testing.T) {
	t.Parallel()
	if got := churnHints("T1", "abc", nil); len(got) != 0 {
		t.Errorf("churnHints(nil) = %q, want none", got)
	}
	got := churnHints("T1", "abc", map[string]string{
		"z.go":  "line endings only",
		"a.txt": "line endings only",
		"w.md":  "whitespace only",
	})
	want := []string{
		"T1 owns: hint: a.txt, z.go differ from the base only in line endings; restore them byte-for-byte (git checkout abc -- a.txt z.go) and re-validate",
		"T1 owns: hint: w.md differ from the base only in whitespace; restore them byte-for-byte (git checkout abc -- w.md) and re-validate",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("churnHints() =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
