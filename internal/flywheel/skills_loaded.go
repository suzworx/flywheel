package flywheel

import "strings"

// skillsNotLoaded returns the skills the task's latest dispatched attempt
// named that no finished event of the unit reports loaded (issue #695): the
// worker's own stream is the evidence. Only events after the task's latest
// planned event count. checkable is false when no dispatched event named
// skills, when its adapter is not claude (loading is not observable there)
// or when no finished event exists yet (still running).
func skillsNotLoaded(events []Event, task string) (missing []string, checkable bool) {
	_, missing, checkable = skillsCheck(events, task)
	return missing, checkable
}

// skillsCheck is skillsNotLoaded that also returns the named skills.
func skillsCheck(events []Event, task string) (named, missing []string, checkable bool) {
	start := 0
	for i, e := range events {
		if e.Task == task && e.Kind == "planned" {
			start = i + 1
		}
	}
	adapter := ""
	dispatched := false
	for _, e := range events[start:] {
		if e.Task == task && e.Kind == "dispatched" {
			named, adapter, dispatched = e.Skills, e.Adapter, true
		}
	}
	if !dispatched || len(named) == 0 || adapter != "claude" {
		return nil, nil, false
	}
	var loaded []string
	finished := false
	for _, e := range events[start:] {
		if e.Task == task && e.Kind == "finished" {
			finished = true
			loaded = append(loaded, e.SkillsLoaded...)
		}
	}
	if !finished {
		return nil, nil, false
	}
	for _, n := range named {
		if !skillLoaded(n, loaded) {
			missing = append(missing, n)
		}
	}
	return named, missing, true
}

// skillLoaded reports whether name is among loaded: an exact match, a plain
// name loaded as "<plugin>:<name>", or a "<plugin>:<name>" loaded as the
// plain name. Case-sensitive.
func skillLoaded(name string, loaded []string) bool {
	for _, l := range loaded {
		if l == name {
			return true
		}
		if !strings.Contains(name, ":") {
			if i := strings.LastIndex(l, ":"); i >= 0 && l[i+1:] == name {
				return true
			}
		} else if i := strings.LastIndex(name, ":"); name[i+1:] == l {
			return true
		}
	}
	return false
}

// skillsNotLoadedFix is the correction a skills-not-loaded reading prints.
func skillsNotLoadedFix(task string, missing []string) string {
	return strings.Join(missing, ", ") + ": the worker never loaded these skills; dispatch a correction with flywheel run " + task + " --delta <file> asking it to load them"
}
