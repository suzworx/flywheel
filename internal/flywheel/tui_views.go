package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The factory view's filters, sorts and the rows of the views added by
// issue #583 (k2): every function here is pure, a function of its inputs,
// so the key handling in tui.go stays the only state.

// filterMatcher builds the `/` filter's matcher for text: `-f text` is a
// fuzzy match (every character, in order), `!re` the inverse of re, and
// anything else a case-insensitive regex. An invalid regex matches
// literally, and literal reports it. An empty pattern matches every row.
func filterMatcher(text string) (match func(string) bool, literal bool) {
	if rest, ok := strings.CutPrefix(text, "-f"); ok {
		needle := []rune(strings.ToLower(strings.TrimSpace(rest)))
		return func(s string) bool { return fuzzyMatch(needle, strings.ToLower(s)) }, false
	}
	if rest, ok := strings.CutPrefix(text, "!"); ok {
		if rest == "" {
			return func(string) bool { return true }, false
		}
		inner, lit := filterMatcher(rest)
		return func(s string) bool { return !inner(s) }, lit
	}
	re, err := regexp.Compile("(?i)" + text)
	if err != nil {
		lower := strings.ToLower(text)
		return func(s string) bool { return strings.Contains(strings.ToLower(s), lower) }, true
	}
	return re.MatchString, false
}

// fuzzyMatch reports whether every rune of needle appears in s, in order.
func fuzzyMatch(needle []rune, s string) bool {
	i := 0
	for _, r := range s {
		if i < len(needle) && r == needle[i] {
			i++
		}
	}
	return i == len(needle)
}

// sortColumn is the index of the column key sorts by in header, -1 when
// the view has none: name is METRIC (the metrics table) else the first
// column, age AGE, stage STAGE (else STATE), cost COST.
func sortColumn(header []string, key string) int {
	switch key {
	case "name":
		if i := slices.Index(header, "METRIC"); i >= 0 {
			return i
		}
		if len(header) > 0 {
			return 0
		}
	case "age":
		return slices.Index(header, "AGE")
	case "stage":
		if i := slices.Index(header, "STAGE"); i >= 0 {
			return i
		}
		return slices.Index(header, "STATE")
	case "cost":
		return slices.Index(header, "COST")
	}
	return -1
}

// sortRows sorts rows by s, stably: rows with equal keys keep their order,
// in either direction. Ages compare as durations, costs as amounts, other
// cells as text (numbers as numbers).
func sortRows(header []string, rows [][]string, s tuiSort) [][]string {
	col := sortColumn(header, s.key)
	if col < 0 {
		return rows
	}
	slices.SortStableFunc(rows, func(a, b []string) int {
		c := compareCells(cell(a, col), cell(b, col))
		if s.desc {
			return -c
		}
		return c
	})
	return rows
}

func cell(row []string, i int) string {
	if i < len(row) {
		return row[i]
	}
	return ""
}

// compareCells compares two cells as numbers when both are ("$1.20", "30s",
// "2h" included), else as text.
func compareCells(a, b string) int {
	x, okA := cellNumber(a)
	y, okB := cellNumber(b)
	switch {
	case okA && okB && x < y:
		return -1
	case okA && okB && x > y:
		return 1
	case okA && okB:
		return 0
	}
	return strings.Compare(a, b)
}

