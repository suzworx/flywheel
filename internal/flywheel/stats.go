package flywheel

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// StatsTasks counts the tasks by a subset of their derived status (see
// status.go for the full set): total, and how many landed, passed or were
// rejected. LeadImplemented counts the landed tasks whose landing carried the
// lead-implemented flag (issue #198).
type StatsTasks struct {
	Total           int `json:"total"`
	Landed          int `json:"landed"`
	LeadImplemented int `json:"lead_implemented"`
	Passed          int `json:"passed"`
	Rejected        int `json:"rejected"`
}

// StatsReport is the factory's own numbers, computed once over the whole
// event log: it writes nothing, calls no model, and never shells out.
type StatsReport struct {
	Tasks              StatsTasks     `json:"tasks"`
	FirstPassRate      float64        `json:"first_pass_rate"`
	FirstPassCount     int            `json:"-"`
	FirstPassTotal     int            `json:"-"`
	CorrectionsPerTask float64        `json:"corrections_per_task"`
	FinishReasons      map[string]int `json:"finish_reasons"`
	UncleanPer100      float64        `json:"unclean_per_100"`
	MeanAttemptSeconds int            `json:"mean_attempt_seconds"`
	CostPerLandedTask  float64        `json:"cost_per_landed_task"`
	Tokens             Tokens         `json:"tokens"`
	Spend              float64        `json:"spend"`
	Baseline           *StatsBaseline `json:"baseline,omitempty"`
	Review             StatsReview    `json:"review"`
}

// StatsBaseline is the frontier-only comparison: the recorded tokens priced
// at the configured baseline model, and actual spend as a fraction of it.
type StatsBaseline struct {
	Model string  `json:"model"`
	Cost  float64 `json:"cost"`
	Ratio float64 `json:"ratio"` // spend / baseline cost; 0 when the baseline cost is 0
}

// attemptKey identifies one attempt of one task, for pairing its dispatched
// and finished timestamps.
type attemptKey struct {
	task    string
	attempt string
}

// Stats reads the event log and computes StatsReport: the factory's health as
// numbers that trend. An empty log yields zeroed fields, not an error.
func Stats(dir string) (StatsReport, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return StatsReport{}, fmt.Errorf("stats %s: %w", dir, err)
	}

	rep := StatsReport{FinishReasons: map[string]int{}, Review: reviewStats(events)}

	st := Derive(events)
	for _, ts := range st.Tasks {
		rep.Tasks.Total++
		switch ts.Status {
		case "landed":
			rep.Tasks.Landed++
		case "passed":
			rep.Tasks.Passed++
		case "rejected":
			rep.Tasks.Rejected++
		}
	}
	for _, e := range events {
		if e.Kind == "landed" && e.LeadImplemented {
			rep.Tasks.LeadImplemented++
		}
	}

	attemptsRecorded := 0
	tasksWithAttempt := 0
	for _, ts := range st.Tasks {
		if ts.Attempts > 0 {
			attemptsRecorded += ts.Attempts
			tasksWithAttempt++
		}
	}
	if tasksWithAttempt > 0 {
		rep.CorrectionsPerTask = round(float64(attemptsRecorded-tasksWithAttempt)/float64(tasksWithAttempt), 2)
	}

	firstVerdict := map[string]string{}
	for _, e := range sortByTS(events) {
		if e.Kind != "inspected" || e.Task == "" {
			continue
		}
		if _, ok := firstVerdict[e.Task]; ok {
			continue
		}
		firstVerdict[e.Task] = e.Verdict
	}
	for _, v := range firstVerdict {
		rep.FirstPassTotal++
		if v == "pass" {
			rep.FirstPassCount++
		}
	}
	if rep.FirstPassTotal > 0 {
		rep.FirstPassRate = round(float64(rep.FirstPassCount)/float64(rep.FirstPassTotal), 2)
	}

	finishes := 0
	unclean := 0
	dispatchTS := map[attemptKey]string{}
	finishTS := map[attemptKey]string{}
	for _, e := range events {
		if e.Attempt != "" {
			k := attemptKey{task: e.Task, attempt: e.Attempt}
			switch e.Kind {
			case "dispatched":
				dispatchTS[k] = e.TS
			case "finished":
				finishTS[k] = e.TS
			}
		}
		if e.Kind != "finished" {
			continue
		}
		finishes++
		rep.FinishReasons[e.Reason]++
		if e.Reason != "stop" {
			unclean++
		}
	}
	if finishes > 0 {
		rep.UncleanPer100 = round(float64(unclean)/float64(finishes)*100, 2)
	}

	var total float64
	var n int
	for k, dts := range dispatchTS {
		fts, ok := finishTS[k]
		if !ok {
			continue
		}
		dt, derr := time.Parse(time.RFC3339Nano, dts)
		ft, ferr := time.Parse(time.RFC3339Nano, fts)
		if derr != nil || ferr != nil {
			continue
		}
		total += ft.Sub(dt).Seconds()
		n++
	}
	if n > 0 {
		rep.MeanAttemptSeconds = int(math.Round(total / float64(n)))
	}

	cost, err := Cost(dir)
	if err != nil {
		return StatsReport{}, fmt.Errorf("stats %s: %w", dir, err)
	}
	rep.Tokens = cost.Total.Tokens
	rep.Spend = round(cost.Total.Cost, 4)
	if rep.Tasks.Landed > 0 {
		rep.CostPerLandedTask = round(cost.Total.Cost/float64(rep.Tasks.Landed), 4)
	}

	// A missing config yields the defaults (no baseline); a broken one is an
	// error, never a silently absent baseline.
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return StatsReport{}, fmt.Errorf("stats %s: %w", dir, err)
	}
	if cfg.Baseline != nil {
		bc := cfg.Baseline.Cost(cost.Total.Tokens)
		rep.Baseline = &StatsBaseline{Model: cfg.Baseline.Model, Cost: round(bc, 4)}
		if bc > 0 {
			rep.Baseline.Ratio = round(cost.Total.Cost/bc, 4)
		}
	}

	return rep, nil
}

