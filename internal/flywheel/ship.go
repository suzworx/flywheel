package flywheel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ErrShipGates is wrapped in the error Ship returns when the gates step
// fails on the merged tree; the CLI exits 5 for it.
var ErrShipGates = errors.New("gates failed")

// ShipOptions configures one `flywheel ship` (issue #457).
type ShipOptions struct {
	Integration string // branch merged in; default IntegrationBranch(dir), else "main"
	Workdir     string // default the task worktree .flywheel/worktrees/<task>
	Message     string // commit message for leftover owned changes; default "<task> ship"
	Remote      string // default "origin"
	Now         func() time.Time
	Progress    io.Writer // one line per step; nil discards

	// The remote half (issue #457 part 3).
	Forge        Forge                 // nil = GhTracker{Repo: Repo}
	Repo         string                // OWNER/REPO for the default Forge; empty lets gh resolve it
	Title        string                // PR title; default the brief's "# TASK:" text, else the task id
	Body         string                // PR body; default a generated summary (shipBody)
	NoMerge      bool                  // stop after ci, leaving merge, landed and closed unrun
	CITimeout    time.Duration         // default 45m
	Poll         time.Duration         // default 30s
	IgnoreChecks []string              // check names ci disregards
	Sleep        func(d time.Duration) // default time.Sleep
	Requeue      int                   // re-runs from merge-base on ErrShipStale; 0 = 2, negative = never (issue #591)
	Version      string                // the binary's version for the signature; "" = "dev"
	NoSignature  bool                  // leave the signature out this run; config ship.signature false does it always
}

// ShipStep is one step's outcome: Result ok, skip or fail, Commit fw/<task>'s
// HEAD after it, and Done when an earlier run's record was trusted instead of
// running the step again.
type ShipStep struct {
	Step   string `json:"step"`
	Result string `json:"result"`
	Commit string `json:"commit,omitempty"`
	Note   string `json:"note,omitempty"`
	Done   bool   `json:"done,omitempty"`
}

// ShipResult lists the steps a ship ran or trusted, in order, up to and
// including the first failure.
type ShipResult struct {
	Task        string     `json:"task"`
	Attempt     string     `json:"attempt"`
	Workdir     string     `json:"workdir"`
	Integration string     `json:"integration"`
	Steps       []ShipStep `json:"steps"`
}

// shipRun is one ship's context, shared by its steps.
type shipRun struct {
	dir, task, attempt, wt string
	o                      ShipOptions
	events                 []Event
	owns                   []string
	pr                     PullRequest // the unit's PR once pr ran or was looked up
	issue                  int         // the planned event's issue, 0 without one
	base                   string      // <remote>/<integration>'s commit merge-base saw, recorded on its event
	trailer                string      // the Shipped-by: trailer, "" with the signature off
	required               []string    // ship.required_checks from config, nil when unset
	// gitMemo holds this run's successful read-only git queries in the
	// workdir (issue #619), and changed its changed paths when changedOK:
	// each git process costs 0.1-3s on a loaded Windows host. forget empties
	// both after every git write; nothing outlives the Ship call.
	gitMemo   map[string]string
	changed   []string
	changedOK bool
}

// gitRead runs the read-only git query args in the workdir once per run
// until the next forget; a failure is not remembered.
func (r *shipRun) gitRead(args ...string) (string, error) {
	key := strings.Join(args, "\x00")
	if out, ok := r.gitMemo[key]; ok {
		return out, nil
	}
	out, err := gitWith(r.wt, nil, args...)
	if err != nil {
		return "", err
	}
	if r.gitMemo == nil {
		r.gitMemo = map[string]string{}
	}
	r.gitMemo[key] = out
	return out, nil
}

// changedPaths is changedPaths(r.wt), once per run until the next forget.
func (r *shipRun) changedPaths() ([]string, error) {
	if r.changedOK {
		return r.changed, nil
	}
	changed, err := changedPaths(r.wt)
	if err != nil {
		return nil, err
	}
	r.changed, r.changedOK = changed, true
	return changed, nil
}

// forget drops what gitRead and changedPaths remember; every step that
// writes to the repository (a commit, fetch, merge, push, gate or landing)
// calls it after the write.
func (r *shipRun) forget() {
	r.gitMemo, r.changed, r.changedOK = nil, nil, false
}

// shipStepFuncs runs each of ShipSteps: the outcome (ok or skip), a note, and
// an error when the step fails.
var shipStepFuncs = map[string]func(*shipRun) (result, note string, err error){
	"preflight":  shipPreflight,
	"commit":     shipCommit,
	"merge-base": shipMergeBase,
	"gates":      shipGates,
	"push":       shipPush,
	"pr":         shipPR,
	"ci":         shipCI,
	"merge":      shipMerge,
	"landed":     shipLanded,
	"closed":     shipClosed,
}

