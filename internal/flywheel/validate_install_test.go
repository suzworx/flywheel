package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reinstallFixture is a flywheel dir whose brief declares
// "needs-state: node_modules/ (install)" and one gate, plus an isolated
// workdir holding pnpm-lock.yaml at v1. It swaps installRunner for a fake
// that creates node_modules/ and exits rc, counting calls; callers are not
// parallel: installRunner is a package variable.
func reinstallFixture(t *testing.T, rc int) (dir, wt string, calls *int) {
	t.Helper()
	dir, err := initTaskNeedsState(t, []string{"test -d node_modules"}, "node_modules/ (install)")
	if err != nil {
		t.Fatalf("initTaskNeedsState() error = %v", err)
	}
	wt = t.TempDir()
	initRepo(t, wt)
	if err := os.WriteFile(filepath.Join(wt, "pnpm-lock.yaml"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := 0
	old := installRunner
	t.Cleanup(func() { installRunner = old })
	installRunner = func(dir, wt, task, command string, timeout time.Duration) (int, string, time.Duration, error) {
		n++
		if err := os.MkdirAll(filepath.Join(wt, "node_modules"), 0o755); err != nil {
			return -1, "", 0, err
		}
		return rc, "fake tail", 0, nil
	}
	return dir, wt, &n
}

// lockSum is the install marker line for the workdir's pnpm-lock.yaml.
func lockSum(t *testing.T, wt string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(wt, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return "pnpm-lock.yaml " + hex.EncodeToString(sum[:])
}

// TestValidateReinstallUpToDate checks (issue #769) a validate whose marker
// matches the lockfile and whose path exists runs no install and records no
// worktree_setup event: the first validate installs, the second is a no-op.
// not parallel: installRunner is a package variable.
func TestValidateReinstallUpToDate(t *testing.T) {
	dir, wt, calls := reinstallFixture(t, 0)
	for i := 1; i <= 2; i++ {
		if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wt}); err != nil {
			t.Fatalf("validate %d: ValidateTask() error = %v", i, err)
		}
	}
	if *calls != 1 {
		t.Errorf("installRunner calls = %d, want 1 (the second validate is up to date)", *calls)
	}
	if evs := kindEvents(t, dir, "worktree_setup"); len(evs) != 1 {
		t.Errorf("worktree_setup events = %+v, want only the first validate's", evs)
	}
}

// TestValidateReinstallLockfileChanged checks (issue #769) a lockfile that
// moved after the marker was written re-runs the install once before the
// gates, rewrites the marker and records one worktree_setup event.
// not parallel: installRunner is a package variable.
func TestValidateReinstallLockfileChanged(t *testing.T) {
	dir, wt, calls := reinstallFixture(t, 0)
	if err := os.MkdirAll(filepath.Join(wt, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(wt, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(wt, ".flywheel", "install.sha256")
	if err := os.WriteFile(marker, []byte(lockSum(t, wt)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "pnpm-lock.yaml"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wt})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if *calls != 1 {
		t.Errorf("installRunner calls = %d, want 1", *calls)
	}
	if b, err := os.ReadFile(marker); err != nil || strings.TrimSpace(string(b)) != lockSum(t, wt) {
		t.Errorf("marker = %q, %v, want %q", b, err, lockSum(t, wt))
	}
	evs := kindEvents(t, dir, "worktree_setup")
	if len(evs) != 1 || len(evs[0].Installed) != 1 || evs[0].Install == "" || !strings.HasPrefix(evs[0].Install, "pnpm install") || !strings.Contains(evs[0].Note, "lockfile changed") {
		t.Errorf("worktree_setup events = %+v, want one with Installed, Install and the validate note", evs)
	}
	if len(res.Gates) != 1 || res.Gates[0].RC != 0 {
		t.Errorf("gates = %+v, want one passing gate", res.Gates)
	}
}

// TestValidateReinstallFailureRefuses checks (issue #769) a failed re-install
// refuses with a setup RuleRefusal before any gate: no validated event, and
// the worktree_setup event carries the validate note.
// not parallel: installRunner is a package variable.
func TestValidateReinstallFailureRefuses(t *testing.T) {
	dir, wt, calls := reinstallFixture(t, 1)
	_, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wt})
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "setup" {
		t.Fatalf("ValidateTask() error = %v, want a setup RuleRefusal", err)
	}
	if *calls != 1 {
		t.Errorf("installRunner calls = %d, want 1", *calls)
	}
	if evs := kindEvents(t, dir, "validated"); len(evs) != 0 {
		t.Errorf("validated events = %+v, want none", evs)
	}
	if evs := kindEvents(t, dir, "worktree_setup"); len(evs) != 1 || !strings.Contains(evs[0].Note, "validate: lockfile changed since the last install") || !strings.Contains(evs[0].Note, "fake tail") {
		t.Errorf("worktree_setup events = %+v, want one carrying the validate note and the install tail", evs)
	}
}
