package flywheel

import (
	"encoding/json"
	"sort"
	"time"
)

// SuperviseResult reports one supervise pass: every task it measured, with
// whether its gauges passed, and every task it skipped with the reason.
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

// NeedsMeasuring returns a sorted slice of task ids whose derived status
// is finished AND that have NO validated event for the task's current
// attempt recorded after the task's latest finished event for that attempt.
func NeedsMeasuring(events []Event) []string {
	var out []string
	for _, ts := range Derive(events).Tasks {
		if ts.Status != "finished" {
			continue
		}
		// The current attempt, or the latest finished attempt when the
		// ledger never recorded a dispatch.
		cur := ts.Attempt
		var finished time.Time
		finishedOK := false
		for _, e := range events {
			if e.Task != ts.ID || e.Kind != "finished" {
				continue
			}
			if ts.Attempt == "" {
				cur = e.Attempt
			}
			if e.Attempt == cur {
				finished, finishedOK = parseTS(e.TS)
			}
		}
		measured := false
		for _, e := range events {
			if e.Task != ts.ID || e.Kind != "validated" || e.Attempt != cur || !finishedOK {
				continue
			}
			if t, ok := parseTS(e.TS); ok && t.After(finished) {
				measured = true
				break
			}
		}
		if !measured {
			out = append(out, ts.ID)
		}
	}
	sort.Strings(out)
	return out
}

// parseTS parses an event timestamp; ok is false when it cannot be read, which
// NeedsMeasuring treats as "not measured after the finish".
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

	taskIDs := NeedsMeasuring(events)
	result := SuperviseResult{
		Measured: make([]SupervisedTask, 0),
	}

	for _, id := range taskIDs {
		res, err := ValidateTask(dir, id, ValidateOptions{Dir: dir})
		if err != nil {
			result.Measured = append(result.Measured, SupervisedTask{
				Task:  id,
				Error: err.Error(),
			})
			continue
		}

		result.Measured = append(result.Measured, SupervisedTask{
			Task:    id,
			Attempt: res.Attempt,
			OK:      res.OK(),
		})
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