// Ship ships task (issue #457): the local half (preflight, commit, merge-base
// and gates, in the task worktree only), then the remote half (push, pr, ci,
// merge, landed and closed, through o.Forge; NoMerge stops after ci). Each
// step appends one shipped event and prints one progress line; a step an
// earlier run already recorded ok or skip (shipTrusted) is not run again. It
// stops at the first failure: a *RuleRefusal for preflight or landed, an error
// wrapping ErrShipGates for gates, ErrShipCI for ci or ErrShipStale for a
// merge whose CI ran before <remote>/<integration> moved, any other error
// otherwise. A stale merge re-runs the steps from merge-base up to Requeue
// times, the returned Steps holding every pass (issue #591).
func Ship(dir, task string, o ShipOptions) (ShipResult, error) {
	if !taskOK(task) {
		return ShipResult{}, fmt.Errorf("task %q does not match ^[A-Za-z0-9._-]+$", task)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ShipResult{}, err
	}
	if o.Integration == "" {
		o.Integration = integrationOrMain(abs)
	}
	if o.Remote == "" {
		o.Remote = "origin"
	}
	if o.Message == "" {
		o.Message = task + " ship"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Progress == nil {
		o.Progress = io.Discard
	}
	wt := o.Workdir
	if wt == "" {
		wt = filepath.Join(abs, ".flywheel", "worktrees", task)
	}
	if _, err := os.Stat(wt); err != nil {
		return ShipResult{}, fmt.Errorf("unit %s has no task worktree at %s; flywheel ship acts only there", task, wt)
	}
	if err := checkTaskWorktree(abs, wt, "fw/"+task); err != nil {
		return ShipResult{}, err
	}
	events, err := ReadEvents(abs)
	if err != nil {
		return ShipResult{}, err
	}
	attempt := ""
	for _, ts := range Derive(events).Tasks {
		if ts.ID == task {
			attempt = currentAttempt(ts, events)
		}
	}
	cfg, _, err := LoadConfig(abs)
	if err != nil {
		return ShipResult{}, err
	}
	r := &shipRun{dir: abs, task: task, attempt: attempt, wt: wt, o: o, events: events, required: cfg.ShipRequiredChecks()}
	r.remoteDefaults()
	if !o.NoSignature && cfg.ShipSignature() {
		var footer string
		r.trailer, footer = shipSignature(events, task, attempt, o.Version)
		r.o.Body = withFooter(r.o.Body, footer)
	}
	res := ShipResult{Task: task, Attempt: attempt, Workdir: wt, Integration: o.Integration}
	n := o.Requeue
	if n == 0 {
		n = 2
	}
	for k := 1; ; k++ {
		res, err = r.steps(res)
		if !errors.Is(err, ErrShipStale) || k > n {
			return res, err
		}
		// The failed merge event is in the ledger now: re-read it so the
		// #577 cut re-runs merge-base, gates, push and ci on the new tree.
		fmt.Fprintf(r.o.Progress, "ship %s merge: %s/%s moved; requeue %d/%d from merge-base\n", task, o.Remote, o.Integration, k, n)
		if r.events, err = ReadEvents(abs); err != nil {
			return res, err
		}
	}
}

// fwHead is fw/<task>'s commit, "" when it does not resolve.
func (r *shipRun) fwHead() string {
	head, err := r.gitRead("rev-parse", "--verify", "-q", "refs/heads/fw/"+r.task)
	if err != nil {
		return ""
	}
	return head
}

// steps runs ShipSteps in order after the trusted prefix, appending one
// shipped event per step it runs; a trusted step prints "(done)" and appends
// nothing.
func (r *shipRun) steps(res ShipResult) (ShipResult, error) {
	trusted := shipTrusted(r.events, r.task, r.attempt, r.fwHead())
	if shipBaseCheck(trusted) {
		cur, cerr := r.integrationCommit()
		if cut := shipTrustBase(trusted, cur); len(cut) < len(trusted) {
			ref, old := r.o.Remote+"/"+r.o.Integration, trusted[len(cut)].Base
			why := fmt.Sprintf("%s moved %s -> %s", ref, short7(old), short7(cur))
			switch {
			case cur == "" && cerr != nil:
				why = fmt.Sprintf("cannot tell whether %s moved (%v)", ref, cerr)
			case cur == "":
				why = ref + " does not resolve"
			case old == "":
				why = "no recorded commit of " + ref
			}
			fmt.Fprintf(r.o.Progress, "ship %s merge-base: %s; re-running from merge-base\n", r.task, why)
			trusted = cut
		}
	}
	for i, name := range ShipSteps {
		if name == "merge" && r.o.NoMerge {
			break
		}
		if i < len(trusted) {
			e := trusted[i]
			res.Steps = append(res.Steps, ShipStep{Step: name, Result: e.Result, Commit: e.Commit, Note: e.Note, Done: true})
			fmt.Fprintf(r.o.Progress, "ship %s %s: %s (done)\n", r.task, name, e.Result)
			continue
		}
		result, note, err := shipStepFuncs[name](r)
		if err != nil {
			result = "fail"
			if note == "" {
				note = err.Error()
			}
		}
		step := ShipStep{Step: name, Result: result, Commit: r.fwHead(), Note: clipNote(note)}
		res.Steps = append(res.Steps, step)
		fmt.Fprintln(r.o.Progress, strings.TrimSpace(fmt.Sprintf("ship %s %s: %s %s", r.task, name, result, step.Note)))
		ev := Event{TS: r.o.Now().UTC().Format(time.RFC3339Nano), Task: r.task, Kind: "shipped", Attempt: r.attempt,
			Step: name, Result: result, Commit: step.Commit, Note: step.Note}
		if name == "merge-base" && err == nil {
			ev.Base = r.base
		}
		if aerr := AppendEvent(r.dir, ev); aerr != nil {
			if err != nil {
				return res, fmt.Errorf("%w (and recording it failed: %v)", err, aerr)
			}
			return res, aerr
		}
		if err != nil {
			return res, err
		}
	}
	_, _ = WriteState(r.dir)
	return res, nil
}

// shipTrusted returns the leading steps an earlier ship of task's attempt
// already completed, whose records need not run again. It takes the latest
// shipped record of each step in ShipSteps order while each is ok or skip and
// was appended after the previous step's; a later step's record is only
// trusted when every earlier one is. The prefix is then cut back to the last
// record whose Commit equals fw/<task>'s HEAD now: the steps up to it left the
// branch where it is, so nothing since has invalidated them. A branch that
// moved since (a commit by hand, a reset) matches no record and every step
// runs again.
func shipTrusted(events []Event, task, attempt, head string) []Event {
	latest := map[string]int{}
	for i, e := range events {
		if e.Task == task && e.Kind == "shipped" && e.Attempt == attempt {
			latest[e.Step] = i
		}
	}
	var chain []Event
	prev := -1
	for _, s := range ShipSteps {
		i, ok := latest[s]
		if !ok || i < prev || events[i].Result != "ok" && events[i].Result != "skip" {
			break
		}
		chain = append(chain, events[i])
		prev = i
	}
	for len(chain) > 0 && (head == "" || chain[len(chain)-1].Commit != head) {
		chain = chain[:len(chain)-1]
	}
	return chain
}

// shipBaseCheck reports whether a trusted chain includes merge-base but not
// merge (issue #577): merge-base's outcome also depends on
// <remote>/<integration>, which must be checked before trusting it. A unit
// already merged keeps its chain.
func shipBaseCheck(chain []Event) bool {
	return len(chain) > slices.Index(ShipSteps, "merge-base") && len(chain) <= slices.Index(ShipSteps, "merge")
}

// shipTrustBase cuts a chain shipBaseCheck selects back to just before
// merge-base unless merge-base's record carries Base equal to current, the
// integration commit now ("" when it could not be fetched or resolved).
func shipTrustBase(chain []Event, current string) []Event {
	if !shipBaseCheck(chain) {
		return chain
	}
	mb := slices.Index(ShipSteps, "merge-base")
	if current == "" || chain[mb].Base != current {
		return chain[:mb]
	}
	return chain
}

// integrationCommit fetches <remote> <integration> (retrying a transient
// error) and resolves <remote>/<integration>'s commit.
func (r *shipRun) integrationCommit() (string, error) {
	if err := r.retry("merge-base", func() error {
		_, err := gitWith(r.wt, shipEnv(), "fetch", r.o.Remote, r.o.Integration)
		r.forget()
		return err
	}); err != nil {
		return "", fmt.Errorf("git fetch %s %s: %w", r.o.Remote, r.o.Integration, err)
	}
	return r.integrationRef()
}

// integrationRef resolves <remote>/<integration>'s commit without fetching.
func (r *shipRun) integrationRef() (string, error) {
	return r.gitRead("rev-parse", "--verify", "-q", r.o.Remote+"/"+r.o.Integration+"^{commit}")
}

// header loads the owns of task's current attempt once per ship.
func (r *shipRun) header() ([]string, error) {
	if r.owns == nil {
		h, _, err := AttemptBrief(r.dir, r.events, r.task)
		if err != nil {
			return nil, err
		}
		r.owns = append([]string{}, h.Owns...)
	}
	return r.owns, nil
}

// shipPreflight refuses (T5) a unit whose status is not passed, as land does,
// and (owns) a workdir with changed paths outside the attempt's owns.
func shipPreflight(r *shipRun) (string, string, error) {
	status := ""
	for _, ts := range Derive(r.events).Tasks {
		if ts.ID == r.task {
			status = ts.Status
		}
	}
	if status != "passed" {
		return "", "", &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s is not passed (status %q); ship only after a passing inspection: flywheel inspect %s --verdict pass --session <session>", r.task, status, r.task)}
	}
	owns, err := r.header()
	if err != nil {
		return "", "", err
	}
	changed, err := r.changedPaths()
	if err != nil {
		return "", "", err
	}
	var outside []string
	for _, p := range changed {
		if !isFlywheelOwnPath(p) && !ownsContains(owns, p) {
			outside = append(outside, p)
		}
	}
	if len(outside) > 0 {
		list := strings.Join(outside, ", ")
		return "", "outside owns: " + list, &RuleRefusal{Rule: "owns", Fix: fmt.Sprintf("%s has changed paths outside %s's owns: %s; commit, move or discard them before shipping", r.wt, r.task, list)}
	}
	return "ok", "status passed", nil
}

// shipCommit commits the changed owned paths left in the workdir on fw/<task>
// the way commitAttempt does: through a temporary index, authored by
// flywheel, moving the branch by compare-and-swap.
func shipCommit(r *shipRun) (string, string, error) {
	owns, err := r.header()
	if err != nil {
		return "", "", err
	}
	changed, err := r.changedPaths()
	if err != nil {
		return "", "", err
	}
	var owned []string // ownedChanged's paths
	for _, p := range changed {
		if !isFlywheelOwnPath(p) && ownsContains(owns, p) {
			owned = append(owned, p)
		}
	}
	if len(owned) == 0 {
		return "skip", "nothing to commit", nil
	}
	defer r.forget()
	tmp, err := os.MkdirTemp("", "flywheel-ship-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(tmp)
	env := append(shipEnv(), "GIT_INDEX_FILE="+filepath.Join(tmp, "index"))
	old, err := gitWith(r.wt, env, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", "", err
	}
	if _, err := gitWith(r.wt, env, "read-tree", "HEAD"); err != nil {
		return "", "", err
	}
	if _, err := gitWith(r.wt, env, append([]string{"--literal-pathspecs", "add", "-A", "--"}, owned...)...); err != nil {
		return "", "", err
	}
	tree, err := gitWith(r.wt, env, "write-tree")
	if err != nil {
		return "", "", err
	}
	if headTree, err := gitWith(r.wt, env, "rev-parse", "HEAD^{tree}"); err == nil && headTree == tree {
		return "skip", "nothing to commit", nil
	}
	trailers := "Flywheel-Task: " + r.task
	if r.trailer != "" {
		trailers += "\n" + r.trailer
	}
	sha, err := gitWith(r.wt, env, "commit-tree", tree, "-p", old, "-m", r.o.Message, "-m", trailers)
	if err != nil {
		return "", "", err
	}
	if _, err := gitWith(r.wt, env, "update-ref", "refs/heads/fw/"+r.task, sha, old); err != nil {
		return "", "", err
	}
	if _, err := gitWith(r.wt, nil, append([]string{"--literal-pathspecs", "reset", "-q", "--"}, owned...)...); err != nil {
		return "", "", fmt.Errorf("committed %s but refreshing the index failed: %w", sha, err)
	}
	return "ok", fmt.Sprintf("committed %d path(s) as %s", len(owned), short7(sha)), nil
}

// shipEnv is the environment flywheel's own ship commits and merges run with.
func shipEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME="+unitCommitName, "GIT_AUTHOR_EMAIL="+unitCommitEmail,
		"GIT_COMMITTER_NAME="+unitCommitName, "GIT_COMMITTER_EMAIL="+unitCommitEmail)
}

