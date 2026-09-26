package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RecoverReport is `flywheel recover`'s one deterministic answer (issue
// #422): whether the ledger is intact, where every unit is, whether the world
// (worktrees, branches, leases, run files) is consistent with the log, and the
// next safe action per unit.
type RecoverReport struct {
	At        string           `json:"at"`
	Integrity RecoverIntegrity `json:"integrity"`
	Tasks     []RecoverTask    `json:"tasks"`
	// DormantAfter is the RecoverOptions.DormantAfter the report used; empty
	// when dormancy was disabled.
	DormantAfter string `json:"dormant_after,omitempty"`
	all          bool   // Text prints every unit and every history item
	session      string // RecoverOptions.Session: Text marks units other leads dispatched
}

// RecoverOptions configures one recover pass (issue #422).
type RecoverOptions struct {
	// DormantAfter marks a non-landed unit whose latest event is older than
	// this dormant: reported, never acted on by RecoverApply. 0 disables.
	DormantAfter time.Duration
	// All makes Text list landed and dormant units and every history item
	// instead of summary lines.
	All bool
	// Session is the current lead session (issue #472): its integrity
	// failures are grouped first, and Text marks units other leads dispatched.
	Session string
}

// RecoverIntegrity is the log chain and the verify rules over every task.
// Failed are the chain and the rule failures on units not landed: they fail
// integrity. History are rule failures on landed units, which can no longer
// be acted on: reported and counted, never failing integrity. ByLead is
// Failed grouped by the lead that dispatched each item's task (issue #472).
type RecoverIntegrity struct {
	Pass    bool               `json:"pass"`
	Chain   LogChain           `json:"chain"`
	Failed  []VerifyItem       `json:"failed,omitempty"`
	ByLead  []RecoverLeadGroup `json:"by_lead,omitempty"`
	History []VerifyItem       `json:"history,omitempty"`
}

// RecoverLeadGroup is the integrity failures on units one lead dispatched:
// Lead is the latest dispatched event's lead, "" when unrecorded; Current
// marks RecoverOptions.Session's group, which is listed first.
type RecoverLeadGroup struct {
	Lead    string       `json:"lead"`
	Current bool         `json:"current,omitempty"`
	Items   []VerifyItem `json:"items"`
}

// groupByLead groups items by leads[item.Task]: the group of session (when
// non-empty) first and Current, then the other recorded leads sorted, then
// the unrecorded ("") last. Items keep their order within a group.
func groupByLead(items []VerifyItem, leads map[string]string, session string) []RecoverLeadGroup {
	byLead := map[string][]VerifyItem{}
	for _, it := range items {
		byLead[leads[it.Task]] = append(byLead[leads[it.Task]], it)
	}
	var others []string
	for l := range byLead {
		if l != "" && (session == "" || l != session) {
			others = append(others, l)
		}
	}
	slices.Sort(others)
	var groups []RecoverLeadGroup
	if its, ok := byLead[session]; ok && session != "" {
		groups = append(groups, RecoverLeadGroup{Lead: session, Current: true, Items: its})
	}
	for _, l := range others {
		groups = append(groups, RecoverLeadGroup{Lead: l, Items: byLead[l]})
	}
	if its, ok := byLead[""]; ok {
		groups = append(groups, RecoverLeadGroup{Lead: "", Items: its})
	}
	return groups
}

// latestLeads maps each task to the lead of its latest dispatched event that
// records one, so a dispatch without --session (a review --fix round) keeps it.
func latestLeads(events []Event) map[string]string {
	leads := map[string]string{}
	for _, e := range events {
		if e.Kind == "dispatched" && e.Task != "" && e.Lead != "" {
			leads[e.Task] = e.Lead
		}
	}
	return leads
}

