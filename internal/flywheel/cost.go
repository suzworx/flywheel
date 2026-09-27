package flywheel

import (
	"fmt"
	"slices"
)

// CostRow is one per-task or per-model cost line: the id, the summed Tokens
// struct and the summed cost of its spend events (spendEvent). Review is the
// part of Cost that came from reviewed events (issue #459).
type CostRow struct {
	ID     string  `json:"id,omitempty"`
	Tokens Tokens  `json:"tokens"`
	Cost   float64 `json:"cost"`
	Review float64 `json:"review,omitempty"`
}

// spendEvent reports whether e records spend: a finished event, or a reviewed
// event carrying a review agent's cost or tokens (issue #459). A hand verdict
// carries neither. Every spend sum (flywheel cost, the unit cost cap, the
// wave budget, the floor) counts exactly these events.
func spendEvent(e Event) bool {
	return e.Kind == "finished" || (e.Kind == "reviewed" && (e.Cost > 0 || e.Tokens != nil))
}

// CostReport is the summed cost view over the log's spend events: one row
// per task and per model with ids sorted, plus the grand total.
type CostReport struct {
	Tasks  []CostRow `json:"tasks"`
	Models []CostRow `json:"models"`
	Total  CostRow   `json:"total"`
}

// Count returns the five-component token sum of the row.
func (r CostRow) Count() int {
	return r.Tokens.Input + r.Tokens.Output + r.Tokens.Reasoning +
		r.Tokens.CacheRead + r.Tokens.CacheWrite
}

// Cost reads the event log and sums the Tokens and cost of every spend event
// (spendEvent), grouped per task and per model. A reviewed event is charged
// to its own Model field, else "unknown", and its cost is also summed into
// the rows' Review. A finished event is charged to the
// model on the dispatched event of the same task and attempt (issue #473), so
// a finish logged after a later attempt's dispatch still counts under its own
// attempt's model. A finished event without an attempt, or whose attempt has
// no dispatched event, falls back to the task's latest dispatched model
// preceding it in log order; with none it lands under "unknown". An empty log
// yields zeroed rows, not an error.
func Cost(dir string) (CostReport, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return CostReport{}, fmt.Errorf("cost %s: %w", dir, err)
	}
	attemptModel := dispatchedModels(events)
	model := map[string]string{}
	byTask := map[string]*CostRow{}
	byModel := map[string]*CostRow{}
	var total CostRow
	for _, e := range events {
		if e.Kind == "dispatched" && e.Model != "" {
			model[e.Task] = e.Model
		}
		if !spendEvent(e) {
			continue
		}
		t := e.Tokens
		if t == nil {
			t = &Tokens{}
		}
		review := 0.0
		m := e.Model
		if e.Kind == "reviewed" {
			review = e.Cost
		} else {
			m = attemptModel[attemptKey{task: e.Task, attempt: e.Attempt}]
			if e.Attempt == "" || m == "" {
				m = model[e.Task]
			}
		}
		if m == "" {
			m = "unknown"
		}
		if byTask[e.Task] == nil {
			byTask[e.Task] = &CostRow{ID: e.Task}
		}
		if byModel[m] == nil {
			byModel[m] = &CostRow{ID: m}
		}
		for _, r := range []*CostRow{byTask[e.Task], byModel[m], &total} {
			addCostRow(r, *t, e.Cost)
			r.Review += review
		}
	}
	return CostReport{
		Tasks:  sortedCostRows(byTask),
		Models: sortedCostRows(byModel),
		Total:  total,
	}, nil
}

// dispatchedModels maps each (task, attempt) to the model on its dispatched
// event; a later dispatched line for the same attempt wins.
func dispatchedModels(events []Event) map[attemptKey]string {
	out := map[attemptKey]string{}
	for _, e := range events {
		if e.Kind == "dispatched" && e.Attempt != "" && e.Model != "" {
			out[attemptKey{task: e.Task, attempt: e.Attempt}] = e.Model
		}
	}
	return out
}

// addCostRow sums t and cost into r.
func addCostRow(r *CostRow, t Tokens, cost float64) {
	r.Tokens.Input += t.Input
	r.Tokens.Output += t.Output
	r.Tokens.Reasoning += t.Reasoning
	r.Tokens.CacheRead += t.CacheRead
	r.Tokens.CacheWrite += t.CacheWrite
	r.Cost += cost
}

// sortedCostRows returns the map's rows sorted by id.
func sortedCostRows(m map[string]*CostRow) []CostRow {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	rows := make([]CostRow, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, *m[id])
	}
	return rows
}