// shipMergeBase fetches <remote> <integration> and merges <remote>/<integration>
// into fw/<task> in the workdir with --no-ff under flywheel's identity. Already
// up to date is skip; a conflict is aborted and names the unmerged paths.
func shipMergeBase(r *shipRun) (string, string, error) {
	env := shipEnv()
	ref := r.o.Remote + "/" + r.o.Integration
	defer r.forget() // the merge or its abort below
	_, err := gitWith(r.wt, env, "fetch", r.o.Remote, r.o.Integration)
	r.forget()
	if err != nil {
		return "", "", fmt.Errorf("git fetch %s %s: %w", r.o.Remote, r.o.Integration, err)
	}
	base, err := r.integrationRef()
	if err != nil {
		return "", "", fmt.Errorf("resolving %s: %w", ref, err)
	}
	r.base = base
	in, err := isAncestor(r.wt, ref, "HEAD")
	if err != nil {
		return "", "", err
	}
	if in {
		return "skip", "already up to date with " + ref, nil
	}
	rc, stdout, stderr, err := runCmdSplit(r.wt, gitArgs([]string{"merge", "--no-ff", "--no-edit", ref}), env)
	if err != nil {
		return "", "", err
	}
	if rc != 0 {
		unmerged, _ := gitWith(r.wt, nil, "diff", "--name-only", "--diff-filter=U")
		var conflicts []string
		for _, p := range strings.Split(unmerged, "\n") {
			if p = strings.TrimSpace(p); p != "" {
				conflicts = append(conflicts, filepath.ToSlash(p))
			}
		}
		_, aerr := gitWith(r.wt, env, "merge", "--abort")
		if len(conflicts) == 0 {
			return "", "", fmt.Errorf("git merge --no-ff --no-edit %s failed (rc=%d): %s", ref, rc, strings.TrimSpace(string(stdout)+"\n"+string(stderr)))
		}
		note := "conflict: " + strings.Join(conflicts, ", ")
		if aerr != nil {
			return "", note, fmt.Errorf("merging %s into fw/%s conflicts in %s, and git merge --abort failed: %w", ref, r.task, strings.Join(conflicts, ", "), aerr)
		}
		return "", note, fmt.Errorf("merging %s into fw/%s conflicts in %s; the merge was aborted and fw/%s is unchanged", ref, r.task, strings.Join(conflicts, ", "), r.task)
	}
	return "ok", "merged " + ref + " as " + short7(r.fwHead()), nil
}

