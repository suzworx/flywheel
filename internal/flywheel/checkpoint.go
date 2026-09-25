package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// checkpointReasons are the finished reasons after which an attempt's
// written owned files are snapshotted (issue #422); length is the capped one.
var checkpointReasons = map[string]bool{
	"error": true, "rate-limited": true, "stalled": true, "silent": true,
	"abandoned-job": true, "length": true,
}

// checkpointRef is the ref one attempt's checkpoint lives under.
func checkpointRef(task, attempt string) string {
	return "refs/flywheel/checkpoints/" + task + "/" + attempt
}

// gitWith runs git in wd with env (nil: the process's) and returns trimmed
// stdout; a non-zero exit is an error naming stderr.
func gitWith(wd string, env []string, args ...string) (string, error) {
	rc, stdout, stderr, err := runCmdSplit(wd, gitArgs(args), env)
	if err != nil {
		return "", err
	}
	if rc != 0 {
		return "", fmt.Errorf("git %s failed (rc=%d): %s", args[0], rc, strings.TrimSpace(string(stderr)))
	}
	return strings.TrimSpace(string(stdout)), nil
}

// ownedChanged lists wt's changed paths inside owns, never flywheel's own.
func ownedChanged(wt string, owns []string) ([]string, error) {
	changed, err := changedPaths(wt)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range changed {
		if !isFlywheelOwnPath(p) && ownsContains(owns, p) {
			out = append(out, p)
		}
	}
	return out, nil
}

// checkpointAttempt snapshots paths of wt as a commit on top of HEAD and
// points refs/flywheel/checkpoints/<task>/<attempt> at it. It builds the tree
// in a temporary index (read-tree HEAD, add the paths, write-tree), exactly
// as commitAttempt does, so it never moves a branch or touches the real index.
func checkpointAttempt(wt, task, attempt string, paths []string) (string, error) {
	tmp, err := os.MkdirTemp("", "flywheel-checkpoint-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"),
		"GIT_AUTHOR_NAME="+unitCommitName, "GIT_AUTHOR_EMAIL="+unitCommitEmail,
		"GIT_COMMITTER_NAME="+unitCommitName, "GIT_COMMITTER_EMAIL="+unitCommitEmail)
	head, err := gitWith(wt, env, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	if _, err := gitWith(wt, env, "read-tree", "HEAD"); err != nil {
		return "", err
	}
	if _, err := gitWith(wt, env, append([]string{"--literal-pathspecs", "add", "-A", "--"}, paths...)...); err != nil {
		return "", err
	}
	tree, err := gitWith(wt, env, "write-tree")
	if err != nil {
		return "", err
	}
	sha, err := gitWith(wt, env, "commit-tree", tree, "-p", head, "-m", "checkpoint "+task+" "+attempt)
	if err != nil {
		return "", err
	}
	if _, err := gitWith(wt, env, "update-ref", checkpointRef(task, attempt), sha); err != nil {
		return "", err
	}
	return sha, nil
}

// checkpointUnclean is Run's hook before an unclean finished event: when
// reason is one of checkpointReasons and the attempt wrote files, its changed
// owned paths in wt are checkpointed. A failure is returned as a note for the
// finished event, never an error: a checkpoint never fails the run.
func checkpointUnclean(wt, task, attempt, reason string, wrote, owns []string) (sha, note string) {
	if !checkpointReasons[reason] || len(wrote) == 0 {
		return "", ""
	}
	if _, err := gitCommonDir(wt); err != nil {
		return "", "" // not a git repository: nothing to checkpoint into
	}
	paths, err := ownedChanged(wt, owns)
	if err == nil && len(paths) > 0 {
		sha, err = checkpointAttempt(wt, task, attempt, paths)
	}
	if err != nil {
		return "", clipNote("checkpoint failed: " + err.Error())
	}
	return sha, ""
}

// checkpointLost checkpoints a lost attempt's changed owned paths: the
// process was killed, so no finished event ever ran checkpointUnclean. The
// sha or a failure note goes on the recovered event.
func checkpointLost(dir, task, attempt string) (sha, note string) {
	if _, err := gitCommonDir(dir); err != nil {
		return "", "" // not a git repository: nothing to checkpoint into
	}
	events, err := ReadEvents(dir)
	if err == nil {
		var header BriefHeader
		if header, _, err = AttemptBrief(dir, events, task); err == nil {
			wt := unitWorktree(dir, events, task)
			var paths []string
			if paths, err = ownedChanged(wt, header.Owns); err == nil && len(paths) > 0 {
				sha, err = checkpointAttempt(wt, task, attempt, paths)
			}
		}
	}
	if err != nil {
		return "", clipNote("checkpoint failed: " + err.Error())
	}
	return sha, ""
}

// Checkpoint is one saved snapshot of an interrupted attempt (issue #422):
// the commit refs/flywheel/checkpoints/<task>/<attempt> names and the paths
// it changed against its parent.
type Checkpoint struct {
	Task    string   `json:"task"`
	Attempt string   `json:"attempt"`
	SHA     string   `json:"sha"`
	Paths   []string `json:"paths"`
}