// cellNumber reads a cell as a number: a plain number, a cost ($1.20) or
// an age as HumanAge writes it (30s, 2m, 1h, 3d) in seconds.
func cellNumber(s string) (float64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	if s == "" {
		return 0, false
	}
	unit := map[rune]float64{'s': 1, 'm': 60, 'h': 3600, 'd': 86400}
	mult := 1.0
	if r := rune(s[len(s)-1]); unit[r] > 0 && len(s) > 1 && unicode.IsDigit(rune(s[len(s)-2])) {
		mult, s = unit[r], s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	return f * mult, err == nil
}

// treeChars are the characters treeRows draws a node's branch with.
const treeChars = "│├└─ "

// treeRows is the needs tree (k9s xray): each unit not landed that no other
// such unit needs is a root, and a node's children are its needs, drawn
// with ├─ and └─. A landed or unknown need is a leaf; a cycle is cut.
func treeRows(d TUIData) (header []string, rows [][]string) {
	header = []string{"UNIT", "STAGE"}
	stage := map[string]string{}
	var open []string
	for _, u := range d.Floor.Units {
		stage[u.Task] = u.Stage
		if u.Stage != "landed" {
			open = append(open, u.Task)
		}
	}
	needed := map[string]bool{}
	for _, t := range open {
		for _, n := range d.Needs[t] {
			needed[n] = true
		}
	}
	shown := map[string]bool{}
	var walk func(task, prefix string, path map[string]bool)
	walk = func(task, prefix string, path map[string]bool) {
		shown[task] = true
		if stage[task] == "landed" {
			return
		}
		path[task] = true
		defer delete(path, task)
		needs := d.Needs[task]
		for i, n := range needs {
			branch, below := "├─ ", "│  "
			if i == len(needs)-1 {
				branch, below = "└─ ", "   "
			}
			st, ok := stage[n]
			switch {
			case path[n]:
				st = "cycle"
			case !ok:
				st = "?"
			}
			rows = append(rows, []string{prefix + branch + n, st})
			if !path[n] {
				walk(n, prefix+below, path)
			}
		}
	}
	for pass := 0; pass < 2; pass++ {
		for _, t := range open {
			// The second pass roots what only a cycle reaches.
			if shown[t] || (pass == 0 && needed[t]) {
				continue
			}
			rows = append(rows, []string{t, stage[t]})
			walk(t, "", map[string]bool{})
		}
	}
	return header, rows
}

// healthRows are the health events, newest first.
func healthRows(d TUIData) (header []string, rows [][]string) {
	header = []string{"TS", "AGE", "RUNNING", "STALLED", "RATE-LIMITED", "ANDON", "PAUSED"}
	for i := len(d.Health) - 1; i >= 0; i-- {
		h := d.Health[i]
		ts, age := h.TS, "-"
		if t, err := time.Parse(time.RFC3339Nano, h.TS); err == nil {
			ts = t.Local().Format("01-02 15:04:05")
			if !d.Floor.Refreshed.IsZero() {
				age = HumanAge(max(0, int(d.Floor.Refreshed.Sub(t).Seconds())))
			}
		}
		var paused []string
		for _, p := range h.Snap.PausedModels {
			paused = append(paused, p.Model)
		}
		rows = append(rows, []string{ts, age, strconv.Itoa(h.Snap.Running), strconv.Itoa(h.Snap.Stalled),
			strconv.Itoa(h.Snap.RateLimited), strconv.Itoa(h.Snap.Andon), strings.Join(paused, ",")})
	}
	return header, rows
}

// ctxHeader is the ctx view's header; PATH stays last, where Enter reads it.
var ctxHeader = []string{"NAME", "KIND", "STATE", "RUNNING", "ANDON", "PAUSED", "HEALTH", "LAST", "PATH"}

// ctxRows are the fleet's ledgers (issue #585 f4), as fleet status folds
// them: the one the view shows marked (*) in NAME, a ledger that failed to
// read with its error as STATE. No registry, or an empty one, is one line
// naming how to register a root; a registry that did not read, its error.
func ctxRows(d TUIData) (header []string, rows [][]string) {
	if d.FleetErr != "" {
		return []string{"FLEET"}, [][]string{{"fleet: " + d.FleetErr}}
	}
	if len(d.Fleet) == 0 {
		return []string{"FLEET"}, [][]string{{"no factory registered: flywheel fleet add <path> registers a root"}}
	}
	age := func(sec *int) string {
		if sec == nil {
			return "-"
		}
		return HumanAge(*sec)
	}
	for _, r := range d.Fleet {
		if r.Kind == FleetKindIdle {
			rows = append(rows, []string{r.Name, r.Kind, "", "", "", "", "", age(r.LastAge), ""})
			continue
		}
		name := r.Name
		if r.Path == d.FleetCur {
			name += "(*)"
		}
		state := "running"
		switch {
		case r.Error != "":
			state = "error: " + r.Error
		case r.Suspended:
			state = "SUSPENDED"
		case len(r.Paused) > 0:
			state = "paused"
		}
		rows = append(rows, []string{name, r.Kind, state, strconv.Itoa(r.Tasks.Dispatched + r.Tasks.Running),
			strconv.Itoa(r.Andon), strings.Join(r.Paused, ","), age(r.HealthAge), age(r.LastAge), r.Path})
	}
	return ctxHeader, rows
}

// andonSeverities rank the andon's signals: high stops the line, medium
// holds it, anything else is low.
var andonSeverities = map[string]string{
	"failed": "high", "failed-dirty": "high", "stalled": "high", "silent": "high",
	"git-write": "high", "permission-denied": "high",
	"rate-limited": "medium", "paused": "medium", "stale": "medium", "no-plan": "medium",
}

// severityRank orders the severities worst first.
var severityRank = map[string]int{"high": 0, "medium": 1, "low": 2}

// andonSignal is an andon entry's signal word: its state's first word, lower
// case ("paused until 10:20" is paused, "STALE: …" is stale).
func andonSignal(a Andon) string {
	word, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(a.State)), " ")
	return strings.TrimSuffix(word, ":")
}