// shipGates runs the task's gates on the merged tree (ValidateTask in the
// workdir); a failing gate or an owns violation fails the step.
func shipGates(r *shipRun) (string, string, error) {
	defer r.forget() // a gate is any command and may write to the repository
	res, err := ValidateTask(r.dir, r.task, ValidateOptions{Dir: r.dir, Workdir: r.wt})
	if err != nil {
		return "", "", err
	}
	if res.Refused != "" {
		return "", res.Refused, fmt.Errorf("%w: %s", ErrShipGates, res.Refused)
	}
	if res.OK() {
		return "ok", fmt.Sprintf("%d gate(s) passed", len(res.Gates)), nil
	}
	var failed, parts []string
	for _, g := range res.Gates {
		if g.RC != 0 || g.HostBlocked {
			failed = append(failed, g.Gate)
		}
	}
	if len(failed) > 0 {
		parts = append(parts, "failing gate(s) "+strings.Join(failed, ", "))
	}
	if !res.OwnsOK {
		parts = append(parts, "outside owns: "+strings.Join(res.Outside, ", "))
	}
	note := strings.Join(parts, "; ")
	return "", note, fmt.Errorf("%w on the merged tree: %s", ErrShipGates, note)
}

// ErrShipCI is wrapped in the error Ship returns when the ci step fails: a
// failed check, or a timeout with checks still pending or none reported; the
// CLI exits 5 for it.
var ErrShipCI = errors.New("CI failed")

