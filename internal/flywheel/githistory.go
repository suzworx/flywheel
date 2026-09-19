package flywheel

import (
	"os/exec"
	"strings"
)

// gitHistoryState describes the worktree's git history state (issue #314):
// "HEAD=<sha> branch=<ref or (detached)> stash=<sha or (none)>". ok is false
// when dir is not in a git repository or HEAD has no commit yet; a caller
// then skips the check. Read-only git commands only.
func gitHistoryState(dir string) (state string, ok bool) {
	head := gitCmd(dir, "rev-parse", "--verify", "-q", "HEAD")
	if head == "" {
		return "", false
	}

	branch := gitCmd(dir, "symbolic-ref", "-q", "HEAD")
	if branch == "" {
		branch = "(detached)"
	}

	stash := gitCmd(dir, "rev-parse", "--verify", "-q", "refs/stash")
	if stash == "" {
		stash = "(none)"
	}

	return "HEAD=" + head + " branch=" + branch + " stash=" + stash, true
}

// gitCmd runs a read-only git command and returns trimmed output or "".
func gitCmd(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