// andonSeverity is an andon entry's severity: high, medium or low.
func andonSeverity(a Andon) string {
	if s, ok := andonSeverities[andonSignal(a)]; ok {
		return s
	}
	return "low"
}

// andonWhat is what happened, on one line: a unit's why, else what the
// factory-wide entry means.
func andonWhat(d TUIData, a Andon) string {
	switch {
	case strings.HasPrefix(a.Task, "model/"):
		return "a rate limit paused " + strings.TrimPrefix(a.Task, "model/") + ": " + a.State
	case a.Task == "health":
		return "the latest health event is stale: the controller is not recording"
	case a.Task == "factory":
		return a.State
	case strings.HasPrefix(a.Task, "staffing/"):
		return "role " + strings.TrimPrefix(a.Task, "staffing/") + " is registered with another session than its config names"
	case strings.HasPrefix(a.Task, "group:"):
		return "the group's integration review has open blocking findings"
	}
	if w := d.Why[a.Task]; w != "" {
		return oneLine.Replace(w)
	}
	return a.State
}

// andonNext is the next step: recover's command for a unit (its action and
// reason when it has no command), and a factory-wide entry's own step.
func andonNext(d TUIData, a Andon) string {
	switch {
	case strings.HasPrefix(a.Task, "model/"):
		return "wait for the reset, then flywheel supervise --resume-limited"
	case a.Task == "health":
		return "flywheel controller"
	case a.Task == "factory":
		return "flywheel resume --session <s>"
	case strings.HasPrefix(a.Task, "staffing/"):
		return "register the role's configured session, or fix its config"
	// The floor measured these in the world, which eventNext never reads:
	// uncommitted changes are recover's to explain, a stacked fix rebases.
	case a.State == "failed-dirty":
		return recoverNext.Command
	case a.State == "stacked":
		return "flywheel rebase " + a.Task
	}
	n, ok := d.Next[a.Task]
	switch {
	case !ok:
		return "flywheel recover"
	case n.Command != "":
		return n.Command
	}
	return strings.TrimSpace(n.Action + ": " + n.Reason)
}

// recoverNext is the next step of a unit whose decision needs recover's
// world checks (its lease, its run file, its worktree).
var recoverNext = Next{Action: "recover", Reason: "needs the world checks", Command: "flywheel recover"}

