package flywheel

import (
	"fmt"
	"strings"
)

// LearningView is one learning: its stable id, the task it was observed on,
// its curation fields, and its dismissal state.
type LearningView struct {
	ID        string   `json:"id"`
	Task      string   `json:"task"`
	Severity  string   `json:"severity"`
	Title     string   `json:"title"`
	Observed  string   `json:"observed"`
	Evidence  string   `json:"evidence"`
	Ask       string   `json:"ask"`
	Signals   []string `json:"signals,omitempty"`
	Dismissed bool     `json:"dismissed"`
	Reason    string   `json:"reason,omitempty"`
}

// Learnings folds the event log into one LearningView per learning event, in
// log order, assigning ids L-01, L-02, ... in the order learning events
// appear. A later dismissed event marks the learning it targets by id;
// dismissing never removes or renumbers a learning.
func Learnings(events []Event) []LearningView {
	var out []LearningView
	index := map[string]int{}
	n := 0
	for _, e := range events {
		if e.Kind != "learning" {
			continue
		}
		n++
		id := fmt.Sprintf("L-%02d", n)
		index[id] = len(out)
		out = append(out, LearningView{
			ID: id, Task: e.Task, Severity: e.Severity, Title: e.Title,
			Observed: e.Observed, Evidence: e.Evidence, Ask: e.Ask, Signals: e.Signals,
		})
	}
	for _, e := range events {
		if e.Kind != "dismissed" {
			continue
		}
		if i, ok := index[e.ID]; ok {
			out[i].Dismissed = true
			out[i].Reason = e.Note
		}
	}
	return out
}

// NextLearningID returns the next L-NN id in log order.
func NextLearningID(events []Event) string {
	n := 0
	for _, e := range events {
		if e.Kind == "learning" {
			n++
		}
	}
	return fmt.Sprintf("L-%02d", n+1)
}

// WriteLearningsFile rewrites <dir>/learnings.md atomically (temp file plus
// rename) from views, in log order: a heading, then one section per learning
// with its severity, observed, evidence, ask, signals (when given) and
// dismissed reason (when dismissed).
func WriteLearningsFile(dir string, views []LearningView) error {
	var b strings.Builder
	b.WriteString("# Learnings\n")
	for _, v := range views {
		fmt.Fprintf(&b, "\n## %s — %s\n", v.ID, v.Title)
		fmt.Fprintf(&b, "severity: %s\n", v.Severity)
		fmt.Fprintf(&b, "observed: %s\n", v.Observed)
		fmt.Fprintf(&b, "evidence: %s\n", v.Evidence)
		fmt.Fprintf(&b, "ask: %s\n", v.Ask)
		if len(v.Signals) > 0 {
			fmt.Fprintf(&b, "signals: %s\n", strings.Join(v.Signals, ", "))
		}
		if v.Dismissed {
			fmt.Fprintf(&b, "dismissed: %s\n", v.Reason)
		}
	}
	return atomicWrite(dir, "learnings.md", "learnings-*.md", []byte(b.String()))
}
