package flywheel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LandMergeOptions configures LandMerge.
type LandMergeOptions struct {
	Dir  string // flywheel root (the main worktree); default "."
	Onto string // integration branch; default the branch checked out in Dir
	Note string // optional landing note
	// AllowUntriaged lands despite untriaged signals, recording this reason
	// (rule T9), as land --allow-untriaged does.
	AllowUntriaged string
}

// LandMergeResult reports a landing through the queue.
type LandMergeResult struct {
	Commit   string // the commit the integration branch now points at
	Onto     string
	Rebased  bool     // false when the branch already contained Onto
	Conflict []string // conflicted paths when the rebase stopped (then Commit is "")
	Delta    string   // the correction brief written for a conflict, relative to Dir
}

// LandMerge lands a passed unit that ran in its own worktree (issue #45),
// one at a time under .flywheel/land.lock (the queue):
//  1. the task's worktree is its recorded Workdir (recordedWorkdir); none →
//     RuleRefusal Rule "land" (run it with --worktree, or land with --commit);
//     a merge left in progress there by an earlier conflict → RuleRefusal
//     Rule "land" saying how to finish it;
//  2. its status must be "passed" (Derive) → else RuleRefusal Rule "T5";
//  3. Onto must be a local branch (never an option-shaped name), the
//     worktree must still have fw/<task> checked out, the worktree and Dir
//     must be clean apart from .flywheel/ and flywheel.md, and Dir must have
//     Onto checked out → else Rule "land";
//  4. in the worktree: git rebase <onto>. When it stops on conflicts, the
//     rebase is aborted and the unit's branch merges <onto> instead, leaving
//     the conflict markers in the worktree with the branch still checked out
//     (a worker can only edit files, and `flywheel run --worktree` needs the
//     branch); .flywheel/briefs/<task>.land-delta.txt — the unit's brief
//     header and a correction naming the conflicted paths — is written, and
//     a RuleRefusal Rule "land" says what to dispatch and how to finish;
//  5. ValidateTask on the rebased worktree re-runs the gates; !OK() → Rule
//     "land" (the branch stays rebased; fix and land again);
//  6. every landing rule (T5, T7, T9 — AllowUntriaged as land
//     --allow-untriaged —, already landed) is checked under the ledger
//     locks, and only then, inside the same critical section, git -C <Dir>
//     merge --ff-only fw/<task> moves the branch and the landing is
//     recorded at its new HEAD (a failed append resets the branch back);
//  7. git worktree remove and git branch -d (a failure here is returned,
//     but the landing stands).
func LandMerge(task string, o LandMergeOptions) (LandMergeResult, error) {
	if o.Dir == "" {
		o.Dir = "."
	}
	release, err := acquireRepoLock(o.Dir, "land.lock", defaultRepoLockTimings())
	if err != nil {
		return LandMergeResult{}, err
	}
	defer release()
	events, err := ReadEvents(o.Dir)
	if err != nil {
		return LandMergeResult{}, err
	}

	workdir := recordedWorkdir(events, task)
	if workdir == "" {
		return LandMergeResult{}, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("task %s has no worktree: run it with flywheel run %s --worktree, or land a merged commit with --commit", task, task)}
	}
	if _, err := landGit(workdir, "rev-parse", "-q", "--verify", "MERGE_HEAD"); err == nil {
		return LandMergeResult{}, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("a merge is in progress in %s (an earlier landing stopped on conflicts): once the conflict markers are resolved (flywheel run %s --delta %s), the lead finishes it with git -C %s add -A && git -C %s commit --no-edit, then validates, inspects and lands again", workdir, task, landDeltaRel(task), workdir, workdir)}
	}

	status := ""
	for _, t := range Derive(events).Tasks {
		if t.ID == task {
			status = t.Status
			break
		}
	}
	if status != "passed" {
		return LandMergeResult{}, &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s is not passed (status %q); land only after a passing inspection: flywheel inspect %s --verdict pass --session <session>", task, status, task)}
	}

	onto := o.Onto
	if onto == "" {
		if onto, err = currentBranch(o.Dir); err != nil {
			return LandMergeResult{}, fmt.Errorf("determine integration branch: %w", err)
		}
	}
	// onto reaches git as an argument: it must name an existing local branch
	// and can never be read as an option (#345 review).
	if strings.HasPrefix(onto, "-") {
		return LandMergeResult{}, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("integration branch %q is not a branch name", onto)}
	}
	if _, err := landGit(o.Dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+onto); err != nil {
		return LandMergeResult{}, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("integration branch %q is not a local branch in %s", onto, o.Dir)}
	}
	// The worktree must still hold the task's own branch: a worktree switched
	// to another branch would land that branch's commits as the task's
	// (#345 review).
	branch := "fw/" + task
	if err := checkTaskWorktree(o.Dir, workdir, branch); err != nil {
		return LandMergeResult{}, &RuleRefusal{Rule: "land", Fix: err.Error()}
	}
	for _, wd := range []string{workdir, o.Dir} {
		dirty, err := dirtyPaths(wd)
		if err != nil {
			return LandMergeResult{}, err
		}
		if len(dirty) > 0 {
			return LandMergeResult{}, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("%s has uncommitted changes in %s: commit them in %s first", wd, strings.Join(dirty, ", "), wd)}
		}
	}
	if current, err := currentBranch(o.Dir); err != nil {
		return LandMergeResult{}, fmt.Errorf("check current branch in %s: %w", o.Dir, err)
	} else if current != onto {
		return LandMergeResult{}, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("%s has branch %q checked out; check out %s there first", o.Dir, current, onto)}
	}

	// A branch that already contains onto is not rebased: after a resolved
	// conflict it holds a merge commit, and a rebase would drop the merge and
	// replay the unit's commits into the same conflict again.
	_, ancestorErr := landGit(workdir, "merge-base", "--is-ancestor", onto, "HEAD")
	result := LandMergeResult{Onto: onto, Rebased: ancestorErr != nil}
	if result.Rebased {
		if out, err := landGit(workdir, "rebase", onto); err != nil {
			conflicts, cerr := landConflicts(workdir)
			if _, aerr := landGit(workdir, "rebase", "--abort"); aerr != nil {
				return result, fmt.Errorf("git rebase --abort in %s after %v: %w", workdir, err, aerr)
			}
			if cerr != nil || len(conflicts) == 0 {
				return result, fmt.Errorf("git rebase %s in %s: %w: %s", onto, workdir, err, strings.TrimSpace(out))
			}
			return landConflict(o.Dir, workdir, task, onto, events, result)
		}
	}

	validation, err := ValidateTask(o.Dir, task, ValidateOptions{Dir: o.Dir, Workdir: workdir})
	if err != nil {
		return result, fmt.Errorf("validate %s rebased onto %s: %w", task, onto, err)
	}
	if !validation.OK() {
		return result, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("gates fail after rebase onto %s (the branch in %s stays rebased): flywheel explain %s", onto, workdir, task)}
	}

	// Every landing rule (T5, T7, T9, already landed) is checked under the
	// ledger locks BEFORE the integration branch moves, and the fast-forward
	// happens inside that same critical section, so a refused landing never
	// advances the branch (#345 review); a failed append undoes it.
	advance := func() (string, func() error, error) {
		old := headCommit(o.Dir)
		if out, err := landGit(o.Dir, "merge", "--ff-only", branch); err != nil {
			return "", nil, fmt.Errorf("fast-forward %s to %s: %w: %s", onto, branch, err, strings.TrimSpace(out))
		}
		undo := func() error {
			if out, err := landGit(o.Dir, "reset", "--keep", old); err != nil {
				return fmt.Errorf("git reset --keep %s: %w: %s", old, err, strings.TrimSpace(out))
			}
			return nil
		}
		return headCommit(o.Dir), undo, nil
	}
	if err := landTask(o.Dir, task, "", o.Note, false, "", "", "", o.AllowUntriaged, advance); err != nil {
		return result, err
	}
	result.Commit = headCommit(o.Dir)

	var errs []string
	if out, err := landGit(o.Dir, "worktree", "remove", workdir); err != nil {
		errs = append(errs, fmt.Sprintf("git worktree remove %s: %v: %s", workdir, err, strings.TrimSpace(out)))
	}
	if out, err := landGit(o.Dir, "branch", "-d", branch); err != nil {
		errs = append(errs, fmt.Sprintf("git branch -d %s: %v: %s", branch, err, strings.TrimSpace(out)))
	}
	if len(errs) > 0 {
		return result, fmt.Errorf("landed %s, cleanup failed: %s", result.Commit, strings.Join(errs, "; "))
	}
	return result, nil
}