// RecoverTask is one unit's derived state, its world checks and next action.
type RecoverTask struct {
	Task    string `json:"task"`
	Status  string `json:"status"`
	Attempt string `json:"attempt,omitempty"`
	Model   string `json:"model,omitempty"`
	Lead    string `json:"lead,omitempty"` // the latest recorded dispatched lead (issue #472)
	// Worktree is the unit's task worktree or recorded workdir; empty when
	// the unit works in the flywheel root, whose changes are shared.
	Worktree string `json:"worktree,omitempty"`
	Head     string `json:"head,omitempty"`   // the worktree's HEAD commit
	Commit   string `json:"commit,omitempty"` // the attempt's finished.commit
	// Uncommitted are the worktree's changed paths (not flywheel's own);
	// Unexplained those the current attempt's wrote list does not name.
	Uncommitted []string `json:"uncommitted,omitempty"`
	Unexplained []string `json:"unexplained,omitempty"`
	Lease       string   `json:"lease"`    // live, dead or none
	RunFile     string   `json:"run_file"` // complete, torn or missing
	Stacked     string   `json:"stacked,omitempty"`
	PausedUntil string   `json:"paused_until,omitempty"`
	Checkpoints []string `json:"checkpoints,omitempty"` // "<attempt> <sha7> (<paths>)"
	// Dormant marks a non-landed unit whose latest event is older than
	// RecoverOptions.DormantAfter: Next is still computed, but RecoverApply
	// never acts on it.
	Dormant bool `json:"dormant,omitempty"`
	Next    Next `json:"next"`
}

// Next is a unit's next action: one of mark-lost, wait-reset, resume-session,
// rebase, assign-owner (issue #458), re-validate, review, inspect, land,
// investigate or none, with the reason and the exact command.
type Next struct {
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Command string `json:"command,omitempty"`
}

// recoverFacts is what nextAction decides from: the task's state and the
// measured world, gathered by Recover.
type recoverFacts struct {
	Task, Status, Attempt string
	FinishReason          string // the current attempt's finished reason, "" when none
	HeadMismatch          string // why the branch head is not the attempt's commit
	Unexplained           []string
	LeaseLive             bool
	Lost                  string // lostEvidence's reason: evidence, "" when not lost
	PausedUntil           string
	Stacked               string
	HaveReading           bool // an owns_checked reading after the finish
	TreeChanged           bool // the tree now differs from that reading's
	InspectReady          bool // inspectionReady
	PanelPending          []string
	NeedsOwner            []string // open blocking findings outside owns (issue #458)
}

// nextAction is the deterministic next-action rule (docs/PROTOCOL.md).
func nextAction(f recoverFacts) Next {
	t := f.Task
	inFlight := f.Status == "dispatched" || f.Status == "running"
	switch {
	case f.Status == "landed":
		return Next{Action: "none", Reason: "landed"}
	case !inFlight && f.HeadMismatch != "":
		return Next{Action: "investigate", Reason: f.HeadMismatch}
	case !inFlight && len(f.Unexplained) > 0:
		return Next{Action: "investigate", Reason: "uncommitted changes no attempt wrote: " + clipNote(strings.Join(f.Unexplained, ", "))}
	case inFlight && f.LeaseLive:
		return Next{Action: "none", Reason: "in flight; its lease is live"}
	case inFlight && f.Lost != "":
		return Next{Action: "mark-lost", Reason: f.Lost, Command: "flywheel recover --apply"}
	case inFlight:
		return Next{Action: "none", Reason: "in flight; no live lease, not yet idle past limits.lost_after"}
	}
	unclean := f.Status == "finished" && f.FinishReason != "stop" || f.Status == "lost"
	if unclean && f.PausedUntil != "" {
		return Next{Action: "wait-reset", Reason: "model paused by a rate limit until " + f.PausedUntil + "; flywheel run --resume waits for the reset", Command: "flywheel run " + t + " --resume"}
	}
	switch {
	case f.Status == "finished" && (f.FinishReason == "rate-limited" || f.FinishReason == "abandoned-job"):
		return Next{Action: "resume-session", Reason: f.FinishReason + "; the model is not paused", Command: "flywheel run " + t + " --resume"}
	case f.Status == "lost":
		return Next{Action: "none", Reason: "attempt " + f.Attempt + " lost; dispatch again (flywheel run " + t + ")"}
	case f.Status == "finished" && f.FinishReason != "stop":
		return Next{Action: "none", Reason: fmt.Sprintf("attempt %s ended %s; correct or dispatch again", f.Attempt, f.FinishReason)}
	case (f.Status == "finished" || f.Status == "passed") && f.Stacked != "":
		return Next{Action: "rebase", Reason: f.Stacked, Command: "flywheel rebase " + t}
	case f.Status == "passed":
		return Next{Action: "land", Reason: "passed", Command: "flywheel land " + t}
	case len(f.NeedsOwner) > 0:
		// No command prints the thread: it is the generated file named here.
		return Next{Action: "assign-owner", Reason: fmt.Sprintf("%d open blocking finding(s) outside owns: %s; assign them to another unit, amend owns (flywheel log --kind amended), or dismiss them (thread %s)",
			len(f.NeedsOwner), clipNote(strings.Join(f.NeedsOwner, ", ")), ReviewThreadPath(t)), Command: "flywheel review " + t + " --dismiss <id> --session <lead> --note <why>"}
	case f.Status != "finished":
		return Next{Action: "none", Reason: f.Status}
	case !f.HaveReading:
		return Next{Action: "re-validate", Reason: "no reading since the finish", Command: "flywheel validate " + t}
	case f.TreeChanged:
		return Next{Action: "re-validate", Reason: "the tree changed since the last reading", Command: "flywheel validate " + t}
	case f.InspectReady && len(f.PanelPending) > 0:
		return Next{Action: "review", Reason: "validated; review panel incomplete: " + strings.Join(f.PanelPending, ", "), Command: "flywheel review " + t + " --agent --panel --session <lead>"}
	case f.InspectReady:
		return Next{Action: "inspect", Reason: "readings complete and passing", Command: "flywheel inspect " + t + " --verdict pass --session <lead>"}
	}
	return Next{Action: "none", Reason: "the latest readings failed on the current tree; correct the unit"}
}

