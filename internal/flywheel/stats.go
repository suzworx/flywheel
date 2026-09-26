package flywheel

import (
	"fmt"
	"math"
	"slices"
	"strings"
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
	// ByModel is the per-model scoreboard (issue #473), filled only when
	// StatsOptions.ByModel asks for it.
	ByModel []StatsModel `json:"by_model,omitempty"`
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
	return StatsWith(dir, StatsOptions{})
}

// StatsOptions selects the optional parts of StatsReport.
type StatsOptions struct {
	ByModel bool // fill StatsReport.ByModel (flywheel stats --by model)
}

// StatsWith is Stats with the optional parts opts selects.
func StatsWith(dir string, opts StatsOptions) (StatsReport, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return StatsReport{}, fmt.Errorf("stats %s: %w", dir, err)
	}

	rep := StatsReport{FinishReasons: map[string]int{}, Review: reviewStats(events)}
	if opts.ByModel {
		rep.ByModel = modelStats(events)
	}

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
	// ByPersona and ByLevel break the review down per reviewer persona and
	// per review level (issue #420).
	ByPersona []StatsPersona `json:"by_persona,omitempty"`
	ByLevel   []StatsLevel   `json:"by_level,omitempty"`
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
	rs.ByPersona, rs.ByLevel = personaStats(events)
	return rs
}

// StatsPersona is one reviewer persona's numbers (issue #420): the panel
// dimensions at level unit, the integration persona at level group, and the
// general reviewer (persona general) for a review without a dimension.
type StatsPersona struct {
	Persona    string         `json:"persona"`
	Level      string         `json:"level"`       // unit or group
	Reviews    int            `json:"reviews"`     // review rounds it ran
	Findings   int            `json:"findings"`    // findings it raised
	BySeverity map[string]int `json:"by_severity"` // those findings per severity
	Fixed      int            `json:"fixed"`       // findings the worker last answered fixed
	Disputed   int            `json:"disputed"`    // findings the worker last disputed
	Dismissed  int            `json:"dismissed"`   // findings a lead dismissed
}

// StatsLevel is one review level's numbers (issue #420): unit (the panel and
// the general reviewer), group (integration reviews) and release (release
// audits, whose findings are its failed checks).
type StatsLevel struct {
	Level    string `json:"level"`
	Rounds   int    `json:"rounds"`   // reviews, group reviews or release audits
	NotPass  int    `json:"not_pass"` // of those, the ones whose verdict was not pass
	Findings int    `json:"findings"`
	Blocking int    `json:"blocking"` // blocker or major; every failed release check
}

// findingPersona is the persona that raised a review_finding: integration, a
// panel dimension (its id is <task>-r<n>-<dimension>-<i>), else general — a
// general reviewer's finding may carry a dimension's name as its category.
func findingPersona(e Event) string {
	if e.Category == IntegrationPersona {
		return e.Category
	}
	if personaKnown(e.Category) && strings.Contains(e.Finding, "-"+e.Category+"-") {
		return e.Category
	}
	return "general"
}

