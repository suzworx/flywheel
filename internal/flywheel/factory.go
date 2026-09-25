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
	Dir          string    // repo directory the floor watches
	Refreshed    time.Time // the clock the floor was drawn at
	Lines        []FloorLine
	ProductLines []ProductLine
	Staffing     Staffing
	Units        []Unit
	Andon        []Andon
	Output       Output
}

// FloorLine is one config worker: a station on the floor. Busy is the number of
// its in-flight units grouped by model.
type FloorLine struct {
	Name        string
	Adapter     string
	Model       string
	MaxParallel int
	Busy        int
}

// Staffing is the floor's roles (issues #69, #355): Lead keeps the rendered
// line the floor has always shown, and Roles carries every role the config
// names or the floor registered.
type Staffing struct {
	Lead  string
	Roles []FloorRole
}

// FloorRole is one role on the floor: what the config asks for and what the
// floor registered. Mismatch is true when both name a session and they
// differ.
type FloorRole struct {
	Name       string // lead, inspector, auditor
	Configured string // the configured role as one line, for text output; "" when unconfigured
	// The configured parts, kept apart from Configured: a display string
	// cannot be split back into columns (#357 review).
	ConfAdapter string
	ConfModel   string
	ConfSession string
	Session     string // the latest staffed event's session; "" when never staffed
	Model       string // that event's model
	Mismatch    bool
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
	LastAge  int       // seconds since the unit's last event
	RunState string    // silent, running, exploring, long-step, stalled, no-writes, blocked, capped, provider-error, rate-limited, abandoned-job, failed, failed-dirty, stacked, done
	Peak     int       // largest single-step reasoning figure, from the latest finished event; 0 when none
	Line     string    // the product line from the latest dispatched event (issue #69); "" when none
	Station  string    // where the unit stands on its line (issue #69 follow-up)
	ResetAt  time.Time // a rate-limited attempt's parsed reset (issue #383); zero when none
	Workdir  string    // the latest dispatched event's workdir (issue #394); "" in the main checkout
	Base     string    // that event's base commit, first 7 characters; "" when none
	Open     int       // open blocking review findings (issue #389); 0 when none or never reviewed
}

