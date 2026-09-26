package flywheel

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A group (issue #420) is a set of units validated together: a goal id (every
// task planned with that GoalID) or an explicit list "tasks:<a>,<b>,...". Its
// ledger records carry Task "group:<id>" (GroupTask), where the id is the goal
// id or the list with ':' and ',' turned into '-' (tasks:a,b -> tasks-a-b).

// IntegrationPersona is the cross-unit reviewer's persona: embedded with the
// panel personas but never a panel dimension (it reviews a group, not a unit).
const IntegrationPersona = "integration"

// GroupID is the ledger-safe id of a group.
func GroupID(group string) string {
	return strings.NewReplacer(":", "-", ",", "-", " ", "").Replace(strings.TrimSpace(group))
}

// GroupTask is the task a group's own records carry: "group:<GroupID>".
func GroupTask(group string) string {
	return "group:" + GroupID(group)
}

// groupTaskOK reports whether s is a group task: "group:" and a task id.
func groupTaskOK(s string) bool {
	id, ok := strings.CutPrefix(s, "group:")
	return ok && taskOK(id)
}

// GroupMembers lists the tasks of group in order: for a goal id, every task
// planned with that GoalID in first-planned order; for "tasks:a,b,...", the
// listed tasks, each of which must be in the ledger. An empty group is an
// error.
func GroupMembers(events []Event, group string) ([]string, error) {
	group = strings.TrimSpace(group)
	seen := map[string]bool{}
	var out []string
	if list, ok := strings.CutPrefix(group, "tasks:"); ok {
		known := map[string]bool{}
		for _, e := range events {
			known[e.Task] = true
		}
		for _, t := range strings.Split(list, ",") {
			if t = strings.TrimSpace(t); t == "" || seen[t] {
				continue
			}
			if !taskOK(t) || !known[t] {
				return nil, fmt.Errorf("group %s: task %q is not in the ledger", group, t)
			}
			seen[t] = true
			out = append(out, t)
		}
	} else {
		if !taskOK(group) {
			return nil, fmt.Errorf("group %q is neither a goal id nor tasks:<a>,<b>,...", group)
		}
		for _, e := range events {
			if e.Kind == "planned" && e.GoalID == group && !seen[e.Task] {
				seen[e.Task] = true
				out = append(out, e.Task)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("group %s has no member tasks", group)
	}
	return out, nil
}

// memberRef is what a member contributes to the integration tree: its task
// branch fw/<task> when it exists (a --worktree unit), else the commit of its
// latest finished event that carries one (#391), else "".
func memberRef(dir string, events []Event, task string) string {
	if _, err := gitRead(dir, []string{"rev-parse", "--verify", "-q", "refs/heads/fw/" + task}); err == nil {
		return "fw/" + task
	}
	commit := ""
	for _, e := range events {
		if e.Task == task && e.Kind == "finished" && e.Commit != "" {
			commit = e.Commit
		}
	}
	return commit
}

// IntegrationTree builds the group's combined tree (issue #420): a detached
// worktree of base (default main) in a temp dir, then each member's work
// (memberRef) merged in member order with git merge --no-ff --no-edit as
// flywheel's identity. A member with nothing to merge is skipped (ReviewGroup
// reports it); a merge that conflicts is aborted, its unmerged paths recorded
// in conflicts[task], and the next member continues. cleanup removes the
// worktree and the temp dir, and is safe to call on every return.
func IntegrationTree(dir string, events []Event, members []string, base string) (wt string, cleanup func(), conflicts map[string][]string, err error) {
	if base == "" {
		base = "main"
	}
	tmp, err := os.MkdirTemp("", "flywheel-group-")
	if err != nil {
		return "", func() {}, nil, err
	}
	wt = filepath.Join(tmp, "tree")
	cleanup = func() {
		_, _ = gitRead(dir, []string{"worktree", "remove", "--force", wt})
		_ = os.RemoveAll(tmp)
		_, _ = gitRead(dir, []string{"worktree", "prune"})
	}
	if _, err := gitRead(dir, []string{"worktree", "add", "--detach", wt, base}); err != nil {
		cleanup()
		return "", func() {}, nil, err
	}
	env := append(os.Environ(), "GIT_AUTHOR_NAME="+unitCommitName, "GIT_AUTHOR_EMAIL="+unitCommitEmail,
		"GIT_COMMITTER_NAME="+unitCommitName, "GIT_COMMITTER_EMAIL="+unitCommitEmail)
	conflicts = map[string][]string{}
	for _, m := range members {
		ref := memberRef(dir, events, m)
		if ref == "" {
			continue
		}
		rc, stdout, stderr, err := runCmdSplit(wt, gitArgs([]string{"merge", "--no-ff", "--no-edit", "-m", "flywheel group: merge " + m, ref}), env)
		if err != nil {
			cleanup()
			return "", func() {}, nil, err
		}
		if rc == 0 {
			continue
		}
		unmerged, _ := gitRead(wt, []string{"diff", "--name-only", "-z", "--diff-filter=U"})
		_, _, _, _ = runCmdSplit(wt, gitArgs([]string{"merge", "--abort"}), env)
		var paths []string
		for _, p := range strings.Split(unmerged, "\x00") {
			if p != "" {
				paths = append(paths, filepath.ToSlash(p))
			}
		}
		if len(paths) == 0 {
			cleanup()
			return "", func() {}, nil, errors.New("merge " + m + " (" + ref + ") failed: " + strings.TrimSpace(string(append(stderr, stdout...))))
		}
		conflicts[m] = paths
	}
	return wt, cleanup, conflicts, nil
}

// GroupGate is one review.group_gates command's reading in the integration
// tree: its gate id g<n>, exit code, duration and captured output.
type GroupGate struct {
	Gate       string
	Command    string
	RC         int
	DurationMS int64
	Output     string
}

// runGroupGates runs each group gate in wt as a gate runs (runGate: bash -c,
// combined output), in order; a gate that cannot start reads rc -1 with the
// error as its output.
func runGroupGates(wt string, gates []string) []GroupGate {
	var out []GroupGate
	for i, g := range gates {
		rc, dur, text, err := runGate(wt, g)
		if err != nil {
			rc, text = -1, []byte(err.Error())
		}
		out = append(out, GroupGate{Gate: fmt.Sprintf("g%d", i+1), Command: g, RC: rc, DurationMS: dur, Output: string(text)})
	}
	return out
}

// routeGroupFinding is the task an integration finding on file is recorded
// on: the first member, in member order, whose effective owns contain file
// (ownsContains); else the group task itself. flywheel's own bookkeeping is
// no member's.
func routeGroupFinding(file string, members []string, owns map[string][]string, gtask string) string {
	if !isFlywheelOwnPath(file) {
		for _, m := range members {
			if ownsContains(owns[m], file) {
				return m
			}
		}
	}
	return gtask
}

// groupFindingEvent is one integration finding recorded on task: persona
// reviewer:integration, category integration, id <gtask>-r<round>-<n>, and
// Reason the group task that raised it.
func groupFindingEvent(task, gtask string, round, n int, session, model, tree string, f ReviewFinding) Event {
	return Event{
		Task: task, Kind: "review_finding", Session: session, Model: model, Tree: tree,
		Persona: "reviewer:" + IntegrationPersona, Category: IntegrationPersona, Reason: gtask,
		Severity: f.Severity, Title: f.Claim, Observed: f.Scenario, Ask: f.Fix, Path: f.File, LineNo: f.Line,
		Finding: fmt.Sprintf("%s-r%d-%d", gtask, round, n),
	}
}

// groupRound is one more than the group reviews already recorded for gtask.
func groupRound(events []Event, gtask string) int {
	n := 0
	for _, e := range events {
		if e.Task == gtask && e.Kind == "group_reviewed" {
			n++
		}
	}
	return n + 1
}

// taskGoal is the goal task was planned under (its latest planned or amended
// event naming one), or "".
func taskGoal(events []Event, task string) string {
	goal := ""
	for _, e := range events {
		if e.Task == task && (e.Kind == "planned" || e.Kind == "amended") && e.GoalID != "" {
			goal = e.GoalID
		}
	}
	return goal
}

// openIntegrationFindings is task's open blocking integration findings (rule
// group, issue #420), in ledger order. Only the group reviewer or the lead
// closes one — a unit's own review never saw the combination: a finding is
// closed by a lead's dismissal (as OpenFindings: by id, or the same file and
// claim), or by a later review of the group that raised it (Reason) — its
// own round's group_reviewed follows it in the same append, so a second one
// after it means a later round, which re-reports the defect under a new id
// when it is still there.
func openIntegrationFindings(events []Event, task string) []Event {
	workers := workerSessions(events, task)
	dismissedID, dismissedKey := map[string]bool{}, map[string]bool{}
	for _, e := range events {
		if e.Task == task && leadDismissal(e, workers) {
			dismissedID[e.Finding] = true
		}
	}
	for _, e := range events {
		if e.Task == task && e.Kind == "review_finding" && dismissedID[e.Finding] {
			dismissedKey[findingKey(e)] = true
		}
	}
	var out []Event
	for i, e := range events {
		if e.Task != task || e.Kind != "review_finding" || e.Category != IntegrationPersona || !blockingFinding(e) ||
			dismissedID[e.Finding] || dismissedKey[findingKey(e)] {
			continue
		}
		later := 0
		for _, g := range events[i+1:] {
			if e.Reason != "" && g.Task == e.Reason && g.Kind == "group_reviewed" {
				later++
			}
		}
		if later < 2 {
			out = append(out, e)
		}
	}
	return out
}

// ReviewGroupOptions configures one group review (issue #420).
type ReviewGroupOptions struct {
	Session  string // the reviewer session; required, never a worker session of a member
	Base     string // the ref the integration tree starts from; default main
	Worker   string // who reviews, as ReviewAgent resolves it
	Adapter  string // or this adapter, with Model
	Model    string
	Progress io.Writer
	Stdout   io.Writer
	Stderr   io.Writer
}

// GroupResult is what one group review recorded.
type GroupResult struct {
	Group      string
	Task       string // the group task, group:<id>
	Round      int
	Members    []string
	Owns       map[string][]string // each member's effective owns
	Missing    []string            // members with no branch and no finished commit: not merged
	Conflicts  map[string][]string // member -> conflicting paths; its merge was aborted
	Gates      []GroupGate
	Findings   []Event // the review_finding events recorded, each on its routed task
	Verdict    string  // correct when any blocking finding or failed group gate, else pass
	Note       string
	Tree       string // the integration tree
	Prompt     string
	Transcript string
}

// ReviewGroup validates a group of units together (issue #420): its members
// (GroupMembers), their integration tree (IntegrationTree on Base), the
// review.group_gates run there, then the integration persona over the
// combined diff base..tree. Each merge conflict becomes a blocker on the
// member whose merge conflicted, and each reviewer finding is routed
// (routeGroupFinding) to the member owning its file, else to the group task.
// The gate readings, the findings and a closing group_reviewed event (verdict
// pass or correct, the integration tree, a note of members, conflicts and
// gates) are recorded in one append. A session that is a worker session of a
// member is refused (T4); a failed reviewer records nothing.
func ReviewGroup(dir, group string, o ReviewGroupOptions) (GroupResult, error) {
	if o.Session == "" {
		return GroupResult{}, &RuleRefusal{Rule: "T4", Fix: "a reviewer --session is required"}
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return GroupResult{}, err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return GroupResult{}, err
	}
	r := GroupResult{Group: group, Task: GroupTask(group), Owns: map[string][]string{}}
	if r.Members, err = GroupMembers(events, group); err != nil {
		return GroupResult{}, err
	}
	// The integration reviewer may run the members' gate commands and the
	// group gates (issue #469).
	var gates []string
	for _, m := range r.Members {
		if c := sessionClash(m, events, o.Session); c != "" {
			return GroupResult{}, &RuleRefusal{Rule: "T4", Fix: c}
		}
		if h, _, err := AttemptBrief(dir, events, m); err == nil {
			r.Owns[m] = h.Owns
			gates = append(gates, h.Gates...)
		}
		if memberRef(dir, events, m) == "" {
			r.Missing = append(r.Missing, m)
		}
	}
	base := o.Base
	if base == "" {
		base = "main"
	}
	baseSHA, err := gitRead(dir, []string{"rev-parse", "--verify", base + "^{commit}"})
	if err != nil {
		return GroupResult{}, fmt.Errorf("base %s: %w", base, err)
	}
	baseSHA = strings.TrimSpace(baseSHA)
	wt, cleanup, conflicts, err := IntegrationTree(dir, events, r.Members, baseSHA)
	defer cleanup()
	if err != nil {
		return GroupResult{}, err
	}
	r.Conflicts = conflicts
	if r.Tree, err = treeHash(wt); err != nil {
		return GroupResult{}, err
	}
	commit := headCommit(wt)
	r.Gates = runGroupGates(wt, cfg.ReviewGroupGates())
	diff, err := gitRead(wt, []string{"diff", "--no-color", baseSHA, "HEAD"})
	if err != nil {
		return GroupResult{}, err
	}
	if len(diff) > maxReviewDiff {
		diff = strings.ToValidUTF8(diff[:maxReviewDiff], "") + fmt.Sprintf("\n[diff truncated at %d KB of %d KB; read the changed files for the rest]\n", maxReviewDiff/1024, len(diff)/1024)
	}
	names, err := gitRead(wt, []string{"diff", "--name-only", "-z", baseSHA, "HEAD"})
	if err != nil {
		return GroupResult{}, err
	}
	changed := strings.FieldsFunc(names, func(c rune) bool { return c == 0 })
	r.Round = groupRound(events, r.Task)

	var findings []ReviewFinding
	worker := Worker{}
	if len(changed) > 0 {
		var adap Adapter
		if worker, adap, err = resolveReviewer(cfg, o.Worker, o.Adapter, o.Model); err != nil {
			return GroupResult{}, err
		}
		persona, err := personaPrompt(IntegrationPersona)
		if err != nil {
			return GroupResult{}, err
		}
		reviews, err := filepath.Abs(filepath.Join(dir, ".flywheel", "reviews"))
		if err == nil {
			err = os.MkdirAll(reviews, 0o755)
		}
		if err != nil {
			return GroupResult{}, err
		}
		stem := filepath.Join(reviews, fmt.Sprintf("group-%s.%d", GroupID(group), r.Round))
		r.Prompt, r.Transcript = stem+".prompt.md", stem+".jsonl"
		extra := reviewerExtraTools(cfg, append(gates, cfg.ReviewGroupGates()...))
		if findings, err = runGroupReviewer(dir, wt, stem, worker, adap, r, buildGroupPrompt(persona, r, diff), changed, extra, o); err != nil {
			return GroupResult{}, err
		}
	}
	return recordGroupReview(dir, r, commit, worker, o.Session, findings, o.Progress)
}

// recordGroupReview records a group review in one append: the group gate
// readings, a blocker per conflicting path on the member whose merge
// conflicted, the reviewer's findings each on its routed task, and the
// closing group_reviewed event; then it refreshes the state and the threads.
func recordGroupReview(dir string, r GroupResult, commit string, worker Worker, session string, findings []ReviewFinding, w io.Writer) (GroupResult, error) {
	evs := groupGateEvents(r.Task, r.Tree, commit, session, r.Gates)
	n := 0
	add := func(task string, f ReviewFinding) {
		n++
		e := groupFindingEvent(task, r.Task, r.Round, n, session, worker.Model, r.Tree, f)
		r.Findings = append(r.Findings, e)
		evs = append(evs, e)
	}
	var conflictNotes []string
	for _, m := range r.Members {
		for _, p := range r.Conflicts[m] {
			add(m, ReviewFinding{
				Severity: "blocker", Category: IntegrationPersona, File: p,
				Claim:    fmt.Sprintf("%s conflicts with the members merged before it in %s", m, p),
				Scenario: fmt.Sprintf("merging %s onto the integration tree of group %s stops with a conflict in %s; the combined tree lacks %s's change", m, r.Group, p, m),
				Fix:      fmt.Sprintf("rebase %s onto the members merged before it (or the landed base) and resolve %s", m, p),
			})
		}
		if len(r.Conflicts[m]) > 0 {
			conflictNotes = append(conflictNotes, m+": "+strings.Join(r.Conflicts[m], ","))
		}
	}
	for _, f := range findings {
		f.Category = IntegrationPersona
		add(routeGroupFinding(f.File, r.Members, r.Owns, r.Task), f)
	}
	r.Verdict = "pass"
	for _, e := range r.Findings {
		if blockingFinding(e) {
			r.Verdict = "correct"
		}
	}
	var gateNotes []string
	for _, g := range r.Gates {
		gateNotes = append(gateNotes, fmt.Sprintf("%s=%d", g.Gate, g.RC))
		if g.RC != 0 {
			r.Verdict = "correct"
		}
	}
	r.Note = fmt.Sprintf("members %s; missing %s; conflicts %s; gates %s; %d finding(s)", strings.Join(r.Members, ","),
		orDash(strings.Join(r.Missing, ",")), orDash(strings.Join(conflictNotes, "; ")), orDash(strings.Join(gateNotes, " ")), len(r.Findings))
	evs = append(evs, Event{
		Task: r.Task, Kind: "group_reviewed", Verdict: r.Verdict, Persona: "reviewer:" + IntegrationPersona,
		Session: session, Model: worker.Model, Adapter: worker.Adapter, Tree: r.Tree, Commit: commit, Note: r.Note,
	})
	if err := AppendEvents(dir, evs); err != nil {
		return GroupResult{}, err
	}
	_, _ = WriteState(dir)
	routed := map[string]bool{}
	for _, e := range r.Findings {
		if e.Task != r.Task && !routed[e.Task] {
			routed[e.Task] = true
			refreshReviewThread(dir, e.Task, w)
		}
	}
	if err := WriteGroupThread(dir, r.Group); err != nil {
		progress(w, fmt.Sprintf("warning: group thread of %s not written: %v", r.Group, err))
	}
	return r, nil
}

// buildGroupPrompt is the integration reviewer's prompt: its persona, the
// group (each member and its owns, missing members and conflicts), the group
// gate readings and the combined diff.
func buildGroupPrompt(persona string, r GroupResult, diff string) string {
	var b strings.Builder
	b.WriteString(persona)
	fmt.Fprintf(&b, "\n# The group %s\n\nMembers, in merge order, and the files each owns:\n\n", r.Group)
	for _, m := range r.Members {
		fmt.Fprintf(&b, "- %s: %s\n", m, orDash(strings.Join(r.Owns[m], ", ")))
	}
	for _, m := range r.Missing {
		fmt.Fprintf(&b, "- %s has no branch and no finished commit: NOT merged\n", m)
	}
	for _, m := range r.Members {
		if c := r.Conflicts[m]; len(c) > 0 {
			fmt.Fprintf(&b, "- merging %s conflicted in %s: its merge was aborted and it is NOT in the tree\n", m, strings.Join(c, ", "))
		}
	}
	b.WriteString("\n# Group gate readings in the integration tree\n\n")
	if len(r.Gates) == 0 {
		b.WriteString("(no review.group_gates configured)\n")
	}
	for _, g := range r.Gates {
		fmt.Fprintf(&b, "- gate %s rc=%d: %s\n", g.Gate, g.RC, g.Command)
	}
	b.WriteString("\n# The combined diff (base..integration tree)\n\n")
	b.WriteString(diff)
	return b.String()
}

// runGroupReviewer runs the integration reviewer in the integration tree wt
// under the git guard, checks its findings (validateFindings, category
// integration) and, on a refused answer, runs it once more with the
// violations appended, as ReviewAgent does. extra is the reviewer's tools
// beyond the read-only base (reviewRunRequest).
func runGroupReviewer(dir, wt, stem string, worker Worker, adap Adapter, r GroupResult, prompt string, changed, extra []string, o ReviewGroupOptions) ([]ReviewFinding, error) {
	guardTask := "group-" + GroupID(r.Group)
	ro := ReviewAgentOptions{Stdout: o.Stdout, Stderr: o.Stderr}
	if err := os.WriteFile(r.Prompt, []byte(prompt), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", r.Prompt, err)
	}
	progress(o.Progress, fmt.Sprintf("%s review round %d: %s %s", r.Task, r.Round, worker.Adapter, worker.Model))
	answer, err := runReviewer(dir, wt, guardTask, adap, reviewRunRequest(guardTask, r.Round, r.Prompt, worker.Model, extra), stem, r.Transcript, ro)
	if err != nil {
		return nil, err
	}
	findings, problems := checkReviewAnswer(wt, changed, answer, IntegrationPersona)
	if len(problems) == 0 {
		return findings, nil
	}
	retry := prompt + "\n# Your previous answer was refused\n\n" + strings.Join(problems, "\n") + "\n\nAnswer again with ONE fenced json block.\n"
	if err := os.WriteFile(r.Prompt, []byte(retry), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", r.Prompt, err)
	}
	progress(o.Progress, fmt.Sprintf("%s review round %d: answer refused (%d violation(s)); asking once more", r.Task, r.Round, len(problems)))
	second := stem + "b.jsonl"
	if answer, err = runReviewer(dir, wt, guardTask, adap, reviewRunRequest(guardTask, r.Round, r.Prompt, worker.Model, extra), stem+"b", second, ro); err != nil {
		return nil, err
	}
	if findings, problems = checkReviewAnswer(wt, changed, answer, IntegrationPersona); len(problems) > 0 {
		return nil, fmt.Errorf("group review answer refused twice; nothing recorded, transcripts %s and %s:\n%s", r.Transcript, second, strings.Join(problems, "\n"))
	}
	return findings, nil
}

// groupGateEvents are the group gates' validated events: Task the group task,
// Gate g<n>, the integration tree and its HEAD commit.
func groupGateEvents(gtask, tree, commit, session string, gates []GroupGate) []Event {
	var evs []Event
	for _, g := range gates {
		rc := g.RC
		evs = append(evs, Event{Task: gtask, Kind: "validated", Gate: g.Gate, Command: g.Command, RC: &rc,
			DurationMS: g.DurationMS, Tree: tree, Commit: commit, Session: session})
	}
	return evs
}