// Recover reads the ledger and the world and reports integrity, every unit's
// checks and its next action (issue #422). It writes nothing.
func Recover(dir string, now time.Time, o RecoverOptions) (RecoverReport, error) {
	rep := RecoverReport{At: now.UTC().Format(time.RFC3339), Tasks: []RecoverTask{}, all: o.All, session: o.Session}
	if o.DormantAfter > 0 {
		rep.DormantAfter = o.DormantAfter.String()
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return rep, err
	}
	state := Derive(events)
	landed := map[string]bool{}
	for _, ts := range state.Tasks {
		landed[ts.ID] = ts.Status == "landed"
	}
	leads := latestLeads(events)
	chain, err := VerifyLogChain(dir)
	if err != nil {
		return rep, err
	}
	vr, err := VerifyTasks(dir, VerifyOptions{Dir: dir, All: true})
	if err != nil {
		return rep, err
	}
	rep.Integrity = RecoverIntegrity{Chain: chain}
	for _, it := range vr.Items {
		switch {
		case it.Pass:
		case landed[it.Task]:
			rep.Integrity.History = append(rep.Integrity.History, it)
		default:
			rep.Integrity.Failed = append(rep.Integrity.Failed, it)
		}
	}
	rep.Integrity.ByLead = groupByLead(rep.Integrity.Failed, leads, o.Session)
	rep.Integrity.Pass = chain.OK() && len(rep.Integrity.Failed) == 0
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return rep, err
	}
	obs, err := observe(dir)
	if err != nil {
		return rep, err
	}
	cps, err := ListCheckpoints(dir, "")
	if err != nil {
		return rep, err
	}
	for _, ts := range state.Tasks {
		t, f := recoverTask(dir, ts, events, obs, cfg, now)
		t.Next = nextAction(f)
		t.Lead = leads[ts.ID]
		if last, err := time.Parse(time.RFC3339Nano, ts.UpdatedAt); err == nil && o.DormantAfter > 0 && ts.Status != "landed" && now.Sub(last) > o.DormantAfter {
			t.Dormant = true
		}
		for _, cp := range cps {
			if cp.Task == ts.ID {
				t.Checkpoints = append(t.Checkpoints, fmt.Sprintf("%s %s (%s)", cp.Attempt, short7(cp.SHA), strings.Join(cp.Paths, ", ")))
			}
		}
		rep.Tasks = append(rep.Tasks, t)
	}
	return rep, nil
}

