package flywheel

import (
	"strings"
	"testing"
)

// TestDefaultDenyReadOnlyGit: the default deny list refuses every git write the
// PATH git guard refuses, and lets through the read-only stash and worktree
// forms the guard allows (issue #916).
func TestDefaultDenyReadOnlyGit(t *testing.T) {
	t.Parallel()
	matched := func(cmd string) string {
		for _, p := range defaultDisallowedTools {
			if denyMatches(p, cmd) {
				return p
			}
		}
		return ""
	}
	readOnly := []string{
		"git stash list",
		"git stash show",
		"git stash show -p stash@{0}",
		"git worktree list",
		"git worktree list --porcelain",
	}
	for _, cmd := range readOnly {
		if p := matched(cmd); p != "" {
			t.Errorf("%q is read-only but %q denies it", cmd, p)
		}
		if refused, _ := GitGuardRefused(strings.Fields(cmd)[1:]); refused {
			t.Errorf("%q: the git guard refuses it", cmd)
		}
	}
	writes := []string{
		"git stash",
		"git stash push -m x",
		"git stash -u",
		"git stash pop",
		"git stash apply",
		"git stash drop",
		"git stash clear",
		"git stash --include-untracked",
		"git worktree add ../x",
		"git worktree remove x",
		"git worktree prune",
		"git commit -m x",
		"git reset --hard",
	}
	for _, cmd := range writes {
		if matched(cmd) == "" {
			t.Errorf("%q writes but no default deny pattern matches it", cmd)
		}
		if refused, _ := GitGuardRefused(strings.Fields(cmd)[1:]); !refused {
			t.Errorf("%q: the git guard allows it", cmd)
		}
	}
}
