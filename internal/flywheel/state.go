package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	statusStartMarker = "<!-- flywheel:status:start -->"
	statusEndMarker   = "<!-- flywheel:status:end -->"
)

// State is the derived snapshot written to .flywheel/state.json.
type State struct {
	Version   int            `json:"version"`
	UpdatedAt string         `json:"updated_at"`
	Tasks     []TaskState    `json:"tasks"`
	Counts    map[string]int `json:"counts"`
}

// TaskState is the per-task derived state.
type TaskState struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Session   string   `json:"session,omitempty"`
	Model     string   `json:"model,omitempty"`
	Attempt   string   `json:"attempt,omitempty"`
	Stale     []string `json:"stale,omitempty"`
	RC        *int     `json:"rc,omitempty"`
	Verdict   string   `json:"verdict,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Brief     string   `json:"brief,omitempty"`
	Needs     []string `json:"needs,omitempty"`
	Owns      []string `json:"owns,omitempty"`
	Increment int      `json:"increment,omitempty"`
	Attempts  int      `json:"attempts"`
	UpdatedAt string   `json:"updated_at"`
}

// kindRank orders same-instant events so the status machine replays in the
// intended order: planned < amended < dispatched < started < worker_plan <
// finished < report < validated < owns_checked < inspected < reviewed <
// blocked < lost < excepted < landed. worker_plan, report, validated and
// owns_checked carry no status; the ranks keep a same-timestamp dispatched,
// started, worker_plan, report, finished sequence deriving finished, and
// gauge kinds after a same-timestamp inspected deriving its verdict.
var kindRank = map[string]int{
	"planned":      0,
	"amended":      1,
	"dispatched":   2,
	"started":      3,
	"worker_plan":  4,
	"finished":     5,
	"report":       6,
	"validated":    7,
	"owns_checked": 8,
	"inspected":    9,
	"reviewed":     10,
	"blocked":      11,
	"lost":         12,
	"excepted":     13,
	"landed":       14,
}

// staleKinds are the result-bearing event kinds whose attempt must match the
// task's current attempt (the attempt of its latest dispatched event); a
// non-empty attempt that differs marks the event stale and it is ignored.
var staleKinds = map[string]bool{
	"started":      true,
	"worker_plan":  true,
	"report":       true,
	"finished":     true,
	"validated":    true,
	"owns_checked": true,
	"lost":         true,
}

// eventSortKey is the precomputed comparison key for one event, so sorting
// does not re-marshal JSON on every comparison.
type eventSortKey struct {
	Event Event
	Time  time.Time
	Valid bool
	Kind  int
	Canon string
}

// canonical is the deterministic JSON encoding of e, used as the final
// tiebreaker when sorting events with equal time, Task and kind rank.
func canonical(e Event) string {
	b, err := marshalEvent(e)
	if err != nil {
		return ""
	}
	return string(b)
}

// Derive computes a State from events. The result is independent of the order
// the events were concatenated in: events are first sorted by parsed TS (an
// unparseable TS sorts after every parsed one), then Task, then kind rank,
// then canonical JSON. Per task the latest status-bearing event decides the
// status; amended only updates brief/needs/owns. A result-bearing event whose
// non-empty attempt differs from the task's latest dispatched attempt is
// stale: it changes no field and is recorded in Stale.
// derivationOrder returns events in the order Derive replays them: by parsed
// TS (an unparseable TS sorts after every parsed one), then Task, then kind
// rank, then canonical JSON — independent of the order the events were
// concatenated in. Anything that summarises a task alongside Derive's status
// uses it, so the two can never disagree about which record is latest.
func derivationOrder(events []Event) []Event {
	keys := make([]eventSortKey, len(events))
	for i, e := range events {
		t, perr := time.Parse(time.RFC3339Nano, e.TS)
		keys[i] = eventSortKey{Event: e, Time: t, Valid: perr == nil, Kind: kindRank[e.Kind], Canon: canonical(e)}
	}
	slices.SortStableFunc(keys, func(a, b eventSortKey) int {
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
		if c := strings.Compare(a.Event.Task, b.Event.Task); c != 0 {
			return c
		}
		if c := a.Kind - b.Kind; c != 0 {
			return c
		}
		return strings.Compare(a.Canon, b.Canon)
	})

	evs := make([]Event, len(keys))
	for i, k := range keys {
		evs[i] = k.Event
	}
	return evs
}

func Derive(events []Event) State {
	evs := derivationOrder(events)

	var tasks map[string]TaskState = map[string]TaskState{}
	cur := map[string]string{}
	disp := map[string]bool{}
	updated := ""
	for _, e := range evs {
		if e.Task == "" {
			continue // floor-level events (staffed) carry no task and no state
		}
		ts, ok := tasks[e.Task]
		if !ok {
			ts = TaskState{ID: e.Task}
		}
		if staleKinds[e.Kind] && e.Attempt != "" && disp[e.Task] && e.Attempt != cur[e.Task] {
			ts.Stale = append(ts.Stale, e.Kind+" "+e.Attempt)
			tasks[e.Task] = ts
			continue
		}
		switch e.Kind {
		case "planned":
			ts.Status = "planned"
			ts.Brief = e.Brief
			ts.Needs = NeedTargets(e.Needs...)
			ts.Owns = e.Owns
		case "dispatched":
			ts.Status = "dispatched"
			ts.Attempts++
			ts.Increment = e.Increment
			if e.Attempt != "" {
				cur[e.Task] = e.Attempt
				disp[e.Task] = true
			}
		case "started":
			ts.Status = "running"
		case "finished":
			ts.Status = "finished"
		case "reviewed":
			// The review agent reads, the gauges measure (issue #389): a
			// reviewed event the agent wrote (persona reviewer with an
			// adapter) never passes a unit; its pass leaves the status
			// unchanged and only validate+inspect or a hand-recorded review
			// (which re-runs the gates) set passed. Its correct still counts.
			agent := e.Persona == "reviewer" && e.Adapter != ""
			switch e.Verdict {
			case "pass":
				if !agent {
					ts.Status = "passed"
				}
			case "correct":
				ts.Status = "needs-correction"
			case "reject":
				ts.Status = "rejected"
			}
		case "blocked":
			ts.Status = "blocked"
		case "lost":
			ts.Status = "lost"
		case "landed":
			ts.Status = "landed"
		case "inspected":
			switch e.Verdict {
			case "pass":
				ts.Status = "passed"
			case "rework":
				ts.Status = "needs-correction"
			case "scrap":
				ts.Status = "rejected"
			case "escalate":
				ts.Status = "blocked"
			}
		case "amended":
			ts.Brief = e.Brief
			ts.Needs = NeedTargets(e.Needs...)
			ts.Owns = e.Owns
		}
		if e.Session != "" && (e.Kind == "started" || e.Kind == "finished") {
			ts.Session = e.Session
		}
		if e.Model != "" {
			ts.Model = e.Model
		}
		if e.Attempt != "" {
			ts.Attempt = e.Attempt
		}
		if e.RC != nil {
			ts.RC = e.RC
		}
		// An audit's verdict describes the audit, not the unit's QC state:
		// it never replaces the inspector's or reviewer's verdict (#301 review).
		if e.Verdict != "" && e.Kind != "audited" {
			ts.Verdict = e.Verdict
		}
		if e.Reason != "" {
			ts.Reason = e.Reason
		}
		ts.UpdatedAt = e.TS
		updated = e.TS
		tasks[e.Task] = ts
	}

	var st State
	st.Version = 2
	st.UpdatedAt = updated
	st.Tasks = []TaskState{}
	ids := taskKeys(tasks)
	slices.Sort(ids)
	for _, id := range ids {
		st.Tasks = append(st.Tasks, tasks[id])
	}
	counts := map[string]int{}
	for _, ts := range st.Tasks {
		counts[ts.Status] = counts[ts.Status] + 1
	}
	st.Counts = counts
	return st
}

func taskKeys(m map[string]TaskState) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// WriteState derives the state from the event log, writes .flywheel/state.json
// via a temp file plus rename, and updates the marked status block in
// flywheel.md. Same input gives byte-identical output.
func WriteState(dir string) (State, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return State{}, err
	}
	st := Derive(events)
	if err := writeStateJSON(dir, st); err != nil {
		return State{}, err
	}
	if err := updateStatusBlock(dir, st); err != nil {
		return State{}, err
	}
	return st, nil
}

func writeStateJSON(dir string, st State) error {
	dot := filepath.Join(dir, ".flywheel")
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	b = append(b, '\n')
	return atomicWrite(dot, "state.json", "state-*.json", b)
}

// atomicWrite writes b to dir/name via a temp file in the same directory plus
// os.Rename, so a crash never leaves a half-written destination file.
func atomicWrite(dir, name, pattern string, b []byte) error {
	return atomicWriteChecked(dir, name, pattern, b, nil)
}

// atomicWriteChecked is atomicWrite with a verification hook: check runs
// immediately before the os.Rename, and a failing check aborts the write
// (removing the temp file) without touching the destination. A nil check
// behaves exactly as atomicWrite.
func atomicWriteChecked(dir, name, pattern string, b []byte, check func() error) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return fmt.Errorf("create temp %s: %w", pattern, err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write temp %s: %w", pattern, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("close temp %s: %w", pattern, err)
	}
	if check != nil {
		if err := check(); err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rename %s: %w", name, err)
	}
	return nil
}

// statusBlock is the inner text placed between the two markers: a leading
// newline, the table, and a trailing newline.
func statusBlock(st State) string {
	body := "| Task | Status | Attempts | Session | Model | Updated |\n" +
		"| --- | --- | --- | --- | --- | --- |\n"
	for _, ts := range st.Tasks {
		body = body + fmt.Sprintf("| %s | %s | %d | %s | %s | %s |\n",
			ts.ID, ts.Status, ts.Attempts, ts.Session, ts.Model, ts.UpdatedAt)
	}
	return "\n" + body + "\n"
}

func updateStatusBlock(dir string, st State) error {
	mdPath := filepath.Join(dir, "flywheel.md")
	block := statusStartMarker + statusBlock(st) + statusEndMarker + "\n"
	b, err := os.ReadFile(mdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return atomicWrite(dir, "flywheel.md", "md-*.md", []byte(block))
		}
		return fmt.Errorf("read %s: %w", mdPath, err)
	}
	content := string(b)
	start := strings.Index(content, statusStartMarker)
	end := strings.Index(content, statusEndMarker)
	if start < 0 || end < 0 {
		sep := ""
		if !strings.HasSuffix(content, "\n") {
			sep = "\n"
		}
		content = content + sep + block
	} else {
		content = content[:start+len(statusStartMarker)] + statusBlock(st) + content[end:]
	}
	return atomicWrite(dir, "flywheel.md", "md-*.md", []byte(content))
}