// landConflict handles a rebase that stopped on conflicts (already
// aborted): the unit's branch merges onto instead, so the markers sit in
// the worktree with the branch checked out; it writes the correction brief
// and returns the refusal naming both.
func landConflict(dir, workdir, task, onto string, events []Event, result LandMergeResult) (LandMergeResult, error) {
	if _, err := landGit(workdir, "merge", "--no-ff", "--no-edit", onto); err == nil {
		// Merging went through where replaying did not: nothing to resolve,
		// but the tree is new, so it must be measured and inspected again.
		return result, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("the rebase onto %s stopped on conflicts but a merge of it went through in %s: validate and inspect %s again, then land", onto, workdir, task)}
	}
	conflicts, err := landConflicts(workdir)
	if err != nil {
		return result, err
	}
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		return result, fmt.Errorf("read the brief of %s for the correction: %w", task, err)
	}
	if err := writeLandDelta(dir, task, header, onto, conflicts); err != nil {
		return result, err
	}
	result.Conflict = conflicts
	result.Delta = landDeltaRel(task)
	return result, &RuleRefusal{Rule: "land", Fix: fmt.Sprintf("merging %s into the unit stopped on conflicts in %s, left in progress in %s: dispatch the correction (flywheel run %s --delta %s; the worker only edits files), then the lead runs git -C %s add -A && git -C %s commit --no-edit, validates, inspects and lands again", onto, strings.Join(conflicts, ", "), workdir, task, result.Delta, workdir, workdir)}
}

