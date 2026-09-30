package flywheel

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// The factory view's "why" (issue #583 k3): pure functions over the ledger
// that say, in plain words, where a unit stands and why, and what happened
// to it over time.

// WhyContext is what the floor already measured about a unit that the ledger
// alone does not say; UnitWhy uses what is set.
type WhyContext struct {
	RunState     string        // the floor's run state (Unit.RunState); "" when unknown
	Steps        int           // the current attempt's steps so far
	StallTimeout time.Duration // the worker's stall_timeout; 0 when unknown
	PauseAt      float64       // limits.rate_limit_pause_at
	NeedsOwner   []string      // open blocking findings outside owns (needsOwnerFindings)
}

// UnitWhy is one or two plain sentences stating task's state and the reason,
// from the ledger's facts only, naming the next step the way recover does
// (nextAction).
func UnitWhy(events []Event, task string, now time.Time, extra WhyContext) string {
	return NewWhyIndex(events).UnitWhy(task, now, extra)
}

// WhyIndex is a ledger grouped once for many UnitWhy calls (issue #630): each
// unit's needs and the indexes of each task's events, in ledger order, so a
// unit's why reads its own events instead of scanning the ledger again.
type WhyIndex struct {
	events []Event
	needs  map[string][]string
	byTask map[string][]int
}

// NewWhyIndex groups events, in ledger order, by task.
func NewWhyIndex(events []Event) *WhyIndex {
	x := &WhyIndex{events: events, needs: unitNeeds(events), byTask: map[string][]int{}}
	for i, e := range events {
		x.byTask[e.Task] = append(x.byTask[e.Task], i)
	}
	return x
}

// UnitWhy is UnitWhy over the indexed ledger.
func (x *WhyIndex) UnitWhy(task string, now time.Time, extra WhyContext) string {
	return unitWhy(x.whyEvents(task), task, now, extra)
}

// whyEvents are the events UnitWhy reads, in ledger order: task's, its
// needs' and the floor's own (a suspension), so Derive sorts a few events,
// not the ledger.
func (x *WhyIndex) whyEvents(task string) []Event {
	keep := map[string]bool{task: true, "": true}
	for _, n := range NeedTargets(x.needs[task]...) {
		keep[n] = true
	}
	var idx []int
	for t := range keep {
		idx = append(idx, x.byTask[t]...)
	}
	slices.Sort(idx)
	out := make([]Event, len(idx))
	for i, j := range idx {
		out[i] = x.events[j]
	}
	return out
}

// unitWhy is UnitWhy over events already cut to whyEvents.
func unitWhy(events []Event, task string, now time.Time, extra WhyContext) string {
	st := Derive(events)
	status := map[string]string{}
	var ts TaskState
	for _, t := range st.Tasks {
		status[t.ID] = t.Status
		if t.ID == task {
			ts = t
		}
	}
	if ts.ID == "" {
		return "unknown: no event names " + task + "."
	}
	att := "attempt " + ts.Attempt
	switch ts.Status {
	case "planned":
		var unmet []string
		for _, n := range ts.Needs {
			if status[n] != "landed" {
				unmet = append(unmet, n)
			}
		}
		if len(unmet) == 1 {
			return "blocked: needs " + unmet[0] + ", which has not landed."
		} else if len(unmet) > 1 {
			return "blocked: needs " + strings.Join(unmet, ", ") + ", which have not landed."
		}
		if f := FactorySuspended(events, now); f.Suspended {
			return "queued: its needs are met, but the factory is frozen by suspend at " + whyClock(f.Since) + "; " + thaws(f) + "."
		}
		return "queued: planned and its needs are met; next: flywheel run " + task + "."
	case "dispatched":
		return fmt.Sprintf("queued: %s dispatched %s ago, waiting for the worker to start.", att, whyDur(now.Sub(lastOf(events, task, "dispatched", ts.Attempt))))
	case "running":
		idle := now.Sub(lastOf(events, task, "", ""))
		state := extra.RunState
		if state == "" && extra.StallTimeout > 0 && idle > extra.StallTimeout {
			state = "stalled"
		}
		timeout := ""
		if extra.StallTimeout > 0 {
			timeout = " (stall timeout " + whyDur(extra.StallTimeout) + ")"
		}
		switch state {
		case "stalled", "silent":
			return fmt.Sprintf("%s: %s has shown no progress for %s%s; the run stops at the timeout and flywheel recover --apply marks it lost.", state, att, whyDur(idle), timeout)
		}
		return fmt.Sprintf("building: %s, %d steps, running for %s.", att, extra.Steps, whyDur(now.Sub(lastOf(events, task, "started", ts.Attempt))))
	case "finished":
		return whyFinished(events, ts, now, extra)
	case "needs-correction":
		if open := openBlockingIDs(events, task); len(open) > 0 {
			return fmt.Sprintf("needs correction: %d open blocking finding(s) (%s); next: flywheel review %s --agent --fix.", len(open), clipNote(strings.Join(open, ", ")), task)
		}
		return "needs correction: the inspection asked for rework; next: flywheel run " + task + "."
	case "passed":
		return withNext("passed, awaiting landing", whyNext(events, ts, now, extra))
	case "landed":
		return whyLanded(events, task)
	case "withdrawn":
		return "withdrawn: " + noteOf(events, task, "withdrawn") + "; a new flywheel plan revives it."
	case "lost":
		return "lost: " + whyNext(events, ts, now, extra).Reason + "."
	}
	return ts.Status + ": " + noteOf(events, task, ts.Status) + "."
}

