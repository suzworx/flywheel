package flywheel

import (
	"fmt"
)

// GateBlocker is one reason a session should not end yet.
type GateBlocker struct {
	Kind   string `json:"kind"` // "uninspected" or "untriaged"
	Task   string `json:"task"`
	Detail string `json:"detail"` // human text, e.g. "finished r1, not yet inspected" or "r1 no-plan (.flywheel/runs/T1.r1.jsonl)"
}

// GateResult lists everything that blocks ending the session; empty means clear.
type GateResult struct {
	Blockers []GateBlocker `json:"blockers"`
}

// OK reports whether nothing blocks.
func (g GateResult) OK() bool { return len(g.Blockers) == 0 }

// Gate returns a GateResult listing every reason a session should not end yet:
// every finished task not yet inspected, and every untriaged signal.
func Gate(events []Event) GateResult {
	var blockers []GateBlocker

	state := Derive(events)
	taskAttempt := map[string]string{}
	for _, e := range events {
		if e.Kind == "finished" && e.Task != "" && e.Attempt != "" {
			taskAttempt[e.Task] = e.Attempt
		}
	}

	for _, task := range state.Tasks {
		if task.Status == "finished" {
			attempt := taskAttempt[task.ID]
			if attempt == "" {
				attempt = "?"
			}
			blockers = append(blockers, GateBlocker{
				Kind:   "uninspected",
				Task:   task.ID,
				Detail: fmt.Sprintf("finished %s, not yet inspected", attempt),
			})
		}
	}

	for _, sig := range UntriagedSignals(events) {
		detail := sig.Attempt + " " + sig.Signal
		if sig.Path != "" {
			detail = detail + " (" + sig.Path + ")"
		}
		blockers = append(blockers, GateBlocker{
			Kind:   "untriaged",
			Task:   sig.Task,
			Detail: detail,
		})
	}

	if blockers == nil {
		blockers = []GateBlocker{}
	}

	return GateResult{Blockers: blockers}
}
