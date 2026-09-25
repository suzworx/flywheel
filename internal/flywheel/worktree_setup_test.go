package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNeedsStateLink checks linkNeedsState (issue #430): a target directory
// in root is reachable through the link in the worktree, a second call is a
// no-op, and a missing target is an error naming it.
func TestNeedsStateLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wt := t.TempDir()
	target := filepath.Join(root, "apps", "web", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "dep.txt"), []byte("dep"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := []string{"apps/web/node_modules/"}
	if err := linkNeedsState(root, wt, paths); err != nil {
		t.Fatalf("linkNeedsState() error = %v", err)
	}
	through := filepath.Join(wt, "apps", "web", "node_modules", "dep.txt")
	b, err := os.ReadFile(through)
	if err != nil || string(b) != "dep" {
		t.Fatalf("read through link = %q, %v, want dep", b, err)
	}
	if err := linkNeedsState(root, wt, paths); err != nil {
		t.Fatalf("second linkNeedsState() error = %v, want a no-op", err)
	}
	if b, err := os.ReadFile(through); err != nil || string(b) != "dep" {
		t.Fatalf("after second call, read through link = %q, %v, want dep", b, err)
	}
	err = linkNeedsState(root, wt, []string{"ghost/"})
	if err == nil || !strings.Contains(err.Error(), "ghost/") {
		t.Fatalf("linkNeedsState(missing) error = %v, want one naming ghost/", err)
	}
}

// TestRunWorktreeSetupEnvAndTail checks runWorktreeSetup runs in the
// worktree with the FLYWHEEL_* variables and returns the exit code and tail.
func TestRunWorktreeSetupEnvAndTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wt := t.TempDir()
	rc, tail, _, err := runWorktreeSetup(dir, wt, "t1", `echo "task=$FLYWHEEL_TASK" && echo ok > marker && exit 3`, time.Minute)
	if err != nil {
		t.Fatalf("runWorktreeSetup() error = %v", err)
	}
	if rc != 3 {
		t.Errorf("rc = %d, want 3", rc)
	}
	if !strings.Contains(tail, "task=t1") {
		t.Errorf("tail = %q, want it to carry task=t1", tail)
	}
	if _, err := os.Stat(filepath.Join(wt, "marker")); err != nil {
		t.Errorf("marker not written in the worktree: %v", err)
	}
}

// TestOutputTailKeepsLastLines checks the tail keeps the last n lines.
func TestOutputTailKeepsLastLines(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line\r\n")
	}
	sb.WriteString("last\r\n")
	got := outputTail(sb.String(), 20)
	lines := strings.Split(got, "\n")
	if len(lines) != 20 || lines[19] != "last" {
		t.Errorf("outputTail() = %d lines ending %q, want 20 ending last", len(lines), lines[len(lines)-1])
	}
}