// whyFinished is UnitWhy for a finished attempt, by its finish reason.
func whyFinished(events []Event, ts TaskState, now time.Time, extra WhyContext) string {
	task, att := ts.ID, "attempt "+ts.Attempt
	fin := finishOf(events, task, ts.Attempt)
	n := whyNext(events, ts, now, extra)
	switch fin.Reason {
	case "rate-limited":
		return withNext(fmt.Sprintf("rate-limited: %s hit %s's limit, which resets at %s", att, fin.Model, whyClock(fin.ResetAt)), n)
	case "length":
		return fmt.Sprintf("capped: %s hit the output cap (peak reasoning %s); correct the brief or dispatch again.", att, tokensK(Tokens{Reasoning: peakReasoningFor(events, task, ts.Attempt)}))
	case "suspended":
		if f := FactorySuspended(events, now); f.Suspended {
			return "suspended: frozen by suspend at " + whyClock(f.Since) + "; " + thaws(f) + "."
		}
		return withNext("suspended: stopped by a suspend that has since thawed", n)
	case "", "stop":
	default:
		return withNext(fmt.Sprintf("failed: %s ended %s", att, fin.Reason), n)
	}
	if gates, cmd := failingGates(events, task, ts.Attempt, fin.TS); len(gates) > 0 {
		return fmt.Sprintf("validation failed: gates %s; first failing `%s`; correct the unit.", strings.Join(gates, ", "), clipNote(cmd))
	}
	if open := openBlockingIDs(events, task); len(open) > 0 {
		return fmt.Sprintf("needs correction: %d open blocking finding(s) (%s); next: flywheel review %s --agent --fix.", len(open), clipNote(strings.Join(open, ", ")), task)
	}
	switch n.Action {
	case "re-validate":
		return withNext("finished, awaiting validation", n)
	case "inspect":
		return withNext("validated, awaiting inspection", n)
	case "review":
		return withNext("validated, awaiting the review panel", n)
	}
	return withNext("finished: "+n.Reason, n)
}

// lastOf is the time of task's latest event of kind ("" any) and attempt
// ("" any); a missing started falls back to the dispatch.
func lastOf(events []Event, task, kind, attempt string) time.Time {
	var at time.Time
	for _, e := range events {
		if e.Task == task && (kind == "" || e.Kind == kind) && (attempt == "" || e.Attempt == attempt) {
			if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(at) {
				at = t
			}
		}
	}
	if at.IsZero() && kind == "started" {
		return lastOf(events, task, "dispatched", attempt)
	}
	return at
}

