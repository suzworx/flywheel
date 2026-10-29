package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLandedTreeNotFound checks that a commit that does not resolve is
// reported as errCommitNotFound (issue #907).
func TestLandedTreeNotFound(t *testing.T) {
	t.Parallel()
	dir, _ := attestSetup(t, []string{"go build ./..."}, "a.go")
	missing := strings.Repeat("ab", 20)
	tree, err := landedTree(dir, missing)
	if !errors.Is(err, errCommitNotFound) {
		t.Fatalf("landedTree(unknown) error = %v, want errCommitNotFound", err)
	}
	if tree != "" || !strings.Contains(err.Error(), missing) {
		t.Errorf("landedTree(unknown) = %q, %v; want no tree and an error naming the commit", tree, err)
	}
}

// TestLandedTreeOK checks that a real commit gives its tree and no error.
func TestLandedTreeOK(t *testing.T) {
	t.Parallel()
	dir, commit := attestSetup(t, []string{"go build ./..."}, "a.go")
	tree, err := landedTree(dir, commit)
	if err != nil || tree == "" {
		t.Fatalf("landedTree(commit) = %q, %v; want a tree and no error", tree, err)
	}
}

// TestLandedTreeStepErrorNamed checks that a commit that resolves but whose
// tree cannot be computed reports the failing step, not errCommitNotFound,
// and that Attest passes git's error on instead of "not in the repository".
//
// The failure is forced by deleting the commit's root tree object: the
// commit object itself still exists, so `git cat-file -e <c>^{commit}`
// succeeds, while `git read-tree <c>` cannot read the tree and fails. Loose
// objects are plain files under .git/objects, so this reproduces on every OS
// (the object is made writable first: git stores it read-only, which
// Windows refuses to delete).
func TestLandedTreeStepErrorNamed(t *testing.T) {
	t.Parallel()
	dir, _ := attestSetup(t, []string{"go build ./..."}, "a.go")
	// A tree no other commit shares, so its loose object can be removed
	// without touching the history attestSetup made.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x // only in the broken commit\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	git(t, dir, []string{"add", "a.go"})
	treeID := git(t, dir, []string{"write-tree"})
	commit := git(t, dir, []string{"commit-tree", treeID, "-p", "HEAD", "-m", "broken"})
	obj := filepath.Join(dir, ".git", "objects", treeID[:2], treeID[2:])
	if err := os.Chmod(obj, 0o644); err != nil {
		t.Fatalf("chmod tree object: %v", err)
	}
	if err := os.Remove(obj); err != nil {
		t.Fatalf("remove tree object: %v", err)
	}

	tree, err := landedTree(dir, commit)
	if err == nil {
		t.Fatalf("landedTree(broken) = %q, nil; want an error", tree)
	}
	if errors.Is(err, errCommitNotFound) {
		t.Fatalf("landedTree(broken) error = %v; the commit resolves, want a step error", err)
	}
	if !strings.Contains(err.Error(), "read-tree") || !strings.Contains(err.Error(), commit) {
		t.Errorf("landedTree(broken) error = %v; want it to name read-tree and the commit", err)
	}

	_, aerr := Attest(dir, "T1", commit, "https://ci.example/run/7", "lead-1")
	if aerr == nil {
		t.Fatal("Attest(broken) error = nil, want git's error")
	}
	if strings.Contains(aerr.Error(), "not in the repository") || !strings.Contains(aerr.Error(), "read-tree") {
		t.Errorf("Attest(broken) error = %v; want git's read-tree error, not \"not in the repository\"", aerr)
	}
}
