package flywheel

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Floor is the live view of the factory floor, built only from the event log,
// the config and the run files. Rendering never calls a model and never runs a
// git write command.
type Floor struct {
	Dir       string    // repo directory the floor watches
	Refreshed time.Time // the clock the floor was drawn at
	Lines     []Line
	Staffing  Staffing
	Units     []Unit
	Andon     []Andon
	Output    Output
}

// Line is one config worker: a station on the floor. busy is the number of
// its in-flight units grouped by model.
type Line struct {
	Name        string
	Adapter     string
	Model       string
	MaxParallel int
	Busy        int
}

// Staffing reports the recorded factory roles. Lead is the rendered floor
// line: `<session> (<model>)` from the latest staffed event for the lead
// role, or "not registered" when no staffed event exists.
type Staffing struct {
	Lead string
}

// staffRole is one registered role holder: the latest staffed event's session
// and model.
type staffRole struct {
	Session string
	Model   string
}

// Unit is one dispatched work order: its task, stage, the latest attempt and
// the run state read from that attempt's run file.
type Unit struct {
	Task     string
	Stage    string // planned, building, finished, inspecting, audited, landed, blocked
	Attempt  string
	Session  string // short form
	Model    string
	Steps    int
	LastAge  int    // seconds since the unit's last event
	RunState string // silent, running, exploring, long-step, stalled, capped, provider-error, failed, done
	Peak     int    // largest single-step reasoning figure, from the latest finished event; 0 when none
}

// peakReasoningFor returns the task's latest finished event's peak_reasoning
// for its current attempt, scanning the accumulated event log in order so the
// last matching event wins (issue #84).
func peakReasoningFor(events []Event, task, attempt string) int {
	peak := 0
	for _, e := range events {
		if e.Kind == "finished" && e.Task == task && e.Attempt == attempt {
			peak = e.PeakReasoning
		}
	}
	return peak
}

// Andon is one stopped-line condition: a unit in silent, stalled, capped,
// provider-error or failed, newest first.
type Andon struct {
	Task  string
	State string
	Age   int
}

// Output is the factory's production summary, aggregated from finished events.
type Output struct {
	LandedToday   int     // landed events with today's date
	Finished      int     // units that finished
	FirstPassRate float64 // first verdict pass (reviewed pass or inspected pass) / first verdicts
	HasReviews    bool    // false until a first verdict exists
	Rework        float64 // correction attempts per unit
	Tokens        int     // billed tokens across finished events
	Cost          float64 // cost across finished events
}

// stageOf maps a derived status (and, while status is "finished", that task's
// finished reason) to a floor stage: cut-off when the reason is "length",
// failed for any other reason except "stop" or empty (a clean finish), and
// finished otherwise. Every other status ignores reason and keeps its own
// mapping.
func stageOf(status, reason string) string {
	switch status {
	case "planned":
		return "planned"
	case "dispatched":
		return "building"
	case "running":
		return "building"
	case "finished":
		switch reason {
		case "length":
			return "cut-off"
		case "", "stop":
			return "finished"
		default:
			return "failed"
		}
	case "passed":
		return "passed"
	case "needs-correction":
		return "building"
	case "rejected":
		return "rejected"
	case "blocked":
		return "blocked"
	case "landed":
		return "landed"
	}
	return "building"
}

// liveRun reports whether a run state is an in-flight, not-yet-dead unit:
// either actively progressing or merely unhealthy enough to reach the andon
// but still live. Capped, provider-error and failed runs are dead: they
// logged a finished event and no longer occupy a busy slot.
func liveRun(state string) bool {
	switch state {
	case "running", "exploring", "long-step", "silent", "stalled":
		return true
	}
	return false
}

