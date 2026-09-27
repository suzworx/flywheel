package flywheel

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// MetricsWindow is the span a MetricsReport covers (issue #583): events with
// Since <= ts < Until count, and every per-bucket series has one value per
// Bucket-long step from Since.
type MetricsWindow struct {
	Since  time.Time     `json:"since"`
	Until  time.Time     `json:"until"`
	Bucket time.Duration `json:"bucket"`
}

// WindowFor is the named window ending at now: 24h in 1h buckets, 7d and 30d
// in 1d buckets.
func WindowFor(name string, now time.Time) (MetricsWindow, error) {
	switch name {
	case "24h":
		return MetricsWindow{Since: now.Add(-24 * time.Hour), Until: now, Bucket: time.Hour}, nil
	case "7d":
		return MetricsWindow{Since: now.Add(-7 * 24 * time.Hour), Until: now, Bucket: 24 * time.Hour}, nil
	case "30d":
		return MetricsWindow{Since: now.Add(-30 * 24 * time.Hour), Until: now, Bucket: 24 * time.Hour}, nil
	}
	return MetricsWindow{}, fmt.Errorf("window %q: want 24h, 7d or 30d", name)
}

// Previous is the equal-length window ending where w starts, for trends.
func (w MetricsWindow) Previous() MetricsWindow {
	d := w.Until.Sub(w.Since)
	return MetricsWindow{Since: w.Since.Add(-d), Until: w.Since, Bucket: w.Bucket}
}

// in reports whether t falls in the window.
func (w MetricsWindow) in(t time.Time) bool {
	return !t.Before(w.Since) && t.Before(w.Until)
}

// buckets are the start times of the window's buckets; none when the window
// is empty or Bucket is not positive.
func (w MetricsWindow) buckets() []time.Time {
	var out []time.Time
	if w.Bucket <= 0 {
		return out
	}
	for t := w.Since; t.Before(w.Until); t = t.Add(w.Bucket) {
		out = append(out, t)
	}
	return out
}

// bucketOf is the index of t's bucket, false when t is outside the window.
func (w MetricsWindow) bucketOf(t time.Time) (int, bool) {
	if !w.in(t) || w.Bucket <= 0 {
		return 0, false
	}
	return int(t.Sub(w.Since) / w.Bucket), true
}

// seconds is the window's length in seconds.
func (w MetricsWindow) seconds() float64 {
	return w.Until.Sub(w.Since).Seconds()
}

