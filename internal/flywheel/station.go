package flywheel

// Stations are the stations a unit passes on its line, in order (issue #69
// follow-up): work is pulled from queue to landed, and blocked is the
// siding a stopped unit sits on.
var Stations = []string{"queue", "build", "measure", "inspect", "land", "landed", "blocked", "scrap", "lost"}

// StationOf is the station a unit's derived status and finish reason put it
// at — the twin of stageOf, which it never replaces:
//
//	planned → queue; dispatched, running, needs-correction → build;
//	finished (any reason) → measure; passed → land; landed → landed;
//	blocked → blocked; rejected, withdrawn → scrap; lost → lost; anything
//	else → queue.
func StationOf(status, reason string) string {
	switch status {
	case "planned":
		return "queue"
	case "dispatched", "running", "needs-correction":
		return "build"
	case "finished":
		return "measure"
	case "passed":
		return "land"
	case "landed":
		return "landed"
	case "blocked":
		return "blocked"
	case "rejected", "withdrawn":
		return "scrap"
	case "lost":
		return "lost"
	}
	return "queue"
}

// StationFor refines StationOf for one task: a finished unit whose readings
// are complete (reconcile.go's inspectionReady) is waiting at inspect, not
// at measure.
func StationFor(ts TaskState, events []Event) string {
	station := StationOf(ts.Status, ts.Reason)
	if station == "measure" && ts.Attempt != "" && inspectionReady(events, ts.ID, ts.Attempt) {
		return "inspect"
	}
	return station
}

// InWIP reports whether a station holds work in flight. queue does not (a
// backlog is not started work — counting it would let planning saturate a
// line), landed, scrap and lost do not (they left the line), and blocked
// does: it is inventory holding a slot.
func InWIP(station string) bool {
	switch station {
	case "queue", "landed", "scrap", "lost":
		return false
	case "build", "measure", "inspect", "land", "blocked":
		return true
	}
	return false
}

// LineOf is the product line of a task: the line recorded on the dispatch
// of its CURRENT attempt (the one Derive settles on, not whichever dispatch
// happens to sit last in the slice — a merged log is not in state-machine
// order), else the line its brief header resolves to, for a unit that was
// planned but never dispatched.
//
// The header comes from the planned or amended event itself (issue #259
// records it), so this stays a derivation of the ledger. Only a legacy
// ledger whose events carry no header falls back to reading the brief file
// under dir, and then a missing file simply means no line.
func LineOf(cfg Config, dir string, events []Event, task string) string {
	var ts TaskState
	for _, t := range Derive(events).Tasks {
		if t.ID == task {
			ts = t
			break
		}
	}
	if ts.ID == "" {
		return ""
	}
	if attempt := currentAttempt(ts, events); attempt != "" {
		if line := lineFor(events, task, attempt); line != "" {
			return line
		}
	}
	header, ok := plannedHeader(events, task)
	if !ok {
		h, _, err := AttemptBrief(dir, events, task)
		if err != nil {
			return ""
		}
		header = h
	}
	resolved, ok, _ := cfg.LineFor(header)
	if !ok {
		return ""
	}
	return resolved.Name
}

// plannedHeader returns the brief header the task's latest planned or
// amended event recorded, and false when neither carries one (a ledger
// written before issue #259).
func plannedHeader(events []Event, task string) (BriefHeader, bool) {
	var h BriefHeader
	found := false
	for _, e := range events {
		if e.Task != task || e.Header == nil {
			continue
		}
		if e.Kind == "planned" || e.Kind == "amended" {
			h, found = *e.Header, true
		}
	}
	return h, found
}
