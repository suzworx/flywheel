package flywheel

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// cfdStart is the synthetic ledgers' first instant.
var cfdStart = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

// cfdEv is an event on task at cfdStart plus d.
func cfdEv(d time.Duration, task, kind string, mods ...func(*Event)) Event {
	e := Event{TS: cfdStart.Add(d).Format(time.RFC3339Nano), Task: task, Kind: kind}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func withAttempt(a string) func(*Event) { return func(e *Event) { e.Attempt = a } }
func withVerdict(v string) func(*Event) { return func(e *Event) { e.Verdict = v } }

// cfdLedger is units units, one every 20 minutes, cycling through six paths:
// inspected and landed; corrected with a stale r1 finished, an agent pass that
// passes nothing and a hand pass; lost; withdrawn and re-planned then left
// running; escalated to blocked; finished and left to go stale. A group
// record, a gate probe and a task-less event ride along.
func cfdLedger(units int) []Event {
	h, m := time.Hour, time.Minute
	out := []Event{cfdEv(0, "", "staffed"), cfdEv(m, "group:g1", "group_reviewed")}
	for u := 0; u < units; u++ {
		t, b := fmt.Sprintf("C%03d", u), time.Duration(u)*20*m
		r1 := withAttempt("r1")
		out = append(out, cfdEv(b, t, "gate_probed"), cfdEv(b, t, "planned"))
		if u%6 == 3 {
			out = append(out, cfdEv(b+h, t, "withdrawn"), cfdEv(b+2*h, t, "planned"), cfdEv(b+3*h, t, "dispatched", r1))
			continue
		}
		out = append(out, cfdEv(b+30*m, t, "dispatched", r1), cfdEv(b+40*m, t, "started", r1), cfdEv(b+2*h, t, "finished", r1))
		switch u % 6 {
		case 0:
			out = append(out, cfdEv(b+3*h, t, "inspected", withVerdict("pass")), cfdEv(b+5*h, t, "landed"))
		case 1:
			r2 := withAttempt("r2")
			agent := func(e *Event) { e.Verdict, e.Persona, e.Adapter = "pass", "reviewer", "claude" }
			out = append(out, cfdEv(b+3*h, t, "reviewed", withVerdict("correct")), cfdEv(b+4*h, t, "dispatched", r2),
				cfdEv(b+4*h+30*m, t, "finished", r1), cfdEv(b+6*h, t, "finished", r2), cfdEv(b+7*h, t, "reviewed", agent),
				cfdEv(b+8*h, t, "reviewed", withVerdict("pass")), cfdEv(b+9*h, t, "landed"))
		case 2:
			out = append(out, cfdEv(b+3*h, t, "lost", r1))
		case 4:
			out = append(out, cfdEv(b+3*h, t, "inspected", withVerdict("escalate")))
		}
	}
	return out
}

// refStages is the per-bucket reference from one Derive per time, as wipTasks
// computed it before issue #630: per stage, the non-stale units in it at t.
func refStages(ev []timed, t time.Time, staleAfter time.Duration) map[string]int {
	var prefix []Event
	last := map[string]time.Time{}
	for _, e := range ev {
		if !e.At.Before(t) {
			break
		}
		prefix = append(prefix, e.Event)
		if e.Task != "" {
			last[e.Task] = e.At
		}
	}
	out := map[string]int{}
	for _, ts := range Derive(prefix).Tasks {
		if st := flowStage(ts.Status); st != "" && last[ts.ID].After(t.Add(-staleAfter)) {
			out[st]++
		}
	}
	return out
}

// TestFlowStageSeries: units move planned → dispatched → finished → passed →
// landed across buckets; a finished unit with no event for StaleAfter drops
// out of every band.
func TestFlowStageSeries(t *testing.T) {
	t.Parallel()
	m := time.Minute
	events := []Event{
		cfdEv(10*m, "A", "planned"), cfdEv(70*m, "A", "dispatched", withAttempt("r1")),
		cfdEv(80*m, "A", "started", withAttempt("r1")), cfdEv(130*m, "A", "finished", withAttempt("r1")),
		cfdEv(190*m, "A", "inspected", withVerdict("pass")), cfdEv(250*m, "A", "landed"),
		cfdEv(20*m, "B", "planned"), cfdEv(30*m, "B", "dispatched", withAttempt("r1")),
		cfdEv(40*m, "B", "finished", withAttempt("r1")),
	}
	w := MetricsWindow{Since: cfdStart, Until: cfdStart.Add(6 * time.Hour), Bucket: time.Hour, StaleAfter: 3 * time.Hour}
	f := Metrics(events, Config{}, w).Flow
	want := map[string][]float64{
		"queued":   {1, 0, 0, 0, 0, 0},
		"running":  {0, 1, 0, 0, 0, 0},
		"finished": {1, 1, 2, 0, 0, 0},
		"passed":   {0, 0, 0, 1, 0, 0},
	}
	for _, s := range FlowStages {
		if !slices.Equal(f.StageSeries[s], want[s]) {
			t.Errorf("%s = %v, want %v", s, f.StageSeries[s], want[s])
		}
	}
	if w := []float64{1, 2, 2, 1, 0, 0}; !slices.Equal(f.WIPSeries, w) {
		t.Errorf("wip = %v, want %v", f.WIPSeries, w)
	}
}

// cfdWindow covers cfdLedger(300) in 1h buckets with a 6h stale threshold.
var cfdWindow = MetricsWindow{Since: cfdStart, Until: cfdStart.Add(112 * time.Hour), Bucket: time.Hour, StaleAfter: 6 * time.Hour}

// TestFlowStageSeriesInvariant: running+finished+passed is WIPSeries at every
// bucket of a 300-unit ledger.
func TestFlowStageSeriesInvariant(t *testing.T) {
	t.Parallel()
	f := Metrics(cfdLedger(300), Config{}, cfdWindow).Flow
	for i, wip := range f.WIPSeries {
		if sum := f.StageSeries["running"][i] + f.StageSeries["finished"][i] + f.StageSeries["passed"][i]; sum != wip {
			t.Errorf("bucket %d: running+finished+passed = %v, wip %v", i, sum, wip)
		}
	}
}

// TestWIPSeriesMatchesDerive: the one-walk series equal one Derive per bucket
// on every bucket of two 300-unit ledgers, and the window's-end WIP and stale
// counts agree too.
func TestWIPSeriesMatchesDerive(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		events []Event
		w      MetricsWindow
	}{
		"cfd":  {cfdLedger(300), cfdWindow},
		"perf": {perfLedger(300), MetricsWindow{Since: cfdStart, Until: cfdStart.Add(204 * time.Hour), Bucket: time.Hour, StaleAfter: 12 * time.Hour}},
	} {
		ev := timedEvents(c.events)
		f := Metrics(c.events, Config{}, c.w).Flow
		seen := map[string]bool{}
		for i, b := range c.w.buckets() {
			end := b.Add(c.w.Bucket)
			if end.After(c.w.Until) {
				end = c.w.Until
			}
			ref := refStages(ev, end, c.w.staleAfter())
			if wip := float64(ref["running"] + ref["finished"] + ref["passed"]); f.WIPSeries[i] != wip {
				t.Errorf("%s bucket %d: wip %v, Derive %v", name, i, f.WIPSeries[i], wip)
			}
			for _, s := range FlowStages {
				seen[s] = seen[s] || ref[s] > 0
				if got := f.StageSeries[s][i]; got != float64(ref[s]) {
					t.Errorf("%s bucket %d: %s %v, Derive %d", name, i, s, got, ref[s])
				}
			}
		}
		for _, s := range FlowStages {
			if name == "cfd" && !seen[s] {
				t.Errorf("cfd ledger never fills %s", s)
			}
		}
		ref := refStages(ev, c.w.Until, c.w.staleAfter())
		if want := ref["running"] + ref["finished"] + ref["passed"]; f.WIP != want {
			t.Errorf("%s: wip at the end %d, Derive %d", name, f.WIP, want)
		}
	}
}

