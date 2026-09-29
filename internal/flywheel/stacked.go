package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
// is T. Any git error, or no recorded base, is ok false. Each call reads git
// afresh; recover shares one squashCache across its units instead.
func SquashedBase(dir string, events []Event, task string) (base, landedAs, baseTask string, ok bool) {
	return squashedBase(dir, events, task, newSquashCache())
}

// squashCache memoises, for one call over one events slice, the git reads
// SquashedBase repeats for every unit (issue #628): the integration branch,
// the landed set, per base its ancestry and the trailer log after the
// merge-base, per branch whether it resolves, and per (base, branch) the
// ancestry. A git error is memoised as the "not ok" it yields. Safe for
// concurrent use; calls counts the git processes started (mainBranch as one).
type squashCache struct {
	calls      atomic.Int32
	mainOnce   sync.Once
	main       string
	landedOnce sync.Once
	landed     map[string]bool
	mu         sync.Mutex
	bases      map[string]*squashBaseMemo
	flags      map[string]*onceBool // "branch\x00<b>" and "anc\x00<base>\x00<b>"
}

// squashBaseMemo is one base's answer: squashable when it is not an ancestor
// of main and the merge-base and log reads succeeded; commits is that log.
type squashBaseMemo struct {
	once       sync.Once
	squashable bool
	commits    []squashCommit
}

// squashCommit is one main commit after the merge-base and its trailer tasks.
type squashCommit struct {
	sha   string
	tasks []string
}

type onceBool struct {
	once sync.Once
	v    bool
}

func newSquashCache() *squashCache {
	return &squashCache{bases: map[string]*squashBaseMemo{}, flags: map[string]*onceBool{}}
}

// flag returns f's answer for key, computing it once.
func (c *squashCache) flag(key string, f func() bool) bool {
	c.mu.Lock()
	o, ok := c.flags[key]
	if !ok {
		o = &onceBool{}
		c.flags[key] = o
	}
	c.mu.Unlock()
	o.once.Do(func() { o.v = f() })
	return o.v
}

// base returns base's memo against main, reading git on the first call only.
func (c *squashCache) base(dir, base, main string) *squashBaseMemo {
	c.mu.Lock()
	m, ok := c.bases[base]
	if !ok {
		m = &squashBaseMemo{}
		c.bases[base] = m
	}
	c.mu.Unlock()
	m.once.Do(func() {
		c.calls.Add(1)
		if in, err := isAncestor(dir, base, main); err != nil || in {
			return
		}
		c.calls.Add(1)
		mb, err := gitRead(dir, []string{"merge-base", base, main})
		if err != nil || strings.TrimSpace(mb) == "" {
			return
		}
		c.calls.Add(1)
		out, err := gitRead(dir, []string{"log", "--format=%H%x00%(trailers:key=Flywheel-Task,valueonly,separator=%x2C)", strings.TrimSpace(mb) + ".." + main})
		if err != nil {
			return
		}
		m.squashable = true
		for _, line := range strings.Split(out, "\n") {
			sha, names, found := strings.Cut(strings.TrimSpace(line), "\x00")
			if !found || names == "" {
				continue
			}
			sc := squashCommit{sha: sha}
			for _, t := range strings.Split(names, ",") {
				if t = strings.TrimSpace(t); t != "" && taskOK(t) {
					sc.tasks = append(sc.tasks, t)
				}
			}
			m.commits = append(m.commits, sc)
		}
	})
	return m
}