// ErrShipStale is wrapped in the error Ship returns when the merge step finds
// <remote>/<integration> moved past what the PR's CI ran on (issue #591): the
// PR head does not contain the current integration commit. Ship re-runs from
// merge-base up to ShipOptions.Requeue times first; the CLI exits 5 for it.
var ErrShipStale = errors.New("integration branch moved after CI")

// remoteDefaults fills the remote half's options and the planned issue.
func (r *shipRun) remoteDefaults() {
	o := &r.o
	if o.Forge == nil {
		o.Forge = GhTracker{Repo: o.Repo}
	}
	if o.CITimeout <= 0 {
		o.CITimeout = 45 * time.Minute
	}
	if o.Poll <= 0 {
		o.Poll = 30 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	for _, e := range r.events {
		if e.Task == r.task && e.Kind == "planned" {
			r.issue = e.Issue
		}
	}
	if o.Title == "" {
		brief, _, _ := latestBaseBriefAndAttempt(r.events, r.task)
		o.Title = briefTitle(r.dir, brief, r.task)
	}
	if o.Body == "" {
		o.Body = r.body()
	}
}

// briefTitle is the text of the brief's "# TASK:" line, else task.
func briefTitle(dir, brief, task string) string {
	if brief != "" {
		if b, err := os.ReadFile(resolveBriefPath(dir, brief)); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if t, ok := strings.CutPrefix(strings.TrimSpace(l), "# TASK:"); ok && strings.TrimSpace(t) != "" {
					return strings.TrimSpace(t)
				}
			}
		}
	}
	return task
}

// body is the generated PR body: the task, its gates, the ship steps and
// Fixes #N when the planned event carries an issue.
func (r *shipRun) body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s: %s\n", r.task, r.o.Title)
	if h, _, err := AttemptBrief(r.dir, r.events, r.task); err == nil && len(h.Gates) > 0 {
		b.WriteString("\nGates:\n")
		for _, g := range h.Gates {
			fmt.Fprintf(&b, "- `%s`\n", g)
		}
	}
	fmt.Fprintf(&b, "\nShipped by flywheel ship: %s.\n", strings.Join(ShipSteps, ", "))
	if r.issue > 0 {
		fmt.Fprintf(&b, "\nFixes #%d\n", r.issue)
	}
	return b.String()
}

// scrubMessage removes every line naming a claude.ai/code/session link or
// starting with Claude-Session: the squash message is public.
func scrubMessage(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "claude.ai/code/session") || strings.HasPrefix(strings.TrimSpace(l), "Claude-Session") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// shipSignature is flywheel's mark on what ship lands, built from the ledger
// and the binary's version ("" is "dev"), never from the brief's prose: the
// Shipped-by: trailer for its commits and squash merge, and the PR footer.
// The gate counts are the latest reading per gate on the tree of attempt's
// latest validated event, left out when it has none; corrections count the
// task's c* attempts, left out when there are none.
func shipSignature(events []Event, task, attempt, version string) (trailer, footer string) {
	if version == "" {
		version = "dev"
	}
	tree, found := "", false
	for _, e := range events {
		if e.Task == task && e.Kind == "validated" && (e.Attempt == "" || e.Attempt == attempt) {
			tree, found = e.Tree, true
		}
	}
	ok := map[string]bool{}
	corrections := map[string]bool{}
	for _, e := range events {
		if e.Task != task {
			continue
		}
		if isCorrection(e.Attempt) {
			corrections[e.Attempt] = true
		}
		if found && e.Kind == "validated" && (e.Attempt == "" || e.Attempt == attempt) && e.Tree == tree {
			ok[e.Gate] = e.Reason != "host-blocked" && e.RC != nil && *e.RC == 0
		}
	}
	passed := 0
	for _, v := range ok {
		if v {
			passed++
		}
	}
	gates := ""
	if found {
		gates = fmt.Sprintf("%d/%d gates", passed, len(ok))
	}
	var tp []string
	tp = append(tp, "unit "+task)
	if attempt != "" {
		tp = append(tp, "attempt "+attempt)
	}
	fp := []string{"Shipped by [flywheel](https://github.com/suzworx/flywheel) " + version, "unit `" + task + "`"}
	if gates != "" {
		tp = append(tp, gates)
		fp = append(fp, gates)
	}
	if n := len(corrections); n > 0 {
		fp = append(fp, fmt.Sprintf("%d correction(s)", n))
	}
	return fmt.Sprintf("Shipped-by: flywheel %s (%s)", version, strings.Join(tp, ", ")), strings.Join(fp, " · ")
}

