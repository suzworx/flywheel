package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeTestFile writes content to dir/rel, creating parent directories.
func writeTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// assertTestFile fails unless dir/rel exists with content.
func assertTestFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("%s gone: %v", rel, err)
	}
	if string(b) != content {
		t.Fatalf("%s = %q, want %q", rel, b, content)
	}
}

// TestRemoveWorktreeKeepsLinkTargets: a worktree whose node_modules is a
// link (junction on Windows) into the main checkout, holding a nested link
// back to tracked files, is removed without deleting any linked file (#822).
func TestRemoveWorktreeKeepsLinkTargets(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{true, false} {
		r := t.TempDir()
		initGitRepoAt(t, r)
		writeTestFile(t, r, "apps/x/t", "tracked\n")
		writeTestFile(t, r, ".gitignore", "node_modules/\n")
		git(t, r, []string{"add", "-A"})
		git(t, r, []string{"commit", "-q", "-m", "initial"})
		writeTestFile(t, r, "node_modules/pkg/f", "dep\n")
		if err := linkNeedsState(filepath.Join(r, "apps"), filepath.Join(r, "node_modules", "@ws"), []string{"x"}); err != nil {
			t.Fatalf("nested link: %v", err)
		}
		wt := filepath.Join(t.TempDir(), "wt")
		git(t, r, []string{"worktree", "add", "-q", "--detach", wt})
		if err := linkNeedsState(r, wt, []string{"node_modules/"}); err != nil {
			t.Fatalf("linkNeedsState: %v", err)
		}
		if out, err := removeWorktree(r, wt, force, gitRead); err != nil {
			t.Fatalf("removeWorktree(force=%v): %v: %s", force, err, out)
		}
		if _, err := os.Lstat(wt); !os.IsNotExist(err) {
			t.Fatalf("force=%v: worktree still present: %v", force, err)
		}
		assertTestFile(t, r, "node_modules/pkg/f", "dep\n")
		assertTestFile(t, r, "apps/x/t", "tracked\n")
		assertTestFile(t, r, "node_modules/@ws/x/t", "tracked\n")
	}
}

// TestRemoveWorktreeUnlinksOnly: unlinkLinks removes and reports the link
// alone; regular files, real directories and the link's target remain.
func TestRemoveWorktreeUnlinksOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	outside := t.TempDir()
	writeTestFile(t, outside, "ext/o", "outside\n")
	writeTestFile(t, dir, "a.txt", "a\n")
	writeTestFile(t, dir, "sub/b.txt", "b\n")
	if err := linkNeedsState(outside, filepath.Join(dir, "sub"), []string{"ext"}); err != nil {
		t.Fatalf("linkNeedsState: %v", err)
	}
	removed, err := unlinkLinks(dir)
	if err != nil {
		t.Fatalf("unlinkLinks: %v", err)
	}
	if want := []string{"sub/ext"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed = %v, want %v", removed, want)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sub", "ext")); !os.IsNotExist(err) {
		t.Fatalf("link still present: %v", err)
	}
	assertTestFile(t, outside, "ext/o", "outside\n")
	assertTestFile(t, dir, "a.txt", "a\n")
	assertTestFile(t, dir, "sub/b.txt", "b\n")
}

// TestUnlinkLinksKeepsTrackedLinks: a symlink git tracks is left for git,
// which removes it as a link; only the untracked link is unlinked, and a
// non-force worktree remove of a tree holding both succeeds (#822, PR #823).
func TestUnlinkLinksKeepsTrackedLinks(t *testing.T) {
	t.Parallel()
	r := t.TempDir()
	initGitRepoAt(t, r)
	git(t, r, []string{"config", "core.symlinks", "true"})
	writeTestFile(t, r, "a.txt", "a\n")
	if err := os.Symlink("a.txt", filepath.Join(r, "tracked-link")); err != nil {
		t.Skipf("os.Symlink unavailable (no symlink privilege?): %v", err)
	}
	git(t, r, []string{"add", "-A"})
	git(t, r, []string{"commit", "-q", "-m", "initial"})
	outside := t.TempDir()
	writeTestFile(t, outside, "ext/o", "outside\n")
	if err := linkNeedsState(outside, r, []string{"ext"}); err != nil {
		t.Fatalf("linkNeedsState: %v", err)
	}
	removed, err := unlinkLinks(r)
	if err != nil {
		t.Fatalf("unlinkLinks: %v", err)
	}
	if want := []string{"ext"}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed = %v, want %v", removed, want)
	}
	if _, err := os.Lstat(filepath.Join(r, "tracked-link")); err != nil {
		t.Fatalf("tracked symlink removed: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	git(t, r, []string{"worktree", "add", "-q", "--detach", wt})
	if err := linkNeedsState(outside, wt, []string{"ext"}); err != nil {
		t.Fatalf("linkNeedsState in worktree: %v", err)
	}
	if out, err := removeWorktree(r, wt, false, gitRead); err != nil {
		t.Fatalf("removeWorktree(force=false): %v: %s", err, out)
	}
	if _, err := os.Lstat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	assertTestFile(t, outside, "ext/o", "outside\n")
}
