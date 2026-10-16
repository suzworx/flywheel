package flywheel

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// unlinkLinks removes every symlink or directory junction inside wt as a link,
// never touching what it points at, and returns the removed paths relative to
// wt (slash-separated, walk order). It never descends into a link or into a
// .git directory. A junction is a mount-point reparse point, which Go reports
// as ModeIrregular rather than ModeSymlink, so both bits count as a link.
// Issue #822: git worktree remove follows junctions on Windows and deletes
// the linked main-checkout files; os.Remove on the link itself does not.
// A link git tracks (git ls-files) is left in place: git removes it as a link
// itself, and deleting it would leave the worktree modified, so a non-force
// remove would refuse. A missing wt has nothing to unlink and runs no git.
func unlinkLinks(wt string) ([]string, error) {
	if _, err := os.Lstat(wt); os.IsNotExist(err) {
		return nil, nil // git reports the missing tree
	}
	out, err := gitRead(wt, []string{"ls-files", "-z"})
	if err != nil {
		return nil, fmt.Errorf("list tracked files in %s: %w", wt, err)
	}
	tracked := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			tracked[f] = true
		}
	}
	var removed []string
	err = filepath.WalkDir(wt, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", p, err)
		}
		if p == wt {
			return nil
		}
		if d.Type()&(fs.ModeSymlink|fs.ModeIrregular) == 0 {
			if d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		fi, lerr := os.Lstat(p)
		if lerr != nil {
			return fmt.Errorf("lstat %s: %w", p, lerr)
		}
		rel, _ := filepath.Rel(wt, p)
		rel = filepath.ToSlash(rel)
		if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 && !tracked[rel] {
			if rerr := os.Remove(p); rerr != nil {
				return fmt.Errorf("unlink %s: %w", p, rerr)
			}
			removed = append(removed, rel)
		}
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	return removed, err
}

// removeWorktree removes the git worktree wt of the repository at dir with
// git worktree remove [--force], run through the caller's git runner, after
// unlinking every link inside it (issue #822: git follows junctions on
// Windows and would delete the linked main-checkout files). An unlink error
// is returned without running git, so a link left in place never reaches git.
func removeWorktree(dir, wt string, force bool, run func(wd string, args []string) (string, error)) (string, error) {
	if _, err := unlinkLinks(wt); err != nil {
		return "", err
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	return run(dir, append(args, wt))
}

// WorktreeRemoveOptions are `flywheel worktree remove`'s choices (issue #851).
type WorktreeRemoveOptions struct {
	Discard bool   // drop a dirty tree's edits, on the record
	Session string // the lead session; required with Discard
	Note    string
}

// WorktreeRemoveResult is what WorktreeRemove removed: the absolute worktree
// and, after a discard, the dirty paths it dropped.
type WorktreeRemoveResult struct {
	Path      string
	Discarded []string
}

// sameWorktreePath reports whether two absolute paths name one directory:
// cleaned, links resolved where possible, case-insensitive on Windows.
func sameWorktreePath(a, b string) bool {
	norm := func(p string) string {
		p = filepath.Clean(p)
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		if runtime.GOOS == "windows" {
			p = strings.ToLower(p)
		}
		return p
	}
	return norm(a) == norm(b)
}

// worktreeDirty returns the status code and path of every changed or
// untracked (never ignored) path in wt, in git status order.
func worktreeDirty(wt string) ([][2]string, error) {
	out, err := gitRead(wt, []string{"status", "--porcelain=v1", "-z", "--untracked-files=all"})
	if err != nil {
		return nil, err
	}
	var dirty [][2]string
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		dirty = append(dirty, [2]string{f[:2], f[3:]})
		if f[0] == 'R' || f[0] == 'C' {
			i++ // the next field is the rename's or copy's source
		}
	}
	return dirty, nil
}

// WorktreeRemove removes the linked worktree path (relative to the cwd) of
// the repository at dir (issue #851). The main checkout and any path that is
// not one of dir's worktrees are refused. A dirty tree is a RuleRefusal
// worktree-dirty naming every dirty path, unless o.Discard: then a
// worktree_discarded event is appended first, and the tree is force-removed
// only after that append succeeds.
func WorktreeRemove(dir, path string, o WorktreeRemoveOptions) (WorktreeRemoveResult, error) {
	wt, err := filepath.Abs(path)
	if err != nil {
		return WorktreeRemoveResult{}, err
	}
	res := WorktreeRemoveResult{Path: wt}
	if o.Discard && o.Session == "" {
		return res, fmt.Errorf("--discard needs --session")
	}
	list, err := gitRead(dir, []string{"worktree", "list", "--porcelain"})
	if err != nil {
		return res, err
	}
	found, first := false, true
	for _, line := range strings.Split(strings.ReplaceAll(list, "\r\n", "\n"), "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if sameWorktreePath(p, wt) {
			if first {
				return res, fmt.Errorf("%s is the main checkout of %s, not a linked worktree: refusing to remove it", wt, dir)
			}
			found = true
		}
		first = false
	}
	if !found {
		return res, fmt.Errorf("%s is not a linked worktree of %s (git worktree list): nothing removed", wt, dir)
	}
	dirty, err := worktreeDirty(wt)
	if err != nil {
		return res, err
	}
	if len(dirty) == 0 {
		if out, err := removeWorktree(dir, wt, false, gitRead); err != nil {
			return res, fmt.Errorf("%w: %s", err, out)
		}
		return res, nil
	}
	var lines []string
	for _, d := range dirty {
		res.Discarded = append(res.Discarded, d[1])
		lines = append(lines, "  "+d[0]+" "+d[1])
	}
	if !o.Discard {
		return WorktreeRemoveResult{Path: wt}, &RuleRefusal{Rule: "worktree-dirty", Fix: fmt.Sprintf(
			"%s has %d uncommitted path(s):\n%s\ncommit, hand off or save the edits (git -C %s diff), delete any copied secrets, then rerun; or pass --discard --session S to drop them on the record",
			wt, len(dirty), strings.Join(lines, "\n"), wt)}
	}
	branch, berr := gitRead(wt, []string{"symbolic-ref", "--short", "-q", "HEAD"})
	if berr != nil || strings.TrimSpace(branch) == "" {
		branch = "(detached)"
	}
	note := "branch " + strings.TrimSpace(branch)
	if o.Note != "" {
		note += "; " + o.Note
	}
	if err := AppendEvent(dir, Event{Kind: "worktree_discarded", Workdir: wt, Commit: headCommit(wt),
		Paths: res.Discarded, Session: o.Session, Note: note}); err != nil {
		return WorktreeRemoveResult{Path: wt}, fmt.Errorf("record the discard (nothing removed): %w", err)
	}
	if out, err := removeWorktree(dir, wt, true, gitRead); err != nil {
		return res, fmt.Errorf("%w: %s", err, out)
	}
	return res, nil
}
