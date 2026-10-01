package flywheel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// slotsDirName is the directory under the git common dir that holds one lease
// file per unit slot (issue #697).
const slotsDirName = "flywheel-slots"

// LeaseSlot returns the unit slot of the working tree tree (issue #697): a
// small integer, unique among the repository's working trees, that a project
// derives ports and database names from (FLYWHEEL_SLOT). Leases live in
// <git common dir>/flywheel-slots/<n>, each holding the tree's path, so every
// worktree of the repository sees every lease. A tree reuses its lease for as
// long as it exists; a lease whose tree directory is gone is reclaimed. A new
// tree takes the lowest free n from 1. The lease is keyed by the working
// tree's top level, so run, setup and validate agree on one slot per tree
// even from a subdirectory. A tree outside a repository has no slot and
// returns (0, nil).
func LeaseSlot(tree string) (int, error) {
	abs, common, ok := slotTree(tree)
	if !ok {
		return 0, nil
	}
	dir := filepath.Join(common, slotsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("unit slot: %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("unit slot: %s: %w", dir, err)
	}
	for _, e := range entries {
		n, err := strconv.Atoi(e.Name())
		if err != nil || n < 1 || e.IsDir() {
			continue
		}
		if recorded, ok := readLease(filepath.Join(dir, e.Name())); ok && sameTree(recorded, abs) {
			return n, nil
		}
	}
	for n := 1; ; n++ {
		p := filepath.Join(dir, strconv.Itoa(n))
		ok, err := createLease(p, abs)
		if err != nil {
			return 0, fmt.Errorf("unit slot: %s: %w", dir, err)
		}
		if ok {
			return n, nil
		}
		recorded, ok := readLease(p)
		if !ok || recorded == "" {
			continue
		}
		if _, err := os.Stat(recorded); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if !reclaimLease(p, recorded) {
			continue
		}
		ok, err = createLease(p, abs)
		if err != nil {
			return 0, fmt.Errorf("unit slot: %s: %w", dir, err)
		}
		if ok {
			return n, nil
		}
	}
}

// slotTree returns the clean top level of the working tree holding p and the
// repository's absolute git common dir, from one git process (a gate leases
// on every run, so it keeps the git process budget); false outside a working
// tree.
func slotTree(p string) (top, common string, ok bool) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", "", false
	}
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir").Output()
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(out), "\r\n", "\n")), "\n")
	if len(lines) != 2 || lines[0] == "" || lines[1] == "" {
		return "", "", false
	}
	return filepath.Clean(lines[0]), filepath.Clean(lines[1]), true
}

// reclaimLease removes the lease file p whose recorded tree gone was read
// earlier and no longer exists; true when p is now free for createLease. Only
// one racer wins the rename to a unique stale name. Between the read and the
// rename another racer may already have reclaimed p and created its own live
// lease there, so the stale file is read again: when it no longer records gone
// it is that racer's live lease and is put back with os.Link (exclusive: it
// fails when p exists), and the caller moves on to n+1. Only a stale file
// still recording gone is deleted.
func reclaimLease(p, gone string) bool {
	stale := fmt.Sprintf("%s.stale-%d-%d", p, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(p, stale); err != nil {
		return false
	}
	if recorded, ok := readLease(stale); !ok || !sameTree(recorded, gone) {
		if err := os.Link(stale, p); err == nil {
			os.Remove(stale)
		}
		return false
	}
	os.Remove(stale)
	return true
}

// createLease creates the lease file p holding tree exclusively; false, nil
// when p already exists.
func createLease(p, tree string) (bool, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	_, werr := f.WriteString(tree + "\n")
	cerr := f.Close()
	if werr != nil {
		return false, werr
	}
	if cerr != nil {
		return false, cerr
	}
	return true, nil
}

// readLease returns the tree path recorded in the lease file p.
func readLease(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// sameTree compares two clean absolute paths, case-insensitively on Windows.
func sameTree(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// slotEnv adds FLYWHEEL_SLOT=<slot> to env (issue #697); slot 0 (no slot)
// returns env unchanged. A nil env starts from os.Environ() so the caller's
// process environment is still inherited.
func slotEnv(env []string, slot int) []string {
	if slot == 0 {
		return env
	}
	if env == nil {
		env = os.Environ()
	}
	return append(env, "FLYWHEEL_SLOT="+strconv.Itoa(slot))
}
