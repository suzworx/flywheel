package flywheel

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// SuperviseResult reports one supervise pass: every task it measured, with
// whether its gauges passed, or the error that stopped its measurement.
type SuperviseResult struct {
	Measured []SupervisedTask `json:"measured"`
}

// SupervisedTask is one measured task.
type SupervisedTask struct {
	Task    string `json:"task"`
	Attempt string `json:"attempt"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"` // set when validation itself failed to run
}

// NeedsMeasuring returns, sorted, the ids of every finished task whose current
// attempt has no complete validate pass (one that reached owns_checked) since
// its latest finish; see measureTargets.
func NeedsMeasuring(events []Event) []string {
	var out []string
	for _, m := range measureTargets(events) {
		out = append(out, m.task)
	}
	return out
}

// measureTarget is one task a supervise pass must measure, with the attempt
// it expects the measurement to cover.
type measureTarget struct {
	task    string
	attempt string
}

// measureTargets returns, sorted by task, every finished task whose current
// attempt has no COMPLETE validate pass since its latest finish. A pass is
// complete once it has recorded owns_checked (its last record), so an
// interrupted pass that recorded only some gates is measured again (#298
// review). The attempt is the derived current one, or — when the ledger never
// recorded a dispatch — the attempt of the newest finish; "latest" means the
// newest timestamp, never the last line, so an out-of-order ledger cannot
// make a stale reading count.
func measureTargets(events []Event) []measureTarget {
	var out []measureTarget
	for _, ts := range Derive(events).Tasks {
		if ts.Status != "finished" {
			continue
		}
		cur := ts.Attempt
		if cur == "" {
			var newest time.Time
			for _, e := range events {
				if e.Task == ts.ID && e.Kind == "finished" {
					if t, ok := parseTS(e.TS); ok && !t.Before(newest) {
						newest, cur = t, e.Attempt
					}
				}
			}
		}
		var finished time.Time
		finishedOK := false
		for _, e := range events {
			if e.Task == ts.ID && e.Kind == "finished" && e.Attempt == cur {
				if t, ok := parseTS(e.TS); ok && (!finishedOK || t.After(finished)) {
					finished, finishedOK = t, true
				}
			}
		}
		measured := false
		for _, e := range events {
			if e.Task != ts.ID || e.Kind != "owns_checked" || e.Attempt != cur || !finishedOK {
				continue
			}
			if t, ok := parseTS(e.TS); ok && t.After(finished) {
				measured = true
				break
			}
		}
		if !measured {
			out = append(out, measureTarget{task: ts.ID, attempt: cur})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].task < out[j].task })
	return out
}

// parseTS parses an event timestamp; ok is false when it cannot be read, which
// measureTargets treats as "not measured after the finish".
func parseTS(ts string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, ts)
	return t, err == nil
}

// Supervise finds every task that needs measuring, runs ValidateTask on each,
// and returns a SuperviseResult. It takes a lock so two supervisors never
// measure at once.
func Supervise(dir string) (SuperviseResult, error) {
	release, err := acquireRepoLock(dir, "supervise.lock", defaultRepoLockTimings())
	if err != nil {
		return SuperviseResult{}, err
	}
	defer release()

	events, err := ReadEvents(dir)
	if err != nil {
		return SuperviseResult{}, err
	}

	result := SuperviseResult{Measured: []SupervisedTask{}}
	for _, m := range measureTargets(events) {
		res, err := ValidateTask(dir, m.task, ValidateOptions{Dir: dir})
		if err != nil {
			result.Measured = append(result.Measured, SupervisedTask{Task: m.task, Attempt: m.attempt, Error: err.Error()})
			continue
		}
		st := SupervisedTask{Task: m.task, Attempt: res.Attempt, OK: res.OK()}
		// validate picks the attempt from the ledger itself; if that is not
		// the attempt this pass meant to measure (an out-of-order ledger),
		// say so rather than silently measuring the wrong one each pass.
		if m.attempt != "" && res.Attempt != m.attempt {
			st.OK = false
			st.Error = fmt.Sprintf("validate measured attempt %s, but the current attempt is %s (the ledger's lines are out of order)", res.Attempt, m.attempt)
		}
		result.Measured = append(result.Measured, st)
	}

	return result, nil
}

// MarshalJSON ensures SuperviseResult marshals with a non-nil Measured slice.
func (r SuperviseResult) MarshalJSON() ([]byte, error) {
	type Alias SuperviseResult
	if r.Measured == nil {
		r.Measured = make([]SupervisedTask, 0)
	}
	return json.Marshal((*Alias)(&r))
}
