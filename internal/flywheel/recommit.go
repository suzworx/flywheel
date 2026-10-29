package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// WorktreeCommitOptions are `flywheel worktree commit`'s inputs: the lead
// session (required) and an optional note, both recorded on the event.
type WorktreeCommitOptions struct {
	Session string
	Note    string
}

// WorktreeCommitResult is what one recommit did: the attempt it committed
// for, the commit on fw/<task> ("" when nothing was committed) and the
// changed paths still outside owns.
type WorktreeCommitResult struct {
	Attempt     string
	Commit      string
	Uncommitted []string
}

// measuredOwns is the owns validate measures a task against (issue #781):
// owns plus each staged destination whose source owns covers, from this
// pass's staged files and every one the task's staged and unstaged events
// recorded. validate and WorktreeCommit share it (issue #897), so the owns a
// recommit commits against never drift from the owns the check reads.
func measuredOwns(owns []string, staged []StagedFile, events []Event, task string) []string {
	return stagedOwns(owns, append(slices.Clone(staged), recordedStaged(events, task)...))
}

// WorktreeCommit re-runs the attempt commit of task's latest attempt in its
// task worktree <dir>/.flywheel/worktrees/<task> (fw/<task> checked out)
// against the owns validate measures now, recorded amendments and staged
// files included (issue #897), and appends a recommitted event. It is the
// recorded remedy for paths a clean stop left uncommitted outside owns: amend
// owns, then recommit. Not a task worktree, or no task in the log, is an
// error and appends nothing.
func WorktreeCommit(dir, task string, o WorktreeCommitOptions) (WorktreeCommitResult, error) {
	if o.Session == "" {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit %s: a session is required", task)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit %s: resolve %s: %w", task, dir, err)
	}
	events, err := ReadEvents(abs)
	if err != nil {
		return WorktreeCommitResult{}, err
	}
	known, attempt := false, ""
	for _, e := range events {
		if e.Task == task {
			known = true
			if e.Attempt != "" {
				attempt = e.Attempt
			}
		}
	}
	if !known {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit: task %q has no events in %s", task, filepath.Join(abs, ".flywheel"))
	}
	if attempt == "" {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit: task %q has no attempt to commit", task)
	}
	wt := filepath.Join(abs, ".flywheel", "worktrees", task)
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit: %s is not a task worktree (flywheel run --worktree makes it)", wt)
	}
	if err := checkTaskWorktree(abs, wt, "fw/"+task); err != nil {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit: %w", err)
	}
	header, _, err := AttemptBrief(abs, events, task)
	if err != nil {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit %s: %w", task, err)
	}
	owns := measuredOwns(header.Owns, nil, events, task)
	sha, outside, cerr := commitAttempt(abs, wt, task, attempt, owns)
	if cerr != nil && sha == "" {
		return WorktreeCommitResult{}, fmt.Errorf("worktree commit %s: commit in %s: %w", task, wt, cerr)
	}
	// A commit that moved fw/<task> is recorded even when refreshing the
	// worktree's index afterwards failed: the ledger never misses a commit.
	if err := AppendEvent(abs, Event{Task: task, Kind: "recommitted", Attempt: attempt, Session: o.Session,
		Commit: sha, Uncommitted: outside, Note: o.Note}); err != nil {
		return WorktreeCommitResult{}, err
	}
	res := WorktreeCommitResult{Attempt: attempt, Commit: sha, Uncommitted: outside}
	if cerr != nil {
		return res, fmt.Errorf("worktree commit %s: commit in %s: %w", task, wt, cerr)
	}
	return res, nil
}
