package flywheel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
}

// shipStepFuncs runs each of ShipSteps: the outcome (ok or skip), a note, and
// an error when the step fails.
var shipStepFuncs = map[string]func(*shipRun) (result, note string, err error){
	"preflight":  shipPreflight,
	"commit":     shipCommit,
	"merge-base": shipMergeBase,
	"gates":      shipGates,
}

// Ship runs the local half of shipping task (issue #457): preflight, commit,
// merge-base and gates, in the task worktree only. Each step appends one
// shipped event and prints one progress line; a step an earlier run already
// recorded ok or skip (shipTrusted) is not run again. It stops at the first
// failure: a *RuleRefusal for preflight, an error wrapping ErrShipGates for
// gates, any other error otherwise.
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
	r := &shipRun{dir: abs, task: task, attempt: attempt, wt: wt, o: o, events: events}
	res := ShipResult{Task: task, Attempt: attempt, Workdir: wt, Integration: o.Integration}
	return r.steps(res)
}

// fwHead is fw/<task>'s commit, "" when it does not resolve.
func (r *shipRun) fwHead() string {
	head, err := gitWith(r.wt, nil, "rev-parse", "--verify", "-q", "refs/heads/fw/"+r.task)
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
	for i, name := range ShipSteps {
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
	changed, err := changedPaths(r.wt)
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
	owned, err := ownedChanged(r.wt, owns)
	if err != nil {
		return "", "", err
	}
	if len(owned) == 0 {
		return "skip", "nothing to commit", nil
	}
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
	sha, err := gitWith(r.wt, env, "commit-tree", tree, "-p", old, "-m", r.o.Message, "-m", "Flywheel-Task: "+r.task)
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
	if _, err := gitWith(r.wt, env, "fetch", r.o.Remote, r.o.Integration); err != nil {
		return "", "", fmt.Errorf("git fetch %s %s: %w", r.o.Remote, r.o.Integration, err)
	}
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
