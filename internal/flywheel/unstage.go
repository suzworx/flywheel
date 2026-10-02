package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UnstageOptions configures Unstage. Workdir is the tree holding the staging
// copies; empty means the task's recorded workdir, else dir.
type UnstageOptions struct {
	Workdir string
}

// Unstage removes the staging copies flywheel validate applied for task
// (issue #781): every source file of the task's latest staged event, then the
// now-empty directories under each stage: line's From (From included), and
// records an unstaged event. Each destination must hold the recorded bytes
// and each source the same bytes or be gone already (an interrupted earlier
// run removed it), or it refuses with rule stage before removing anything.
// With every source gone and an unstaged event newer than
// the latest staged one it returns the files and records nothing.
func Unstage(dir, task string, o UnstageOptions) ([]StagedFile, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	wd := o.Workdir
	if wd == "" {
		if wd = recordedWorkdir(events, task); wd == "" {
			wd = dir
		}
	}
	wd = absPath(wd)
	latest, unstaged := -1, false
	for i, e := range events {
		switch {
		case e.Task != task:
		case e.Kind == "staged":
			latest, unstaged = i, false
		case e.Kind == "unstaged" && latest >= 0:
			unstaged = true
		}
	}
	if latest < 0 {
		return nil, fmt.Errorf("%s has no staged event; run flywheel validate first", task)
	}
	files := events[latest].Staged
	abs := func(p string) string { return filepath.Join(wd, filepath.FromSlash(p)) }
	if unstaged {
		gone := true
		for _, f := range files {
			if _, err := os.Lstat(abs(f.From)); !os.IsNotExist(err) {
				gone = false
			}
		}
		if gone {
			return files, nil
		}
	}
	for _, f := range files {
		for _, p := range []string{f.To, f.From} {
			what := stagedDiff(abs(p), f.SHA256)
			if p == f.From && what == "is missing" {
				// A source already removed by an earlier, interrupted run: its
				// destination (checked first) still holds the recorded bytes.
				what = ""
			}
			if what != "" {
				return nil, &RuleRefusal{Rule: "stage", Fix: fmt.Sprintf("%s %s: staged content changed since validate measured it: re-run flywheel validate", p, what)}
			}
		}
	}
	for _, f := range files {
		if err := os.Remove(abs(f.From)); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("unstage %s: %w", f.From, err)
		}
	}
	var roots []string
	if header, _, err := AttemptBrief(dir, events, task); err == nil {
		for _, m := range header.Stage {
			roots = append(roots, m.From)
		}
	}
	for _, f := range files {
		if err := removeEmptyUnder(wd, f.From, roots); err != nil {
			return nil, err
		}
	}
	ev := Event{Task: task, Kind: "unstaged", Staged: files, Workdir: workdirField(wd, dir)}
	if err := AppendEvent(dir, ev); err != nil {
		return nil, err
	}
	return files, nil
}

// stagedDiff says how the file at p differs from the recorded hex SHA-256
// sum, or "" when it holds those bytes.
func stagedDiff(p, sum string) string {
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "is missing"
	}
	if err != nil {
		return fmt.Sprintf("is unreadable (%v)", err)
	}
	if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != sum {
		return "has different content"
	}
	return ""
}

// removeEmptyUnder removes, from the parent of the repo-relative file upward,
// each empty directory up to and including the stage root (a From in roots)
// holding it. A file under no root leaves every directory alone.
func removeEmptyUnder(wd, file string, roots []string) error {
	root := ""
	for _, r := range roots {
		if strings.HasPrefix(file, r) {
			root = strings.TrimSuffix(r, "/")
		}
	}
	for d := filepath.ToSlash(filepath.Dir(filepath.FromSlash(file))); root != "" && strings.HasPrefix(d+"/", root+"/"); d = filepath.ToSlash(filepath.Dir(filepath.FromSlash(d))) {
		p := filepath.Join(wd, filepath.FromSlash(d))
		entries, err := os.ReadDir(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || len(entries) > 0 {
			return err
		}
		if err := os.Remove(p); err != nil {
			return fmt.Errorf("unstage remove %s: %w", d, err)
		}
	}
	return nil
}
