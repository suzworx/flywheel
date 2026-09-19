package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	handoffStartMarker = "<!-- flywheel:handoff:start -->"
	handoffEndMarker   = "<!-- flywheel:handoff:end -->"
)

// HandoffTask is one in-flight task: its id, derived status (dispatched or
// running) and, when the log carries them, the session and model.
type HandoffTask struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Session string `json:"session,omitempty"`
	Model   string `json:"model,omitempty"`
}

// Handoff is the derived handoff summary: one list per section, tasks sorted
// by id, plus the default worker model from the config. Untriaged carries
// forward any untriaged signals so a new head can triage them (rule T9).
type Handoff struct {
	InFlight  []HandoffTask `json:"in_flight,omitempty"`
	Blocked   []string      `json:"blocked,omitempty"`
	Ready     []string      `json:"ready,omitempty"`
	Untriaged []SignalView  `json:"untriaged,omitempty"`
	Model     string        `json:"model"`
}

// DeriveHandoff derives the handoff lists from events. In-flight holds the
// tasks whose derived status is dispatched or running, blocked the blocked
// ones, and ready the planned tasks whose needs (from the latest
// planned/amended event) are all among the landed or passed tasks; a planned
// task with no needs is ready. Untriaged carries forward any signals not yet
// triaged, so the next head can judge them (rule T9). The derived state
// sorts tasks by id, so each list is sorted too.
func DeriveHandoff(events []Event) Handoff {
	st := Derive(events)
	var done map[string]bool = map[string]bool{}
	for _, ts := range st.Tasks {
		if ts.Status == "landed" || ts.Status == "passed" {
			done[ts.ID] = true
		}
	}
	var h Handoff
	for _, ts := range st.Tasks {
		switch ts.Status {
		case "dispatched", "running":
			h.InFlight = append(h.InFlight, HandoffTask{ID: ts.ID, Status: ts.Status, Session: ts.Session, Model: ts.Model})
		case "blocked":
			h.Blocked = append(h.Blocked, ts.ID)
		case "planned":
			ok := true
			for _, need := range ts.Needs {
				if !done[need] {
					ok = false
				}
			}
			if ok {
				h.Ready = append(h.Ready, ts.ID)
			}
		}
	}
	h.Untriaged = UntriagedSignals(events)
	return h
}

// HandoffSummary derives the handoff from the event log and the config's
// default worker model. A missing event log means an empty summary (not an
// error); a missing config means the built-in default model.
func HandoffSummary(dir string) (Handoff, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return Handoff{}, err
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return Handoff{}, err
	}
	h := DeriveHandoff(events)
	h.Model = cfg.DefaultWorker().Model
	return h, nil
}

// HandoffText renders the summary one line per section, tasks sorted by id:
// in-flight: t1 (running, session ses_1, model m1)
// blocked: t2
// ready: t3 t4
// untriaged signals: T1 r1 no-plan
// model: <the default worker model>
// The session and model appear only when the task has them; an empty section
// reads none. Untriaged signals are carried forward so the next head can
// triage them; an empty list reads "untriaged signals: none" (rule T9).
func HandoffText(h Handoff) string {
	return strings.Join([]string{
		inFlightLine(h.InFlight),
		sectionLine("blocked", h.Blocked),
		sectionLine("ready", h.Ready),
		untriagedLine(h.Untriaged),
		"model: " + h.Model,
	}, "\n")
}

// sectionLine renders one id list as "label: id1 id2", or "label: none".
func sectionLine(label string, ids []string) string {
	if len(ids) == 0 {
		return label + ": none"
	}
	return label + ": " + strings.Join(ids, " ")
}

// inFlightLine renders the in-flight tasks, each as
// "id (status[, session S][, model M])", or "in-flight: none".
func inFlightLine(ts []HandoffTask) string {
	if len(ts) == 0 {
		return "in-flight: none"
	}
	var parts []string
	for _, t := range ts {
		parts = append(parts, handoffTaskText(t))
	}
	return "in-flight: " + strings.Join(parts, " ")
}

// handoffTaskText renders one in-flight task; the session and model appear
// only when the task has them.
func handoffTaskText(t HandoffTask) string {
	parts := []string{t.Status}
	if t.Session != "" {
		parts = append(parts, "session "+t.Session)
	}
	if t.Model != "" {
		parts = append(parts, "model "+t.Model)
	}
	return t.ID + " (" + strings.Join(parts, ", ") + ")"
}

// untriagedLine renders the untriaged signals as "untriaged signals: none"
// when empty, otherwise "untriaged signals: " + entries joined with ", ",
// each entry as "<task> <attempt> <signal>".
func untriagedLine(sigs []SignalView) string {
	if len(sigs) == 0 {
		return "untriaged signals: none"
	}
	var parts []string
	for _, s := range sigs {
		parts = append(parts, s.Task+" "+s.Attempt+" "+s.Signal)
	}
	return "untriaged signals: " + strings.Join(parts, ", ")
}

// WriteHandoff writes the summary into flywheel.md between the handoff
// markers, replacing an existing block or appending one, the same way the
// status block is updated. Same input gives byte-identical output.
func WriteHandoff(dir string, h Handoff) error {
	mdPath := filepath.Join(dir, "flywheel.md")
	block := handoffStartMarker + handoffBlock(h) + handoffEndMarker + "\n"
	b, err := os.ReadFile(mdPath)
	if err != nil {
		if os.IsNotExist(err) {
			return atomicWrite(dir, "flywheel.md", "md-*.md", []byte(block))
		}
		return fmt.Errorf("read %s: %w", mdPath, err)
	}
	content := string(b)
	start := strings.Index(content, handoffStartMarker)
	end := strings.Index(content, handoffEndMarker)
	if start < 0 || end < 0 {
		sep := ""
		if !strings.HasSuffix(content, "\n") {
			sep = "\n"
		}
		content = content + sep + block
	} else {
		content = content[:start+len(handoffStartMarker)] + handoffBlock(h) + content[end:]
	}
	return atomicWrite(dir, "flywheel.md", "md-*.md", []byte(content))
}

// handoffBlock is the inner text placed between the two markers: a leading
// newline, the summary lines, and a trailing newline.
func handoffBlock(h Handoff) string {
	return "\n" + HandoffText(h) + "\n"
}