// StatsReview is the review agent's numbers (issue #389).
type StatsReview struct {
	Reviews    int            `json:"reviews"`     // agent review rounds run
	Findings   int            `json:"findings"`    // review findings raised
	BySeverity map[string]int `json:"by_severity"` // findings per severity
	ByCategory map[string]int `json:"by_category"` // findings per category; "uncategorized" when none
	// CleanUnits counts the units whose latest agent review is pass;
	// MedianRoundsToClean is the median of their agent review rounds.
	CleanUnits          int     `json:"clean_units"`
	MedianRoundsToClean float64 `json:"median_rounds_to_clean"`
	// CaughtAfterGates counts the findings raised on a unit whose every gate
	// reading since its latest dispatch had passed: what review caught that
	// the gates missed.
	CaughtAfterGates int `json:"caught_after_gates"`
	Dismissals       int `json:"dismissals"` // findings a lead dismissed
}

// reviewStats folds the event log into StatsReview.
func reviewStats(events []Event) StatsReview {
	rs := StatsReview{BySeverity: map[string]int{}, ByCategory: map[string]int{}}
	rounds := map[string]int{}
	latest := map[string]string{}
	gates := map[string]map[string]bool{} // task → gate → latest reading passed
	workers := map[string]map[string]bool{}
	var order []string
	for _, e := range events {
		switch {
		case e.Kind == "dispatched":
			gates[e.Task] = map[string]bool{}
		case e.Kind == "validated":
			if gates[e.Task] == nil {
				gates[e.Task] = map[string]bool{}
			}
			gates[e.Task][e.Gate] = e.RC != nil && *e.RC == 0
		case e.Kind == "review_finding":
			rs.Findings++
			rs.BySeverity[e.Severity]++
			cat := e.Category
			if cat == "" {
				cat = "uncategorized"
			}
			rs.ByCategory[cat]++
			if g := gates[e.Task]; len(g) > 0 && !slices.Contains(mapValues(g), false) {
				rs.CaughtAfterGates++
			}
		case agentReviewed(e):
			rs.Reviews++
			if rounds[e.Task] == 0 {
				order = append(order, e.Task)
			}
			rounds[e.Task]++
			latest[e.Task] = e.Verdict
		case e.Kind == "finding_response":
			if workers[e.Task] == nil {
				workers[e.Task] = workerSessions(events, e.Task)
			}
			if leadDismissal(e, workers[e.Task]) {
				rs.Dismissals++
			}
		}
	}
	var clean []int
	for _, task := range order {
		if latest[task] == "pass" {
			clean = append(clean, rounds[task])
		}
	}
	rs.CleanUnits = len(clean)
	rs.MedianRoundsToClean = median(clean)
	return rs
}

// mapValues lists m's values in no particular order.
func mapValues(m map[string]bool) []bool {
	out := make([]bool, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// median is the median of xs, 0 when empty.
func median(xs []int) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	if n := len(s); n%2 == 1 {
		return float64(s[n/2])
	}
	return float64(s[len(s)/2-1]+s[len(s)/2]) / 2
}

// round rounds x to n decimal places.
func round(x float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(x*p) / p
}

// sortByTS returns a copy of events sorted by parsed ts ascending; an
// unparseable ts sorts after every parsed one, ties keep the original order.
func sortByTS(events []Event) []Event {
	type tsKey struct {
		Event Event
		Time  time.Time
		Valid bool
	}
	keys := make([]tsKey, len(events))
	for i, e := range events {
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		keys[i] = tsKey{Event: e, Time: t, Valid: err == nil}
	}
	slices.SortStableFunc(keys, func(a, b tsKey) int {
		if a.Valid != b.Valid {
			if a.Valid {
				return -1
			}
			return 1
		}
		if !a.Valid {
			return 0
		}
		if a.Time.Before(b.Time) {
			return -1
		}
		if a.Time.After(b.Time) {
			return 1
		}
		return 0
	})
	out := make([]Event, len(keys))
	for i, k := range keys {
		out[i] = k.Event
	}
	return out
}
