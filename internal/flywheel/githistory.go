package flywheel

import (
	"errors"
	"os/exec"
	"strings"
)

// gitHistoryState describes the worktree's git history state (issue #314):
// "HEAD=<sha> branch=<ref or (detached)> stash=<sha or (none)>". ok is false
// when dir is not in a git repository, HEAD has no commit yet, or git fails;
// a caller then skips the check. Only an explicit "not there" answer (exit 1
// from a -q query) reads as detached or no stash; any other failure is not
// mistaken for state (#318 review). Read-only git commands only.
func gitHistoryState(dir string) (state string, ok bool) {
	head, absent, err := gitQuery(dir, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil || absent || head == "" {
		return "", false
	}
	branch, absent, err := gitQuery(dir, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return "", false
	}
	if absent || branch == "" {
		branch = "(detached)"
	}
	stash, absent, err := gitQuery(dir, "rev-parse", "--verify", "-q", "refs/stash")
	if err != nil {
		return "", false
	}
	if absent || stash == "" {
		stash = "(none)"
	}
	return "HEAD=" + head + " branch=" + branch + " stash=" + stash, true
}

// gitQuery runs a read-only git query and returns its trimmed output. absent
// is true when git answered "not there" (exit status 1, as -q queries do);
// err is any other failure.
func gitQuery(dir string, args ...string) (out string, absent bool, err error) {
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", true, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(b)), false, nil
}

// gitWriteNote compares dir's git history state with before, the state
// captured at dispatch (issue #314). changed is false when nothing was
// captured (captured false) or nothing moved; a final state that cannot be
// read counts as a change (#318 review). note describes the change.
//
// It compares end points only: a push, or a write undone before the attempt
// ends, leaves them equal. Concurrent attempts in one worktree share HEAD, and
// every worktree of a repository shares refs/stash, so a change is flagged on
// each attempt that overlapped it.
func gitWriteNote(dir, before string, captured bool) (changed bool, note string) {
	if !captured {
		return false, ""
	}
	after, ok := gitHistoryState(dir)
	if !ok {
		after = "(unreadable)"
	}
	if after == before {
		return false, ""
	}
	return true, "git history changed during the attempt: " + before + " -> " + after
}

// joinNote appends extra to note with "; " when both are non-empty.
func joinNote(note, extra string) string {
	switch {
	case extra == "":
		return note
	case note == "":
		return extra
	}
	return note + "; " + extra
}
