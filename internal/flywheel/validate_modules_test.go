package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validateModulesFixture is initTask with a gate that writes ran.txt, a root
// node_modules, worktree.strict_links set to strict, and a separate workdir
// nested at root/.flywheel/worktrees/T1: a git repo holding a.go and a
// package.json, plus its own node_modules when own is set (issue #829).
func validateModulesFixture(t *testing.T, strict, own bool) (root, wd string) {
	t.Helper()
	root, err := initTask(t, []string{"echo ran > ran.txt"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Worktree = &WorktreeConfig{StrictLinks: strict}
	if err := WriteConfig(root, cfg); err != nil {
		t.Fatal(err)
	}
	wd = filepath.Join(root, ".flywheel", "worktrees", "T1")
	for _, d := range []string{filepath.Join(root, "node_modules"), wd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	initRepo(t, wd)
	if err := os.WriteFile(filepath.Join(wd, ".gitignore"), []byte("node_modules/\nran.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wd, []string{"add", "-A"})
	git(t, wd, []string{"commit", "-m", "package"})
	if own {
		if err := os.Mkdir(filepath.Join(wd, "node_modules"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, wd
}

// validateSetupEvents returns T1's worktree_setup events in root's ledger.
func validateSetupEvents(t *testing.T, root string) []Event {
	t.Helper()
	evs, err := ReadEvents(root)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "worktree_setup" {
			out = append(out, e)
		}
	}
	return out
}

// TestValidateModulesStrict checks that validate re-checks a workdir that
// inherits the main checkout's node_modules (issue #829): with
// worktree.strict_links it is a setup RuleRefusal before any gate runs, and a
// worktree_setup event noted "validate: refused: " records Inherited.
func TestValidateModulesStrict(t *testing.T) {
	t.Parallel()
	root, wd := validateModulesFixture(t, true, false)
	res, err := ValidateTask(root, "T1", ValidateOptions{Dir: root, Workdir: wd})
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "setup" || !strings.Contains(rr.Fix, "(install)") || !strings.Contains(rr.Fix, "#829") {
		t.Fatalf("ValidateTask() = %+v, %v, want a setup RuleRefusal naming (install) and #829", res, err)
	}
	if _, err := os.Stat(filepath.Join(wd, "ran.txt")); !errors.Is(err, os.ErrNotExist) || len(res.Gates) != 0 {
		t.Errorf("a gate ran (ran.txt: %v, gates %+v), want none", err, res.Gates)
	}
	evs := validateSetupEvents(t, root)
	if len(evs) != 1 || !strings.HasPrefix(evs[0].Note, "validate: refused: ") || !samePath(evs[0].Inherited, filepath.Join(root, "node_modules")) || evs[0].Attempt != "r1" {
		t.Errorf("worktree_setup events = %+v, want one refused with Inherited, attempt r1", evs)
	}
}

// TestValidateModulesWarn checks the same workdir without strict_links: the
// event is recorded with a "validate: " note and the gate still runs and
// passes (issue #829).
func TestValidateModulesWarn(t *testing.T) {
	t.Parallel()
	root, wd := validateModulesFixture(t, false, false)
	res, err := ValidateTask(root, "T1", ValidateOptions{Dir: root, Workdir: wd})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Gates) != 1 || res.Gates[0].RC != 0 {
		t.Errorf("gates = %+v, want one passing gate", res.Gates)
	}
	if _, err := os.Stat(filepath.Join(wd, "ran.txt")); err != nil {
		t.Errorf("gate did not run: %v", err)
	}
	evs := validateSetupEvents(t, root)
	if len(evs) != 1 || !strings.HasPrefix(evs[0].Note, "validate: ") || strings.Contains(evs[0].Note, "refused") || !samePath(evs[0].Inherited, filepath.Join(root, "node_modules")) {
		t.Errorf("worktree_setup events = %+v, want one validate: note with Inherited", evs)
	}
}

// TestValidateModulesClean checks that a workdir with its own node_modules
// and no escaping links records no worktree_setup event (issue #829).
func TestValidateModulesClean(t *testing.T) {
	t.Parallel()
	root, wd := validateModulesFixture(t, true, true)
	res, err := ValidateTask(root, "T1", ValidateOptions{Dir: root, Workdir: wd})
	if err != nil || len(res.Gates) != 1 || res.Gates[0].RC != 0 {
		t.Fatalf("ValidateTask() = %+v, %v, want one passing gate", res.Gates, err)
	}
	if evs := validateSetupEvents(t, root); len(evs) != 0 {
		t.Errorf("worktree_setup events = %+v, want none", evs)
	}
}