// squashedBase is SquashedBase reading git through c.
func squashedBase(dir string, events []Event, task string, c *squashCache) (base, landedAs, baseTask string, ok bool) {
	base = dispatchBase(events, task, "")
	if base == "" {
		return "", "", "", false
	}
	c.mainOnce.Do(func() { c.calls.Add(1); c.main = mainBranch(dir) })
	if c.main == "" {
		return "", "", "", false
	}
	m := c.base(dir, base, c.main)
	if !m.squashable {
		return "", "", "", false
	}
	c.landedOnce.Do(func() {
		c.landed = map[string]bool{}
		for _, e := range events {
			if e.Kind == "landed" {
				c.landed[e.Task] = true
			}
		}
	})
	for _, sc := range m.commits {
		for _, t := range sc.tasks {
			if t == task {
				continue
			}
			branch := "refs/heads/fw/" + t
			if c.flag("branch\x00"+branch, func() bool {
				c.calls.Add(1)
				rc, _, _, err := runCmdSplit(dir, gitArgs([]string{"rev-parse", "--verify", "-q", branch}), nil)
				return err == nil && rc == 0
			}) {
				if c.flag("anc\x00"+base+"\x00"+branch, func() bool {
					c.calls.Add(1)
					in, err := isAncestor(dir, base, branch)
					return err == nil && in
				}) {
					return base, sc.sha, t, true
				}
				continue
			}
			if c.landed[t] {
				return base, sc.sha, t, true
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
// When the branch was rebased outside flywheel onto a newer integration
// commit (issue #672), the upstream is the branch's real fork point from
// effectiveBase, not the stale recorded base, and the Note adds ", branch
// forked from <fork> (rebased outside flywheel)". After the rebased event it
// re-runs worktree.setup when configured (rerunSetup); a failed setup is the
// returned error, with newBase still set.
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
	from, drifted := effectiveBase(wt, old, newBase)
	rc, stdout, stderr, err := git("rebase", "--onto", newBase, from, "fw/"+task)
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
	note := "was " + old + ", onto " + onto
	if drifted {
		note += ", branch forked from " + from + " (rebased outside flywheel)"
	}
	if err := AppendEvent(abs, Event{Task: task, Kind: "rebased", Base: newBase, Note: note}); err != nil {
		return newBase, nil, err
	}
	_, _ = WriteState(abs)
	return newBase, nil, rerunSetup(abs, wt, task)
}

// rerunSetup runs worktree.setup, when configured, in the rebased unit's
// worktree wt (issue #672: a rebase that moved the lockfile leaves installed
// dependencies stale) and appends a worktree_setup event shaped like
// prepareWorktree's, without its needs-state links or copies. A setup that
// errs or exits non-zero is an error naming the command, the rc and the tail.
func rerunSetup(dir, wt, task string) error {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return err
	}
	command := cfg.SetupCommand()
	if command == "" {
		return nil
	}
	timeout, err := cfg.SetupTimeoutDuration()
	if err != nil {
		return fmt.Errorf("worktree.setup_timeout: %w", err)
	}
	rc, tail, dur, serr := runWorktreeSetup(dir, wt, task, command, timeout)
	ev := Event{Task: task, Kind: "worktree_setup", Command: command, RC: &rc, DurationMS: dur.Milliseconds(), Note: clipSetupNote(tail)}
	if serr != nil {
		ev.Note = clipSetupNote(serr.Error() + "\n" + tail)
	}
	if err := AppendEvent(dir, ev); err != nil {
		return err
	}
	_, _ = WriteState(dir)
	if serr != nil || rc != 0 {
		why := fmt.Sprintf("exited %d", rc)
		if serr != nil {
			why = serr.Error()
		}
		return fmt.Errorf("rebased, but worktree.setup %q %s (rc=%d) in %s; fix it and re-run it by hand; output tail:\n%s", command, why, rc, wt, tail)
	}
	return nil
}

// effectiveBase is the base the unit's branch really forks from (issue #672):
// the merge base of HEAD in wd and onto when it differs from recorded and
// recorded is its ancestor, i.e. the branch was rebased outside flywheel onto
// a newer integration commit; drifted is then true. Otherwise, and on any git
// error, it is recorded: the check never fails a caller.
func effectiveBase(wd, recorded, onto string) (base string, drifted bool) {
	out, err := gitRead(wd, []string{"merge-base", "HEAD", onto})
	if err != nil {
		return recorded, false
	}
	mb := strings.TrimSpace(out)
	if mb == "" || strings.EqualFold(mb, recorded) {
		return recorded, false
	}
	// A recorded full sha is compared as is: validate's git process budget
	// (TestShipGitProcessBudget) has no room for a rev-parse per reading.
	if len(recorded) != len(mb) || strings.Trim(strings.ToLower(recorded), "0123456789abcdef") != "" {
		rec, err := gitRead(wd, []string{"rev-parse", "--verify", "-q", recorded + "^{commit}"})
		if err != nil || strings.TrimSpace(rec) == mb {
			return recorded, false
		}
	}
	if in, err := isAncestor(wd, recorded, mb); err != nil || !in {
		return recorded, false
	}
	return mb, true
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