// eventNext is each of tasks' next action as recover decides it (issue #583
// k7 c1), from the ledger alone: recover's facts that the events hold (the
// status, the finish reason, the rate-limit pause, the readings, the panel,
// the suspension; needsOwner the unit's findings outside owns) through
// nextAction. It never reads the world, so it assumes the worktree matches
// the ledger (no head mismatch, no unexplained change, not stacked, the tree
// unchanged since the last reading); an attempt in flight, whose next step is
// its lease and run file's to decide, gets recoverNext.
func eventNext(events []Event, cfg Config, now time.Time, pauseAt float64, tasks []string, needsOwner map[string][]string) map[string]Next {
	want := map[string]bool{}
	for _, t := range tasks {
		want[t] = true
	}
	suspended := FactorySuspended(events, now).Suspended
	byTask, paused := tasksEvents(events), map[string]string{}
	out := map[string]Next{}
	for _, ts := range Derive(events).Tasks {
		if !want[ts.ID] {
			continue
		}
		if ts.Status == "dispatched" || ts.Status == "running" {
			out[ts.ID] = recoverNext
			continue
		}
		f := recoverFacts{Task: ts.ID, Status: ts.Status, Attempt: ts.Attempt, Suspended: suspended, NeedsOwner: needsOwner[ts.ID]}
		// The unit's own events (every read below filters by its task, issue
		// #630), save the model's pause, which every task's limits set.
		own := byTask[ts.ID]
		var fin *Event
		for i := range own {
			if e := &own[i]; e.Kind == "finished" && e.Attempt == ts.Attempt {
				fin = e
			}
		}
		if fin != nil {
			f.FinishReason = fin.Reason
		}
		if _, done := paused[ts.Model]; !done && ts.Model != "" {
			paused[ts.Model] = ""
			if p, ok := rateLimitPausedAt(events, ts.Model, now, pauseAt); ok {
				paused[ts.Model] = p.Until.UTC().Format(time.RFC3339)
			}
		}
		f.PausedUntil = paused[ts.Model]
		if fin != nil && fin.Reason == "stop" {
			have, at, tree, _ := latestReading(own, ts.ID, "owns_checked", ts.Attempt)
			ft, _ := time.Parse(time.RFC3339Nano, fin.TS)
			f.HaveReading = have && at.After(ft)
			f.InspectReady = inspectionReady(own, ts.ID, ts.Attempt)
			if panel := panelFor(own, ts.ID, tree, cfg.PanelDimensions()); f.InspectReady && len(panel) > 0 && panelApplies(own, ts.ID, cfg.ReviewRequired()) {
				f.PanelPending = panelIncomplete(VerdictMatrix(own, ts.ID, tree, panel), panel)
			}
		}
		out[ts.ID] = nextAction(f)
	}
	return out
}

// andonRows are the andon view's rows, worst first; entries of one severity
// keep the floor's order (newest first). UNIT leads, as the row's key; SINCE
// is the clock time the condition began, so a refresh leaves it unchanged.
func andonRows(d TUIData) (header []string, rows [][]string) {
	header = []string{"UNIT", "SEVERITY", "SIGNAL", "SINCE", "WHAT HAPPENED", "NEXT"}
	andons := slices.Clone(d.Floor.Andon)
	slices.SortStableFunc(andons, func(a, b Andon) int {
		return severityRank[andonSeverity(a)] - severityRank[andonSeverity(b)]
	})
	for _, a := range andons {
		since := HumanAge(a.Age)
		if !d.Floor.Refreshed.IsZero() {
			since = d.Floor.Refreshed.Add(-time.Duration(a.Age) * time.Second).Local().Format("15:04")
		}
		rows = append(rows, []string{a.Task, andonSeverity(a), a.State, since, andonWhat(d, a), andonNext(d, a)})
	}
	return header, rows
}

// learningRows are the learnings, newest first.
func learningRows(d TUIData) (header []string, rows [][]string) {
	header = []string{"ID", "TITLE", "SEVERITY", "TASK", "DISMISSED"}
	for i := len(d.Learnings) - 1; i >= 0; i-- {
		l := d.Learnings[i]
		dismissed := ""
		if l.Dismissed {
			dismissed = "dismissed"
		}
		rows = append(rows, []string{l.ID, l.Title, l.Severity, l.Task, dismissed})
	}
	return header, rows
}