// finishOf is the attempt's latest finished event.
func finishOf(events []Event, task, attempt string) Event {
	var fin Event
	for _, e := range events {
		if e.Task == task && e.Kind == "finished" && e.Attempt == attempt {
			fin = e
		}
	}
	return fin
}

// noteOf is the note (else reason) of task's latest event of kind.
func noteOf(events []Event, task, kind string) string {
	note := "no note recorded"
	for _, e := range events {
		if e.Task == task && e.Kind == kind && e.Note+e.Reason != "" {
			note = clipNote(strings.TrimSpace(e.Note + " " + e.Reason))
		}
	}
	return note
}

// whyLanded names the effective landed commit (a land_corrected event
// supersedes the landed one, issue #673), and the PR a ship opened or reused.
func whyLanded(events []Event, task string) string {
	commit, pr := "", ""
	if c, _, _ := landedCommit(events, task); c != "" {
		commit = short7(c)
	}
	for _, e := range events {
		switch {
		case e.Task != task:
		case e.Kind == "shipped" && e.Step == "pr" && e.Result != "fail":
			if i := strings.Index(e.Note, "#"); i >= 0 {
				pr, _, _ = strings.Cut(e.Note[i:], " ")
			}
		}
	}
	s := "landed"
	if commit != "" {
		s += " as " + commit
	}
	if pr != "" {
		s += " (PR " + pr + ")"
	}
	return s + "; nothing left to do."
}

// whyNext is recover's next action (nextAction) from the facts the ledger
// holds; the world checks (worktree, lease, tree) are recover's alone.
func whyNext(events []Event, ts TaskState, now time.Time, extra WhyContext) Next {
	fin := finishOf(events, ts.ID, ts.Attempt)
	f := recoverFacts{Task: ts.ID, Status: ts.Status, Attempt: ts.Attempt, FinishReason: fin.Reason,
		NeedsOwner: extra.NeedsOwner, Suspended: FactorySuspended(events, now).Suspended}
	if p, ok := rateLimitPausedAt(events, ts.Model, now, extra.PauseAt); ok && ts.Model != "" {
		f.PausedUntil = p.Until.UTC().Format(time.RFC3339)
	}
	if fin.Reason == "stop" {
		oh, ot, _, _ := latestReading(events, ts.ID, "owns_checked", ts.Attempt)
		ft, _ := time.Parse(time.RFC3339Nano, fin.TS)
		f.HaveReading = oh && ot.After(ft)
		f.InspectReady = inspectionReady(events, ts.ID, ts.Attempt)
	}
	return nextAction(f)
}

// withNext ends s with the next step's command, when there is one.
func withNext(s string, n Next) string {
	if n.Command != "" {
		return s + "; next: " + n.Command + "."
	}
	return s + "."
}

// failingGates are the attempt's gates whose latest reading after the finish
// failed, in the order first read, and the first one's command.
func failingGates(events []Event, task, attempt, finTS string) (gates []string, first string) {
	ft, _ := time.Parse(time.RFC3339Nano, finTS)
	var order []string
	latest := map[string]Event{}
	for _, e := range events {
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if e.Task != task || e.Kind != "validated" || e.Attempt != "" && e.Attempt != attempt || err != nil || !t.After(ft) {
			continue
		}
		if _, seen := latest[e.Gate]; !seen {
			order = append(order, e.Gate)
		}
		latest[e.Gate] = e
	}
	for _, g := range order {
		if e := latest[g]; e.RC == nil || *e.RC != 0 || e.Reason == "host-blocked" {
			if first == "" {
				first = e.Command
			}
			gates = append(gates, g)
		}
	}
	return gates, first
}

