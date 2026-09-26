package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StatusReport is the deterministic factory summary: one number per group,
// derived only from the event log (via Derive) and the factory floor's run
// states and andon.
type StatusReport struct {
	Factory        string          `json:"factory"`
	Tasks          StatusTasks     `json:"tasks"`
	Attempts       StatusAttempts  `json:"attempts"`
	Goals          StatusGoals     `json:"goals"`
	Leases         StatusLeases    `json:"leases"`
	LastEventAt    *LastEvent      `json:"last_event_at,omitempty"`
	LastProgressAt *LastEvent      `json:"last_progress_at,omitempty"`
	Andon          int             `json:"andon"`
	Attention      []AttentionLine `json:"attention,omitempty"`
}

// AttentionLine is one task whose current attempt ended for a reason other
// than a clean stop: the task id, its current attempt and that attempt's
// finished reason (length, start-failed, silent, error, ...).
type AttentionLine struct {
	Task    string `json:"task"`
	Attempt string `json:"attempt"`
	Reason  string `json:"reason"`
}

// StatusGoals counts the goals in each status plus one entry per goal, sorted
// by id, as the status list.
type StatusGoals struct {
	Active    int        `json:"active"`
	Met       int        `json:"met"`
	Failed    int        `json:"failed"`
	Abandoned int        `json:"abandoned"`
	List      []GoalLine `json:"list"`
}

// GoalLine is one goal in the status list: its id, status, progress line and
// title.
type GoalLine struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Progress string `json:"progress"`
	Title    string `json:"title"`
}

// StatusTasks counts the tasks in each derived status.
type StatusTasks struct {
	Total           int `json:"total"`
	Planned         int `json:"planned"`
	Dispatched      int `json:"dispatched"`
	Running         int `json:"running"`
	Finished        int `json:"finished"`
	Passed          int `json:"passed"`
	NeedsCorrection int `json:"needs-correction"`
	Rejected        int `json:"rejected"`
	Blocked         int `json:"blocked"`
	Lost            int `json:"lost"`
	Withdrawn       int `json:"withdrawn,omitempty"`
	Landed          int `json:"landed"`
}

// StatusAttempts summarises the attempts: live counts the tasks whose current
// attempt is dispatched or running; lost counts the attempts whose lease file
// has expired at now, one per LostList entry; stale totals the stale entries
// across tasks.
type StatusAttempts struct {
	Live     int           `json:"live"`
	Lost     int           `json:"lost"`
	Stale    int           `json:"stale"`
	LostList []LostAttempt `json:"lost_list,omitempty"`
}

// LostAttempt is one lost attempt with its evidence: the task is still
// dispatched or running, its current attempt has a lease file, and the lease
// has expired at now.
type LostAttempt struct {
	Task      string `json:"task"`
	Attempt   string `json:"attempt"`
	ExpiresAt string `json:"expires_at"`
	RunFile   string `json:"run_file"`
}

// StatusLeases counts the lease files in .flywheel/leases, judged by LeaseLive
// at now: live while its expiry is still ahead, expired once past it. Skipped
// counts the lease files ReadLeases could not read or parse.
type StatusLeases struct {
	Live    int `json:"live"`
	Expired int `json:"expired"`
	Skipped int `json:"skipped,omitempty"`
}

// LastEvent is one timestamp plus its age in whole seconds from now.
type LastEvent struct {
	TS  string `json:"ts"`
	Age int    `json:"age"`
}

// Status is a pure read of the factory: the event log via Derive plus the
// floor's run states and andon. It writes nothing and calls no model.
func Status(dir string, now time.Time) (StatusReport, error) {
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		return StatusReport{}, fmt.Errorf("status %s: %w", dir, err)
	}
	st := Derive(w.events)
	var rep StatusReport
	rep.Factory = filepath.Base(dir)
	cur := map[string]TaskState{}
	for _, ts := range st.Tasks {
		cur[ts.ID] = ts
		rep.Tasks.Total++
		switch ts.Status {
		case "planned":
			rep.Tasks.Planned++
		case "dispatched":
			rep.Tasks.Dispatched++
		case "running":
			rep.Tasks.Running++
		case "finished":
			rep.Tasks.Finished++
		case "passed":
			rep.Tasks.Passed++
		case "needs-correction":
			rep.Tasks.NeedsCorrection++
		case "rejected":
			rep.Tasks.Rejected++
		case "blocked":
			rep.Tasks.Blocked++
		case "lost":
			rep.Tasks.Lost++
		case "withdrawn":
			rep.Tasks.Withdrawn++
		case "landed":
			rep.Tasks.Landed++
		}
		if ts.Status == "finished" && ts.Reason != "" && ts.Reason != "stop" {
			rep.Attention = append(rep.Attention, AttentionLine{Task: ts.ID, Attempt: ts.Attempt, Reason: ts.Reason})
		}
		if ts.Attempt == "" {
			continue
		}
		if ts.Status == "dispatched" || ts.Status == "running" {
			rep.Attempts.Live++
		}
		rep.Attempts.Stale += len(ts.Stale)
	}
	leases, lerr := ReadLeases(dir)
	if lerr != nil {
		rep.Leases.Skipped = countLeaseFiles(dir) - len(leases)
	}
	for _, l := range leases {
		if LeaseLive(l, now) {
			rep.Leases.Live++
			continue
		}
		rep.Leases.Expired++
		ts, ok := cur[l.Task]
		if !ok || l.Attempt == "" || l.Attempt != ts.Attempt {
			continue
		}
		if ts.Status != "dispatched" && ts.Status != "running" {
			continue
		}
		rep.Attempts.Lost++
		lost := LostAttempt{Task: l.Task, Attempt: l.Attempt, ExpiresAt: l.ExpiresAt, RunFile: l.RunFile}
		rep.Attempts.LostList = append(rep.Attempts.LostList, lost)
	}
	rep.LastEventAt = latestEvent(w.events, now, func(e Event) bool { return true })
	rep.LastProgressAt = latestEvent(w.events, now, func(e Event) bool {
		return e.Kind == "landed" || e.Kind == "inspected" && e.Verdict == "pass"
	})
	for _, g := range Goals(w.events) {
		switch g.Status {
		case "active":
			rep.Goals.Active++
		case "met":
			rep.Goals.Met++
		case "failed":
			rep.Goals.Failed++
		case "abandoned":
			rep.Goals.Abandoned++
		}
		rep.Goals.List = append(rep.Goals.List, GoalLine{ID: g.ID, Status: g.Status, Progress: g.Progress, Title: g.Title})
	}
	rep.Andon = len(fl.Andon)
	return rep, nil
}

// latestEvent returns the newest event matching match as LastEvent (ts plus its
// age in seconds from now), or nil when no event matches.
func latestEvent(events []Event, now time.Time, match func(Event) bool) *LastEvent {
	have := false
	var best time.Time
	ts := ""
	for _, e := range events {
		if !match(e) {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			continue
		}
		if !have || t.After(best) {
			best = t
			ts = e.TS
			have = true
		}
	}
	if !have {
		return nil
	}
	p := new(LastEvent)
	*p = LastEvent{TS: ts, Age: ageOfTime(best, now)}
	return p
}

// countLeaseFiles counts the lease candidates in .flywheel/leases with the
// same filter ReadLeases applies, so status can report how many were skipped.
func countLeaseFiles(dir string) int {
	entries, err := os.ReadDir(filepath.Join(dir, ".flywheel", "leases"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		n++
	}
	return n
}
