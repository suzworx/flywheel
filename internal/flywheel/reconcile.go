package flywheel

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Observed is what Reconcile sees of the world beyond the event log: the
// lease files as they read now.
type Observed struct {
	Leases []Lease `json:"leases"`
}

// Policy caps the factory's parallelism for one tick.
type Policy struct {
	MaxParallel int      `json:"max_parallel"`
	PerHost     int      `json:"per_host,omitempty"`   // PerHost caps attempts in flight on this host across every model (limits.per_host); 0 means no cap.
	Model       string   `json:"model,omitempty"`      // the default worker's model, which DISPATCH would use
	BudgetUSD   float64  `json:"budget_usd,omitempty"` // limits.budget.wave_cost_usd; 0 means no budget
	Breaker     *Breaker `json:"breaker,omitempty"`    // limits.breaker; nil means no breaker
}

// PolicyFromConfig derives the policy from the configuration: the default
// worker's max_parallel (where 0 means 1), the default worker's model, the
// budget from limits.budget.wave_cost_usd when set, and the breaker from
// limits.breaker when set.
func PolicyFromConfig(cfg Config) Policy {
	mp := cfg.DefaultWorker().MaxParallel
	if mp < 1 {
		mp = 1
	}
	budgetUSD := 0.0
	if cfg.Limits.Budget != nil {
		budgetUSD = cfg.Limits.Budget.WaveCostUSD
	}
	return Policy{MaxParallel: mp, PerHost: cfg.Limits.PerHost, Model: cfg.DefaultWorker().Model, BudgetUSD: budgetUSD, Breaker: cfg.Limits.Breaker}
}

