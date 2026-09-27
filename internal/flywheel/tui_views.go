package flywheel

import (
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
