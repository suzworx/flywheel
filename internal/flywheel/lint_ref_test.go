package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeCommit writes each file under dir and commits them all.
func writeCommit(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, append([]string{"add"}, files...))
	git(t, dir, []string{"commit", "-q", "-m", "c"})
}

// staleCheckout is a checkout on branch old (unrelated history, holding
// stale-only.txt) whose origin's dev branch holds on-ref.txt and pkg/x.go,
// with integration.branch dev (issue #694).
func staleCheckout(t *testing.T) string {
	t.Helper()
	origin := t.TempDir()
	initGitRepoAt(t, origin)
	writeCommit(t, origin, "on-ref.txt", "pkg/x.go")
	git(t, origin, []string{"branch", "-M", "dev"})
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	writeCommit(t, dir, "stale-only.txt")
	git(t, dir, []string{"branch", "-M", "old"})
	git(t, dir, []string{"remote", "add", "origin", origin})
	git(t, dir, []string{"fetch", "-q", "origin"})
	setIntegration(t, dir, "dev")
	return dir
}

// lintOwns runs lintStructure on a brief owning owns in dir.
func lintOwns(t *testing.T, dir, owns string) LintResult {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brief.txt")
	brief := "owns: " + owns + "\nneeds: none\ngate: go test ./...\n\n# TASK: x\n\n## Checks\nAt most one write per response.\n"
	if err := os.WriteFile(path, []byte(brief), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := lintStructure(dir, path)
	if err != nil {
		t.Fatalf("lintStructure: %v", err)
	}
	return res
}

// hasText reports whether some line in lines contains s.
func hasText(lines []string, s string) bool {
	return slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, s) })
}

// TestLintOwnsRefPresentOnRefPasses: a path on origin/dev but not in the
// stale checkout is no problem; a pattern resolves on the ref.
func TestLintOwnsRefPresentOnRefPasses(t *testing.T) {
	t.Parallel()
	dir := staleCheckout(t)
	res := lintOwns(t, dir, "on-ref.txt, pkg/, pkg/*.go")
	if len(res.Problems) != 0 {
		t.Errorf("problems = %v, want none", res.Problems)
	}
}

// TestLintOwnsRefCheckoutOnlyIsProblem: a path only in the checkout is a
// problem naming the ref, and so is a pattern matching nothing on the ref.
func TestLintOwnsRefCheckoutOnlyIsProblem(t *testing.T) {
	t.Parallel()
	dir := staleCheckout(t)
	res := lintOwns(t, dir, "stale-only.txt, stale-*.txt, on-ref.txt/, new.txt (new)")
	for _, w := range []string{
		"owns path stale-only.txt does not exist on origin/dev; if the unit creates it, annotate it: stale-only.txt (new)",
		"owns pattern stale-*.txt matches no file on origin/dev",
		"owns path on-ref.txt/ is not a directory on origin/dev",
	} {
		if !hasText(res.Problems, w) {
			t.Errorf("problems = %v, want %q", res.Problems, w)
		}
	}
	if len(res.Problems) != 3 {
		t.Errorf("problems = %v, want exactly 3", res.Problems)
	}
}

// TestLintOwnsRefNoRemoteKeepsCheckout: without a ref lint checks the
// checkout, with the old texts.
func TestLintOwnsRefNoRemoteKeepsCheckout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	writeCommit(t, dir, "here.txt")
	git(t, dir, []string{"branch", "-M", "trunk"})
	res := lintOwns(t, dir, "here.txt, h*.txt, gone.txt")
	want := []string{"owns path gone.txt does not exist under " + dir + "; if the unit creates it, annotate it: gone.txt (new)"}
	if !slices.Equal(res.Problems, want) {
		t.Errorf("problems = %v, want %v", res.Problems, want)
	}
	if hasText(res.Warnings, "checkout HEAD") {
		t.Errorf("warnings = %v, want no HEAD warning without a ref", res.Warnings)
	}
}

// TestLintOwnsAheadOfRefReadsCheckout: a checkout one commit ahead of
// origin/dev (a stacked unit's worktree) checks owns against the checkout, so
// a file its commit adds is no problem and there is no warning.
func TestLintOwnsAheadOfRefReadsCheckout(t *testing.T) {
	t.Parallel()
	dir := staleCheckout(t)
	git(t, dir, []string{"checkout", "-q", "-B", "dev", "origin/dev"})
	writeCommit(t, dir, "pkg/base-unit.go")
	res := lintOwns(t, dir, "pkg/base-unit.go, pkg/base-*.go, on-ref.txt")
	if len(res.Problems) != 0 {
		t.Errorf("problems = %v, want none", res.Problems)
	}
	if hasText(res.Warnings, "checkout HEAD") {
		t.Errorf("warnings = %v, want no HEAD warning", res.Warnings)
	}
}

// TestLintHeadBehindWarns: a stale HEAD warns, a HEAD at the ref's tip or
// ahead of it does not.
func TestLintHeadBehindWarns(t *testing.T) {
	t.Parallel()
	dir := staleCheckout(t)
	res := lintOwns(t, dir, "on-ref.txt")
	if !hasText(res.Warnings, "is not at origin/dev (") || !hasText(res.Warnings, "workers are based on origin/dev; read and write owns from that tree") {
		t.Errorf("stale: warnings = %v, want the HEAD warning", res.Warnings)
	}
	if hasText(res.Problems, "checkout HEAD") {
		t.Errorf("stale: the HEAD check must never be a problem: %v", res.Problems)
	}
	git(t, dir, []string{"checkout", "-q", "-B", "dev", "origin/dev"})
	if res := lintOwns(t, dir, "on-ref.txt"); hasText(res.Warnings, "checkout HEAD") {
		t.Errorf("at tip: warnings = %v, want no HEAD warning", res.Warnings)
	}
	writeCommit(t, dir, "ahead.txt")
	if res := lintOwns(t, dir, "on-ref.txt"); hasText(res.Warnings, "checkout HEAD") {
		t.Errorf("ahead: warnings = %v, want no HEAD warning", res.Warnings)
	}
}