// recoverTask measures one unit's world and returns its report row (Next
// unset) and the facts nextAction decides from.
func recoverTask(dir string, ts TaskState, events []Event, obs Observed, cfg Config, now time.Time) (RecoverTask, recoverFacts) {
	id, att := ts.ID, ts.Attempt
	t := RecoverTask{Task: id, Status: ts.Status, Attempt: att, Model: ts.Model, Lease: "none", RunFile: runFileState(dir, id, att)}
	f := recoverFacts{Task: id, Status: ts.Status, Attempt: att}
	for _, l := range obs.Leases {
		if l.Task == id && l.Attempt == att {
			t.Lease, f.LeaseLive = "dead", LeaseLive(l, now)
			if f.LeaseLive {
				t.Lease = "live"
			}
		}
	}
	if reason, evidence, ok := lostEvidence(ts, events, obs, PolicyFromConfig(cfg), now); ok {
		f.Lost = reason + ": " + evidence
	}
	if p, ok := rateLimitPausedAt(events, ts.Model, now, cfg.Limits.RateLimitPauseThreshold()); ok && ts.Model != "" {
		t.PausedUntil = p.Until.UTC().Format(time.RFC3339)
		f.PausedUntil = t.PausedUntil
	}
	if ts.Status == "finished" || ts.Status == "passed" {
		if base, landedAs, baseTask, ok := SquashedBase(dir, events, id); ok {
			t.Stacked = stackedFix(id, base, landedAs, baseTask)
			f.Stacked = t.Stacked
		}
	}
	var fin *Event
	for i := range events {
		if e := events[i]; e.Task == id && e.Kind == "finished" && e.Attempt == att {
			fin = &events[i]
		}
	}
	if fin != nil {
		f.FinishReason, t.Commit = fin.Reason, fin.Commit
	}
	if abs, err := filepath.Abs(dir); err == nil {
		if wt := filepath.Join(abs, ".flywheel", "worktrees", id); dirExists(wt) {
			t.Worktree = wt
		} else if rw := recordedWorkdir(events, id); rw != "" && !samePath(rw, abs) {
			t.Worktree = rw
		}
	}
	if t.Worktree != "" && ts.Status != "landed" {
		worldChecks(&t, &f, fin, events)
	}
	if ts.Status != "landed" {
		f.NeedsOwner = needsOwnerFindings(dir, events, id)
	}
	if fin != nil && fin.Reason == "stop" {
		oh, ot, tree, _ := latestReading(events, id, "owns_checked", att)
		ft, _ := time.Parse(time.RFC3339Nano, fin.TS)
		f.HaveReading = oh && ot.After(ft)
		wd := t.Worktree
		if wd == "" {
			wd = dir
		}
		if cur, err := treeHash(wd); f.HaveReading && err == nil && cur != tree {
			f.TreeChanged = true
		}
		f.InspectReady = inspectionReady(events, id, att)
		if panel := cfg.PanelDimensions(); f.InspectReady && len(panel) > 0 && panelApplies(events, id, cfg.ReviewRequired()) {
			f.PanelPending = panelIncomplete(VerdictMatrix(events, id, tree, panel), panel)
		}
	}
	return t, f
}

// worldChecks compares the unit's worktree with the ledger: its HEAD against
// the attempt's finished.commit (a later rebased event explains a move), and
// its uncommitted paths against the attempt's wrote list, the dispatch
// baseline (same content) and the paths worktree setup linked.
func worldChecks(t *RecoverTask, f *recoverFacts, fin *Event, events []Event) {
	wt := t.Worktree
	t.Head = headCommit(wt)
	// A lead commit or merge on top of the attempt's commit is the normal flow:
	// only a HEAD that does not contain the commit disagrees with the ledger.
	// A git failure (an unknown object, say) is unknown, never a mismatch.
	if fin != nil && fin.Commit != "" && t.Head != "" && t.Head != fin.Commit && !rebasedAfter(events, t.Task, fin.TS) {
		if in, err := isAncestor(wt, fin.Commit, "HEAD"); err == nil && !in {
			f.HeadMismatch = fmt.Sprintf("worktree HEAD %s does not contain attempt %s's commit %s", short7(t.Head), t.Attempt, short7(fin.Commit))
		}
	}
	changed, err := changedPaths(wt)
	if err != nil {
		return
	}
	for _, p := range changed {
		if !isFlywheelOwnPath(p) {
			t.Uncommitted = append(t.Uncommitted, p)
		}
	}
	if fin == nil {
		return // no finished attempt: no wrote list to explain the changes by
	}
	explained := map[string]bool{}
	for _, p := range fin.Wrote {
		explained[relTo(wt, p)] = true
	}
	baseline := baselineFor(events, t.Task)
	var linked []string
	for _, e := range events {
		if e.Task == t.Task && e.Kind == "worktree_setup" {
			linked = append(linked, e.Linked...)
		}
	}
	for _, p := range t.Uncommitted {
		if h, ok := baseline[p]; explained[p] || ok && h == fileSHA(wt, p) || ownsContains(linked, p) && len(linked) > 0 {
			continue
		}
		t.Unexplained = append(t.Unexplained, p)
	}
	f.Unexplained = t.Unexplained
}