// trailerLine matches a git trailer line, "Key: value" with no space in Key.
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*: \S`)

// splitTrailers splits s, its trailing blank lines dropped, into the text
// before its final paragraph and that paragraph when every line of it is a
// trailer line; otherwise rest is all of it and trailers is "".
func splitTrailers(s string) (rest, trailers string) {
	s = strings.TrimRight(s, " \t\r\n")
	i := strings.LastIndex(s, "\n\n")
	last := s[i+1:]
	for _, l := range strings.Split(strings.TrimLeft(last, "\n"), "\n") {
		if !trailerLine.MatchString(strings.TrimRight(l, "\r")) {
			return s, ""
		}
	}
	if i < 0 {
		return "", strings.TrimLeft(last, "\n")
	}
	return s[:i], strings.TrimLeft(last, "\n")
}

// withFooter adds footer to body as its own paragraph, above a final trailer
// paragraph (Co-Authored-By: and the like) so that stays the last one. A body
// already carrying a "Shipped by [flywheel]" line is returned as is.
func withFooter(body, footer string) string {
	if strings.Contains(body, "Shipped by [flywheel]") {
		return body
	}
	rest, trailers := splitTrailers(body)
	if trailers == "" {
		return rest + "\n\n" + footer + "\n"
	}
	if rest == "" {
		return footer + "\n\n" + trailers + "\n"
	}
	return rest + "\n\n" + footer + "\n\n" + trailers + "\n"
}

// withTrailer appends trailer to msg's final trailer paragraph, or as a new
// paragraph when msg does not end in one. A message already carrying a
// "Shipped-by: flywheel" line is returned as is.
func withTrailer(msg, trailer string) string {
	for _, l := range strings.Split(msg, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "Shipped-by: flywheel") {
			return msg
		}
	}
	if _, trailers := splitTrailers(msg); trailers != "" {
		return strings.TrimRight(msg, " \t\r\n") + "\n" + trailer + "\n"
	}
	return strings.TrimRight(msg, " \t\r\n") + "\n\n" + trailer + "\n"
}

// transientErr reports whether err is a network blip worth retrying,
// name-resolution failures included (issue #577).
func transientErr(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "tls handshake timeout") || strings.Contains(s, "connection reset") || strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "could not resolve host") || strings.Contains(s, "temporary failure in name resolution")
}

// retry runs f, retrying a transient error after 2s, 4s, 8s and 16s.
func (r *shipRun) retry(what string, f func() error) error {
	backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i := 0; ; i++ {
		err := f()
		if err == nil || !transientErr(err) || i == len(backoff) {
			return err
		}
		fmt.Fprintf(r.o.Progress, "ship %s %s: transient error, retrying in %s: %v\n", r.task, what, backoff[i], err)
		r.o.Sleep(backoff[i])
	}
}

// lookupPR finds fw/<task>'s open or merged PR once per ship.
func (r *shipRun) lookupPR() (bool, error) {
	if r.pr.Number != 0 {
		return true, nil
	}
	found := false
	err := r.retry("pr", func() (err error) { r.pr, found, err = r.o.Forge.PR("fw/" + r.task); return err })
	return found, err
}

// needPR is lookupPR for the steps after pr, which need a PR to act on.
func (r *shipRun) needPR() error {
	found, err := r.lookupPR()
	if err == nil && !found {
		err = fmt.Errorf("fw/%s has no open or merged PR; rerun flywheel ship %s so its pr step opens one", r.task, r.task)
	}
	return err
}

// prState re-reads the PR's state and merge commit.
func (r *shipRun) prState() (string, string, error) {
	var state, commit string
	err := r.retry("state", func() (err error) { state, commit, err = r.o.Forge.PRState(r.pr.Number); return err })
	if err == nil {
		r.pr.State, r.pr.MergeCommit = state, commit
	}
	return state, commit, err
}

// shipPush pushes fw/<task> to the remote with flywheel's git environment.
func shipPush(r *shipRun) (string, string, error) {
	branch := "fw/" + r.task
	err := r.retry("push", func() error { _, err := gitWith(r.wt, shipEnv(), "push", "-u", r.o.Remote, branch); return err })
	r.forget() // push -u writes the remote-tracking ref and the upstream config
	if err != nil {
		return "", "", fmt.Errorf("git push -u %s %s: %w", r.o.Remote, branch, err)
	}
	return "ok", fmt.Sprintf("pushed %s at %s to %s", branch, short7(r.fwHead()), r.o.Remote), nil
}

// shipPR reuses fw/<task>'s open or merged PR (skip) or opens one against the
// integration branch with Title and Body.
func shipPR(r *shipRun) (string, string, error) {
	found, err := r.lookupPR()
	if err != nil {
		return "", "", err
	}
	if found {
		return "skip", fmt.Sprintf("reusing #%d %s (%s)", r.pr.Number, r.pr.URL, r.pr.State), nil
	}
	if err := r.retry("pr", func() (err error) {
		r.pr, err = r.o.Forge.CreatePR(r.o.Integration, "fw/"+r.task, r.o.Title, r.o.Body)
		return err
	}); err != nil {
		return "", "", err
	}
	return "ok", fmt.Sprintf("opened #%d %s", r.pr.Number, r.pr.URL), nil
}

// keepChecks is names without IgnoreChecks.
func (r *shipRun) keepChecks(names []string) []string {
	var out []string
	for _, c := range names {
		if !slices.Contains(r.o.IgnoreChecks, c) {
			out = append(out, c)
		}
	}
	return out
}

// shipExpectedFrom is how many of the pull requests last merged into the
// integration branch expectedChecks reads.
const shipExpectedFrom = 3

// expectedChecks is the checks ci waits for (issue #640), sorted, each once,
// IgnoreChecks disregarded: ship.required_checks when set (commit statuses
// included), else the check runs reported on every one of the head commits of
// the last shipExpectedFrom pull requests merged into the integration branch
// (fewer when fewer merged, none when none did). Not the integration branch's
// own head: it also carries the checks of workflows that run only on a push
// to it, which never run on a PR. Not commit statuses: they come from
// external bots, which stop posting and would then block every ship (c3).
func (r *shipRun) expectedChecks() ([]string, error) {
	names := slices.Clone(r.required)
	if len(names) == 0 {
		var heads []string
		if err := r.retry("ci", func() (err error) {
			heads, err = r.o.Forge.MergedPRHeads(r.o.Integration, shipExpectedFrom)
			return err
		}); err != nil {
			return nil, err
		}
		for i, h := range heads {
			var on []string
			if err := r.retry("ci", func() (err error) { on, err = r.o.Forge.CommitCheckRuns(h); return err }); err != nil {
				return nil, err
			}
			if i == 0 {
				names = slices.Clone(on)
				continue
			}
			names = slices.DeleteFunc(names, func(c string) bool { return !slices.Contains(on, c) })
		}
	}
	names = r.keepChecks(names)
	slices.Sort(names)
	return slices.Compact(names), nil
}

// shipCI polls the PR's checks every Poll (issue #640), IgnoreChecks
// disregarded and checks reported on another commit than fw/<task>'s head
// not counted. It passes only when every expected check (expectedChecks) is
// on the head, passed or skipped (NEUTRAL, SKIPPED), none is pending or
// failed, at least one passed, and the
// set of check names was the same on two consecutive polls, so a check that
// passes at once cannot merge the PR before CI's jobs register. A failed
// check with none pending fails at once. When ship.required_checks is set, a
// required check that concluded SKIPPED or NEUTRAL fails at once too, even
// with checks pending (note required-check-skipped, issue #653): a required
// check must conclude SUCCESS. After CITimeout it fails naming the expected
// checks still missing and the checks still pending.
func shipCI(r *shipRun) (string, string, error) {
	if err := r.needPR(); err != nil {
		return "", "", err
	}
	n := r.pr.Number
	if r.pr.State == "MERGED" {
		return "skip", fmt.Sprintf("#%d already merged", n), nil
	}
	expected, err := r.expectedChecks()
	if err != nil {
		return "", "", err
	}
	exp := ""
	if len(expected) > 0 {
		exp = " (expected: " + strings.Join(expected, ", ") + ")"
	}
	head := r.fwHead()
	last, polled := "", false
	for waited := time.Duration(0); ; waited += r.o.Poll {
		var cs ChecksState
		if err := r.retry("ci", func() (err error) { cs, err = r.o.Forge.Checks(n); return err }); err != nil {
			return "", "", err
		}
		if cs.Head != "" && head != "" && cs.Head != head {
			cs = ChecksState{Head: cs.Head}
		}
		pending, failed, passed := r.keepChecks(cs.Pending), r.keepChecks(cs.Failed), r.keepChecks(cs.Passed)
		if len(pending) == 0 && len(failed) > 0 {
			list := strings.Join(failed, ", ")
			return "", "failed: " + list, fmt.Errorf("%w on #%d: %s", ErrShipCI, n, list)
		}
		skipped, neutral := r.keepChecks(cs.Skipped), r.keepChecks(cs.Neutral)
		// A check named in ship.required_checks must conclude SUCCESS: a skip
		// is final, so fail at once, pending checks or not (issue #653).
		if len(r.required) > 0 {
			var did []string
			for _, e := range expected {
				if slices.Contains(skipped, e) {
					did = append(did, e+" (SKIPPED)")
				} else if slices.Contains(neutral, e) {
					did = append(did, e+" (NEUTRAL)")
				}
			}
			if len(did) > 0 {
				list := strings.Join(did, ", ")
				return "", "required-check-skipped: " + list, fmt.Errorf("%w on #%d: required check(s) did not run: %s; a path filter or job condition skipped them, and a required check must conclude SUCCESS", ErrShipCI, n, list)
			}
		}
		// A skipped or neutral check is present: not missing, not passed.
		all := slices.Concat(pending, failed, passed, skipped, neutral)
		slices.Sort(all)
		all = slices.Compact(all)
		key := strings.Join(all, "\n")
		settled := polled && key == last
		last, polled = key, true
		var missing []string
		for _, e := range expected {
			if !slices.Contains(all, e) {
				missing = append(missing, e)
			}
		}
		if len(pending) == 0 && len(missing) == 0 && len(passed) > 0 && settled {
			return "ok", fmt.Sprintf("%d check(s) passed on #%d%s", len(passed), n, exp), nil
		}
		if waited >= r.o.CITimeout {
			var still []string
			if len(missing) > 0 {
				still = append(still, "missing: "+strings.Join(missing, ", "))
			}
			if len(pending) > 0 {
				still = append(still, "still pending: "+strings.Join(pending, ", "))
			}
			if len(still) == 0 && len(all) == 0 {
				still = append(still, "no checks reported")
			} else if len(still) == 0 {
				still = append(still, "checks still changing")
			}
			s := strings.Join(still, "; ")
			return "", "timed out, " + s, fmt.Errorf("%w: #%d timed out after %s, %s", ErrShipCI, n, r.o.CITimeout, s)
		}
		r.o.Sleep(r.o.Poll)
	}
}

// shipMerge squash merges the PR with the title "<Title> (#<n>)" and the
// scrubbed Body ending in the Shipped-by: trailer (unless it is off), then re-reads the PR and requires MERGED: a merge call's
// success is not proof. An already merged PR is skip, with no merge call.
func shipMerge(r *shipRun) (string, string, error) {
	if err := r.needPR(); err != nil {
		return "", "", err
	}
	n := r.pr.Number
	state, commit, err := r.prState()
	if err != nil {
		return "", "", err
	}
	if state == "MERGED" {
		return "skip", fmt.Sprintf("#%d already merged as %s", n, short7(commit)), nil
	}
	// CI measured fw/<task>'s pushed HEAD: refuse it unless it contains the
	// integration commit it would land on (issue #591).
	cur, err := r.integrationCommit()
	if err != nil {
		return "", "", fmt.Errorf("resolving %s/%s before merge: %w", r.o.Remote, r.o.Integration, err)
	}
	in, err := isAncestor(r.wt, cur, "refs/heads/fw/"+r.task)
	if err != nil {
		return "", "", err
	}
	if !in {
		note := fmt.Sprintf("%s/%s moved to %s after CI ran on %s", r.o.Remote, r.o.Integration, short7(cur), short7(r.fwHead()))
		return "", note, fmt.Errorf("%w: %s", ErrShipStale, note)
	}
	title := fmt.Sprintf("%s (#%d)", r.o.Title, n)
	msg := scrubMessage(r.o.Body)
	if r.trailer != "" {
		msg = withTrailer(msg, r.trailer)
	}
	merr := r.retry("merge", func() error { return r.o.Forge.Merge(n, title, msg) })
	state, commit, err = r.prState()
	if err != nil {
		return "", "", err
	}
	if state == "MERGED" {
		return "ok", fmt.Sprintf("merged #%d as %s", n, short7(commit)), nil
	}
	if merr != nil {
		return "", "", merr
	}
	return "", "state " + state, fmt.Errorf("merging #%d reported success but the PR is %s, not MERGED", n, state)
}

// shipLanded records the landing with the PR's merge commit through LandTask,
// the function `flywheel land --commit` uses; already landed with it is skip.
func shipLanded(r *shipRun) (string, string, error) {
	defer r.forget() // LandTask may write to the repository
	if err := r.needPR(); err != nil {
		return "", "", err
	}
	state, commit, err := r.prState()
	if err != nil {
		return "", "", err
	}
	if state != "MERGED" || !CommitOK(commit) {
		return "", "", fmt.Errorf("#%d is %s with merge commit %q; land needs a merged PR", r.pr.Number, state, commit)
	}
	err = LandTask(r.dir, r.task, commit, fmt.Sprintf("shipped as #%d", r.pr.Number), false, "")
	if errors.Is(err, ErrAlreadyLanded) {
		return "skip", "already landed " + short7(commit), nil
	}
	if err != nil {
		return "", "", err
	}
	return "ok", "landed " + commit, nil
}

// shipClosed comments the PR link on the planned issue and closes it when the
// Body says Fixes #<issue>; otherwise it only comments that part landed.
func shipClosed(r *shipRun) (string, string, error) {
	if r.issue == 0 {
		return "skip", "no issue", nil
	}
	if err := r.needPR(); err != nil {
		return "", "", err
	}
	n, issue := r.pr.Number, r.issue
	fixes := regexp.MustCompile(fmt.Sprintf(`(?i)\bfixes #%d\b`, issue)).MatchString(r.o.Body)
	if !fixes {
		msg := fmt.Sprintf("Part of this issue landed in #%d", n)
		if err := r.retry("closed", func() error { return r.o.Forge.CommentIssue(issue, msg) }); err != nil {
			return "", "", err
		}
		return "ok", fmt.Sprintf("commented on #%d, left open (the body has no Fixes #%d)", issue, issue), nil
	}
	msg := fmt.Sprintf("Landed in #%d %s", n, r.pr.URL)
	if err := r.retry("closed", func() error { return r.o.Forge.CommentIssue(issue, strings.TrimSpace(msg)) }); err != nil {
		return "", "", err
	}
	if err := r.retry("closed", func() error { return r.o.Forge.CloseIssue(issue, "") }); err != nil {
		return "", "", err
	}
	return "ok", fmt.Sprintf("commented on and closed #%d", issue), nil
}
