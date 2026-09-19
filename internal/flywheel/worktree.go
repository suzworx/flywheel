package flywheel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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

	// Check if the path already exists as a git worktree
	if _, err := os.Stat(path); err == nil {
		// Directory exists; assume it's already a worktree
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
				if _, err := os.Stat(e.Workdir); err != nil {
					return ""
				}
			}
			return e.Workdir
		}
	}
	return ""
}
