package flywheel

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// shipTemplate is the ship fixture's git state, built once per test binary: a
// bare origin, a repository whose main is pushed to it and fetched back, and
// T's worktree on fw/T, snapshotted file by file. Each git process costs
// 0.1-0.5s on a loaded Windows host, so copying the snapshot instead of
// running the dozen setup commands per fixture takes seconds off every ship
// test (issue #619).
var shipTemplate struct {
	once  sync.Once
	root  string            // the directory the template was built in
	dirs  []string          // slash paths under root, parents first
	files map[string][]byte // slash path under root -> content
	base  string            // main's commit
}

// shipTemplateCopy writes the template to a new temporary root and returns
// its origin, repository and worktree directories and main's commit. The
// absolute paths git recorded (origin's URL, the worktree's links) are
// rewritten to the new root; object files hold none and are copied as is.
func shipTemplateCopy(t *testing.T) (origin, dir, wt, base string) {
	t.Helper()
	shipTemplate.once.Do(func() { shipTemplateBuild(t) })
	if shipTemplate.files == nil {
		t.Fatal("the ship fixture template failed to build; see the first ship test's output")
	}
	root := t.TempDir()
	for _, d := range shipTemplate.dirs {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldSlash, newSlash := []byte(filepath.ToSlash(shipTemplate.root)), []byte(filepath.ToSlash(root))
	oldNative, newNative := []byte(shipTemplate.root), []byte(root)
	for p, b := range shipTemplate.files {
		if !bytes.Contains([]byte(p), []byte("/objects/")) {
			b = bytes.ReplaceAll(bytes.ReplaceAll(b, oldSlash, newSlash), oldNative, newNative)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(p)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir = filepath.Join(root, "dir")
	return filepath.Join(root, "origin"), dir, filepath.Join(dir, ".flywheel", "worktrees", "T"), shipTemplate.base
}

// shipTemplateBuild runs the fixture's git setup once in t's temporary
// directory and snapshots it into shipTemplate. Automatic maintenance is off
// in both repositories so no git spawns a gc check after a commit, fetch or
// push; no ship test depends on it.
func shipTemplateBuild(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	origin, dir := filepath.Join(root, "origin"), filepath.Join(root, "dir")
	for _, d := range []string{origin, dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shipGit(t, origin, "init", "-q", "--bare")
	shipGit(t, origin, "config", "receive.autogc", "false")
	shipGit(t, dir, "init", "-q")
	shipGit(t, dir, "config", "core.autocrlf", "false")
	shipGit(t, dir, "config", "maintenance.auto", "false")
	shipGit(t, dir, "config", "gc.auto", "0")
	shipWrite(t, dir, ".gitignore", ".flywheel/\nflywheel.md\n")
	shipWrite(t, dir, "src/a.go", "package src // v0\n")
	shipGit(t, dir, "add", "-A")
	shipGit(t, dir, "commit", "-q", "-m", "init")
	shipGit(t, dir, "branch", "-M", "main")
	shipGit(t, dir, "remote", "add", "origin", filepath.ToSlash(origin))
	shipGit(t, dir, "push", "-q", "origin", "main")
	shipGit(t, dir, "fetch", "-q", "origin")
	base := shipGit(t, dir, "rev-parse", "HEAD")
	shipGit(t, dir, "worktree", "add", "-q", "-b", "fw/T", filepath.Join(dir, ".flywheel", "worktrees", "T"), "main")
	var dirs []string
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, filepath.ToSlash(rel))
			return nil
		}
		b, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	shipTemplate.root, shipTemplate.dirs, shipTemplate.files, shipTemplate.base = root, dirs, files, base
}
