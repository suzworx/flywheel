package flywheel

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Column widths for the units table. They total 80 with the single-space
// separators, so the table fits the minimum render width and every wider
// terminal draws the same columns; overlong cells are truncated, never pushed
// off the line.
const (
	taskW    = 12
	stageW   = 9
	attW     = 4
	sessW    = 12
	modelW   = 10
	stepsW   = 4
	ageW     = 5
	runW     = 13
	floorW   = 12
	adapterW = 8
	modelL   = 40
	andonW   = 14
	lineW    = 8  // the units table's LINE column, shown when a unit has a line
	treeW    = 14 // the units table's TREE column, shown when a unit has a workdir
)

// treeCell is a unit's TREE cell (issue #394): the worktree's last path
// element and "@" plus its base commit, or "" in the main checkout. Either
// separator ends a path element, so a ledger recorded on another OS still reads.
func treeCell(u Unit) string {
	if u.Workdir == "" {
		return ""
	}
	name := strings.TrimRight(u.Workdir, `/\`)
	name = name[strings.LastIndexAny(name, `/\`)+1:]
	if u.Base != "" {
		name += "@" + u.Base
	}
	return name
}

// ANSI colour codes. They are only emitted when colour is enabled (colour=true
// in RenderText); otherwise cells carry no escape sequences.
const (
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiReset  = "\x1b[0m"
)

// tableWidths expands the fixed 80-column layout by giving the extra
// (width-80) columns to MODEL (two thirds) and TASK (one third). modelLine is
// the floor line's model width, grown by the same extra share from modelL.
func tableWidths(width int) (task, model, modelLine int) {
	extra := width - 80
	if extra < 0 {
		extra = 0
	}
	taskGrow := extra / 3
	modelGrow := extra - taskGrow
	return taskW + taskGrow, modelW + modelGrow, modelL + modelGrow
}

// RenderText draws the floor as a text dashboard to w. Columns are truncated
// to fit the width (the CLI uses 100 by default and 80 as a floor); colour is
// applied only when color is true.
func RenderText(w io.Writer, f Floor, width int, color bool) {
	if width < 80 {
		width = 80
	}
	taskWd, modelWd, modelLineWd := tableWidths(width)
	renderHeader(w, f, width)
	renderFloor(w, f, modelLineWd, width)
	renderProductLines(w, f, width)
	renderUnits(w, f, taskWd, modelWd, color)
	renderAndon(w, f, color)
	renderOutput(w, f)
}

// renderHeader prints the title, the watched repo and the refresh clock. The
// repo path is truncated so the header never exceeds the render width.
func renderHeader(w io.Writer, f Floor, width int) {
	fmt.Fprintf(w, "flywheel factory\n")
	fmt.Fprintf(w, "repo  %s\n", truncate(f.Dir, width-8))
	h, m, s := f.Refreshed.UTC().Clock()
	fmt.Fprintf(w, "refreshed %02d:%02d:%02d UTC\n", h, m, s)
}

// renderFloor prints the stations (one per config worker) and the staffing.
func renderFloor(w io.Writer, f Floor, modelLine, width int) {
	fmt.Fprintf(w, "\nfloor\n")
	for _, l := range f.Lines {
		fmt.Fprintf(w, "  %-*s  %-*s  %s  max %d  busy %d\n",
			floorW, truncate(l.Name, floorW),
			adapterW, truncate(l.Adapter, adapterW),
			truncate(l.Model, modelLine), l.MaxParallel, l.Busy)
	}
	fmt.Fprintf(w, "  lead  %s\n", f.Staffing.Lead)

	hasConfigured := false
	for _, r := range f.Staffing.Roles {
		if r.Configured != "" {
			hasConfigured = true
			break
		}
	}
	if hasConfigured {
		for _, r := range f.Staffing.Roles {
			session := r.Session
			if session == "" {
				session = "not registered"
			}
			line := fmt.Sprintf("  %-9s  config %-9s  floor %s", r.Name, r.Configured, session)
			if r.Mismatch {
				line += " !"
			}
			fmt.Fprintf(w, "%s\n", truncate(line, width))
		}
	}
}

// renderProductLines prints the product lines and their unit counts, each
// line cut to the render width, or nothing when ProductLines is empty.
func renderProductLines(w io.Writer, f Floor, width int) {
	if len(f.ProductLines) == 0 {
		return
	}
	fmt.Fprintf(w, "\nlines\n")
	for _, pl := range f.ProductLines {
		line := fmt.Sprintf("  %-12s  %-10s  units %d  building %d  landed %d",
			truncate(pl.Name, 12), truncate(pl.Worker, 10),
			pl.Units, pl.Building, pl.Landed)
		if len(pl.Owns) > 0 {
			line = line + "  owns " + strings.Join(pl.Owns, ", ")
		}
		fmt.Fprintf(w, "%s\n", truncate(line, width))
	}
}

// truncate cuts s to at most n runes, marking overflow with a trailing "~".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:1]
	}
	return s[:n-1] + "~"
}

// HumanAge renders a whole-second age in human units, rounding down: seconds
// below 60, minutes below an hour, hours below 48, days beyond.
func HumanAge(seconds int) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 48*3600:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/(24*3600))
	}
}

// padLeft left-justifies s in a field of n runes.
func padLeft(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// stateColor returns the ANSI colour for a run state, or "" for none.
func stateColor(state string) string {
	switch state {
	case "done", "landed":
		return ansiGreen
	case "exploring", "long-step":
		return ansiYellow
	case "stalled", "capped", "provider-error", "mismatch":
		return ansiRed
	}
	return ""
}

// paint wraps s in colour when enabled and a colour applies, else returns s.
func paint(color bool, code, s string) string {
	if !color || code == "" {
		return s
	}
	return code + s + ansiReset
}

// renderUnits prints the units table: one row per task, in-flight first, then
// by last update, as sorted by the watcher. Numeric columns are right-aligned
// and every cell is truncated to its column width.
func renderUnits(w io.Writer, f Floor, taskWd, modelWd int, color bool) {
	fmt.Fprintf(w, "\nunits (%d)\n", len(f.Units))
	hasLine := false
	for _, u := range f.Units {
		if u.Line != "" {
			hasLine = true
			break
		}
	}
	hasTree := false
	for _, u := range f.Units {
		if u.Workdir != "" {
			hasTree = true
			break
		}
	}
	sessWd := sessW
	need := 0
	if hasLine {
		need += lineW + 1
	}
	if hasTree {
		need += treeW + 1
	}
	if need > 0 {
		// The LINE and TREE columns' cells come out of MODEL, then TASK, then
		// SESSION, so the table still fits the render width (issues #69, #394).
		for _, c := range []struct {
			wd    *int
			floor int
		}{{&modelWd, 6}, {&taskWd, 8}, {&sessWd, 6}} {
			if n := min(need, *c.wd-c.floor); n > 0 {
				*c.wd -= n
				need -= n
			}
		}
	}
	row := func(task, line, tree, stage, att, sess, model, steps, age, run string) {
		cells := []string{fmt.Sprintf("%-*s", taskWd, task)}
		if hasLine {
			cells = append(cells, fmt.Sprintf("%-*s", lineW, line))
		}
		if hasTree {
			cells = append(cells, fmt.Sprintf("%-*s", treeW, tree))
		}
		cells = append(cells,
			fmt.Sprintf("%-*s", stageW, stage), fmt.Sprintf("%-*s", attW, att),
			fmt.Sprintf("%-*s", sessWd, sess), fmt.Sprintf("%-*s", modelWd, model),
			fmt.Sprintf("%*s", stepsW, steps), fmt.Sprintf("%*s", ageW, age), run)
		fmt.Fprintf(w, "  %s\n", strings.Join(cells, " "))
	}
	row("TASK", "LINE", "TREE", "STAGE", "ATT", "SESSION", "MODEL", "STEPS", "AGE", fmt.Sprintf("%-*s", runW, "RUN"))
	for _, u := range f.Units {
		cell := truncate(u.RunState, runW)
		if u.RunState == "capped" && u.Peak > 0 {
			cell = truncate("capped "+tokensK(Tokens{Reasoning: u.Peak}), runW)
		}
		if u.RunState == "rate-limited" && !u.ResetAt.IsZero() {
			// The last column: the reset clock may run past runW (issue #383).
			cell = "rate-limited until " + u.ResetAt.Local().Format("15:04")
		}
		run := paint(color, stateColor(u.RunState), padLeft(cell, runW))
		row(truncate(u.Task, taskWd), truncate(u.Line, lineW), truncate(treeCell(u), treeW), truncate(u.Stage, stageW),
			truncate(u.Attempt, attW), truncate(u.Session, sessWd), truncate(u.Model, modelWd),
			fmt.Sprintf("%d", u.Steps), HumanAge(u.LastAge), run)
	}
}

// renderAndon prints the stopped-line conditions, newest first.
func renderAndon(w io.Writer, f Floor, color bool) {
	fmt.Fprintf(w, "\nandon (%d)\n", len(f.Andon))
	for _, a := range f.Andon {
		st := paint(color, stateColor(a.State), padLeft(a.State, runW))
		fmt.Fprintf(w, "  %-*s  %s  %s\n", andonW, truncate(a.Task, andonW), st, HumanAge(a.Age))
	}
}

// renderOutput prints the production summary.
func renderOutput(w io.Writer, f Floor) {
	rate := "n/a"
	if f.Output.HasReviews {
		rate = fmt.Sprintf("%d%%", int(f.Output.FirstPassRate*100))
	}
	fmt.Fprintf(w, "\noutput\n")
	fmt.Fprintf(w, "  landed today %d  finished %d  first-pass %s  rework %.2f  tokens %d  cost $%.4f\n",
		f.Output.LandedToday, f.Output.Finished, rate,
		f.Output.Rework, f.Output.Tokens, f.Output.Cost)
}

// jLine, jStaff, jUnit, jAndon, jOutput and jFloor are the JSON mirror of the
// view model, tagged so json.MarshalIndent emits stable key order.
type jLine struct {
	Name        string `json:"name"`
	Adapter     string `json:"adapter"`
	Model       string `json:"model"`
	MaxParallel int    `json:"max_parallel"`
	Busy        int    `json:"busy"`
}

type jFloorRole struct {
	Name       string `json:"name"`
	Configured string `json:"configured,omitempty"`
	Session    string `json:"session,omitempty"`
	Model      string `json:"model,omitempty"`
	Mismatch   bool   `json:"mismatch,omitempty"`
}

type jStaff struct {
	Lead  string       `json:"lead"`
	Roles []jFloorRole `json:"roles,omitempty"`
}

type jUnit struct {
	Task          string `json:"task"`
	Line          string `json:"line,omitempty"`
	Stage         string `json:"stage"`
	Attempt       string `json:"attempt"`
	Session       string `json:"session"`
	Model         string `json:"model"`
	Steps         int    `json:"steps"`
	LastAge       int    `json:"last_age"`
	RunState      string `json:"run_state"`
	PeakReasoning int    `json:"peak_reasoning"`
	Station       string `json:"station,omitempty"`
	ResetAt       string `json:"reset_at,omitempty"` // a rate-limited unit's reset, RFC 3339 (issue #383)
	Workdir       string `json:"workdir,omitempty"`  // the unit's worktree (issue #394)
	Base          string `json:"base,omitempty"`     // its base commit, first 7 characters
}

type jAndon struct {
	Task  string `json:"task"`
	State string `json:"state"`
	Age   int    `json:"age"`
}

type jOutput struct {
	LandedToday   int     `json:"landed_today"`
	Finished      int     `json:"finished"`
	FirstPassRate float64 `json:"first_pass_rate"`
	HasReviews    bool    `json:"has_reviews"`
	Rework        float64 `json:"rework"`
	Tokens        int     `json:"tokens"`
	Cost          float64 `json:"cost"`
}

type jProductLine struct {
	Name     string         `json:"name"`
	Worker   string         `json:"worker"`
	Owns     []string       `json:"owns,omitempty"`
	Units    int            `json:"units"`
	Building int            `json:"building"`
	Landed   int            `json:"landed"`
	Stations map[string]int `json:"stations,omitempty"`
	WIP      int            `json:"wip,omitempty"`
	Limit    int            `json:"limit,omitempty"`
}

type jFloor struct {
	Dir          string         `json:"dir"`
	Refreshed    string         `json:"refreshed"`
	Lines        []jLine        `json:"lines"`
	ProductLines []jProductLine `json:"product_lines,omitempty"`
	Staffing     jStaff         `json:"staffing"`
	Units        []jUnit        `json:"units"`
	Andon        []jAndon       `json:"andon"`
	Output       jOutput        `json:"output"`
}

// RenderJSON writes the floor as indented JSON with stable field order. On a
// marshalling error it writes nothing.
func RenderJSON(w io.Writer, f Floor) {
	var j jFloor
	j.Dir = f.Dir
	j.Refreshed = f.Refreshed.UTC().Format(time.RFC3339Nano)
	for _, l := range f.Lines {
		j.Lines = append(j.Lines, jLine{Name: l.Name, Adapter: l.Adapter, Model: l.Model, MaxParallel: l.MaxParallel, Busy: l.Busy})
	}
	for _, pl := range f.ProductLines {
		j.ProductLines = append(j.ProductLines, jProductLine{Name: pl.Name, Worker: pl.Worker, Owns: pl.Owns, Units: pl.Units, Building: pl.Building, Landed: pl.Landed, Stations: pl.Stations, WIP: pl.WIP, Limit: pl.Limit})
	}
	js := jStaff{Lead: f.Staffing.Lead}
	for _, r := range f.Staffing.Roles {
		js.Roles = append(js.Roles, jFloorRole{Name: r.Name, Configured: r.Configured, Session: r.Session, Model: r.Model, Mismatch: r.Mismatch})
	}
	j.Staffing = js
	for _, u := range f.Units {
		ju := jUnit{Task: u.Task, Line: u.Line, Stage: u.Stage, Attempt: u.Attempt, Session: u.Session, Model: u.Model, Steps: u.Steps, LastAge: u.LastAge, RunState: u.RunState, PeakReasoning: u.Peak, Station: u.Station, Workdir: u.Workdir, Base: u.Base}
		if !u.ResetAt.IsZero() {
			ju.ResetAt = u.ResetAt.UTC().Format(time.RFC3339)
		}
		j.Units = append(j.Units, ju)
	}
	for _, a := range f.Andon {
		j.Andon = append(j.Andon, jAndon{Task: a.Task, State: a.State, Age: a.Age})
	}
	j.Output = jOutput{LandedToday: f.Output.LandedToday, Finished: f.Output.Finished, FirstPassRate: f.Output.FirstPassRate, HasReviews: f.Output.HasReviews, Rework: f.Output.Rework, Tokens: f.Output.Tokens, Cost: f.Output.Cost}
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return
	}
}