// classifyRun maps a run's observed signals to a run state. done is set when
// the attempt's finished event exists; age is the seconds since the run file
// last grew. Once done, lastReason (the caller overrides it with the
// attempt's finished-event reason when one is recorded, since that is always
// a complete line while the run file's own tail may not be) decides the run
// state outright: "length" is capped, "error" is provider-error, "stop" or
// empty is a clean done, and any other reason (start-failed, silent, ...) is
// failed. Live (not done) classification still checks hasError and
// lastReason=="length" before the other live signals, since a capped or
// provider-error run in progress has not recorded a finished event yet.
func classifyRun(done bool, steps int, files int, edits int, hasError bool, lastReason string, size int64, age int) string {
	if done {
		switch lastReason {
		case "length":
			return "capped"
		case "error":
			return "provider-error"
		case "", "stop":
			return "done"
		default:
			return "failed"
		}
	}
	if hasError {
		return "provider-error"
	}
	if lastReason == "length" {
		return "capped"
	}
	if size == 0 && age > 60 {
		return "silent"
	}
	if age > 600 {
		return "stalled"
	}
	if age > 300 {
		return "long-step"
	}
	if steps >= 10 && files >= 3 && edits == 0 {
		return "exploring"
	}
	return "running"
}

// Watcher reads the event log and the run files incrementally, remembering the
// file offset it last read so a refresh reads only appended bytes. A truncated
// or replaced file (size smaller than the offset) is re-read from zero.
// EventsBytes and RunBytes count the bytes actually read, for tests.
type Watcher struct {
	events      []Event
	eventsOff   int64
	runSteps    map[string]int
	runFiles    map[string]map[string]bool
	runEdits    map[string]int
	runErr      map[string]bool
	runReason   map[string]string
	runOff      map[string]int64
	EventsBytes int64
	RunBytes    int64
}

// NewWatcher returns a Watcher with its maps ready to use.
func NewWatcher() Watcher {
	return Watcher{
		events:    []Event{},
		runSteps:  map[string]int{},
		runFiles:  map[string]map[string]bool{},
		runEdits:  map[string]int{},
		runErr:    map[string]bool{},
		runReason: map[string]string{},
		runOff:    map[string]int64{},
	}
}

// Refresh draws a fresh Floor from the config, the event log and the run files
// at clock now, reading only bytes appended since the previous refresh.
func (w *Watcher) Refresh(dir string, now time.Time) (Floor, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return Floor{}, err
	}
	if err := readEvents(dir, w); err != nil {
		return Floor{}, err
	}
	st := Derive(w.events)
	units, byModel, uerr := buildUnits(w, st, now, dir)
	if uerr != nil {
		return Floor{}, uerr
	}
	fl := Floor{Dir: dir, Refreshed: now}
	fl.Lines = buildLines(cfg, byModel)
	fl.Staffing = buildStaffing(w.events)
	fl.Units = units
	fl.Andon = buildAndon(units)
	fl.Output = buildOutput(w.events, now)
	return fl, nil
}