// worktreeFor returns the workdir and the 7-character base commit the task's
// latest dispatched event for attempt recorded, scanning in order so the last
// one wins, the same way lineFor does (issue #394).
func worktreeFor(events []Event, task, attempt string) (workdir, base string) {
	for _, e := range events {
		if e.Kind == "dispatched" && e.Task == task && e.Attempt == attempt {
			workdir, base = e.Workdir, e.Base
		}
	}
	if len(base) > 7 {
		base = base[:7]
	}
	return workdir, base
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

// lineFor returns the product line the task's latest dispatched event for
// attempt recorded, scanning in order so the last one wins (issue #69).
func lineFor(events []Event, task, attempt string) string {
	line := ""
	for _, e := range events {
		if e.Kind == "dispatched" && e.Task == task && e.Attempt == attempt {
			line = e.Line
		}
	}
	return line
}

// wroteFor returns the task's latest finished event's wrote paths for its
// current attempt, scanning the accumulated event log in order so the last
// matching event wins, the same way peakReasoningFor does (issue #163).
func wroteFor(events []Event, task, attempt string) []string {
	var wrote []string
	for _, e := range events {
		if e.Kind == "finished" && e.Task == task && e.Attempt == attempt {
			wrote = e.Wrote
		}
	}
	return wrote
}

// hasNoPlan reports whether a no-plan event was recorded for this task's
// current attempt: the run reached step 20 with no PLAN text seen yet
// (issue #150), the signal classifyRun's no-writes state consumes (#176).
func hasNoPlan(events []Event, task, attempt string) bool {
	for _, e := range events {
		if e.Kind == "no-plan" && e.Task == task && e.Attempt == attempt {
			return true
		}
	}
	return false
}

// stopStateFor returns the run state a clean stop of this task's attempt
// shows instead of done (issue #364): "blocked" when the run recorded a
// permission-denied signal, else "no-writes" when the attempt's finished
// event has reason stop and wrote nothing (a floor state only, never a
// signal: some units legitimately write nothing), else "". Both apply only
// while the task's status is finished, awaiting judgement (issue #364): a
// passed, rejected or landed unit shows done, and an old log's clean finishes,
// which carry no wrote field, never read as no-writes.
func stopStateFor(events []Event, task, attempt, status string) string {
	if status != "finished" {
		return ""
	}
	stopped := false
	for _, e := range events {
		if e.Task != task || e.Attempt != attempt {
			continue
		}
		if e.Kind == "signal" && e.Signal == "permission-denied" {
			return "blocked"
		}
		if e.Kind == "finished" {
			stopped = e.Reason == "stop"
		}
	}
	if stopped && len(wroteFor(events, task, attempt)) == 0 {
		return "no-writes"
	}
	return ""
}

// ProductLine is one configured product line on the floor (issue #69): who
// builds it and how many of the floor's units are on it.
type ProductLine struct {
	Name     string
	Worker   string
	Owns     []string
	Units    int            // units whose Line is Name
	Building int            // of those, Stage "building"
	Landed   int            // of those, Stage "landed"
	Stations map[string]int // units per station, by Stations' names
	WIP      int            // units at a station InWIP reports
	Limit    int            // lines[].wip, 0 when unlimited
}

// buildProductLines returns one ProductLine per cfg.Lines entry, in config
// order, counting units by Line; when any unit has a Line that is empty or
// names no configured line and cfg.Lines is not empty, a last entry named
// "(none)" (Worker "") counts those units. No configured lines: nil.
func buildProductLines(cfg Config, units []Unit) []ProductLine {
	if len(cfg.Lines) == 0 {
		return nil
	}
	lineMap := map[string]*ProductLine{}
	for _, cl := range cfg.Lines {
		lineMap[cl.Name] = &ProductLine{Name: cl.Name, Worker: cl.Worker, Owns: cl.Owns, Limit: cl.WIP, Stations: map[string]int{}}
	}
	noneEntry := &ProductLine{Name: "(none)", Worker: ""}
	for _, u := range units {
		pl, ok := lineMap[u.Line]
		if ok {
			pl.Units++
			if u.Stage == "building" {
				pl.Building++
			}
			if u.Stage == "landed" {
				pl.Landed++
			}
			pl.Stations[u.Station]++
			if InWIP(u.Station) {
				pl.WIP++
			}
		} else {
			noneEntry.Units++
			if u.Stage == "building" {
				noneEntry.Building++
			}
			if u.Stage == "landed" {
				noneEntry.Landed++
			}
		}
	}
	var result []ProductLine
	for _, cl := range cfg.Lines {
		result = append(result, *lineMap[cl.Name])
	}
	if noneEntry.Units > 0 {
		result = append(result, *noneEntry)
	}
	return result
}

// Andon is one stopped-line condition: a unit in silent, stalled, no-writes,
// capped, provider-error, failed or failed-dirty, newest first.
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
	case "running", "exploring", "long-step", "silent", "stalled", "no-writes":
		return true
	}
	return false
}

