package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inheritFixture builds a root holding node_modules/ and a nested worktree
// root/.flywheel/worktrees/T1 holding a package.json and nothing else.
func inheritFixture(t *testing.T) (root, wt string) {
	t.Helper()
	root = t.TempDir()
	wt = filepath.Join(root, ".flywheel", "worktrees", "T1")
	for _, d := range []string{filepath.Join(root, "node_modules"), wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, wt
}

// TestInheritedModules checks inheritedModules (issue #802): a worktree with
// a package.json and no node_modules resolves the root's; its own
// node_modules, a .pnp.cjs or no package.json is "".
func TestInheritedModules(t *testing.T) {
	t.Parallel()
	root, wt := inheritFixture(t)
	got, err := inheritedModules(root, wt)
	if err != nil || !samePath(got, filepath.Join(root, "node_modules")) {
		t.Fatalf("inheritedModules() = %q, %v, want %s", got, err, filepath.Join(root, "node_modules"))
	}
	if !filepath.IsAbs(got) {
		t.Errorf("inheritedModules() = %q, want an absolute path", got)
	}
	for _, own := range []string{"node_modules", ".pnp.cjs"} {
		root, wt := inheritFixture(t)
		p := filepath.Join(wt, own)
		var err error
		if own == "node_modules" {
			err = os.Mkdir(p, 0o755)
		} else {
			err = os.WriteFile(p, nil, 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		if got, err := inheritedModules(root, wt); err != nil || got != "" {
			t.Errorf("with its own %s: inheritedModules() = %q, %v, want \"\"", own, got, err)
		}
	}
	bareRoot, bare := inheritFixture(t)
	if err := os.Remove(filepath.Join(bare, "package.json")); err != nil {
		t.Fatal(err)
	}
	if got, err := inheritedModules(bareRoot, bare); err != nil || got != "" {
		t.Errorf("without package.json: inheritedModules() = %q, %v, want \"\"", got, err)
	}
	// A node_modules above the main checkout root (a home ~/node_modules) is
	// never reported, and neither is a worktree outside the root.
	above := t.TempDir()
	if err := os.Mkdir(filepath.Join(above, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(above, "repo")
	innerWt := filepath.Join(inner, ".flywheel", "worktrees", "T1")
	if err := os.MkdirAll(innerWt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerWt, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := inheritedModules(inner, innerWt); err != nil || got != "" {
		t.Errorf("node_modules only above the root: inheritedModules() = %q, %v, want \"\"", got, err)
	}
	otherRoot, _ := inheritFixture(t)
	if got, err := inheritedModules(otherRoot, innerWt); err != nil || got != "" {
		t.Errorf("worktree outside the root: inheritedModules() = %q, %v, want \"\"", got, err)
	}
}

// TestPrepareWorktreeInheritedModules checks prepareWorktree with nothing
// configured (issue #802): an inherited node_modules is warned about and
// recorded as Inherited, a setup RuleRefusal with worktree.strict_links, and a
// worktree without package.json records nothing.
func TestPrepareWorktreeInheritedModules(t *testing.T) {
	t.Parallel()
	for _, strict := range []bool{false, true} {
		root, wt := inheritFixture(t)
		cfg := Config{Worktree: &WorktreeConfig{StrictLinks: strict}}
		warnings, err := prepareWorktree(root, wt, "T1", "r1", cfg, nil, nil, nil)
		var rr *RuleRefusal
		if strict && (!errors.As(err, &rr) || rr.Rule != "setup" || !strings.Contains(rr.Fix, "#802")) {
			t.Errorf("strict: prepareWorktree() error = %v, want a setup RuleRefusal naming #802", err)
		}
		if !strict && err != nil {
			t.Errorf("prepareWorktree() error = %v, want nil", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "no node_modules of its own") || !strings.Contains(warnings[0], "#802") {
			t.Errorf("strict=%v: warnings = %q, want the #802 warning", strict, warnings)
		}
		evs, err := ReadEvents(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].Kind != "worktree_setup" || !samePath(evs[0].Inherited, filepath.Join(root, "node_modules")) {
			t.Fatalf("strict=%v: events = %+v, want one worktree_setup with Inherited", strict, evs)
		}
		if strict != strings.Contains(evs[0].Note, "refused") {
			t.Errorf("strict=%v: note = %q, want refused only when strict", strict, evs[0].Note)
		}
	}
	root, wt := inheritFixture(t)
	if err := os.Remove(filepath.Join(wt, "package.json")); err != nil {
		t.Fatal(err)
	}
	warnings, err := prepareWorktree(root, wt, "T1", "r1", Config{}, nil, nil, nil)
	if err != nil || len(warnings) != 0 {
		t.Errorf("non-JS worktree: prepareWorktree() = %q, %v, want nothing", warnings, err)
	}
	if evs, err := ReadEvents(root); err != nil || len(evs) != 0 {
		t.Errorf("non-JS worktree: events = %+v, %v, want none", evs, err)
	}
}
