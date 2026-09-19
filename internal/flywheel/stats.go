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

	rep := StatsReport{FinishReasons: map[string]int{}}

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