// TestFlowStageJSON: the report's JSON carries flow.stage_series with the
// four stages, one count per bucket each.
func TestFlowStageJSON(t *testing.T) {
	t.Parallel()
	b, err := json.Marshal(Metrics(cfdLedger(12), Config{}, cfdWindow))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Flow struct {
			StageSeries map[string][]float64 `json:"stage_series"`
		} `json:"flow"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Flow.StageSeries) != len(FlowStages) {
		t.Fatalf("stage_series = %v, want the keys %v", doc.Flow.StageSeries, FlowStages)
	}
	for _, s := range FlowStages {
		if got := len(doc.Flow.StageSeries[s]); got != 112 {
			t.Errorf("stage_series[%s] has %d buckets, want 112", s, got)
		}
	}
}

// TestTUIWIPChartStages: the WIP drill-down draws one band per stage with the
// stage names in its legend, and an older report without stage series still
// draws the single total band.
func TestTUIWIPChartStages(t *testing.T) {
	t.Parallel()
	i := slices.IndexFunc(metricDefs, func(d metricDef) bool { return d.id == "flow.wip" })
	if i < 0 {
		t.Fatal("no flow.wip metric")
	}
	rep := Metrics(cfdLedger(60), Config{}, cfdWindow)
	chart := strings.Join(metricChart(metricDefs[i], rep, nil, 100), "\n")
	for _, s := range FlowStages {
		if !strings.Contains(chart, s) {
			t.Errorf("chart has no %q legend:\n%s", s, chart)
		}
	}
	rep.Flow.StageSeries = nil
	lines := metricChart(metricDefs[i], rep, nil, 100)
	old := strings.Join(lines, "\n")
	if len(lines) != 10 || !strings.Contains(old, "wip") || strings.Contains(old, "queued") || strings.Contains(old, "stacked flow") {
		t.Errorf("chart without stage series:\n%s", old)
	}
}
