package flywheel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sameDir reports whether a and b name the same directory once symlinks are
// resolved (macOS temp dirs sit behind /var -> /private/var).
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", a, err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", b, err)
	}
	return ra == rb
}

// TestLintDirNearestFlywheelAncestor: a subdirectory resolves to the ancestor
// holding .flywheel/, and that root resolves to itself (issue #808).
func TestLintDirNearestFlywheelAncestor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "sub", "dir")
	for _, d := range []string{filepath.Join(root, ".flywheel"), sub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if got := LintDir(sub); got != root {
		t.Errorf("LintDir(%s) = %s, want %s", sub, got, root)
	}
	if got := LintDir(root); got != root {
		t.Errorf("LintDir(%s) = %s, want %s", root, got, root)
	}
}

// TestLintDirGitTopLevel: with no .flywheel anywhere, a repository
// subdirectory resolves to the repository top level.
func TestLintDirGitTopLevel(t *testing.T) {
	t.Parallel()
	skipIfLintDirAncestor(t)
	root := t.TempDir()
	initGitRepoAt(t, root)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	if got := LintDir(sub); !sameDir(t, got, root) {
		t.Errorf("LintDir(%s) = %s, want %s", sub, got, root)
	}
}

// TestLintDirPlainDirUnchanged: no repository and no .flywheel returns the
// directory unchanged.
func TestLintDirPlainDirUnchanged(t *testing.T) {
	t.Parallel()
	skipIfLintDirAncestor(t)
	dir := t.TempDir()
	if got := LintDir(dir); got != dir {
		t.Errorf("LintDir(%s) = %s, want it unchanged", dir, got)
	}
}

// skipIfLintDirAncestor skips when the temp area already sits under a
// .flywheel directory or inside a git repository, as on some dev boxes.
func skipIfLintDirAncestor(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	for d := tmp; ; {
		if st, err := os.Stat(filepath.Join(d, ".flywheel")); err == nil && st.IsDir() {
			t.Skipf("%s holds .flywheel", d)
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if exec.Command("git", "-C", tmp, "rev-parse", "--show-toplevel").Run() == nil {
		t.Skipf("%s is inside a git repository", tmp)
	}
}

// TestLintDirOwnsResolveAgainstDir: owns resolve against the dir lint is
// given, and a missing path names that dir.
func TestLintDirOwnsResolveAgainstDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	brief := filepath.Join(root, "brief.txt")
	content := "owns: a.go\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	if err := os.WriteFile(brief, []byte(content), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	res, err := LintBrief(root, brief)
	if err != nil {
		t.Fatalf("LintBrief(root) error = %v", err)
	}
	if len(res.Problems) != 0 {
		t.Errorf("LintBrief(root) problems = %v, want none", res.Problems)
	}
	res, err = LintBrief(sub, brief)
	if err != nil {
		t.Fatalf("LintBrief(sub) error = %v", err)
	}
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0], "under ") || !strings.Contains(res.Problems[0], sub) {
		t.Errorf("LintBrief(sub) problems = %v, want one naming under %s", res.Problems, sub)
	}
}
