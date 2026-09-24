package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Flywheel commits as this identity: the attempt's work is the worker's, the
// commit is the framework's (#391).
const (
	unitCommitName  = "flywheel"
	unitCommitEmail = "flywheel@localhost"
)

// commitAttempt commits one attempt's owned changes on its task branch
// fw/<task> (issue #391). It acts only when wt is the task worktree
// `flywheel run --worktree` made (<dir>/.flywheel/worktrees/<task> with
// fw/<task> checked out); anywhere else it returns "", nil, nil.
//
// The commit is built in a temporary index (GIT_INDEX_FILE in a temp dir,
// never the worktree's own): read-tree HEAD, then every changed path inside
// owns that is not flywheel's own bookkeeping; the other changed paths are
// returned as outside and left as they are. Nothing to commit returns "",
// outside, nil. The commit ("<task> <attempt>" with a Flywheel-Task trailer,
// authored by flywheel) moves fw/<task> by compare-and-swap, and the
// worktree's index is then reset to HEAD for the committed paths so git
// status is clean for them.
func commitAttempt(dir, wt, task, attempt string, owns []string) (sha string, outside []string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	branch := "fw/" + task
	if !samePath(wt, filepath.Join(abs, ".flywheel", "worktrees", task)) || checkTaskWorktree(abs, wt, branch) != nil {
		return "", nil, nil
	}
	changed, err := changedPaths(wt)
	if err != nil {
		return "", nil, err
	}
	var owned []string
	for _, p := range changed {
		switch {
		case isFlywheelOwnPath(p):
		case ownsContains(owns, p):
			owned = append(owned, p)
		default:
			outside = append(outside, p)
		}
	}
	if len(owned) == 0 {
		return "", outside, nil
	}

	tmp, err := os.MkdirTemp("", "flywheel-index-")
	if err != nil {
		return "", outside, err
	}
	defer os.RemoveAll(tmp)
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"),
		"GIT_AUTHOR_NAME="+unitCommitName, "GIT_AUTHOR_EMAIL="+unitCommitEmail,
		"GIT_COMMITTER_NAME="+unitCommitName, "GIT_COMMITTER_EMAIL="+unitCommitEmail)
	git := func(env []string, args ...string) (string, error) {
		rc, stdout, stderr, err := runCmdSplit(wt, gitArgs(args), env)
		if err != nil {
			return "", err
		}
		if rc != 0 {
			return "", fmt.Errorf("git %s failed (rc=%d): %s", args[0], rc, strings.TrimSpace(string(stderr)))
		}
		return strings.TrimSpace(string(stdout)), nil
	}

	old, err := git(env, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", outside, err
	}
	if _, err := git(env, "read-tree", "HEAD"); err != nil {
		return "", outside, err
	}
	// -A stages deletions too; literal pathspecs keep names like "a*b" exact.
	if _, err := git(env, append([]string{"--literal-pathspecs", "add", "-A", "--"}, owned...)...); err != nil {
		return "", outside, err
	}
	tree, err := git(env, "write-tree")
	if err != nil {
		return "", outside, err
	}
	if headTree, err := git(env, "rev-parse", "HEAD^{tree}"); err == nil && headTree == tree {
		return "", outside, nil
	}
	sha, err = git(env, "commit-tree", tree, "-p", old, "-m", task+" "+attempt, "-m", "Flywheel-Task: "+task)
	if err != nil {
		return "", outside, err
	}
	if _, err := git(env, "update-ref", "refs/heads/"+branch, sha, old); err != nil {
		return "", outside, err
	}
	// The worktree's own index, by flywheel itself (never the worker's guard).
	if _, err := git(nil, append([]string{"--literal-pathspecs", "reset", "-q", "--"}, owned...)...); err != nil {
		return sha, outside, fmt.Errorf("committed %s but refreshing the index failed: %w", sha, err)
	}
	return sha, outside, nil
}
