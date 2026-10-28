package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestClaudeAbsPattern checks the "//" rule form on every OS (issue #886).
func TestClaudeAbsPattern(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`C:\Users\a`: "//c/Users/a",
		"D:/x/y":     "//d/x/y",
		"/home/a":    "//home/a",
		`C:\a\b\`:    "//c/a/b",
	} {
		if got := claudeAbsPattern(in); got != want {
			t.Errorf("claudeAbsPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// readRules are the Read, Grep and Glob rules for path.
func readRules(path string) []string {
	p := claudeAbsPattern(path)
	return []string{"Read(" + p + "/**)", "Grep(" + p + "/**)", "Glob(" + p + "/**)"}
}

// TestLinkedReadRules checks a linked needs-state path gets read-only rules
// for the link and its target, and a plain or missing entry gets none
// (issue #886).
func TestLinkedReadRules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	main := filepath.Join(root, "main")
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(filepath.Join(main, "nm", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "nm", "pkg", "x.d.ts"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := linkNeedsState(main, wt, []string{"nm/"}); err != nil {
		t.Fatalf("linkNeedsState() error = %v", err)
	}
	wtResolved, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	// The link's target is main/nm: EvalSymlinks leaves a Windows junction
	// unresolved, so it resolves the target directly.
	target, err := filepath.EvalSymlinks(filepath.Join(main, "nm"))
	if err != nil {
		t.Fatal(err)
	}
	want := append(readRules(filepath.Join(wtResolved, "nm")), readRules(target)...)
	got := linkedReadRules(wt, []string{"nm/"})
	if !slices.Equal(got, want) {
		t.Errorf("linkedReadRules(nm/) = %q, want %q", got, want)
	}
	if got := linkedReadRules(wt, []string{"plain/"}); len(got) != 0 {
		t.Errorf("linkedReadRules(plain/) = %q, want none", got)
	}
	if got := linkedReadRules(wt, []string{"missing/"}); len(got) != 0 {
		t.Errorf("linkedReadRules(missing/) = %q, want none", got)
	}
	for _, r := range linkedReadRules(wt, []string{"nm/", "plain/", "missing/", "nm"}) {
		if strings.Contains(r, "Edit") || strings.Contains(r, "Write") {
			t.Errorf("rule %q widens writes", r)
		}
	}
}

// TestRunLinkedReadRules checks a --worktree dispatch passes the worker the
// read rules for its needs-state link on top of the default tools, without
// mutating defaultAllowedTools (issue #886).
func TestRunLinkedReadRules(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := worktreeRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\nnm/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte("owns: a.go\nneeds: none\nneeds-state: nm/ (link)\n\n# TASK: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nm", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nm", "pkg", "x.d.ts"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "needs-state"})

	before := slices.Clone(defaultAllowedTools)
	var allowed []string
	commandHook = func(r RunRequest) { allowed = r.AllowedTools }
	defer func() { commandHook = nil }()
	if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "nm", "pkg", "x.d.ts")); err != nil {
		t.Fatalf("nm/ not linked into the worktree: %v", err)
	}
	// wt/nm resolves to the root's nm/ (EvalSymlinks leaves a Windows
	// junction unresolved, so the target is resolved directly).
	target, err := filepath.EvalSymlinks(filepath.Join(dir, "nm"))
	if err != nil {
		t.Fatal(err)
	}
	rule := "Read(" + claudeAbsPattern(target) + "/**)"
	if !slices.Contains(allowed, "Bash") || !slices.Contains(allowed, rule) {
		t.Errorf("AllowedTools = %q, want Bash and %q", allowed, rule)
	}
	if !slices.Equal(defaultAllowedTools, before) {
		t.Errorf("defaultAllowedTools = %q, want unchanged %q", defaultAllowedTools, before)
	}
}