// personaStats folds the event log into per-persona and per-level rows,
// sorted by level (unit, group) then persona; levels with no round and no
// finding are left out.
func personaStats(events []Event) ([]StatsPersona, []StatsLevel) {
	rows := map[string]*StatsPersona{}
	row := func(persona string) *StatsPersona {
		if rows[persona] == nil {
			level := "unit"
			if persona == IntegrationPersona {
				level = "group"
			}
			rows[persona] = &StatsPersona{Persona: persona, Level: level, BySeverity: map[string]int{}}
		}
		return rows[persona]
	}
	levels := map[string]*StatsLevel{"unit": {Level: "unit"}, "group": {Level: "group"}, "release": {Level: "release"}}
	raised := map[string]string{} // task \x00 finding id → persona
	for _, e := range events {
		switch {
		case e.Kind == "review_finding":
			p := row(findingPersona(e))
			p.Findings++
			p.BySeverity[e.Severity]++
			raised[e.Task+"\x00"+e.Finding] = p.Persona
			l := levels[p.Level]
			l.Findings++
			if blockingFinding(e) {
				l.Blocking++
			}
		case agentReviewed(e):
			dim := reviewDimension(e)
			if dim == "" {
				dim = "general"
			}
			row(dim).Reviews++
			levels["unit"].Rounds++
			if e.Verdict != "pass" {
				levels["unit"].NotPass++
			}
		case e.Kind == "group_reviewed":
			row(IntegrationPersona).Reviews++
			levels["group"].Rounds++
			if e.Verdict != "pass" {
				levels["group"].NotPass++
			}
		case e.Kind == "release_audited":
			l := levels["release"]
			l.Rounds++
			if e.Verdict != "pass" {
				l.NotPass++
			}
			for _, c := range e.Checks {
				if strings.HasSuffix(c, "=fail") {
					l.Findings++
					l.Blocking++
				}
			}
		}
	}
	answer := map[string]string{} // task \x00 finding id → dismissed, fixed or disputed
	workers := map[string]map[string]bool{}
	for _, e := range events {
		k := e.Task + "\x00" + e.Finding
		if e.Kind != "finding_response" || raised[k] == "" || answer[k] == "dismissed" {
			continue
		}
		if workers[e.Task] == nil {
			workers[e.Task] = workerSessions(events, e.Task)
		}
		if leadDismissal(e, workers[e.Task]) {
			answer[k] = "dismissed"
		} else {
			answer[k] = e.Verdict
		}
	}
	for k, a := range answer {
		p := rows[raised[k]]
		switch a {
		case "dismissed":
			p.Dismissed++
		case "fixed":
			p.Fixed++
		case "disputed":
			p.Disputed++
		}
	}
	var out []StatsPersona
	for _, p := range rows {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b StatsPersona) int {
		if a.Level != b.Level {
			return strings.Compare(b.Level, a.Level) // unit before group
		}
		return strings.Compare(a.Persona, b.Persona)
	})
	var lv []StatsLevel
	for _, name := range []string{"unit", "group", "release"} {
		if l := levels[name]; l.Rounds > 0 || l.Findings > 0 {
			lv = append(lv, *l)
		}
	}
	return out, lv
}

// StatsMinSample is the smallest denominator a per-model rate is computed over
// (issue #473): below it the rate is null in JSON and n/a in text, never a 0%
// or 100% read off one or two attempts.
const StatsMinSample = 3

// StatsModel is one (adapter, model, variant) row of the per-model scoreboard
// (issue #473), every number attributed to the attempt's own dispatched event.
type StatsModel struct {
	Adapter  string `json:"adapter"`
	Model    string `json:"model"`
	Variant  string `json:"variant"`
	Attempts int    `json:"attempts"` // dispatched attempts
	Finished int    `json:"finished"` // of those, the ones with a finished event
	Clean    int    `json:"clean"`    // finished with reason stop
	// Silent and Stalled count finishes with reason silent and stalled; Failed
	// counts the other finishes classifyRun maps to failed or failed-dirty
	// (every unclean reason except length, error, rate-limited and
	// abandoned-job, which it maps to their own states).
	Silent    int      `json:"silent"`
	Failed    int      `json:"failed"`
	Stalled   int      `json:"stalled"`
	CleanRate *float64 `json:"clean_rate"` // clean / finished
	// Validated counts attempts with at least one conclusive validated event
	// (an rc, reason neither inconclusive nor host-blocked); GatePass those
	// whose every gate's first conclusive reading in log order had rc 0.
	Validated    int      `json:"validated"`
	GatePass     int      `json:"gate_pass"`
	GatePassRate *float64 `json:"gate_pass_rate"` // gate_pass / validated
	// Inspected counts tasks whose first inspected verdict followed an attempt
	// of this row; Accepted those whose verdict was pass.
	Inspected            int      `json:"inspected"`
	Accepted             int      `json:"accepted"`
	AcceptedRate         *float64 `json:"accepted_rate"`        // accepted / inspected
	CorrectionsPerTask   float64  `json:"corrections_per_task"` // (attempts - tasks) / tasks
	MedianAttemptSeconds float64  `json:"median_attempt_seconds"`
	Spend                float64  `json:"spend"`             // sum of the attempts' finished cost
	CostPerAccepted      float64  `json:"cost_per_accepted"` // spend / accepted; 0 when accepted is 0
}