// whyClock is an RFC 3339 time as HH:MM UTC, so the sentence is the same on
// every machine.
func whyClock(ts string) string {
	if ts == "" {
		return "an unrecorded time"
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return t.UTC().Format("15:04") + " UTC"
}

// whyDur is a duration as 45s, 12m, 1h20m or 2d3h.
func whyDur(d time.Duration) string {
	d = max(0, d)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// thaws says when a suspension ends.
func thaws(f SuspendState) string {
	if f.Until != "" {
		return "it thaws at " + whyClock(f.Until)
	}
	return "it resumes on flywheel resume"
}

// TimelineRow is one row of a unit's timeline: an event (Kind its kind), a
// gap between two events longer than timelineGap (Kind "gap", Dur its length,
// Detail what it was spent on), or the closing summary (Kind "summary", Dur
// the total, Touch the attempts' run time, Flow their ratio).
type TimelineRow struct {
	TS     time.Time
	Kind   string
	Detail string
	Dur    time.Duration
	Touch  time.Duration
	Flow   float64
}

// timelineGap is the shortest pause between two events the timeline names.
const timelineGap = 5 * time.Minute

// UnitTimeline is every event of task in ledger order, a gap row between two
// events more than timelineGap apart, and a summary row: the total from the
// first event to the last, the touch time (each attempt's start to its
// finish or loss) and the flow efficiency, touch over total.
func UnitTimeline(events []Event, task string) []TimelineRow {
	return UnitTimelineOrdered(derivationOrder(events), task)
}

// UnitTimelineOrdered is UnitTimeline over events already in derivation
// order (derivationOrder), so a caller that orders the ledger once passes it
// to every unit (issue #630).
func UnitTimelineOrdered(ordered []Event, task string) []TimelineRow {
	var rows []TimelineRow
	var frozen [][2]time.Time // suspensions: from, to (zero while open)
	starts := map[string]time.Time{}
	var prev Event
	var first, last time.Time
	var touch time.Duration
	for _, e := range ordered {
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			continue
		}
		switch {
		case e.Task == "" && e.Kind == "suspended":
			end, _ := time.Parse(time.RFC3339, e.Until)
			frozen = append(frozen, [2]time.Time{t, end})
		case e.Task == "" && e.Kind == "unsuspended" && len(frozen) > 0 && frozen[len(frozen)-1][1].IsZero():
			frozen[len(frozen)-1][1] = t
		}
		if e.Task != task {
			continue
		}
		if first.IsZero() {
			first = t
		} else if gap := t.Sub(last); gap > timelineGap {
			label := gapLabel(prev, last, t, frozen)
			if prev.Kind == "dispatched" || prev.Kind == "started" {
				label = "attempt " + prev.Attempt + " running"
			}
			rows = append(rows, TimelineRow{TS: last, Kind: "gap", Dur: gap, Detail: label})
		}
		switch e.Kind {
		case "dispatched", "started":
			starts[e.Attempt] = t
		case "finished", "lost":
			if s, ok := starts[e.Attempt]; ok {
				touch += t.Sub(s)
				delete(starts, e.Attempt)
			}
		}
		rows = append(rows, TimelineRow{TS: t, Kind: e.Kind, Detail: explainLine(e)})
		prev, last = e, t
	}
	total := last.Sub(first)
	sum := TimelineRow{TS: last, Kind: "summary", Dur: total, Touch: touch}
	if total > 0 {
		sum.Flow = float64(touch) / float64(total)
	}
	sum.Detail = fmt.Sprintf("total %s · touch %s · flow efficiency %.0f%%", whyDur(total), whyDur(touch), sum.Flow*100)
	return append(rows, sum)
}

// gapLabel says what a gap after prev, from from to to, was spent on when
// no attempt was running.
func gapLabel(prev Event, from, to time.Time, frozen [][2]time.Time) string {
	if prev.Kind == "finished" && prev.Reason == "rate-limited" {
		return "waiting for the rate-limit reset at " + whyClock(prev.ResetAt)
	}
	for _, f := range frozen {
		if end := f[1]; f[0].Before(to) && (end.IsZero() || end.After(from)) {
			return "frozen by suspend"
		}
	}
	switch {
	case prev.Kind == "finished":
		return "waiting for validation"
	case prev.Kind == "owns_checked", prev.Kind == "validated" && prev.RC != nil && *prev.RC == 0:
		return "waiting for inspection"
	}
	return "idle"
}
