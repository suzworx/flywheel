package flywheel

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
