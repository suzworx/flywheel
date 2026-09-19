package flywheel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TaskWorktree must be called under the dispatch lock (Run does): its
// existence check, branch lookup and creation are separate git commands.
//
// TaskWorktree returns the absolute path of task's worktree under dir,
// <dir>/.flywheel/worktrees/<task>, creating it when missing with
// `git -C <dir> worktree add -b fw/<task> <path> HEAD` (or, when branch
// fw/<task> already exists, `git -C <dir> worktree add <path> fw/<task>`).
// An existing directory that is a git worktree is reused as is. Errors name
// the git command and its output.
func TaskWorktree(dir, task string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	worktreesDir := filepath.Join(abs, ".flywheel", "worktrees")
	path := filepath.Join(worktreesDir, task)

	// An existing path is reused only when it is a worktree of this same
	// repository with fw/<task> checked out; anything else is refused rather
	// than silently running a worker in the wrong tree (#333 review).
	if _, err := os.Stat(path); err == nil {
		if err := checkTaskWorktree(abs, path, "fw/"+task); err != nil {
			return "", err
		}
		return path, nil
	}

	// Create the worktrees directory if it doesn't exist
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", worktreesDir, err)
	}

	// Check if branch fw/<task> already exists
	branchName := "fw/" + task
	checkBranchCmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "-q", "refs/heads/"+branchName)
	branchExists := checkBranchCmd.Run() == nil

	// Build the worktree add command
	var args []string
	if branchExists {
		// Branch exists, use it
		args = []string{"worktree", "add", path, branchName}
	} else {
		// Branch doesn't exist, create it
		args = []string{"worktree", "add", "-b", branchName, path, "HEAD"}
	}

	// Run git worktree add
	cmd := exec.Command("git", "-C", dir)
	cmd.Args = append([]string{"git", "-C", dir}, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}

	return path, nil
}

// recordedWorkdir returns the Workdir of the latest dispatched event of
// task's current attempt (as currentAttempt resolves it), or "" when none.
func recordedWorkdir(events []Event, task string) string {
	state := Derive(events)
	var ts TaskState
	found := false
	for _, t := range state.Tasks {
		if t.ID == task {
			ts = t
			found = true
			break
		}
	}
	if !found {
		return ""
	}
	attempt := currentAttempt(ts, events)
	if attempt == "" {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Task == task && e.Attempt == attempt && e.Kind == "dispatched" {
			// A worktree removed after landing no longer holds the unit:
			// measure the flywheel root instead, as before --worktree.
			if e.Workdir != "" {
				// Only a worktree that is gone falls back; any other error
				// keeps the recorded path so validation fails loudly there
				// (#333 review).
				if _, err := os.Stat(e.Workdir); os.IsNotExist(err) {
					return ""
				}
			}
			return e.Workdir
		}
	}
	return ""
}

// checkTaskWorktree verifies that path is a git worktree sharing dir's
// repository (the same git common directory) with branch checked out.
func checkTaskWorktree(dir, path, branch string) error {
	common := func(d string) (string, error) {
		out, err := exec.Command("git", "-C", d, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		if err != nil {
			return "", err
		}
		return filepath.Clean(strings.TrimSpace(string(out))), nil
	}
	want, err := common(dir)
	if err != nil {
		return fmt.Errorf("resolve the repository of %s: %w", dir, err)
	}
	got, err := common(path)
	if err != nil || !samePath(got, want) {
		return fmt.Errorf("%s exists but is not a worktree of this repository; remove it (git worktree prune) and rerun", path)
	}
	head, err := exec.Command("git", "-C", path, "symbolic-ref", "-q", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != "refs/heads/"+branch {
		return fmt.Errorf("%s does not have %s checked out; switch it back or remove the worktree and rerun", path, branch)
	}
	return nil
}
