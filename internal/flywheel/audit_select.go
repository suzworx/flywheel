package flywheel

import (
	"fmt"
	"hash/fnv"
)

// AuditSelection is the result of selecting units to audit (issue #61).
type AuditSelection struct {
	Mode  string   `json:"mode"`           // "sample" or "first-article"
	Rate  float64  `json:"rate,omitempty"` // the effective rate, for sample
	Seed  int64    `json:"seed,omitempty"` // for sample
	Tasks []string `json:"tasks"`          // selected, in log order of first appearance; [] never null
}

// AuditCandidates returns the tasks whose latest inspected verdict is pass and
// that have not been audited since, in log order of their first event.
func AuditCandidates(events []Event) []string {
	// Track positions of latest inspected and latest audited for each task
	type taskState struct {
		latestInspectedPos int    // position of latest inspected event
		latestInspectedVer string // verdict of latest inspected event
		latestAuditedPos   int    // position of latest audited event (-1 if none)
	}
	states := make(map[string]taskState)

	for pos, e := range events {
		if e.Task == "" {
			continue
		}
		state, ok := states[e.Task]
		if !ok {
			state = taskState{latestInspectedPos: -1, latestAuditedPos: -1}
		}
		if e.Kind == "inspected" {
			state.latestInspectedPos = pos
			state.latestInspectedVer = e.Verdict
		} else if e.Kind == "audited" {
			state.latestAuditedPos = pos
		}
		states[e.Task] = state
	}

	// Collect candidates in log order of first appearance
	seen := make(map[string]bool)
	var candidates []string
	for _, e := range events {
		if e.Task == "" || seen[e.Task] {
			continue
		}
		seen[e.Task] = true
		state := states[e.Task]
		// A candidate's latest inspected must be pass, and no audited event after it
		if state.latestInspectedVer == "pass" && state.latestAuditedPos < state.latestInspectedPos {
			candidates = append(candidates, e.Task)
		}
	}

	return candidates
}

// SelectFirstArticles returns, for each line (adapter/model) with no audited
// task yet, the candidate of that line dispatched first.
func SelectFirstArticles(events []Event) AuditSelection {
	candidates := AuditCandidates(events)
	// A unit's line is the adapter/model of its latest dispatch before its
	// latest inspected pass: a unit retried on another model belongs to the
	// line that built the passing attempt.
	lastPass := map[string]int{}
	for pos, e := range events {
		if e.Kind == "inspected" && e.Verdict == "pass" {
			lastPass[e.Task] = pos
		}
	}
	type lineInfo struct {
		pos  int
		task string
	}
	unitLine := map[string]lineInfo{} // task -> its line's dispatch
	for pos, e := range events {
		if e.Kind != "dispatched" || e.Task == "" {
			continue
		}
		if p, ok := lastPass[e.Task]; ok && pos > p {
			continue
		}
		unitLine[e.Task] = lineInfo{pos: pos, task: e.Adapter + "/" + e.Model}
	}
	// linesWithAudits: every line one of whose units has been audited.
	linesWithAudits := map[string]bool{}
	for _, e := range events {
		if e.Kind == "audited" {
			if li, ok := unitLine[e.Task]; ok && li.task != "/" {
				linesWithAudits[li.task] = true
			}
		}
	}
	// lineFirst: per line, the candidate whose line dispatch comes first.
	lineFirst := map[string]lineInfo{} // line -> {pos, task}
	for _, c := range candidates {
		li, ok := unitLine[c]
		if !ok || li.task == "/" {
			continue
		}
		if cur, seen := lineFirst[li.task]; !seen || li.pos < cur.pos {
			lineFirst[li.task] = lineInfo{pos: li.pos, task: c}
		}
	}

	// Select first article of each line that has no audits
	selected := make(map[string]bool)
	for line, info := range lineFirst {
		if !linesWithAudits[line] {
			selected[info.task] = true
		}
	}

	// Return in log order of first appearance
	var tasks []string
	seen := make(map[string]bool)
	for _, e := range events {
		if e.Task != "" && !seen[e.Task] && selected[e.Task] {
			tasks = append(tasks, e.Task)
			seen[e.Task] = true
		}
	}

	if tasks == nil {
		tasks = []string{}
	}

	return AuditSelection{
		Mode:  "first-article",
		Tasks: tasks,
	}
}

// EffectiveAuditRate adjusts the base sampling rate by the last 10 audits.
func EffectiveAuditRate(events []Event, rate float64) float64 {
	// Find the last 10 audited events
	var audited []Event
	for _, e := range events {
		if e.Kind == "audited" {
			audited = append(audited, e)
		}
	}

	if len(audited) == 0 {
		return rate
	}

	// Get the last 10
	start := len(audited) - 10
	if start < 0 {
		start = 0
	}
	last10 := audited[start:]

	// Count nonconformances
	nonconformances := 0
	for _, e := range last10 {
		if e.Verdict == "nonconformance" {
			nonconformances++
		}
	}

	if nonconformances > 0 {
		// r * (1 + n), capped at 1
		effective := rate * (1.0 + float64(nonconformances))
		if effective > 1.0 {
			effective = 1.0
		}
		return effective
	}

	if len(last10) == 10 {
		// All 10 are conforming (no nonconformances)
		return rate / 2.0
	}

	return rate
}

// SelectSample returns the candidates sampled at the effective rate with seed.
func SelectSample(events []Event, rate float64, seed int64) AuditSelection {
	candidates := AuditCandidates(events)
	effectiveRate := EffectiveAuditRate(events, rate)

	// Hash each candidate and check if sampled
	var selected []string
	for _, task := range candidates {
		h := fnv.New32a()
		fmt.Fprintf(h, "%d/%s", seed, task)
		hash := h.Sum32()
		threshold := uint32(effectiveRate * 10000.0)
		if hash%10000 < threshold {
			selected = append(selected, task)
		}
	}

	if selected == nil {
		selected = []string{}
	}

	return AuditSelection{
		Mode:  "sample",
		Rate:  effectiveRate,
		Seed:  seed,
		Tasks: selected,
	}
}
