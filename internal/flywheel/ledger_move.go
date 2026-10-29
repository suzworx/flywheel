package flywheel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ledgerMoveAside, under .flywheel/, holds the in-tree log while the moved
// copy's chain is verified; a crashed move's aside is put back on the next run.
const ledgerMoveAside = "ledger-move.old"

// LedgerMoveResult is what flywheel ledger move did (issue #464).
type LedgerMoveResult struct {
	Source      string   `json:"source"`
	Destination string   `json:"destination"`
	Files       []string `json:"files"`
	Bytes       int64    `json:"bytes"`
	Moved       bool     `json:"moved"`
	Resumed     bool     `json:"resumed"`
	ChainOK     bool     `json:"chain_ok"`
	Tracked     bool     `json:"tracked"`
	Note        string   `json:"note,omitempty"`
}

// LedgerUsageError is a LedgerMove error the CLI reports as a usage error
// (exit 2): ledger.dir unset or refused.
type LedgerUsageError struct{ Err error }

func (e *LedgerUsageError) Error() string { return e.Err.Error() }
func (e *LedgerUsageError) Unwrap() error { return e.Err }

// LedgerMove moves the in-tree event log of dir (.flywheel/events.jsonl and
// .flywheel/events/) into ledger.dir (issue #464): under the in-tree
// events.lock it copies every file into a temp dir beside the destination,
// fsyncs and compares each copy, renames the temp dir onto the destination,
// verifies the chain there and only then removes the in-tree files (the
// working tree only; git is never written). A destination already holding
// identical copies resumes a crashed move; any other log there is a
// RuleRefusal "two-live-logs" that leaves both untouched.
func LedgerMove(dir string) (LedgerMoveResult, error) {
	src := filepath.Join(dir, ".flywheel")
	res := LedgerMoveResult{Source: src, Files: []string{}}
	root, external, err := resolveLedgerDir(dir, false)
	if err != nil {
		return res, &LedgerUsageError{err}
	}
	if !external {
		return res, &LedgerUsageError{fmt.Errorf("ledger.dir is not set: set config ledger.dir (or %s) first", ledgerDirEnv)}
	}
	res.Destination = root
	aside := filepath.Join(src, ledgerMoveAside)
	if !anyExists(filepath.Join(src, legacyLogName), filepath.Join(src, shardDirName), aside) {
		res.Note = "nothing to move: no in-tree log under " + src
		return res, nil
	}
	release, err := acquireRepoLock(dir, "events.lock", eventsLockTimings())
	if err != nil {
		return res, err
	}
	defer release()
	if err := restoreLedgerAside(src, aside); err != nil {
		return res, err
	}
	files, size, err := inTreeLogFiles(src)
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		res.Note = "nothing to move: no in-tree log under " + src
		return res, nil
	}
	res.Files, res.Bytes = files, size
	state, err := ledgerDestState(root, src, files)
	if err != nil {
		return res, err
	}
	switch state {
	case destRefuse:
		return res, &RuleRefusal{Rule: "two-live-logs", Fix: fmt.Sprintf("%s already holds a different event log or other files; the in-tree log under %s and %s were left untouched: reconcile them by hand, then run flywheel ledger move again", root, src, root)}
	case destResume:
		res.Resumed = true
	default:
		if err := installLedgerCopy(src, root, files, state == destLocksOnly); err != nil {
			return res, err
		}
	}
	res.Tracked = ledgerLogTracked(dir)
	// Set the in-tree log aside, so VerifyLogChain resolves the destination.
	if err := os.Mkdir(aside, 0o755); err != nil {
		return res, fmt.Errorf("create %s: %w", aside, err)
	}
	for _, name := range []string{legacyLogName, shardDirName} {
		if !anyExists(filepath.Join(src, name)) {
			continue
		}
		if err := os.Rename(filepath.Join(src, name), filepath.Join(aside, name)); err != nil {
			rerr := restoreLedgerAside(src, aside)
			return res, errors.Join(fmt.Errorf("set aside %s: %w", filepath.Join(src, name), err), rerr)
		}
	}
	chain, err := VerifyLogChain(dir)
	if err != nil {
		rerr := restoreLedgerAside(src, aside)
		return res, errors.Join(fmt.Errorf("verify the moved log in %s: %w; the in-tree log is kept", root, err), rerr)
	}
	if err := os.RemoveAll(aside); err != nil {
		return res, fmt.Errorf("remove the moved in-tree log %s: %w", aside, err)
	}
	res.Moved, res.ChainOK = true, chain.OK()
	return res, nil
}

// anyExists reports whether any of paths exists (Lstat).
func anyExists(paths ...string) bool {
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}

// restoreLedgerAside puts a set-aside in-tree log back under src and removes
// the aside dir; no aside dir is no change.
func restoreLedgerAside(src, aside string) error {
	if !anyExists(aside) {
		return nil
	}
	for _, name := range []string{legacyLogName, shardDirName} {
		from, to := filepath.Join(aside, name), filepath.Join(src, name)
		if !anyExists(from) {
			continue
		}
		if anyExists(to) {
			return fmt.Errorf("both %s and %s exist: keep one by hand", from, to)
		}
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("restore %s: %w", to, err)
		}
	}
	if err := os.Remove(aside); err != nil {
		return fmt.Errorf("remove %s: %w", aside, err)
	}
	return nil
}