// classifyRun maps a run's observed signals to a run state. done is set when
// the attempt's finished event exists; age is the seconds since the run file
// last grew; stallTimeout is the default worker's configured stall_timeout in
// seconds (issue #85), the same threshold flywheel run stops a live run at:
// stalled is age > stallTimeout, long-step is age > stallTimeout/2. Once
// done, lastReason (the caller overrides it with the attempt's finished-event
// reason when one is recorded, since that is always a complete line while the
// run file's own tail may not be) decides the run state outright: "length" is
// capped, "error" is provider-error, "stop" or empty is a clean done, and any
// other reason (start-failed, silent, stalled, ...) is failed. Live (not
// done) classification still checks hasError and lastReason=="length" before
// the other live signals, since a capped or provider-error run in progress
// has not recorded a finished event yet. noPlan is whether the task's current
// attempt has a recorded no-plan event (issue #150); a live run with 20+
// steps, zero edits and noPlan classifies no-writes, checked right after the
// provider-error and capped signals and before silent/stalled/long-step, so a
// run that is also genuinely stalled still needs the stall threshold itself
// to ever report stalled, and every other live state keeps its own meaning
// unchanged (issue #176). wrote is whether the done attempt's finished event
// lists any written files; when done and lastReason is neither "" nor "stop",
// a true wrote turns what would be "capped" (lastReason "length") or "failed"
// (every other unclean reason) into "failed-dirty" instead — a failed attempt
// that left files behind, needing a human decision the andon otherwise treats
// the same as a clean failure (issue #163). stopState replaces a clean done
// when non-empty: the caller passes "blocked" (a permission-denied signal) or
// "no-writes" (a stop finish that wrote nothing) for the attempt (issue #364).
func classifyRun(done bool, steps int, files int, edits int, hasError bool, lastReason string, size int64, age int, stallTimeout int, noPlan bool, wrote bool, stopState string) string {
	if done {
		switch lastReason {
		case "length":
			if wrote {
				return "failed-dirty"
			}
			return "capped"
		case "error":
			return "provider-error"
		case "rate-limited":
			// Cut off by a rate limit, files or not: the resume continues
			// the same session (issue #380).
			return "rate-limited"
		case "abandoned-job":
			// Ended its session while a background job it started ran,
			// files or not: the resume re-runs the job (issue #390).
			return "abandoned-job"
		case "", "stop":
			if stopState != "" {
				return stopState
			}
			return "done"
		default:
			if wrote {
				return "failed-dirty"
			}
			return "failed"
		}
	}
	if hasError {
		return "provider-error"
	}
	if lastReason == "length" {
		return "capped"
	}
	if steps >= 20 && edits == 0 && noPlan {
		return "no-writes"
	}
	if size == 0 && age > 60 {
		return "silent"
	}
	if age > stallTimeout {
		return "stalled"
	}
	if age > stallTimeout/2 {
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
	log         *logReader
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
		log:       newLogReader(),
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
	stallTimeout := int(cfg.DefaultWorker().stallTimeoutDuration().Seconds())
	units, byModel, uerr := buildUnits(w, st, now, dir, stallTimeout, cfg)
	if uerr != nil {
		return Floor{}, uerr
	}
	fl := Floor{Dir: dir, Refreshed: now}
	fl.Lines = buildLines(cfg, byModel)
	fl.ProductLines = buildProductLines(cfg, units)
	fl.Staffing = buildStaffing(cfg, w.events)
	fl.Units = units
	fl.Andon = buildAndon(units, fl.Staffing.Roles, pausedAndon(w.events, now, cfg.Limits.RateLimitPauseThreshold()))
	fl.Output = buildOutput(w.events, now)
	return fl, nil
}

// readEvents folds only the bytes appended since the last refresh into the
// watcher's accumulated event list.
func readEvents(dir string, w *Watcher) error {
	changed, err := w.log.refresh(dir)
	if err != nil {
		return err
	}
	w.EventsBytes = w.log.Bytes
	if changed {
		w.events = w.log.merged()
	}
	return nil
}

// runAdapter returns the adapter that produced task's attempt: the Adapter
// of its latest dispatched event for that attempt, resolved with AdapterFor;
// opencode when the event names none (a legacy ledger) or names an unknown one.
func runAdapter(events []Event, task, attempt string) Adapter {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Task == task && e.Attempt == attempt && e.Kind == "dispatched" {
			adap, err := AdapterFor(e.Adapter)
			if err != nil {
				adap, _ = AdapterFor("opencode")
			}
			return adap
		}
	}
	adap, _ := AdapterFor("opencode")
	return adap
}