// ListCheckpoints lists task's checkpoints (every task's when task is ""),
// by task then attempt. A directory that is not a git repository has none.
func ListCheckpoints(dir, task string) ([]Checkpoint, error) {
	prefix := "refs/flywheel/checkpoints/"
	if task != "" {
		prefix += task + "/"
	}
	out, err := gitWith(dir, nil, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		if _, gerr := gitCommonDir(dir); gerr != nil {
			return nil, nil
		}
		return nil, err
	}
	var cps []Checkpoint
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(strings.TrimSpace(line), " ")
		rest, _ := strings.CutPrefix(ref, "refs/flywheel/checkpoints/")
		t, a, ok2 := strings.Cut(rest, "/")
		if !ok || !ok2 {
			continue
		}
		cp := Checkpoint{Task: t, Attempt: a, SHA: sha}
		names, err := gitWith(dir, nil, "diff-tree", "-r", "--no-renames", "--name-only", "-z", sha+"^", sha)
		if err != nil {
			return nil, err
		}
		for _, p := range strings.Split(names, "\x00") {
			if p != "" {
				cp.Paths = append(cp.Paths, filepath.ToSlash(p))
			}
		}
		cps = append(cps, cp)
	}
	slices.SortFunc(cps, func(x, y Checkpoint) int {
		if c := strings.Compare(x.Task, y.Task); c != 0 {
			return c
		}
		return compareAttempt(x.Attempt, y.Attempt)
	})
	return cps, nil
}

// findCheckpoint returns task's checkpoint of attempt, or its latest when
// attempt is "".
func findCheckpoint(dir, task, attempt string) (Checkpoint, error) {
	cps, err := ListCheckpoints(dir, task)
	if err != nil {
		return Checkpoint{}, err
	}
	for i := len(cps) - 1; i >= 0; i-- {
		if attempt == "" || cps[i].Attempt == attempt {
			return cps[i], nil
		}
	}
	return Checkpoint{}, fmt.Errorf("no checkpoint for %s %s", task, attempt)
}

// DiffCheckpoint is `git diff <checkpoint> -- <paths>` in the unit's
// worktree: how the worktree differs from what the attempt had written.
func DiffCheckpoint(dir, task, attempt string) (string, error) {
	cp, err := findCheckpoint(dir, task, attempt)
	if err != nil {
		return "", err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return "", err
	}
	return gitWith(unitWorktree(dir, events, task), nil, append([]string{"--literal-pathspecs", "diff", cp.SHA, "--"}, cp.Paths...)...)
}

// RestoreCheckpoint writes the checkpoint's files into the unit's worktree,
// through a temporary index (the worktree's own index and branch are never
// touched); a path the checkpoint deleted is removed. It refuses when any of
// those paths has uncommitted changes, unless force. It returns the paths.
func RestoreCheckpoint(dir, task, attempt string, force bool) ([]string, error) {
	cp, err := findCheckpoint(dir, task, attempt)
	if err != nil {
		return nil, err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	wt := unitWorktree(dir, events, task)
	changed, err := changedPaths(wt)
	if err != nil {
		return nil, err
	}
	if dirty := slices.DeleteFunc(changed, func(p string) bool { return !slices.Contains(cp.Paths, p) }); len(dirty) > 0 && !force {
		return nil, fmt.Errorf("%s has uncommitted changes to %s; commit or discard them, or pass --force", wt, strings.Join(dirty, ", "))
	}
	tmp, err := os.MkdirTemp("", "flywheel-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
	if _, err := gitWith(wt, env, "read-tree", cp.SHA); err != nil {
		return nil, err
	}
	for _, p := range cp.Paths {
		if _, err := gitWith(wt, env, "cat-file", "-e", cp.SHA+":"+p); err != nil {
			if rerr := os.Remove(filepath.Join(wt, filepath.FromSlash(p))); rerr != nil && !os.IsNotExist(rerr) {
				return nil, rerr
			}
			continue
		}
		if _, err := gitWith(wt, env, "checkout-index", "-f", "--", p); err != nil {
			return nil, err
		}
	}
	return cp.Paths, nil
}

// DropCheckpoint deletes the checkpoint ref of task's attempt.
func DropCheckpoint(dir, task, attempt string) error {
	cp, err := findCheckpoint(dir, task, attempt)
	if err != nil {
		return err
	}
	_, err = gitWith(dir, nil, "update-ref", "-d", checkpointRef(cp.Task, cp.Attempt), cp.SHA)
	return err
}

// unitWorktree is where task's work lives: its task worktree, else the
// recorded workdir of its current attempt, else the flywheel root.
func unitWorktree(dir string, events []Event, task string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	if wt := filepath.Join(abs, ".flywheel", "worktrees", task); dirExists(wt) {
		return wt
	}
	if rw := recordedWorkdir(events, task); rw != "" {
		return rw
	}
	return abs
}