// modelStats folds the event log into StatsModel rows, one per (adapter,
// model, variant) of dispatched attempts, sorted by adapter, model, variant.
// Finished and validated events count under their own attempt's dispatched
// event; a task's first inspected verdict counts under the task's latest
// dispatched attempt before it in time order.
func modelStats(events []Event) []StatsModel {
	type rowKey struct{ adapter, model, variant string }
	rowOf := map[attemptKey]rowKey{}
	dispatchTS := map[attemptKey]string{}
	for _, e := range events {
		if e.Kind == "dispatched" && e.Attempt != "" {
			k := attemptKey{task: e.Task, attempt: e.Attempt}
			rowOf[k] = rowKey{e.Adapter, e.Model, e.Variant}
			dispatchTS[k] = e.TS
		}
	}
	rows := map[rowKey]*StatsModel{}
	tasks := map[rowKey]map[string]bool{}
	secs := map[rowKey][]int{}
	for k, rk := range rowOf {
		if rows[rk] == nil {
			rows[rk] = &StatsModel{Adapter: rk.adapter, Model: rk.model, Variant: rk.variant}
			tasks[rk] = map[string]bool{}
		}
		rows[rk].Attempts++
		tasks[rk][k.task] = true
	}
	// first holds each attempt's first conclusive reading per gate id, in log
	// order: real validate stamps every gate with its own time, so a round is
	// not one ts.
	first := map[attemptKey]map[string]bool{}
	for _, e := range events {
		k := attemptKey{task: e.Task, attempt: e.Attempt}
		rk, ok := rowOf[k]
		if !ok {
			continue
		}
		r := rows[rk]
		switch e.Kind {
		case "finished":
			r.Finished++
			r.Spend += e.Cost
			switch e.Reason {
			case "stop":
				r.Clean++
			case "silent":
				r.Silent++
			case "stalled":
				r.Stalled++
			default:
				if s := classifyRun(true, 0, 0, 0, false, e.Reason, 0, 0, 0, false, len(e.Wrote) > 0, ""); s == "failed" || s == "failed-dirty" {
					r.Failed++
				}
			}
			dt, derr := time.Parse(time.RFC3339Nano, dispatchTS[k])
			ft, ferr := time.Parse(time.RFC3339Nano, e.TS)
			if derr == nil && ferr == nil {
				secs[rk] = append(secs[rk], int(math.Round(ft.Sub(dt).Seconds())))
			}
		case "validated":
			if e.RC == nil || e.Reason == "inconclusive" || e.Reason == "host-blocked" {
				continue
			}
			if first[k] == nil {
				first[k] = map[string]bool{}
			}
			if _, seen := first[k][e.Gate]; !seen {
				first[k][e.Gate] = *e.RC == 0
			}
		}
	}
	for k, gates := range first {
		r := rows[rowOf[k]]
		r.Validated++
		pass := true
		for _, ok := range gates {
			pass = pass && ok
		}
		if pass {
			r.GatePass++
		}
	}
	latest := map[string]attemptKey{}
	inspected := map[string]bool{}
	for _, e := range sortByTS(events) {
		switch {
		case e.Kind == "dispatched" && e.Attempt != "":
			latest[e.Task] = attemptKey{task: e.Task, attempt: e.Attempt}
		case e.Kind == "inspected" && e.Task != "" && !inspected[e.Task]:
			inspected[e.Task] = true
			if k, ok := latest[e.Task]; ok {
				r := rows[rowOf[k]]
				r.Inspected++
				if e.Verdict == "pass" {
					r.Accepted++
				}
			}
		}
	}
	out := make([]StatsModel, 0, len(rows))
	for rk, r := range rows {
		n := len(tasks[rk])
		r.CleanRate = minRate(r.Clean, r.Finished)
		r.GatePassRate = minRate(r.GatePass, r.Validated)
		r.AcceptedRate = minRate(r.Accepted, r.Inspected)
		r.CorrectionsPerTask = round(float64(r.Attempts-n)/float64(n), 2)
		r.MedianAttemptSeconds = median(secs[rk])
		r.Spend = round(r.Spend, 4)
		if r.Accepted > 0 {
			r.CostPerAccepted = round(r.Spend/float64(r.Accepted), 4)
		}
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b StatsModel) int {
		if c := strings.Compare(a.Adapter, b.Adapter); c != 0 {
			return c
		}
		if c := strings.Compare(a.Model, b.Model); c != 0 {
			return c
		}
		return strings.Compare(a.Variant, b.Variant)
	})
	return out
}

// minRate is n/d rounded to 2 places, or nil when d < StatsMinSample.
func minRate(n, d int) *float64 {
	if d < StatsMinSample {
		return nil
	}
	r := round(float64(n)/float64(d), 2)
	return &r
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