// readEvents folds only the bytes appended since the last refresh into the
// watcher's accumulated event list.
func readEvents(dir string, w *Watcher) error {
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	size := info.Size()
	if size < w.eventsOff {
		w.events = []Event{}
		w.eventsOff = 0
	}
	if size == w.eventsOff {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if w.eventsOff > 0 {
		if _, err := f.Seek(w.eventsOff, io.SeekStart); err != nil {
			f.Close()
			return fmt.Errorf("seek %s: %w", path, err)
		}
	}
	b, rerr := io.ReadAll(f)
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if rerr != nil {
		return fmt.Errorf("read %s: %w", path, rerr)
	}
	if idx := bytes.LastIndexByte(b, '\n'); idx >= 0 {
		from := w.eventsOff
		evs, perr := ParseEvents(bytes.NewReader(b[:idx+1]), false)
		if perr != nil {
			return perr
		}
		w.EventsBytes += int64(idx + 1)
		for _, e := range evs {
			w.events = append(w.events, e)
		}
		w.eventsOff = from + int64(idx+1)
	}
	return nil
}

// readRun folds only the appended bytes of one run file into the watcher's
// per-run accumulators and returns its size and mtime. A missing run file means
// a zero-byte run at the epoch.
func readRun(dir string, w *Watcher, rel string) (size int64, mtime time.Time, err error) {
	path := filepath.Join(dir, rel)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, fmt.Errorf("stat %s: %w", rel, err)
	}
	size = info.Size()
	off := w.runOff[rel]
	if size < off {
		w.runOff[rel] = 0
		w.runSteps[rel] = 0
		w.runFiles[rel] = map[string]bool{}
		w.runEdits[rel] = 0
		w.runErr[rel] = false
		w.runReason[rel] = ""
		off = 0
	}
	if size == off {
		return size, info.ModTime(), nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return size, info.ModTime(), fmt.Errorf("open %s: %w", rel, err)
	}
	if off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			f.Close()
			return size, info.ModTime(), fmt.Errorf("seek %s: %w", rel, err)
		}
	}
	b, rerr := io.ReadAll(f)
	if err := f.Close(); err != nil {
		return size, info.ModTime(), fmt.Errorf("close %s: %w", rel, err)
	}
	if rerr != nil {
		return size, info.ModTime(), fmt.Errorf("read %s: %w", rel, rerr)
	}
	if idx := bytes.LastIndexByte(b, '\n'); idx >= 0 {
		oc := opencodeAdapter{}
		sc := bufio.NewScanner(bytes.NewReader(b[:idx+1]))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			obs, ok := oc.Parse(sc.Bytes())
			if !ok {
				continue
			}
			switch obs.Kind {
			case "step":
				w.runSteps[rel] = w.runSteps[rel] + 1
				if obs.Reason != "" {
					w.runReason[rel] = obs.Reason
				}
			case "tool":
				if obs.Tool == "read" && obs.Path != "" {
					if _, ok := w.runFiles[rel]; !ok {
						w.runFiles[rel] = map[string]bool{}
					}
					w.runFiles[rel][obs.Path] = true
				}
				if obs.Tool == "edit" || obs.Tool == "write" {
					w.runEdits[rel] = w.runEdits[rel] + 1
				}
			case "error":
				w.runErr[rel] = true
			}
		}
		if err := sc.Err(); err != nil {
			return size, info.ModTime(), fmt.Errorf("read %s: %w", rel, err)
		}
		w.runOff[rel] = off + int64(idx+1)
		w.RunBytes += int64(idx + 1)
	}
	return size, info.ModTime(), nil
}

// buildUnits derives one Unit per task, reads the latest attempt's run file
// for its run state, and tallies in-flight units by model.
func buildUnits(w *Watcher, st State, now time.Time, dir string) ([]Unit, map[string]int, error) {
	var units []Unit
	byModel := map[string]int{}
	for _, t := range st.Tasks {
		u := Unit{
			Task: t.ID, Stage: stageOf(t.Status, t.Reason), Attempt: t.Attempt,
			Session: shortSession(t.Session), Model: t.Model,
			Steps: 0, LastAge: ageOf(t.UpdatedAt, now), RunState: "waiting",
		}
		if t.Attempt != "" {
			rel := ".flywheel/runs/" + t.ID + "." + t.Attempt + ".jsonl"
			size, mtime, rerr := readRun(dir, w, rel)
			if rerr != nil {
				return nil, byModel, rerr
			}
			done := t.Status != "planned" && t.Status != "dispatched" && t.Status != "running"
			age := ageOfTime(mtime, now)
			// A done attempt's finished-event reason (from the event log, always
			// a complete line) overrides the run file's own last-reason scan,
			// which can miss a final line the process never newline-terminated.
			reason := w.runReason[rel]
			if done && t.Reason != "" {
				reason = t.Reason
			}
			u.RunState = classifyRun(done, w.runSteps[rel], len(w.runFiles[rel]), w.runEdits[rel], w.runErr[rel], reason, size, age)
			u.Steps = w.runSteps[rel]
			u.Peak = peakReasoningFor(w.events, t.ID, t.Attempt)
		}
		if liveRun(u.RunState) {
			byModel[u.Model] = byModel[u.Model] + 1
		}
		units = append(units, u)
	}
	slices.SortStableFunc(units, func(a, b Unit) int {
		af := 0
		bf := 0
		if a.Stage == "building" {
			af = 1
		}
		if b.Stage == "building" {
			bf = 1
		}
		if af != bf {
			return bf - af
		}
		if a.LastAge != b.LastAge {
			return a.LastAge - b.LastAge
		}
		return strings.Compare(a.Task, b.Task)
	})
	return units, byModel, nil
}

// buildLines turns each config worker into a line, with busy = in-flight units
// on the worker's model.
func buildLines(cfg Config, byModel map[string]int) []Line {
	var lines []Line
	for _, w := range cfg.Workers {
		lines = append(lines, Line{Name: w.Name, Adapter: w.Adapter, Model: w.Model, MaxParallel: w.MaxParallel, Busy: byModel[w.Model]})
	}
	return lines
}