// Action is one transition Reconcile recommends. Nothing executes the
// actions in this phase: flywheel next only prints them.
type Action struct {
	Kind     string `json:"kind"`
	Task     string `json:"task"`
	Attempt  string `json:"attempt,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// Reconcile computes the next actions from the derived state, the event log,
// the observed leases, the policy and the clock. It is pure: no I/O, no clock
// reads, no randomness; the same inputs give identical output.
//
// Order of evaluation: lost, then inspection, then needs (BLOCK and WAIT),
// then dispatch. The output is sorted by that order, then by planned time,
// then by task id.
//
//   - MARK_LOST: the current attempt of a dispatched or running task whose
//     lease file exists in obs and has expired at now; the evidence names the
//     lease's expires_at.
//   - REQUEST_INSPECTION: a finished task whose latest validated reading per
//     gate all passed, on the tree of its latest owns_checked, which is clean
//     and after the latest finished, and with no inspected event after the
//     readings: the pure version of inspect.go's T3 pass check, without the
//     brief's gate list.
//   - BLOCK: a planned task with a needs target that is rejected; the reason
//     names the first rejected target in brief order.
//   - WAIT: a planned task with a needs target not yet passed or landed; the
//     reason lists every unmet target in the order they appear in the brief.
//   - DISPATCH: a planned task whose needs are all passed or landed, with no
//     live lease, up to the free capacity (MaxParallel minus live leases, further
//     capped by per_host if set, never below 0); the reason names the free capacity.
//   - HOLD: a task that would be DISPATCHed while the wave's budget is spent or
//     the default model's breaker is open; the reason names which.
func Reconcile(s State, events []Event, obs Observed, p Policy, now time.Time) []Action {
	byID := map[string]TaskState{}
	planned := map[string]bool{}
	pt := map[string]time.Time{}
	for _, ts := range s.Tasks {
		byID[ts.ID] = ts
	}
	for _, e := range events {
		if e.Kind != "planned" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			continue
		}
		if !planned[e.Task] || t.Before(pt[e.Task]) {
			planned[e.Task] = true
			pt[e.Task] = t
		}
	}

	var keys []actionKey
	// 1. lost: the current attempt of a dispatched or running task whose
	// lease file exists in obs and is no longer live at now. A task whose
	// status is already lost is skipped, so a repeated tick never appends a
	// second lost event for the same attempt.
	for _, ts := range s.Tasks {
		if ts.Status == "lost" {
			continue
		}
		if ts.Status != "dispatched" && ts.Status != "running" {
			continue
		}
		if ts.Attempt == "" {
			continue
		}
		for _, l := range obs.Leases {
			if l.Task != ts.ID || l.Attempt != ts.Attempt {
				continue
			}
			if LeaseLive(l, now) {
				break
			}
			keys = append(keys, actionKey{Rank: 0, Valid: planned[ts.ID], Time: pt[ts.ID],
				Action: Action{Kind: "MARK_LOST", Task: ts.ID, Attempt: ts.Attempt,
					Evidence: "lease expired at " + l.ExpiresAt}})
			break
		}
	}
	// 2. inspection: a finished task whose latest readings cover its latest
	// owns_checked tree and all pass, with no inspected event after them.
	for _, ts := range s.Tasks {
		if ts.Status != "finished" {
			continue
		}
		if inspectionReady(events, ts.ID, ts.Attempt) {
			keys = append(keys, actionKey{Rank: 1, Valid: planned[ts.ID], Time: pt[ts.ID],
				Action: Action{Kind: "REQUEST_INSPECTION", Task: ts.ID,
					Reason: "validated pass awaits inspection"}})
		}
	}
	// 3. needs: a planned task is BLOCK when a needs target is rejected (the
	// reason names the first such target in brief order), otherwise WAIT when
	// a target is not yet passed or landed (the reason lists the unmet
	// targets in brief order).
	for _, ts := range s.Tasks {
		if ts.Status != "planned" {
			continue
		}
		rejected := ""
		var unmet []string
		for _, need := range ts.Needs {
			st := ""
			if nt, ok := byID[need]; ok {
				st = nt.Status
			}
			if st == "rejected" && rejected == "" {
				rejected = need
			}
			if st != "passed" && st != "landed" {
				unmet = append(unmet, need)
			}
		}
		if rejected != "" {
			keys = append(keys, actionKey{Rank: 2, Valid: planned[ts.ID], Time: pt[ts.ID],
				Action: Action{Kind: "BLOCK", Task: ts.ID, Reason: "needs " + rejected}})
			continue
		}
		if len(unmet) > 0 {
			keys = append(keys, actionKey{Rank: 2, Valid: planned[ts.ID], Time: pt[ts.ID],
				Action: Action{Kind: "WAIT", Task: ts.ID, Reason: "needs " + strings.Join(unmet, ", ")}})
		}
	}
	// 4. dispatch: ready planned tasks (needs all passed or landed, no live
	// lease) in planned-time order, up to the free capacity. The capacity is
	// the smaller of MaxParallel and per_host (when set), minus live leases.
	// It is computed once from the observed leases, so a tick never invents work
	// and the same inputs give the same actions.
	live := 0
	for _, l := range obs.Leases {
		if LeaseLive(l, now) {
			live++
		}
	}
	capacity := p.MaxParallel - live
	if p.PerHost > 0 && p.PerHost-live < capacity {
		capacity = p.PerHost - live
	}
	if capacity < 0 {
		capacity = 0
	}

	// Compute hold reason ONCE: budget or breaker.
	holdReason := ""
	if p.BudgetUSD > 0 {
		spent := 0.0
		for _, e := range events {
			if e.Kind == "finished" {
				spent += e.Cost
			}
		}
		if spent >= p.BudgetUSD {
			holdReason = fmt.Sprintf("budget: recorded spend $%.4f has reached limits.budget.wave_cost_usd $%.4f", spent, p.BudgetUSD)
		}
	}
	if holdReason == "" && p.Breaker != nil && p.Model != "" {
		if open, until := breakerOpen(events, p.Model, *p.Breaker, now); open {
			holdReason = fmt.Sprintf("breaker: model %s is open until %s; dispatch another model with flywheel run <task> --model <m>", p.Model, until.UTC().Format(time.RFC3339))
		}
	}

	var ready []actionKey
	for _, ts := range s.Tasks {
		if ts.Status != "planned" {
			continue
		}
		met := true
		for _, need := range ts.Needs {
			st := ""
			if nt, ok := byID[need]; ok {
				st = nt.Status
			}
			if st != "passed" && st != "landed" {
				met = false
				break
			}
		}
		if !met {
			continue
		}
		hasLive := false
		for _, l := range obs.Leases {
			if l.Task == ts.ID && LeaseLive(l, now) {
				hasLive = true
				break
			}
		}
		if hasLive {
			continue
		}
		kind := "DISPATCH"
		reason := fmt.Sprintf("ready, needs met, capacity %d free", capacity)
		if holdReason != "" {
			kind = "HOLD"
			reason = holdReason
		}
		ready = append(ready, actionKey{Rank: 3, Valid: planned[ts.ID], Time: pt[ts.ID],
			Action: Action{Kind: kind, Task: ts.ID, Reason: reason}})
	}
	slices.SortStableFunc(ready, func(a, b actionKey) int { return keyCompare(a, b) })
	if len(ready) > capacity {
		ready = ready[:capacity]
	}
	keys = append(keys, ready...)
	slices.SortStableFunc(keys, func(a, b actionKey) int { return keyCompare(a, b) })
	out := make([]Action, len(keys))
	for i, k := range keys {
		out[i] = k.Action
	}
	return out
}

// actionKey orders one action for the final sort.
type actionKey struct {
	Action Action
	Rank   int
	Time   time.Time
	Valid  bool
}

// keyCompare orders actionKeys: evaluation rank, then planned time (a task
// with no parseable planned event sorts last), then task id.
func keyCompare(a, b actionKey) int {
	if c := a.Rank - b.Rank; c != 0 {
		return c
	}
	if a.Valid != b.Valid {
		if a.Valid {
			return -1
		}
		return 1
	}
	if a.Valid {
		if a.Time.Before(b.Time) {
			return -1
		}
		if a.Time.After(b.Time) {
			return 1
		}
	}
	return strings.Compare(a.Action.Task, b.Action.Task)
}

// inspectionReady reports whether a finished task's latest readings cover
// its latest owns_checked tree: the owns_checked is clean and after the
// latest finished, every gate's latest validated reading on that tree after
// the latest finished passes (at least one gate), and no inspected event
// comes after the readings. It mirrors inspect.go's T3 pass check on the
// events alone, without the brief's gate list.
func inspectionReady(events []Event, task, cur string) bool {
	fh, ft, _, _ := latestReading(events, task, "finished", cur)
	if !fh {
		return false
	}
	oh, ot, tree, outside := latestReading(events, task, "owns_checked", cur)
	if !oh || outside != 0 || !ot.After(ft) {
		return false
	}
	last := ot
	gateTime := map[string]time.Time{}
	gateOK := map[string]bool{}
	for _, e := range events {
		if e.Task != task || e.Kind != "validated" {
			continue
		}
		if e.Attempt != "" && e.Attempt != cur {
			continue
		}
		if e.Tree != tree {
			continue
		}
		vt, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil || !vt.After(ft) {
			continue
		}
		ok := e.Reason != "host-blocked" && e.RC != nil && *e.RC == 0
		if _, seen := gateTime[e.Gate]; !seen || vt.After(gateTime[e.Gate]) {
			gateOK[e.Gate] = ok
			gateTime[e.Gate] = vt
		}
		if vt.After(last) {
			last = vt
		}
	}
	if len(gateOK) == 0 {
		return false
	}
	for g := range gateOK {
		if !gateOK[g] {
			return false
		}
	}
	for _, e := range events {
		if e.Task != task || e.Kind != "inspected" {
			continue
		}
		if it, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && it.After(last) {
			return false
		}
	}
	return true
}

// latestReading returns the latest event of kind for a task whose attempt is
// the task's current one (or empty): whether one exists, its time, its tree
// and, for owns_checked, how many paths sat outside. A result of another
// attempt is stale, exactly as Derive treats it.
func latestReading(events []Event, task, kind, cur string) (have bool, t time.Time, tree string, outside int) {
	have = false
	for _, e := range events {
		if e.Task != task || e.Kind != kind {
			continue
		}
		if e.Attempt != "" && e.Attempt != cur {
			continue
		}
		pt, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			continue
		}
		if !have || pt.After(t) {
			have = true
			t = pt
			tree = e.Tree
			outside = len(e.Outside)
		}
	}
	return have, t, tree, outside
}

// NextActions is the read-only command path: it reads the event log, the
// lease files and the config, derives the state, and returns the reconciled
// actions without executing them.
func NextActions(dir string, now time.Time) ([]Action, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, fmt.Errorf("read events %s: %w", dir, err)
	}
	leases, err := ReadLeases(dir)
	if err != nil {
		return nil, fmt.Errorf("read leases %s: %w", dir, err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", dir, err)
	}
	return Reconcile(Derive(events), events, Observed{Leases: leases}, PolicyFromConfig(cfg), now), nil
}
