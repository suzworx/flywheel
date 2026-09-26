package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// mainBranch returns the repository's integration branch as dir sees it
// (IntegrationBranch), "" when there is none or a configured one does not
// resolve.
func mainBranch(dir string) string {
	b, configured := IntegrationBranch(dir)
	if configured && !branchResolves(dir, b) {
		return ""
	}
	return b
}

// isAncestor reports whether commit a is an ancestor of b (git merge-base
// --is-ancestor): rc 0 is yes, rc 1 is no, anything else is an error.
func isAncestor(dir, a, b string) (bool, error) {
	rc, _, stderr, err := runCmdSplit(dir, gitArgs([]string{"merge-base", "--is-ancestor", a, b}), nil)
	if err != nil {
		return false, err
	}
	switch rc {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s failed (rc=%d): %s", a, b, rc, strings.TrimSpace(string(stderr)))
}

// SquashedBase reports whether task's unit is stacked on a base that has
// since landed on main as a different (squash) commit (issue #414). The base
// is the unit's recorded one (dispatchBase: the first dispatch, or the latest
// rebased event). It is squashed when it is not an ancestor of main and a
// commit on main after their merge-base carries a Flywheel-Task trailer for
// another unit T whose branch fw/<T> contains the base, or, when fw/<T> is
// gone, T is landed in the ledger. landedAs is that main commit and baseTask
// is T. Any git error, or no recorded base, is ok false.
func SquashedBase(dir string, events []Event, task string) (base, landedAs, baseTask string, ok bool) {
	base = dispatchBase(events, task, "")
	main := mainBranch(dir)
	if base == "" || main == "" {
		return "", "", "", false
	}
	if in, err := isAncestor(dir, base, main); err != nil || in {
		return "", "", "", false
	}
	mb, err := gitRead(dir, []string{"merge-base", base, main})
	if err != nil || strings.TrimSpace(mb) == "" {
		return "", "", "", false
	}
	out, err := gitRead(dir, []string{"log", "--format=%H%x00%(trailers:key=Flywheel-Task,valueonly,separator=%x2C)", strings.TrimSpace(mb) + ".." + main})
	if err != nil {
		return "", "", "", false
	}
	landed := map[string]bool{}
	for _, e := range events {
		if e.Kind == "landed" {
			landed[e.Task] = true
		}
	}
	for _, line := range strings.Split(out, "\n") {
		sha, names, found := strings.Cut(strings.TrimSpace(line), "\x00")
		if !found || names == "" {
			continue
		}
		for _, t := range strings.Split(names, ",") {
			t = strings.TrimSpace(t)
			if t == "" || t == task || !taskOK(t) {
				continue
			}
			branch := "refs/heads/fw/" + t
			if rc, _, _, err := runCmdSplit(dir, gitArgs([]string{"rev-parse", "--verify", "-q", branch}), nil); err == nil && rc == 0 {
				if in, err := isAncestor(dir, base, branch); err == nil && in {
					return base, sha, t, true
				}
				continue
			}
			if landed[t] {
				return base, sha, t, true
			}
		}
	}
	return "", "", "", false
}

// stackedFix is the remedy text for a unit stacked on a squashed base: the
// owns_checked warning and land's stacked refusal both carry it.
func stackedFix(task, base, landedAs, baseTask string) string {
	return fmt.Sprintf("base %s (unit %s) was squash-merged as %s; run: flywheel rebase %s", short7(base), baseTask, short7(landedAs), task)
}

// RebaseUnit moves task's branch fw/<task> off its recorded base onto onto
// (default: integration.branch, else main, else master) with `git rebase --onto <onto> <base>
// fw/<task>`, run in the unit's task worktree <dir>/.flywheel/worktrees/<task>
// only, with flywheel's own git environment (never a worker's guard). On a
// conflict it lists the unmerged paths, runs `git rebase --abort` so the
// branch is left as it was, and returns the paths with a nil error. On
// success it appends a rebased event (Base the new base, Note "was <old>,
// onto <onto>") so dispatchBase measures the unit from there (issue #414).
func RebaseUnit(dir, task, onto string) (newBase string, conflicts []string, err error) {
	if !taskOK(task) {
		return "", nil, fmt.Errorf("task %q does not match ^[A-Za-z0-9._-]+$", task)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	wt := filepath.Join(abs, ".flywheel", "worktrees", task)
	if _, err := os.Stat(wt); err != nil {
		return "", nil, fmt.Errorf("unit %s has no task worktree at %s; flywheel rebase acts only there", task, wt)
	}
	if err := checkTaskWorktree(abs, wt, "fw/"+task); err != nil {
		return "", nil, err
	}
	events, err := ReadEvents(abs)
	if err != nil {
		return "", nil, err
	}
	old := dispatchBase(events, task, "")
	if old == "" {
		return "", nil, fmt.Errorf("unit %s has no recorded base; nothing to rebase from", task)
	}
	if onto == "" {
		b, configured := IntegrationBranch(abs)
		switch {
		case configured && !branchResolves(abs, b):
			return "", nil, fmt.Errorf("integration.branch %q does not resolve in %s; fetch it or pass --onto", b, abs)
		case b == "":
			return "", nil, fmt.Errorf("no integration.branch, main or master branch; pass --onto")
		}
		onto = b
	}
	env := append(os.Environ(),
		"GIT_COMMITTER_NAME="+unitCommitName, "GIT_COMMITTER_EMAIL="+unitCommitEmail)
	git := func(args ...string) (int, string, string, error) {
		rc, stdout, stderr, err := runCmdSplit(wt, gitArgs(args), env)
		return rc, strings.TrimSpace(string(stdout)), strings.TrimSpace(string(stderr)), err
	}
	rc, newBase, stderr, err := git("rev-parse", "--verify", onto+"^{commit}")
	if err != nil {
		return "", nil, err
	}
	if rc != 0 {
		return "", nil, fmt.Errorf("cannot resolve %s: %s", onto, stderr)
	}
	rc, stdout, stderr, err := git("rebase", "--onto", newBase, old, "fw/"+task)
	if err != nil {
		return "", nil, err
	}
	if rc != 0 {
		_, unmerged, _, _ := git("diff", "--name-only", "--diff-filter=U")
		for _, p := range strings.Split(unmerged, "\n") {
			if p = strings.TrimSpace(p); p != "" {
				conflicts = append(conflicts, filepath.ToSlash(p))
			}
		}
		arc, _, astderr, aerr := git("rebase", "--abort")
		if aerr == nil && arc != 0 && len(conflicts) > 0 {
			return "", conflicts, fmt.Errorf("git rebase --abort failed (rc=%d): %s", arc, astderr)
		}
		if len(conflicts) == 0 {
			return "", nil, fmt.Errorf("git rebase failed (rc=%d): %s", rc, strings.TrimSpace(stdout+"\n"+stderr))
		}
		return "", conflicts, nil
	}
	if err := AppendEvent(abs, Event{Task: task, Kind: "rebased", Base: newBase, Note: "was " + old + ", onto " + onto}); err != nil {
		return newBase, nil, err
	}
	_, _ = WriteState(abs)
	return newBase, nil, nil
}

// inTaskWorktree reports whether workdir is task's worktree under dir,
// <dir>/.flywheel/worktrees/<task>, by path alone (no git).
func inTaskWorktree(dir, workdir, task string) bool {
	abs, err := filepath.Abs(dir)
	return err == nil && samePath(workdir, filepath.Join(abs, ".flywheel", "worktrees", task))
}

// short7 returns the first seven characters of a commit sha.
func short7(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
