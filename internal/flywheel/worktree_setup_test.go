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

// TestWorktreeSetupResolveCommand checks resolveSetupCommand (issue #471): a
// relative script missing from the worktree but present in the root resolves
// against the root; every other command is unchanged.
func TestWorktreeSetupResolveCommand(t *testing.T) {
	t.Parallel()
	wt, root, spaced := filepath.FromSlash("/w"), filepath.FromSlash("/r"), filepath.FromSlash("/my root")
	have := map[string]bool{
		filepath.Join(root, ".flywheel", "briefs", "setup.mjs"): true,
		filepath.Join(root, "setup.sh"):                         true,
		filepath.Join(root, "both.sh"):                          true,
		filepath.Join(wt, "both.sh"):                            true,
		filepath.Join(spaced, "setup.sh"):                       true,
	}
	exists := func(p string) bool { return have[p] }
	abs := func(r, rel string) string { return filepath.ToSlash(filepath.Join(r, filepath.FromSlash(rel))) }
	cases := []struct{ root, in, want string }{
		{root, "node .flywheel/briefs/setup.mjs", "node " + abs(root, ".flywheel/briefs/setup.mjs")},
		{root, "./setup.sh --x", abs(root, "setup.sh") + " --x"},
		{root, "bash setup.sh && npm ci", "bash " + abs(root, "setup.sh") + " && npm ci"},
		{spaced, "sh setup.sh", `sh "` + abs(spaced, "setup.sh") + `"`},
		{root, "sh both.sh", "sh both.sh"},
		{root, "node /abs/setup.mjs", "node /abs/setup.mjs"},
		{root, "npm ci", "npm ci"},
		{root, "node --version", "node --version"},
		{root, "node missing.mjs", "node missing.mjs"},
		{root, `node "setup.sh"`, `node "setup.sh"`},
		{root, "./setup.sh|tee log", "./setup.sh|tee log"},
		{root, "", ""},
	}
	for _, c := range cases {
		if got := resolveSetupCommand(c.in, wt, c.root, exists); got != c.want {
			t.Errorf("resolveSetupCommand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWorktreeSetupRunsRootScript checks runWorktreeSetup runs a script that
// exists only in the root, with cwd still the worktree.
func TestWorktreeSetupRunsRootScript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte("echo from-root > marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, tail, _, err := runWorktreeSetup(dir, wt, "t1", "sh setup.sh", time.Minute)
	if err != nil || rc != 0 {
		t.Fatalf("runWorktreeSetup() = %d, %v; tail %q", rc, err, tail)
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