// checkpointRows are the checkpoints, by task then attempt.
func checkpointRows(d TUIData) (header []string, rows [][]string) {
	header = []string{"TASK", "ATTEMPT", "SHA", "FILES", "AGE"}
	for _, c := range d.Checkpoints {
		age := "-"
		if c.Age >= 0 {
			age = HumanAge(c.Age)
		}
		rows = append(rows, []string{c.Task, c.Attempt, shortSHA(c.SHA), strconv.Itoa(len(c.Paths)), age})
	}
	return header, rows
}

func shortSHA(sha string) string { return sha[:min(7, len(sha))] }

// searchLimit caps the search view's results.
const searchLimit = 500

// SearchDoc is one text the search looks through: its source (event, log,
// report, brief), the unit and attempt it belongs to, its lines, and the
// number of its first line (an event is one line of events.jsonl).
type SearchDoc struct {
	Source, Task, Attempt string
	First                 int
	Lines                 []string
}

// SearchHit is one line holding the search text, with an excerpt: the
// match and about 40 characters on each side.
type SearchHit struct {
	Source, Task, Attempt string
	Line                  int
	Excerpt               string
}

// SearchDocs finds text in docs, case-insensitively, one hit per matching
// line in docs' order, and stops at limit hits (capped true).
func SearchDocs(docs []SearchDoc, text string, limit int) (hits []SearchHit, capped bool) {
	needle := lowerRunes(text)
	if len(needle) == 0 {
		return nil, false
	}
	for _, doc := range docs {
		for i, line := range doc.Lines {
			at := runeIndex(lowerRunes(line), needle)
			if at < 0 {
				continue
			}
			if len(hits) == limit {
				return hits, true
			}
			hits = append(hits, SearchHit{Source: doc.Source, Task: doc.Task, Attempt: doc.Attempt,
				Line: doc.First + i, Excerpt: excerpt([]rune(line), at, len(needle))})
		}
	}
	return hits, false
}

// lowerRunes lowers each rune on its own, so indexes into it are indexes
// into the original's runes.
func lowerRunes(s string) []rune {
	r := []rune(s)
	for i := range r {
		r[i] = unicode.ToLower(r[i])
	}
	return r
}