// rebasedAfter reports whether task has a rebased event after ts.
func rebasedAfter(events []Event, task, ts string) bool {
	at, err := time.Parse(time.RFC3339Nano, ts)
	for _, e := range events {
		if e.Task == task && e.Kind == "rebased" {
			if et, eerr := time.Parse(time.RFC3339Nano, e.TS); err != nil || eerr != nil || et.After(at) {
				return true
			}
		}
	}
	return false
}

// relTo returns a wrote path relative to wt with forward slashes.
func relTo(wt, p string) string {
	if filepath.IsAbs(p) || isRootedPath(p) {
		if r, err := filepath.Rel(wt, p); err == nil {
			p = r
		}
	}
	return filepath.ToSlash(filepath.Clean(p))
}

// runFileState is complete when the attempt's run file ends in a newline (or
// is empty), torn when its last line is cut off, missing when there is none.
func runFileState(dir, task, attempt string) string {
	if attempt == "" {
		return "missing"
	}
	fh, err := os.Open(filepath.Join(dir, ".flywheel", "runs", task+"."+attempt+".jsonl"))
	if err != nil {
		return "missing"
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil || info.Size() == 0 {
		return "complete"
	}
	b := make([]byte, 1)
	if _, err := fh.ReadAt(b, info.Size()-1); err != nil || b[0] != '\n' {
		return "torn"
	}
	return "complete"
}

// RecoverApplied is what `flywheel recover --apply` did: the safe actions it
// applied, the actions it left for the lead, and the report taken after.
type RecoverApplied struct {
	Applied []string      `json:"applied"`
	Left    []string      `json:"left"`
	Report  RecoverReport `json:"report"`
}

// RecoverApply runs only the safe-by-construction next actions (issue #422):
// mark-lost (markLost, which also checkpoints the lost attempt's work),
// re-validate (ValidateTask, a measurement) and rebase when the squashed base
// is certain (RebaseUnit aborts on a conflict and leaves the branch as it
// was). resume-session, land, inspect, review, assign-owner, wait-reset and
// investigate are never run; they are listed in Left. Anything applied is recorded in one
// recovered event (Note the actions, Paths the tasks). A dormant unit is never
// acted on: it goes to Left as dormant.
func RecoverApply(dir string, now time.Time, o RecoverOptions) (RecoverApplied, error) {
	out := RecoverApplied{Applied: []string{}, Left: []string{}}
	rep, err := Recover(dir, now, o)
	if err != nil {
		return out, err
	}
	var tasks []string
	lostDone := false
	for _, t := range rep.Tasks {
		n := t.Next
		switch {
		case n.Action == "none":
		case t.Dormant:
			out.Left = append(out.Left, fmt.Sprintf("dormant %s: %s (%s), never applied", t.Task, n.Action, n.Reason))
		case n.Action == "mark-lost":
			if lostDone {
				continue
			}
			lostDone = true
			marked, err := markLostActive(dir, now, rep.Tasks)
			if err != nil {
				return out, err
			}
			for _, a := range marked {
				line := fmt.Sprintf("mark-lost %s %s (%s)", a.Task, a.Attempt, a.Reason)
				if sha, note := checkpointLost(dir, a.Task, a.Attempt); sha != "" || note != "" {
					line += " " + strings.TrimSpace("checkpoint "+short7(sha)+" "+note)
				}
				out.Applied = append(out.Applied, line)
				tasks = append(tasks, a.Task)
			}
		case n.Action == "re-validate":
			if _, err := ValidateTask(dir, t.Task, ValidateOptions{Dir: dir}); err != nil {
				out.Left = append(out.Left, fmt.Sprintf("re-validate %s: %v", t.Task, err))
				continue
			}
			out.Applied = append(out.Applied, "re-validate "+t.Task)
			tasks = append(tasks, t.Task)
		case n.Action == "rebase" && t.Stacked != "":
			newBase, conflicts, err := RebaseUnit(dir, t.Task, "")
			switch {
			case err != nil:
				out.Left = append(out.Left, fmt.Sprintf("rebase %s: %v", t.Task, err))
			case len(conflicts) > 0:
				out.Left = append(out.Left, fmt.Sprintf("rebase %s: conflicts in %s; aborted, fw/%s unchanged", t.Task, strings.Join(conflicts, ", "), t.Task))
			default:
				out.Applied = append(out.Applied, fmt.Sprintf("rebase %s onto %s", t.Task, short7(newBase)))
				tasks = append(tasks, t.Task)
			}
		default:
			left := n.Action + " " + t.Task + ": " + n.Reason
			if n.Command != "" {
				left += " (" + n.Command + ")"
			}
			out.Left = append(out.Left, left)
		}
	}
	if len(out.Applied) > 0 {
		slices.Sort(tasks)
		if err := AppendEvent(dir, Event{Kind: "recovered", Note: strings.Join(out.Applied, "; "), Paths: slices.Compact(tasks)}); err != nil {
			return out, err
		}
		_, _ = WriteState(dir)
	}
	out.Report, err = Recover(dir, now, o)
	return out, err
}

// markLostActive is markLost limited to the units of tasks whose next action
// is mark-lost and that are not dormant: it appends a lost event for each
// MARK_LOST action Reconcile gives for them, exactly as markLost does.
func markLostActive(dir string, now time.Time, tasks []RecoverTask) ([]Action, error) {
	active := map[string]bool{}
	for _, t := range tasks {
		active[t.Task] = t.Next.Action == "mark-lost" && !t.Dormant
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	obs, err := observe(dir)
	if err != nil {
		return nil, err
	}
	ts := now.UTC().Format(time.RFC3339Nano)
	var marked []Action
	for _, a := range Reconcile(Derive(events), events, obs, PolicyFromConfig(cfg), now) {
		if a.Kind != "MARK_LOST" || !active[a.Task] {
			continue
		}
		if err := appendLost(dir, ts, a); err != nil {
			return marked, err
		}
		marked = append(marked, a)
	}
	return marked, nil
}

// OK reports whether the world can be resumed without a human: integrity
// passes and no unit's next action is investigate.
func (r RecoverReport) OK() bool {
	if !r.Integrity.Pass {
		return false
	}
	for _, t := range r.Tasks {
		if t.Next.Action == "investigate" {
			return false
		}
	}
	return true
}

// Text renders the report for a terminal: integrity, history, one summary
// line each for landed and dormant units (every unit with RecoverOptions.All),
// then one block per remaining unit.
func (r RecoverReport) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "flywheel recover at %s\n", r.At)
	c, hist := r.Integrity.Chain, r.Integrity.History
	if r.Integrity.Pass {
		// An acknowledged break (issue #436) passes and is named.
		fmt.Fprintf(&b, "integrity: pass (log chain intact, %d lines%s; no rule fails on a unit not landed)\n", c.Lines, c.AckText())
	} else {
		b.WriteString("integrity: FAIL\n")
		if !c.OK() {
			file, reason := c.File, c.BreakReason
			if file == "" {
				file = "events.jsonl"
			}
			if reason == "" {
				reason = "prev matches no earlier line"
			}
			fmt.Fprintf(&b, "  log chain: %s line %d: %s", file, c.BreakLine, reason)
			if c.BreakPrev != "" {
				fmt.Fprintf(&b, " (prev %s)", c.BreakPrev[:min(12, len(c.BreakPrev))])
			}
			b.WriteString("\n")
		}
		if recorded := slices.ContainsFunc(r.Integrity.ByLead, func(g RecoverLeadGroup) bool { return g.Lead != "" }); !recorded {
			for _, it := range r.Integrity.Failed {
				fmt.Fprintf(&b, "  %s %s: %s\n", it.Rule, it.Task, it.Reason)
			}
		} else {
			for _, g := range r.Integrity.ByLead {
				switch {
				case g.Lead == "":
					fmt.Fprintf(&b, "  lead unrecorded: %d\n", len(g.Items))
				case g.Current:
					fmt.Fprintf(&b, "  lead %s (this session): %d\n", g.Lead, len(g.Items))
				default:
					fmt.Fprintf(&b, "  lead %s: %d\n", g.Lead, len(g.Items))
				}
				for _, it := range g.Items {
					fmt.Fprintf(&b, "    %s %s: %s\n", it.Rule, it.Task, it.Reason)
				}
			}
		}
	}
	if len(hist) > 0 {
		fmt.Fprintf(&b, "history: %d rule failure(s) on landed units (reported, never failing integrity)\n", len(hist))
		shown := hist
		if !r.all && len(hist) > 5 {
			shown = hist[:5]
		}
		for _, it := range shown {
			fmt.Fprintf(&b, "  %s %s: %s\n", it.Rule, it.Task, it.Reason)
		}
		if len(shown) < len(hist) {
			fmt.Fprintf(&b, "  ... %d more (--all lists them)\n", len(hist)-len(shown))
		}
	}
	var landed, dormant []string
	for _, t := range r.Tasks {
		switch {
		case t.Status == "landed":
			landed = append(landed, t.Task)
		case t.Dormant:
			dormant = append(dormant, t.Task)
		}
	}
	if !r.all && len(landed) > 0 {
		fmt.Fprintf(&b, "%d landed units (--all lists them)\n", len(landed))
	}
	if !r.all && len(dormant) > 0 {
		fmt.Fprintf(&b, "%d dormant units, no event for over %s; --apply never acts on them: %s\n", len(dormant), r.DormantAfter, clipNote(strings.Join(dormant, ", ")))
	}
	for _, t := range r.Tasks {
		if !r.all && (t.Status == "landed" || t.Dormant) {
			continue
		}
		fmt.Fprintf(&b, "\n%s %s", t.Task, t.Status)
		if t.Dormant {
			b.WriteString(" (dormant)")
		}
		if t.Attempt != "" {
			fmt.Fprintf(&b, " %s", t.Attempt)
		}
		fmt.Fprintf(&b, " -> %s: %s", t.Next.Action, t.Next.Reason)
		if r.session != "" && t.Lead != "" && t.Lead != r.session {
			fmt.Fprintf(&b, " [lead %s]", t.Lead) // dispatched by another lead (issue #472)
		}
		b.WriteString("\n")
		if t.Next.Command != "" {
			fmt.Fprintf(&b, "  run: %s\n", t.Next.Command)
		}
		if t.Worktree != "" {
			fmt.Fprintf(&b, "  worktree %s head %s", t.Worktree, short7(t.Head))
			if t.Commit != "" {
				fmt.Fprintf(&b, " (attempt commit %s)", short7(t.Commit))
			}
			b.WriteString("\n")
		}
		if len(t.Uncommitted) > 0 {
			fmt.Fprintf(&b, "  uncommitted: %s\n", strings.Join(t.Uncommitted, ", "))
		}
		if len(t.Unexplained) > 0 {
			fmt.Fprintf(&b, "  unexplained: %s\n", strings.Join(t.Unexplained, ", "))
		}
		if t.Attempt != "" && t.Status != "landed" {
			fmt.Fprintf(&b, "  lease %s; run file %s\n", t.Lease, t.RunFile)
		}
		if t.PausedUntil != "" {
			fmt.Fprintf(&b, "  model %s paused until %s\n", t.Model, t.PausedUntil)
		}
		if t.Stacked != "" {
			fmt.Fprintf(&b, "  stacked: %s\n", t.Stacked)
		}
		for _, cp := range t.Checkpoints {
			fmt.Fprintf(&b, "  checkpoint %s\n", cp)
		}
	}
	return b.String()
}

// dirExists reports whether p is an existing directory.
func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
