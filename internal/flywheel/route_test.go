package flywheel

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
)

// routeLedger appends n accepted, gate-passing tasks of model, each attempt
// costing cost, as modelStats reads them (issue #474).
func routeLedger(events []Event, model string, n int, cost float64) []Event {
	zero := 0
	for i := range n {
		task := fmt.Sprintf("%s-%d", model, i)
		ts := func(s int) string { return fmt.Sprintf("2026-09-20T%02d:%02d:%02dZ", len(events)%24, i, s) }
		events = append(events,
			Event{TS: ts(1), Task: task, Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: model},
			Event{TS: ts(2), Task: task, Kind: "finished", Attempt: "r1", Reason: "stop", Cost: cost},
			Event{TS: ts(3), Task: task, Kind: "validated", Attempt: "r1", Gate: "g1", Tree: "t", RC: &zero},
			Event{TS: ts(4), Task: task, Kind: "inspected", Verdict: "pass"},
		)
	}
	return events
}

// routeWorker is a claude worker routing over candidates by cost_per_accepted.
func routeWorker(explore float64, candidates ...string) Worker {
	return Worker{Name: "w", Adapter: "claude", Model: "m", Routing: &Routing{
		Candidates: candidates, Objective: "cost_per_accepted", Explore: explore, Seed: "s",
	}}
}

func TestRouteExploit(t *testing.T) {
	t.Parallel()
	events := routeLedger(routeLedger(nil, "A", 3, 2), "B", 3, 1)
	c := routeModel(events, routeWorker(0, "A", "B"), "T9")
	if c.Model != "B" || c.Pick != "exploit" || c.Objective != "cost_per_accepted" {
		t.Fatalf("routeModel() = %+v, want exploit B by cost_per_accepted", c)
	}
	if len(c.Scores) != 2 {
		t.Fatalf("scores = %+v, want 2", c.Scores)
	}
	for i, want := range []float64{2, 1} {
		s := c.Scores[i]
		if s.Attempts != 3 || s.Score == nil || *s.Score != want {
			t.Errorf("scores[%d] = %+v (score %v), want 3 attempts, score %v", i, s, s.Score, want)
		}
	}
}

func TestRouteExplore(t *testing.T) {
	t.Parallel()
	events := routeLedger(routeLedger(nil, "A", 3, 2), "B", 3, 1)
	c := routeModel(events, routeWorker(1, "A", "B"), "T9")
	if c.Model != "A" || c.Pick != "explore" {
		t.Fatalf("routeModel() = %+v, want explore A", c)
	}
}

func TestRouteUndersampled(t *testing.T) {
	t.Parallel()
	events := routeLedger(nil, "C", 1, 1)
	for i := range 20 {
		task := fmt.Sprintf("T%d", i)
		c := routeModel(events, routeWorker(0, "A", "B", "C"), task)
		if c.Pick != "explore" || !slices.Contains([]string{"A", "B"}, c.Model) {
			t.Fatalf("routeModel(%s) = %+v, want explore to A or B, never C", task, c)
		}
	}
}

// kindLedger appends routeLedger's tasks of model, each renamed under kind
// and planned with a brief of that kind (issue #475).
func kindLedger(events []Event, kind, model string, n int, cost float64) []Event {
	for _, e := range routeLedger(nil, model, n, cost) {
		e.Task = kind + "-" + e.Task
		if e.Kind == "dispatched" {
			events = append(events, Event{TS: e.TS, Task: e.Task, Kind: "planned", Header: &BriefHeader{Kind: kind}})
		}
		events = append(events, e)
	}
	return events
}

// TestRouteModelKind checks per-kind routing (issue #475): A is best
// model-wide, B best on refactor.
func TestRouteModelKind(t *testing.T) {
	t.Parallel()
	events := routeLedger(routeLedger(nil, "A", 10, 1), "B", 3, 5)
	events = kindLedger(kindLedger(events, "refactor", "A", 3, 9), "refactor", "B", 3, 2)
	events = append(events,
		Event{TS: "2026-09-21T00:00:00Z", Task: "R1", Kind: "planned", Header: &BriefHeader{Kind: "refactor"}},
		Event{TS: "2026-09-21T00:00:00Z", Task: "D1", Kind: "planned", Header: &BriefHeader{Kind: "docs"}},
		Event{TS: "2026-09-21T00:00:00Z", Task: "N1", Kind: "planned", Header: &BriefHeader{}},
	)
	w := routeWorker(0, "A", "B")
	if c := routeModel(events, w, "R1"); c.Model != "B" || c.Pick != "exploit" || c.Basis != "kind" || c.Kind != "refactor" ||
		c.Scores[0].Attempts != 3 || *c.Scores[0].Score != 9 || *c.Scores[1].Score != 2 {
		t.Errorf("refactor task: routeModel() = %+v, want exploit B on the refactor rows", c)
	}
	wide := routeScores(modelStats(events), w)
	if c := routeModel(events, w, "D1"); c.Model != "A" || c.Pick != "exploit" || c.Basis != "model" || c.Kind != "docs" || !reflect.DeepEqual(c.Scores, wide) {
		t.Errorf("docs task: routeModel() = %+v, want exploit A on the model-wide rows, kind docs", c)
	}
	// A task without a kind routes as before the kind existed: same model,
	// pick, draw and model-wide scores.
	for _, task := range []string{"N1", "T9"} {
		want := RouteChoice{Model: "A", Pick: "exploit", Objective: "cost_per_accepted", Draw: round(routeDraw("s\x00"+task+"\x000"), 4), Basis: "model", Scores: wide}
		if c := routeModel(events, w, task); !reflect.DeepEqual(c, want) {
			t.Errorf("%s: routeModel() = %+v, want %+v", task, c, want)
		}
	}
	// Only A has refactor evidence and B is best model-wide: the choice
	// stays on the refactor rows, where B is not eligible to exploit.
	only := kindLedger(routeLedger(routeLedger(nil, "A", 3, 5), "B", 3, 1), "refactor", "A", 3, 9)
	only = append(only, Event{TS: "2026-09-21T00:00:00Z", Task: "R1", Kind: "planned", Header: &BriefHeader{Kind: "refactor"}})
	if c := routeModel(only, w, "R1"); c.Model != "A" || c.Pick != "exploit" || c.Basis != "kind" || c.Scores[1].Attempts != 0 {
		t.Errorf("one-sided refactor evidence: routeModel() = %+v, want exploit A on the refactor rows", c)
	}
}

func TestRouteDeterministic(t *testing.T) {
	t.Parallel()
	events := routeLedger(routeLedger(nil, "A", 3, 2), "B", 3, 1)
	w := routeWorker(0.5, "A", "B")
	first, second := routeModel(events, w, "T9"), routeModel(events, w, "T9")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("routeModel() not deterministic: %+v then %+v", first, second)
	}
	more := append(slices.Clone(events), Event{TS: "2026-09-21T00:00:00Z", Task: "T9", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "A"})
	if next := routeModel(more, w, "T9"); next.Draw == first.Draw {
		t.Errorf("draw %v unchanged after another dispatch of the task", next.Draw)
	}
}