// runeIndex is the index of the first needle in hay, -1 when none.
func runeIndex(hay, needle []rune) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if slices.Equal(hay[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

// excerptContext is how many characters an excerpt keeps on each side.
const excerptContext = 40

// excerpt is line around the n runes at at, "…" marking a cut, on one line.
func excerpt(line []rune, at, n int) string {
	from, to := max(0, at-excerptContext), min(len(line), at+n+excerptContext)
	s := strings.Join(strings.Fields(string(line[from:to])), " ")
	if from > 0 {
		s = "…" + s
	}
	if to < len(line) {
		s += "…"
	}
	return s
}

// searchRows are the search view's rows.
func searchRows(hits []SearchHit) (header []string, rows [][]string) {
	header = []string{"SOURCE", "TASK", "ATTEMPT", "LINE", "EXCERPT"}
	for _, h := range hits {
		rows = append(rows, []string{h.Source, h.Task, h.Attempt, strconv.Itoa(h.Line), h.Excerpt})
	}
	return header, rows
}

// localDetail is the lines of a drill-down built from d itself: "learning"
// (id is the learning's id), "checkpoint" (id is task/attempt) or "views".
func localDetail(d TUIData, kind, id string) []string {
	var out []string
	switch kind {
	case "learning":
		for _, l := range d.Learnings {
			if l.ID != id {
				continue
			}
			out = append(out, l.ID+"  "+l.Title,
				"task "+l.Task+" · severity "+l.Severity+" · scope "+l.Scope)
			if l.Dismissed {
				out = append(out, "dismissed: "+l.Reason)
			}
			for _, part := range [][2]string{{"observed", l.Observed}, {"evidence", l.Evidence}, {"ask", l.Ask}} {
				out = append(out, "", part[0]+":")
				out = append(out, strings.Split(part[1], "\n")...)
			}
			if len(l.Signals) > 0 {
				out = append(out, "", "signals: "+strings.Join(l.Signals, ", "))
			}
		}
	case "checkpoint":
		for _, c := range d.Checkpoints {
			if c.Task+"/"+c.Attempt != id {
				continue
			}
			out = append(out, checkpointRef(c.Task, c.Attempt)+" "+c.SHA, "", "changed paths:")
			for _, p := range c.Paths {
				out = append(out, "  "+p)
			}
		}
	case "views":
		out = append(out, "Views (:name or :alias):")
		for _, v := range tuiViews {
			out = append(out, fmt.Sprintf("  %-12s :%-3s %s", v.name, v.alias, v.what))
		}
		out = append(out, "  :q quits; Esc goes back, - swaps to the previous view, [ ] walk the commands")
	}
	if len(out) == 0 {
		out = []string{kind + " " + id + ": no longer in the ledger"}
	}
	return out
}

// timelineLines draws a unit's timeline (UnitTimeline): one line per event,
// a "·· <length> <what>" line per gap, and the summary last.
func timelineLines(rows []TimelineRow) []string {
	out := []string{"timeline:"}
	for _, r := range rows {
		switch r.Kind {
		case "gap":
			out = append(out, fmt.Sprintf("  %14s  ·· %s %s", "", whyDur(r.Dur), r.Detail))
		case "summary":
			out = append(out, "", r.Detail)
		default:
			out = append(out, fmt.Sprintf("  %s  %-13s %s", r.TS.Local().Format("01-02 15:04:05"), r.Kind, r.Detail))
		}
	}
	return out
}

// findingLines are the unit's review findings, open ones marked, each
// followed by the responses to it.
func findingLines(events []Event, task string) []string {
	open := map[string]bool{}
	for _, f := range OpenFindings(events, task) {
		open[f.Finding] = true
	}
	var out []string
	for _, e := range events {
		if e.Task != task || e.Kind != "review_finding" {
			continue
		}
		state := "closed"
		if open[e.Finding] {
			state = "OPEN"
		}
		where := e.Path
		if e.LineNo > 0 {
			where += fmt.Sprintf(":%d", e.LineNo)
		}
		out = append(out, strings.TrimSpace(fmt.Sprintf("%s %s %s %s %s", state, e.Finding, e.Severity, where, oneLine.Replace(e.Title))))
		for _, r := range events {
			if r.Task == task && r.Kind == "finding_response" && r.Finding == e.Finding {
				out = append(out, "    ↳ "+strings.TrimSpace(r.Verdict+" by "+r.Session+": "+oneLine.Replace(r.Note)))
			}
		}
	}
	if len(out) == 0 {
		out = []string{"no review findings"}
	}
	return out
}

// noClock stands for a step's time: a run stream's lines carry none.
const noClock = "--:--:--"

// runStep is one tool call of a run as the log tab draws it.
type runStep struct {
	n                  int
	tool, target, diff string
	cmd                string // the command of a shell call, "" for any other tool
	rc                 string // "rc=N" once its result arrived, for a shell call
	failed             string // the first failing line of a failed command
}

// runExitCode reads claude's failed-command result, "Exit code N".
var runExitCode = regexp.MustCompile(`^Exit code (\d+)`)

// runFailLine finds a failing test or compile line in a command's output.
var runFailLine = regexp.MustCompile(`--- FAIL|^FAIL\b|^panic:|\.go:\d+(:\d+)?: |(?i)\berror\b`)

// runLogLines are the worker's run (issue #583 k7): the adapter's
// observations of the attempt's run file, one line per tool call — its
// number, tool and target (file or command), +N −M for an edit, rc=N for a
// command and the first failing line of a failed one — then how it ended;
// head is the dispatch line, ended the finished event's time ("" while it
// runs). A torn last line is left for the next read.
func runLogLines(adap Adapter, run []byte, head, ended string) []string {
	if i := bytes.LastIndexByte(run, '\n'); i >= 0 {
		run = run[:i+1]
	} else {
		run = nil
	}
	var steps []*runStep
	var end string
	for _, line := range bytes.Split(run, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if obs, ok := adap.Parse(line); ok {
			switch obs.Kind {
			case "tool":
				s := &runStep{n: len(steps) + 1, tool: obs.Tool, target: strings.Join(obsPaths(obs), ", ")}
				if obs.Command != "" {
					s.cmd = oneLine.Replace(obs.Command)
					s.target = s.cmd
				}
				s.diff = editDiff(obs.Input)
				steps = append(steps, s)
			case "step":
				end = strings.TrimSpace(obs.Reason + fmt.Sprintf(" $%.2f", obs.Cost))
			case "error":
				end = "error " + oneLine.Replace(obs.Error)
			}
		}
		// A command's result answers the latest call, the only one the
		// adapter reports per turn; the adapter keeps no rc, so it is read
		// from the result line itself.
		if isErr, text, ok := toolResult(line); ok && len(steps) > 0 {
			s := steps[len(steps)-1]
			if s.rc != "" || s.cmd == "" {
				continue
			}
			s.rc = "rc=0"
			if isErr {
				s.rc = "rc=1"
				if m := runExitCode.FindStringSubmatch(text); m != nil {
					s.rc = "rc=" + m[1]
				}
				for _, l := range strings.Split(text, "\n") {
					if l = strings.TrimSpace(l); runFailLine.MatchString(l) && !runExitCode.MatchString(l) {
						s.failed = l
						break
					}
				}
			}
		}
	}
	out := []string{head}
	for _, s := range steps {
		l := strings.TrimRight(fmt.Sprintf("%s #%-3d %-6s %s %s %s", noClock, s.n, s.tool, s.target, s.diff, s.rc), " ")
		out = append(out, strings.Join(strings.Fields(l), " "))
		if s.failed != "" {
			out = append(out, noClock+"      ↳ "+s.failed)
		}
	}
	switch {
	case ended != "":
		out = append(out, ended+" ended "+end)
	case end != "":
		out = append(out, noClock+" ended "+end)
	}
	return out
}

// editDiff is an edit's "+N −M" (lines written, lines replaced), from its
// input's new_string (or content) and old_string; "" when it has neither.
func editDiff(input string) string {
	var in struct {
		Old     string `json:"old_string"`
		New     string `json:"new_string"`
		Content string `json:"content"`
	}
	if input == "" || json.Unmarshal([]byte(input), &in) != nil {
		return ""
	}
	count := func(s string) int {
		if s == "" {
			return 0
		}
		return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
	}
	added := count(in.New) + count(in.Content)
	if added == 0 && in.Old == "" {
		return ""
	}
	return fmt.Sprintf("+%d −%d", added, count(in.Old))
}

// toolResult is a claude user line's first tool_result: whether it is an
// error, and its text (toolResultText); ok false for any other line.
func toolResult(line []byte) (isErr bool, text string, ok bool) {
	var l struct {
		Type    string `json:"type"`
		Message struct {
			Content []struct {
				Type    string          `json:"type"`
				IsError bool            `json:"is_error"`
				Content json.RawMessage `json:"content"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &l) != nil || l.Type != "user" {
		return false, "", false
	}
	for _, b := range l.Message.Content {
		if b.Type == "tool_result" {
			return b.IsError, toolResultText(b.Content), true
		}
	}
	return false, "", false
}

// unitEventLines are the unit's events as their ledger records.
func unitEventLines(events []Event, task string) []string {
	var out []string
	for _, e := range events {
		if e.Task != task {
			continue
		}
		e.Prev = ""
		b, err := marshalEvent(e)
		if err != nil {
			out = append(out, "event: "+err.Error())
			continue
		}
		out = append(out, string(b))
	}
	return out
}

// checkpointLines are the unit's checkpoints and their changed paths.
func checkpointLines(cps []Checkpoint) []string {
	var out []string
	for _, c := range cps {
		out = append(out, checkpointRef(c.Task, c.Attempt)+" "+shortSHA(c.SHA))
		for _, p := range c.Paths {
			out = append(out, "  "+p)
		}
	}
	if len(out) == 0 {
		out = []string{"no checkpoints"}
	}
	return out
}