// buildStaffing reports the recorded lead: the latest staffed event per role
// (persona holds the role), rendered as `<session> (<model>)` with the
// parentheses dropped when the model is empty. "not registered" only when no
// staffed event exists.
func buildStaffing(events []Event) Staffing {
	roles := map[string]staffRole{}
	for _, e := range events {
		if e.Kind != "staffed" {
			continue
		}
		roles[e.Persona] = staffRole{Session: e.Session, Model: e.Model}
	}
	r, ok := roles["lead"]
	if !ok {
		return Staffing{Lead: "not registered"}
	}
	line := r.Session
	if r.Model != "" {
		line = line + " (" + r.Model + ")"
	}
	return Staffing{Lead: line}
}

// shortSession truncates a long session id for the table.
func shortSession(s string) string {
	if len(s) <= 16 {
		return s
	}
	return s[:16]
}

// ageOf parses an RFC3339 timestamp and returns the whole seconds from now.
func ageOf(ts string, now time.Time) int {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return 0
	}
	return ageOfTime(t, now)
}

// ageOfTime returns the whole seconds from t to now, clamped at zero.
func ageOfTime(t, now time.Time) int {
	d := now.Sub(t)
	if d.Seconds() < 0 {
		return 0
	}
	return int(d.Seconds())
}

// buildAndon lists the units in silent, stalled, capped, provider-error or
// failed, newest first.
func buildAndon(units []Unit) []Andon {
	var out []Andon
	for _, u := range units {
		switch u.RunState {
		case "silent", "stalled", "capped", "provider-error", "failed":
			out = append(out, Andon{Task: u.Task, State: u.RunState, Age: u.LastAge})
		}
	}
	slices.SortStableFunc(out, func(a, b Andon) int {
		if a.Age != b.Age {
			return a.Age - b.Age
		}
		return strings.Compare(a.Task, b.Task)
	})
	return out
}

// buildOutput aggregates the production summary from finished events. The
// first-pass rate uses each task's FIRST verdict from either inspected (pass
// counts as first pass; rework, scrap and escalate do not) or reviewed (pass
// / correct), whichever came first in the log.
func buildOutput(events []Event, now time.Time) Output {
	var o Output
	o.LandedToday = landedToday(events, now)
	finished := map[string]bool{}
	dispatched := map[string]bool{}
	firstVerdict := map[string]string{}
	tokens := 0
	cost := 0.0
	corrections := 0
	for _, e := range events {
		if e.Kind == "finished" {
			finished[e.Task] = true
			if e.Tokens != nil {
				tokens += e.Tokens.Input + e.Tokens.Output + e.Tokens.Reasoning
			}
			cost += e.Cost
		}
		if e.Kind == "reviewed" || e.Kind == "inspected" {
			if _, ok := firstVerdict[e.Task]; !ok {
				firstVerdict[e.Task] = e.Verdict
			}
		}
		if e.Kind == "dispatched" {
			dispatched[e.Task] = true
			if e.Attempt != "" && e.Attempt[0] == 'c' {
				corrections++
			}
		}
	}
	o.Finished = len(finished)
	o.Tokens = tokens
	o.Cost = cost
	if len(firstVerdict) > 0 {
		o.HasReviews = true
		passes := 0
		for _, v := range firstVerdict {
			if v == "pass" {
				passes++
			}
		}
		o.FirstPassRate = float64(passes) / float64(len(firstVerdict))
	} else {
		o.HasReviews = false
		o.FirstPassRate = 0
	}
	if len(dispatched) > 0 {
		o.Rework = float64(corrections) / float64(len(dispatched))
	}
	return o
}

// landedToday counts landed events whose UTC calendar date matches now's.
func landedToday(events []Event, now time.Time) int {
	n := 0
	for _, e := range events {
		if e.Kind != "landed" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			continue
		}
		y1, m1, d1 := t.UTC().Date()
		y2, m2, d2 := now.UTC().Date()
		if y1 == y2 && m1 == m2 && d1 == d2 {
			n++
		}
	}
	return n
}