// readRun folds only the appended bytes of one run file into the watcher's
// per-run accumulators and returns its size and mtime. A missing run file means
// a zero-byte run at the epoch.
func readRun(dir string, w *Watcher, rel string, adap Adapter) (size int64, mtime time.Time, err error) {
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
		sc := bufio.NewScanner(bytes.NewReader(b[:idx+1]))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			obs, ok := adap.Parse(sc.Bytes())
			if !ok {
				continue
			}
			// Steps are turn-ending lines, as run.go counts them; the reason
			// comes from any step line (codex's turn.failed ends no turn).
			if obs.EndsTurn {
				w.runSteps[rel] = w.runSteps[rel] + 1
			}
			if obs.Kind == "step" && obs.Reason != "" {
				w.runReason[rel] = obs.Reason
			}
			switch obs.Kind {
			case "tool":
				if obs.Tool == "read" {
					for _, p := range obsPaths(obs) {
						if p != "" {
							if _, ok := w.runFiles[rel]; !ok {
								w.runFiles[rel] = map[string]bool{}
							}
							w.runFiles[rel][p] = true
						}
					}
				}
				if obs.Tool == "edit" || obs.Tool == "write" || obs.Tool == "delete" {
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
// for its run state, and tallies in-flight units by model. stallTimeout is
// the default worker's configured stall_timeout in seconds, passed through to
// classifyRun (issue #85).
func buildUnits(w *Watcher, st State, now time.Time, dir string, stallTimeout int, cfg Config) ([]Unit, map[string]int, error) {
	var units []Unit
	byModel := map[string]int{}
	for _, t := range st.Tasks {
		u := Unit{
			Task: t.ID, Stage: stageOf(t.Status, t.Reason), Attempt: t.Attempt,
			Session: shortSession(t.Session), Model: t.Model,
			Steps: 0, LastAge: ageOf(t.UpdatedAt, now), RunState: "waiting",
		}
		// A finished attempt never occupies a busy slot, even when its clean
		// stop shows no-writes, which is also a live state (issue #364).
		finished := false
		if t.Attempt != "" {
			rel := ".flywheel/runs/" + t.ID + "." + t.Attempt + ".jsonl"
			size, mtime, rerr := readRun(dir, w, rel, runAdapter(w.events, t.ID, t.Attempt))
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
			noPlan := hasNoPlan(w.events, t.ID, t.Attempt)
			wrote := done && len(wroteFor(w.events, t.ID, t.Attempt)) > 0
			stopState := ""
			if done {
				stopState = stopStateFor(w.events, t.ID, t.Attempt, t.Status)
			}
			u.RunState = classifyRun(done, w.runSteps[rel], len(w.runFiles[rel]), w.runEdits[rel], w.runErr[rel], reason, size, age, stallTimeout, noPlan, wrote, stopState)
			if u.RunState == "rate-limited" {
				u.ResetAt = resetAtFor(w.events, t.ID, t.Attempt)
			}
			finished = done
			u.Steps = w.runSteps[rel]
			u.Peak = peakReasoningFor(w.events, t.ID, t.Attempt)
			u.Line = lineFor(w.events, t.ID, t.Attempt)
			u.Workdir, u.Base = worktreeFor(w.events, t.ID, t.Attempt)
			// stacked (issue #414): a done, unlanded unit in its own task
			// worktree whose base landed as a squash. Computed only there,
			// since it costs a few git reads, and never over a live state.
			if done && t.Status != "landed" && u.Workdir != "" && inTaskWorktree(dir, u.Workdir, t.ID) {
				if _, _, _, ok := SquashedBase(dir, w.events, t.ID); ok {
					u.RunState = "stacked"
				}
			}
		}
		if u.Line == "" {
			// A unit planned but never dispatched still belongs to the line
			// its brief resolves to.
			u.Line = LineOf(cfg, dir, w.events, t.ID)
		}
		u.Station = StationFor(t, w.events)
		// An agent review is inspection work (issue #389): a unit whose latest
		// event is one stands at inspect — not back at measure after a pass, nor
		// at build after a correct verdict until a correction is dispatched.
		if (u.Station == "measure" || u.Station == "build") && latestIsAgentReview(w.events, t.ID) {
			u.Station = "inspect"
		}
		u.Open = len(openBlockingIDs(w.events, t.ID))
		if !finished && liveRun(u.RunState) {
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

// buildLines turns each config worker into a floor line, with busy = in-flight units
// on the worker's model.
func buildLines(cfg Config, byModel map[string]int) []FloorLine {
	var lines []FloorLine
	for _, w := range cfg.Workers {
		lines = append(lines, FloorLine{Name: w.Name, Adapter: w.Adapter, Model: w.Model, MaxParallel: w.MaxParallel, Busy: byModel[w.Model]})
	}
	return lines
}

// buildStaffing reports the recorded factory roles: the lead line for backward
// compatibility, and a Roles list for each role the config names or the floor
// registered, with mismatch detection.
func buildStaffing(cfg Config, events []Event) Staffing {
	staffed := map[string]staffRole{}
	for _, e := range events {
		if e.Kind == "staffed" {
			staffed[e.Persona] = staffRole{Session: e.Session, Model: e.Model}
		}
	}
	r, ok := staffed["lead"]
	if !ok {
		r = staffRole{}
	}
	line := r.Session
	if r.Model != "" {
		line = line + " (" + r.Model + ")"
	}
	if line == "" {
		line = "not registered"
	}

	s := Staffing{Lead: line}
	if cfg.Staffing == nil {
		return s
	}

	for _, role := range cfg.Staffing.roles() {
		if role.Cfg == nil || (role.Cfg.Adapter == "" && role.Cfg.Model == "" && role.Cfg.Session == "") {
			if fl, ok := staffed[role.Name]; ok {
				s.Roles = append(s.Roles, FloorRole{
					Name: role.Name, Session: fl.Session, Model: fl.Model, Mismatch: false,
				})
			}
			continue
		}
		floor := staffed[role.Name]
		mismatch := role.Cfg.Session != "" && floor.Session != "" && floor.Session != role.Cfg.Session
		s.Roles = append(s.Roles, FloorRole{
			Name: role.Name, Configured: RoleSummary(role.Cfg),
			ConfAdapter: role.Cfg.Adapter, ConfModel: role.Cfg.Model, ConfSession: role.Cfg.Session,
			Session: floor.Session, Model: floor.Model, Mismatch: mismatch,
		})
	}
	return s
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

// buildAndon lists the units in silent, stalled, no-writes, blocked, capped,
// provider-error, rate-limited, abandoned-job, failed, failed-dirty or stacked,
// newest first, and adds
// andon entries for mismatching roles and the paused entries (pausedAndon).
func buildAndon(units []Unit, roles []FloorRole, paused []Andon) []Andon {
	var out []Andon
	for _, u := range units {
		switch u.RunState {
		case "silent", "stalled", "no-writes", "blocked", "capped", "provider-error", "rate-limited", "abandoned-job", "failed", "failed-dirty", "stacked":
			out = append(out, Andon{Task: u.Task, State: u.RunState, Age: u.LastAge})
		}
		if u.Open > 0 {
			out = append(out, Andon{Task: u.Task, State: fmt.Sprintf("review-open (%d)", u.Open), Age: u.LastAge})
		}
	}
	for _, r := range roles {
		if r.Mismatch {
			out = append(out, Andon{Task: "staffing/" + r.Name, State: "mismatch", Age: 0})
		}
	}
	out = append(out, paused...)
	slices.SortStableFunc(out, func(a, b Andon) int {
		if a.Age != b.Age {
			return a.Age - b.Age
		}
		return strings.Compare(a.Task, b.Task)
	})
	return out
}

// latestIsAgentReview reports whether the task's latest event is a reviewed
// event written by the review agent (issue #389).
func latestIsAgentReview(events []Event, task string) bool {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Task == task {
			return agentReviewed(events[i])
		}
	}
	return false
}

// pausedAndon is one andon entry per model a rate limit pauses at now (issue
// #383): task model/<model>, state "paused until HH:MM" in the viewer's zone,
// with " (96% used)" when the utilization threshold is the cause (issue #417).
func pausedAndon(events []Event, now time.Time, pauseAt float64) []Andon {
	models, pauses := pausedModels(events, now, pauseAt)
	var out []Andon
	for _, m := range models {
		p := pauses[m]
		state := "paused until " + p.Until.Local().Format("15:04")
		if p.Utilization > 0 {
			state += fmt.Sprintf(" (%.0f%% used)", p.Utilization*100)
		}
		out = append(out, Andon{Task: "model/" + m, State: state, Age: 0})
	}
	return out
}

// resetAtFor returns the reset_at of the task's latest finished event for
// attempt, or the zero time when it has none (issue #383).
func resetAtFor(events []Event, task, attempt string) time.Time {
	var at time.Time
	for _, e := range events {
		if e.Kind == "finished" && e.Task == task && e.Attempt == attempt {
			at, _ = time.Parse(time.RFC3339, e.ResetAt)
		}
	}
	return at
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