// landConflicts lists the unmerged paths in wd, NUL-delimited so no name is
// quoted.
func landConflicts(wd string) ([]string, error) {
	out, err := landGit(wd, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, fmt.Errorf("git diff --diff-filter=U in %s: %w", wd, err)
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// landDeltaRel is the correction brief a landing conflict writes, relative
// to the flywheel root.
func landDeltaRel(task string) string {
	return ".flywheel/briefs/" + task + ".land-delta.txt"
}

// landGit runs git -C wd with args and returns its combined output.
func landGit(wd string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", wd}, args...)...).CombinedOutput()
	return string(out), err
}

// dirtyPaths returns the uncommitted paths in wd (git status --porcelain
// -z, untracked included), leaving out .flywheel/ and flywheel.md.
func dirtyPaths(wd string) ([]string, error) {
	out, err := exec.Command("git", "-C", wd, "status", "--porcelain", "-z", "--untracked-files=all").Output()
	if err != nil {
		return nil, fmt.Errorf("git status in %s: %w", wd, err)
	}
	var dirty []string
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		if e[0] == 'R' || e[0] == 'C' {
			i++ // a rename or copy is followed by its source path
		}
		p := e[3:]
		if p == "flywheel.md" || p == ".flywheel" || strings.HasPrefix(p, ".flywheel/") {
			continue
		}
		dirty = append(dirty, p)
	}
	return dirty, nil
}

// currentBranch returns the currently checked out branch in wd.
func currentBranch(wd string) (string, error) {
	cmd := exec.Command("git", "-C", wd, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeLandDelta writes the correction brief for a landing conflict: the
// unit's brief header (owns, needs, gates) and an imperative task naming the
// conflicted files.
func writeLandDelta(dir, task string, header BriefHeader, onto string, conflicts []string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "owns: %s\n", strings.Join(header.Owns, ", "))
	needs := strings.Join(header.Needs, ", ")
	if needs == "" {
		needs = "none"
	}
	fmt.Fprintf(&b, "needs: %s\n", needs)
	for _, g := range header.Gates {
		fmt.Fprintf(&b, "gate: %s\n", g)
	}
	fmt.Fprintf(&b, "\n# TASK (correction): landing merged %s into this unit and it stopped on conflicts in: %s.\n", onto, strings.Join(conflicts, ", "))
	b.WriteString("EDIT each of those files: it holds conflict markers (<<<<<<<, =======, >>>>>>>); keep both sides' intent and remove every marker. Then run every gate above. Do not run git.\n")
	p := filepath.Join(dir, filepath.FromSlash(landDeltaRel(task)))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}