// inTreeLogFiles lists the event-log files under src (events.jsonl and every
// file under events/, *.tmp skipped) as sorted slash paths relative to src,
// with their total size. A symlink or an unexpected file type is an error.
func inTreeLogFiles(src string) ([]string, int64, error) {
	var files []string
	var size int64
	add := func(path string, info fs.FileInfo) error {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file (%s): refusing to move it", path, info.Mode().Type())
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		files, size = append(files, filepath.ToSlash(rel)), size+info.Size()
		return nil
	}
	legacy := filepath.Join(src, legacyLogName)
	if info, err := os.Lstat(legacy); err == nil {
		if err := add(legacy, info); err != nil {
			return nil, 0, err
		}
	} else if !os.IsNotExist(err) {
		return nil, 0, err
	}
	shards := filepath.Join(src, shardDirName)
	if info, err := os.Lstat(shards); err == nil {
		if !info.IsDir() {
			return nil, 0, fmt.Errorf("%s is not a directory: refusing to move it", shards)
		}
		err := filepath.WalkDir(shards, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() || strings.HasSuffix(d.Name(), ".tmp") && d.Type().IsRegular() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return add(path, info)
		})
		if err != nil {
			return nil, 0, err
		}
	} else if !os.IsNotExist(err) {
		return nil, 0, err
	}
	sort.Strings(files)
	return files, size, nil
}

// The destination states ledgerDestState tells apart.
const (
	destMissing   = iota // no destination directory
	destEmpty            // an empty directory
	destLocksOnly        // a directory holding only locks/
	destResume           // byte-identical copies of every in-tree file
	destRefuse           // any other log or file
)

// ledgerDestState classifies root against the in-tree files of src.
func ledgerDestState(root, src string, files []string) (int, error) {
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return destMissing, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return destRefuse, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	locks, others := false, 0
	for _, e := range entries {
		if e.Name() == "locks" && e.IsDir() {
			locks = true
		} else {
			others++
		}
	}
	switch {
	case others == 0 && locks:
		return destLocksOnly, nil
	case others == 0:
		return destEmpty, nil
	}
	var held []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir() && rel == "locks":
			return filepath.SkipDir
		case d.IsDir() || d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".tmp"):
			return nil
		}
		held = append(held, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Strings(held)
	if len(held) != len(files) {
		return destRefuse, nil
	}
	for i, f := range files {
		if held[i] != f {
			return destRefuse, nil
		}
		if same, err := sameFileBytes(filepath.Join(src, filepath.FromSlash(f)), filepath.Join(root, filepath.FromSlash(f))); err != nil || !same {
			return destRefuse, err
		}
	}
	return destResume, nil
}

// sameFileBytes reports whether a and b are regular files with equal bytes.
func sameFileBytes(a, b string) (bool, error) {
	if info, err := os.Lstat(b); err != nil || !info.Mode().IsRegular() {
		return false, nil
	}
	x, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	y, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(x, y), nil
}

// installLedgerCopy copies files from src into a temp dir beside root,
// fsyncs and compares each copy, then renames the temp dir onto root (an
// empty root is removed first). locksOnly, root keeps its locks/ and each
// top-level entry of the copy is renamed into it. The temp dir never outlives
// the call.
func installLedgerCopy(src, root string, files []string, locksOnly bool) error {
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(root)+".move-")
	if err != nil {
		return fmt.Errorf("create a temp dir in %s: %w", parent, err)
	}
	defer os.RemoveAll(tmp)
	for _, f := range files {
		from, to := filepath.Join(src, filepath.FromSlash(f)), filepath.Join(tmp, filepath.FromSlash(f))
		if err := copyFileSynced(from, to); err != nil {
			return err
		}
		if same, err := sameFileBytes(from, to); err != nil || !same {
			return errors.Join(fmt.Errorf("the copy %s differs from %s", to, from), err)
		}
	}
	if !locksOnly {
		if err := os.Remove(root); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove the empty %s: %w", root, err)
		}
		if err := os.Rename(tmp, root); err != nil {
			return fmt.Errorf("rename %s to %s: %w", tmp, root, err)
		}
		return nil
	}
	for _, name := range []string{shardDirName, legacyLogName} {
		if !anyExists(filepath.Join(tmp, name)) {
			continue
		}
		if err := os.Rename(filepath.Join(tmp, name), filepath.Join(root, name)); err != nil {
			return fmt.Errorf("rename %s into %s: %w", name, root, err)
		}
	}
	return nil
}

// copyFileSynced copies from to a new file to (creating its directory) and
// fsyncs it.
func copyFileSynced(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s: %w", from, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return fmt.Errorf("fsync %s: %w", to, err)
	}
	return out.Close()
}

// ledgerLogTracked reports whether git tracks any in-tree event-log path of
// dir: a read-only ls-files query, false outside git or on any git error.
func ledgerLogTracked(dir string) bool {
	out, err := exec.Command("git", "-C", dir, "ls-files", "--", ".flywheel/"+legacyLogName, ".flywheel/"+shardDirName).Output()
	return err == nil && len(bytes.TrimSpace(out)) > 0
}