// Dist summarises a set of durations: nearest-rank percentiles, the maximum
// and the mean. JSON carries each duration in nanoseconds.
type Dist struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"p50"`
	P90   time.Duration `json:"p90"`
	Max   time.Duration `json:"max"`
	Mean  time.Duration `json:"mean"`
}

// distOf is the Dist of ds; the zero Dist when ds is empty.
func distOf(ds []time.Duration) Dist {
	if len(ds) == 0 {
		return Dist{}
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	rank := func(p int) time.Duration {
		i := (p*len(s)+99)/100 - 1 // nearest rank: ceil(p/100 × n), 1-based
		return s[max(i, 0)]
	}
	return Dist{Count: len(s), P50: rank(50), P90: rank(90), Max: s[len(s)-1], Mean: sum / time.Duration(len(s))}
}

// MetricsReport is the factory's lean metrics over one window (issue #583),
// computed from the event log alone. Every series is aligned to Buckets.
type MetricsReport struct {
	Window      MetricsWindow      `json:"window"`
	Buckets     []time.Time        `json:"buckets"`
	Flow        FlowMetrics        `json:"flow"`
	Quality     QualityMetrics     `json:"quality"`
	Reliability ReliabilityMetrics `json:"reliability"`
	Cost        CostMetrics        `json:"cost"`
	Capacity    CapacityMetrics    `json:"capacity"`
}

// FlowMetrics is how work moves through the factory.
type FlowMetrics struct {
	Throughput       int       `json:"throughput"`        // units whose first landed event is in the window
	ThroughputSeries []float64 `json:"throughput_series"` // landed units per bucket
	WIP              int       `json:"wip"`               // units in progress at the window's end
	WIPSeries        []float64 `json:"wip_series"`        // units in progress at each bucket's end
	LeadTime         Dist      `json:"lead_time"`         // first planned → landed
	CycleTime        Dist      `json:"cycle_time"`        // first dispatched → landed
	QueueTime        Dist      `json:"queue_time"`        // first planned → first dispatched
	TouchTime        Dist      `json:"touch_time"`        // per unit: sum of dispatched → finished
	FlowEfficiency   float64   `json:"flow_efficiency"`   // mean of touch / cycle
}

// wipStatuses are the derived statuses a unit is in progress in.
var wipStatuses = map[string]bool{"dispatched": true, "running": true, "finished": true, "passed": true}

// attemptSpan is one attempt's dispatched → finished interval; Open when no
// finished event closed it (End is then the task's next dispatched, lost,
// withdrawn or landed event, or zero while none came).
type attemptSpan struct {
	Task, Attempt, Worker, Model string
	Start, End                   time.Time
	Open                         bool
}

// unitTimes is one unit's milestones: the first planned, dispatched and
// landed times (zero when absent), its distinct attempts in order and spans.
type unitTimes struct {
	Planned, Dispatched, Landed time.Time
	Attempts                    []string
	Spans                       []attemptSpan
}

// unitTimeline folds the sorted events into each unit's milestones.
func unitTimeline(ev []timed) map[string]*unitTimes {
	units := map[string]*unitTimes{}
	open := map[attemptKey]int{} // attempt → index of its span while open
	for _, e := range ev {
		if e.Task == "" {
			continue
		}
		u := units[e.Task]
		if u == nil {
			u = &unitTimes{}
			units[e.Task] = u
		}
		first := func(t *time.Time) {
			if t.IsZero() {
				*t = e.At
			}
		}
		k := attemptKey{task: e.Task, attempt: e.Attempt}
		switch e.Kind {
		case "planned":
			first(&u.Planned)
		case "dispatched":
			first(&u.Dispatched)
			u.closeOpen(open, e.At)
			if e.Attempt != "" && !slices.Contains(u.Attempts, e.Attempt) {
				u.Attempts = append(u.Attempts, e.Attempt)
			}
			open[k] = len(u.Spans)
			u.Spans = append(u.Spans, attemptSpan{Task: e.Task, Attempt: e.Attempt, Worker: e.Worker, Model: e.Model, Start: e.At, Open: true})
		case "finished":
			if i, ok := open[k]; ok {
				u.Spans[i].End, u.Spans[i].Open = e.At, false
				delete(open, k)
			}
		case "lost", "withdrawn", "landed":
			if e.Kind == "landed" {
				first(&u.Landed)
			}
			u.closeOpen(open, e.At)
		}
	}
	return units
}

// closeOpen ends the unit's open spans at t, leaving them marked Open.
func (u *unitTimes) closeOpen(open map[attemptKey]int, t time.Time) {
	for k, i := range open {
		if i < len(u.Spans) && u.Spans[i].Task == k.task && u.Spans[i].End.IsZero() {
			u.Spans[i].End = t
			delete(open, k)
		}
	}
}

// touch is the unit's summed closed attempt time.
func (u *unitTimes) touch() time.Duration {
	var d time.Duration
	for _, s := range u.Spans {
		if !s.Open {
			d += s.End.Sub(s.Start)
		}
	}
	return d
}

// landedIn lists the units whose first landing is in w, sorted.
func landedIn(units map[string]*unitTimes, w MetricsWindow) []string {
	var out []string
	for id, u := range units {
		if !u.Landed.IsZero() && w.in(u.Landed) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// flowMetrics computes FlowMetrics over the sorted events.
func flowMetrics(ev []timed, units map[string]*unitTimes, w MetricsWindow, buckets []time.Time) FlowMetrics {
	f := FlowMetrics{ThroughputSeries: make([]float64, len(buckets)), WIPSeries: make([]float64, len(buckets))}
	var lead, cycle, queue, touch []time.Duration
	var eff float64
	nEff := 0
	for _, id := range landedIn(units, w) {
		u := units[id]
		f.Throughput++
		if i, ok := w.bucketOf(u.Landed); ok {
			f.ThroughputSeries[i]++
		}
		if !u.Planned.IsZero() {
			lead = append(lead, u.Landed.Sub(u.Planned))
		}
		if !u.Planned.IsZero() && !u.Dispatched.IsZero() {
			queue = append(queue, u.Dispatched.Sub(u.Planned))
		}
		if u.Dispatched.IsZero() {
			continue
		}
		c := u.Landed.Sub(u.Dispatched)
		cycle = append(cycle, c)
		t := u.touch()
		touch = append(touch, t)
		if c > 0 {
			eff += t.Seconds() / c.Seconds()
			nEff++
		}
	}
	f.LeadTime, f.CycleTime, f.QueueTime, f.TouchTime = distOf(lead), distOf(cycle), distOf(queue), distOf(touch)
	if nEff > 0 {
		f.FlowEfficiency = round(eff/float64(nEff), 4)
	}
	for i, b := range buckets {
		end := b.Add(w.Bucket)
		if end.After(w.Until) {
			end = w.Until
		}
		f.WIPSeries[i] = float64(wipAt(ev, end))
	}
	f.WIP = wipAt(ev, w.Until)
	return f
}

// wipAt counts the units whose derived status, over the events before t, is
// one of wipStatuses.
func wipAt(ev []timed, t time.Time) int {
	var prefix []Event
	for _, e := range ev {
		if !e.At.Before(t) {
			break
		}
		prefix = append(prefix, e.Event)
	}
	n := 0
	for _, ts := range Derive(prefix).Tasks {
		if wipStatuses[ts.Status] {
			n++
		}
	}
	return n
}

// QualityMetrics is how often work is right the first time.
type QualityMetrics struct {
	Landed         int        `json:"landed"`           // units landed in the window
	FirstPass      int        `json:"first_pass"`       // of those, landed on r1 alone with no correct/rework verdict
	FirstPassYield float64    `json:"first_pass_yield"` // first_pass / landed
	Corrections    int        `json:"corrections"`      // attempts beyond the first, over landed units
	ReworkRate     float64    `json:"rework_rate"`      // corrections / landed
	Gates          []GateRate `json:"gates"`            // conclusive gate readings in the window
	Reviewed       int        `json:"reviewed"`         // units with an agent review in the window
	Findings       int        `json:"findings"`         // review findings in the window
	Blocking       int        `json:"blocking"`         // of those, blocker or major
	ReviewFindRate float64    `json:"review_find_rate"` // findings / reviewed
	BlockingShare  float64    `json:"blocking_share"`   // blocking / findings
	Escapes        int        `json:"escapes"`          // units landed in the window planned again after landing
}

// GateRate is one gate's conclusive readings: Gate the gate id (its 1-based
// index), Text the gate command truncated to 60 characters.
type GateRate struct {
	Gate  string  `json:"gate"`
	Text  string  `json:"text"`
	Pass  int     `json:"pass"`
	Total int     `json:"total"`
	Rate  float64 `json:"rate"`
}

// correctionVerdicts are the verdicts that send a unit back for rework.
var correctionVerdicts = map[string]bool{"correct": true, "rework": true}

// qualityMetrics computes QualityMetrics over the sorted events.
func qualityMetrics(ev []timed, units map[string]*unitTimes, w MetricsWindow) QualityMetrics {
	q := QualityMetrics{}
	corrected := map[string]bool{}
	for _, e := range ev {
		if (e.Kind == "inspected" || e.Kind == "reviewed") && correctionVerdicts[e.Verdict] {
			corrected[e.Task] = true
		}
	}
	for _, id := range landedIn(units, w) {
		u := units[id]
		q.Landed++
		q.Corrections += max(len(u.Attempts)-1, 0)
		if len(u.Attempts) == 1 && u.Attempts[0] == "r1" && !corrected[id] {
			q.FirstPass++
		}
		for _, e := range ev {
			if e.Task == id && e.Kind == "planned" && e.At.After(u.Landed) {
				q.Escapes++
				break
			}
		}
	}
	if q.Landed > 0 {
		q.FirstPassYield = round(float64(q.FirstPass)/float64(q.Landed), 4)
		q.ReworkRate = round(float64(q.Corrections)/float64(q.Landed), 4)
	}
	gates := map[[2]string]*GateRate{}
	reviewed := map[string]bool{}
	for _, e := range ev {
		if !w.in(e.At) {
			continue
		}
		switch {
		case e.Kind == "validated" && e.RC != nil && e.Reason != "inconclusive" && e.Reason != "host-blocked":
			k := [2]string{e.Gate, clipRunes(e.Command, 60)}
			if gates[k] == nil {
				gates[k] = &GateRate{Gate: k[0], Text: k[1]}
			}
			gates[k].Total++
			if *e.RC == 0 {
				gates[k].Pass++
			}
		case e.Kind == "review_finding":
			q.Findings++
			if blockingFinding(e.Event) {
				q.Blocking++
			}
		case agentReviewed(e.Event):
			reviewed[e.Task] = true
		}
	}
	for _, g := range gates {
		g.Rate = round(float64(g.Pass)/float64(g.Total), 4)
		q.Gates = append(q.Gates, *g)
	}
	slices.SortFunc(q.Gates, func(a, b GateRate) int {
		if len(a.Gate) != len(b.Gate) {
			return len(a.Gate) - len(b.Gate) // "2" before "10"
		}
		if a.Gate != b.Gate {
			return strings.Compare(a.Gate, b.Gate)
		}
		return strings.Compare(a.Text, b.Text)
	})
	q.Reviewed = len(reviewed)
	if q.Reviewed > 0 {
		q.ReviewFindRate = round(float64(q.Findings)/float64(q.Reviewed), 4)
	}
	if q.Findings > 0 {
		q.BlockingShare = round(float64(q.Blocking)/float64(q.Findings), 4)
	}
	return q
}

// ReliabilityMetrics is how often the line stops and for how long.
type ReliabilityMetrics struct {
	Andons      map[string]int `json:"andons"`       // andons in the window by kind
	AndonTotal  int            `json:"andon_total"`  // sum of Andons
	AndonSeries []float64      `json:"andon_series"` // andons per bucket
	Cleared     int            `json:"cleared"`      // andons in the window a later event cleared
	MTTR        Dist           `json:"mttr"`         // andon → the clearing event
	Frozen      FrozenTime     `json:"frozen"`       // suspended time inside the window
	Paused      []ModelPause   `json:"paused"`       // rate-limit pause time per model inside the window
}

// FrozenTime is suspended time split by who froze the factory. The ledger's
// suspended event carries no automatic flag yet, so every interval is Manual.
type FrozenTime struct {
	Total  time.Duration `json:"total"`
	Manual time.Duration `json:"manual"`
	Auto   time.Duration `json:"auto"`
}

// ModelPause is one model's rate-limit pause time.
type ModelPause struct {
	Model  string        `json:"model"`
	Paused time.Duration `json:"paused"`
}

// andonReasons are the finished reasons that stop a unit's line.
var andonReasons = map[string]bool{"stalled": true, "silent": true, "capped": true, "rate-limited": true}

// andonKind names e's andon, "" when e is none. A finished reason already
// recorded as a signal for the same attempt counts once, as the signal.
func andonKind(e Event, signalled map[[3]string]bool) string {
	if e.Kind == "signal" {
		return e.Signal
	}
	if e.Kind == "finished" && andonReasons[e.Reason] && !signalled[[3]string{e.Task, e.Attempt, e.Reason}] {
		return e.Reason
	}
	return ""
}

// clears reports whether e clears an andon on its task.
func clears(e Event) bool {
	switch e.Kind {
	case "landed", "withdrawn", "dismissed":
		return true
	case "inspected", "reviewed":
		return e.Verdict == "pass" && !agentReviewed(e)
	}
	return false
}

// reliabilityMetrics computes ReliabilityMetrics over the sorted events.
func reliabilityMetrics(ev []timed, cfg Config, w MetricsWindow, buckets []time.Time) ReliabilityMetrics {
	r := ReliabilityMetrics{Andons: map[string]int{}, AndonSeries: make([]float64, len(buckets))}
	signalled := map[[3]string]bool{}
	for _, e := range ev {
		if e.Kind == "signal" {
			signalled[[3]string{e.Task, e.Attempt, e.Signal}] = true
		}
	}
	var repair []time.Duration
	for i, e := range ev {
		kind := andonKind(e.Event, signalled)
		if kind == "" || !w.in(e.At) {
			continue
		}
		r.Andons[kind]++
		r.AndonTotal++
		if b, ok := w.bucketOf(e.At); ok {
			r.AndonSeries[b]++
		}
		for _, n := range ev[i+1:] {
			if e.Task != "" && n.Task == e.Task && clears(n.Event) {
				repair = append(repair, n.At.Sub(e.At))
				break
			}
		}
	}
	r.Cleared, r.MTTR = len(repair), distOf(repair)
	r.Frozen = frozenTime(ev, w)
	r.Paused = pausedTime(ev, cfg, w)
	return r
}

// overlap is how much of [a, b) lies inside w.
func overlap(a, b time.Time, w MetricsWindow) time.Duration {
	if a.Before(w.Since) {
		a = w.Since
	}
	if b.After(w.Until) {
		b = w.Until
	}
	return max(b.Sub(a), 0)
}

// frozenTime sums the suspended intervals inside w: a suspended event opens
// one (a later suspended event while frozen only moves its thaw time), an
// unsuspended event or the thaw time closes it, and one still open at the
// window's end runs to it.
func frozenTime(ev []timed, w MetricsWindow) FrozenTime {
	var f FrozenTime
	frozen := false
	var start, thaw time.Time // thaw zero: until flywheel resume
	closeAt := func(t time.Time) {
		f.Total += overlap(start, t, w)
		frozen = false
	}
	for _, e := range ev {
		if frozen && !thaw.IsZero() && !e.At.Before(thaw) {
			closeAt(thaw)
		}
		switch e.Kind {
		case "suspended":
			if !frozen {
				frozen, start = true, e.At
			}
			thaw = time.Time{}
			if u, err := time.Parse(time.RFC3339, e.Until); err == nil {
				thaw = u
			}
		case "unsuspended":
			if frozen {
				closeAt(e.At)
			}
		}
	}
	if frozen {
		end := w.Until
		if !thaw.IsZero() && thaw.Before(end) {
			end = thaw
		}
		closeAt(end)
	}
	f.Manual = f.Total
	return f
}

// pausedTime is each model's rate-limit pause time inside w, models sorted:
// at every finished event rateLimitPausedAt decides whether the model is
// paused from then, until the pause ends or the model's next finish.
func pausedTime(ev []timed, cfg Config, w MetricsWindow) []ModelPause {
	threshold := cfg.Limits.RateLimitPauseThreshold()
	per := map[string]time.Duration{}
	prefix := make([]Event, 0, len(ev))
	for i, e := range ev {
		prefix = append(prefix, e.Event)
		if e.Kind != "finished" || e.Model == "" {
			continue
		}
		p, ok := rateLimitPausedAt(prefix, e.Model, e.At, threshold)
		if !ok {
			continue
		}
		end := p.Until
		for _, n := range ev[i+1:] {
			if n.Kind == "finished" && n.Model == e.Model {
				if n.At.Before(end) {
					end = n.At
				}
				break
			}
		}
		if d := overlap(e.At, end, w); d > 0 {
			per[e.Model] += d
		}
	}
	out := []ModelPause{}
	for m, d := range per {
		out = append(out, ModelPause{Model: m, Paused: d})
	}
	slices.SortFunc(out, func(a, b ModelPause) int { return strings.Compare(a.Model, b.Model) })
	return out
}

// CostMetrics is what the window's work cost.
type CostMetrics struct {
	Spend         float64      `json:"spend"`           // finished cost plus reviewed cost, USD
	SpendSeries   []float64    `json:"spend_series"`    // spend per bucket
	Units         int          `json:"units"`           // units with a finished event in the window
	CostPerUnit   float64      `json:"cost_per_unit"`   // spend / units
	CostPerLanded float64      `json:"cost_per_landed"` // spend / units landed in the window
	Tokens        Tokens       `json:"tokens"`          // finished events' tokens
	Steps         int          `json:"steps"`           // finished events' steps
	TokensPerStep float64      `json:"tokens_per_step"` // (input + output + reasoning) / steps
	ByModel       []StatsModel `json:"by_model"`        // the stats scoreboard over the window's events
}

// CapacityMetrics is how much of the configured worker capacity was busy.
type CapacityMetrics struct {
	Workers     []WorkerLoad `json:"workers"`
	Utilization float64      `json:"utilization"` // busy seconds / capacity seconds over every worker
	IdleShare   float64      `json:"idle_share"`  // 1 - utilization
}

// WorkerLoad is one worker's busy share of its capacity in the window.
type WorkerLoad struct {
	Worker      string  `json:"worker"`
	MaxParallel int     `json:"max_parallel"`
	BusySeconds float64 `json:"busy_seconds"`
	Utilization float64 `json:"utilization"` // busy / (max_parallel × window seconds)
}

// costMetrics computes CostMetrics over the sorted events.
func costMetrics(ev []timed, landed int, w MetricsWindow, buckets []time.Time) CostMetrics {
	c := CostMetrics{SpendSeries: make([]float64, len(buckets))}
	units := map[string]bool{}
	var inWindow []Event
	for _, e := range ev {
		if !w.in(e.At) {
			continue
		}
		inWindow = append(inWindow, e.Event)
		if e.Kind != "finished" && e.Kind != "reviewed" {
			continue
		}
		c.Spend += e.Cost
		if b, ok := w.bucketOf(e.At); ok {
			c.SpendSeries[b] += e.Cost
		}
		if e.Kind == "finished" {
			units[e.Task] = true
			c.Steps += e.Steps
			if t := e.Tokens; t != nil {
				c.Tokens.Input, c.Tokens.Output, c.Tokens.Reasoning = c.Tokens.Input+t.Input, c.Tokens.Output+t.Output, c.Tokens.Reasoning+t.Reasoning
				c.Tokens.CacheRead, c.Tokens.CacheWrite = c.Tokens.CacheRead+t.CacheRead, c.Tokens.CacheWrite+t.CacheWrite
			}
		}
	}
	c.Spend = round(c.Spend, 4)
	for i := range c.SpendSeries {
		c.SpendSeries[i] = round(c.SpendSeries[i], 4)
	}
	c.Units = len(units)
	if c.Units > 0 {
		c.CostPerUnit = round(c.Spend/float64(c.Units), 4)
	}
	if landed > 0 {
		c.CostPerLanded = round(c.Spend/float64(landed), 4)
	}
	if c.Steps > 0 {
		c.TokensPerStep = round(float64(c.Tokens.Input+c.Tokens.Output+c.Tokens.Reasoning)/float64(c.Steps), 2)
	}
	c.ByModel = modelStats(inWindow)
	return c
}

// capacityMetrics computes CapacityMetrics from the units' attempt spans; an
// attempt's worker is its dispatched worker, else the configured worker on
// its model, else the default worker.
func capacityMetrics(units map[string]*unitTimes, cfg Config, w MetricsWindow) CapacityMetrics {
	cap := CapacityMetrics{Workers: []WorkerLoad{}}
	rows := map[string]*WorkerLoad{}
	var names []string
	row := func(name string, mp int) *WorkerLoad {
		if rows[name] == nil {
			rows[name] = &WorkerLoad{Worker: name, MaxParallel: max(mp, 1)}
			names = append(names, name)
		}
		return rows[name]
	}
	for _, wk := range cfg.Workers {
		row(wk.Name, wk.MaxParallel)
	}
	ids := make([]string, 0, len(units))
	for id := range units {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		for _, s := range units[id].Spans {
			name := s.Worker
			for _, wk := range cfg.Workers {
				if name == "" && s.Model != "" && wk.Model == s.Model {
					name = wk.Name
				}
			}
			if name == "" {
				name = cfg.DefaultWorker().Name
			}
			end := s.End
			if end.IsZero() {
				end = w.Until
			}
			row(name, 1).BusySeconds += overlap(s.Start, end, w).Seconds()
		}
	}
	var busy, capacity float64
	secs := w.seconds()
	for _, n := range names {
		r := rows[n]
		busy += r.BusySeconds
		capacity += float64(r.MaxParallel) * secs
		if secs > 0 {
			r.Utilization = round(r.BusySeconds/(float64(r.MaxParallel)*secs), 4)
		}
		r.BusySeconds = round(r.BusySeconds, 3)
		cap.Workers = append(cap.Workers, *r)
	}
	if capacity > 0 {
		cap.Utilization = round(busy/capacity, 4)
		cap.IdleShare = round(max(1-busy/capacity, 0), 4)
	}
	return cap
}

// Metrics computes the MetricsReport of events over w (issue #583). It is
// pure: the clock is w, and cfg supplies the worker capacity and the
// rate-limit pause threshold. docs/metrics.md defines every number.
func Metrics(events []Event, cfg Config, w MetricsWindow) MetricsReport {
	ev := timedEvents(events)
	units := unitTimeline(ev)
	buckets := w.buckets()
	if buckets == nil {
		buckets = []time.Time{}
	}
	rep := MetricsReport{Window: w, Buckets: buckets}
	rep.Flow = flowMetrics(ev, units, w, buckets)
	rep.Quality = qualityMetrics(ev, units, w)
	rep.Reliability = reliabilityMetrics(ev, cfg, w, buckets)
	rep.Cost = costMetrics(ev, rep.Quality.Landed, w, buckets)
	rep.Capacity = capacityMetrics(units, cfg, w)
	return rep
}

// MetricsFor reads dir's event log and config and computes Metrics over w.
func MetricsFor(dir string, w MetricsWindow) (MetricsReport, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return MetricsReport{}, fmt.Errorf("metrics %s: %w", dir, err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return MetricsReport{}, fmt.Errorf("metrics %s: %w", dir, err)
	}
	return Metrics(events, cfg, w), nil
}

// clipRunes is s cut to its first n runes.
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// timed is an event with its parsed ts; events whose ts does not parse are
// left out of every metric.
type timed struct {
	Event
	At time.Time
}

// timedEvents is events sorted by ts with the times parsed.
func timedEvents(events []Event) []timed {
	var out []timed
	for _, e := range sortByTS(events) {
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
			out = append(out, timed{Event: e, At: t})
		}
	}
	return out
}
