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
//	blocked → blocked; rejected → scrap; lost → lost; anything else → queue.
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
	case "rejected":
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

// LineOf is the product line of a task: the line its latest dispatched
// event recorded (lineFor), else the line its brief resolves to
// (AttemptBrief + Config.LineFor) for a unit that was planned but never
// dispatched, else "". dir is the factory directory the brief paths are
// relative to.
func LineOf(cfg Config, dir string, events []Event, task string) string {
	// Find the latest dispatched attempt for this task
	var latestAttempt string
	for _, e := range events {
		if e.Kind == "dispatched" && e.Task == task && e.Attempt != "" {
			latestAttempt = e.Attempt
		}
	}
	if latestAttempt != "" {
		line := lineFor(events, task, latestAttempt)
		if line != "" {
			return line
		}
	}
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		return ""
	}
	resolved, ok, _ := cfg.LineFor(header)
	if !ok {
		return ""
	}
	return resolved.Name
}
