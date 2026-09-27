package flywheel

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"
)

// metricsFixture parses testdata/metrics-ledger.jsonl: seven units over
// 2026-08-31..09-02 (one landed before the window, one first-pass, one
// corrected, one stalled then resumed, one rate-limited, one withdrawn, one
// running), a review with two findings and a two-hour manual suspension.
func metricsFixture(t *testing.T) []Event {
	t.Helper()
	f, err := os.Open("testdata/metrics-ledger.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	events, err := ParseEvents(f, true)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// metricsTS parses an RFC 3339 time or fails the test.
func metricsTS(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// metricsWindow is 2026-09-01..09-03 in 1d buckets.
func metricsWindow(t *testing.T) MetricsWindow {
	return MetricsWindow{Since: metricsTS(t, "2026-09-01T00:00:00Z"), Until: metricsTS(t, "2026-09-03T00:00:00Z"), Bucket: 24 * time.Hour}
}

// metricsConfig is one worker w1 on m1 with two slots.
var metricsConfig = Config{Workers: []Worker{{Name: "w1", Adapter: "claude", Model: "m1", MaxParallel: 2}}}

func TestMetricsFlow(t *testing.T) {
	t.Parallel()
	f := Metrics(metricsFixture(t), metricsConfig, metricsWindow(t)).Flow
	h := time.Hour
	if f.Throughput != 3 || !slices.Equal(f.ThroughputSeries, []float64{2, 1}) {
		t.Errorf("throughput = %d %v, want 3 [2 1]", f.Throughput, f.ThroughputSeries)
	}
	// 09-02T00:00: c-stalled finished (stalled); 09-03T00:00: d-limited
	// finished, f-running running; a-first re-planned is not in progress.
	if f.WIP != 2 || !slices.Equal(f.WIPSeries, []float64{1, 2}) {
		t.Errorf("wip = %d %v, want 2 [1 2]", f.WIP, f.WIPSeries)
	}
	// lead a 2h, b 6h, c 6h; cycle 1.5h, 5h, 5h; queue 30m, 1h, 1h; touch
	// 30m, 1h30m, 2h.
	want := map[string][2]Dist{
		"lead":  {f.LeadTime, {Count: 3, P50: 6 * h, P90: 6 * h, Max: 6 * h, Mean: 4*h + 40*time.Minute}},
		"cycle": {f.CycleTime, {Count: 3, P50: 5 * h, P90: 5 * h, Max: 5 * h, Mean: 3*h + 50*time.Minute}},
		"queue": {f.QueueTime, {Count: 3, P50: h, P90: h, Max: h, Mean: 50 * time.Minute}},
		"touch": {f.TouchTime, {Count: 3, P50: 90 * time.Minute, P90: 2 * h, Max: 2 * h, Mean: 80 * time.Minute}},
	}
	for name, d := range want {
		if d[0] != d[1] {
			t.Errorf("%s = %+v, want %+v", name, d[0], d[1])
		}
	}
	// mean(0.5/1.5, 1.5/5, 2/5) = 0.34444
	if f.FlowEfficiency != 0.3444 {
		t.Errorf("flow efficiency = %v, want 0.3444", f.FlowEfficiency)
	}
}

func TestMetricsQuality(t *testing.T) {
	t.Parallel()
	q := Metrics(metricsFixture(t), metricsConfig, metricsWindow(t)).Quality
	if q.Landed != 3 || q.FirstPass != 1 || q.FirstPassYield != 0.3333 {
		t.Errorf("first pass = %d/%d %v, want 1/3 0.3333", q.FirstPass, q.Landed, q.FirstPassYield)
	}
	if q.Corrections != 2 || q.ReworkRate != 0.6667 {
		t.Errorf("rework = %d %v, want 2 0.6667", q.Corrections, q.ReworkRate)
	}
	if len(q.Gates) != 2 {
		t.Fatalf("gates = %+v, want 2 rows", q.Gates)
	}
	if g := q.Gates[0]; g != (GateRate{Gate: "1", Text: "go test ./...", Pass: 3, Total: 4, Rate: 0.75}) {
		t.Errorf("gate 1 = %+v", g)
	}
	long := "go test -count=1 -run 'TestMetricsEveryFamilyOnTheFixture' ./internal/flywheel/ ./cmd/flywheel/"
	if g := q.Gates[1]; g.Gate != "2" || g.Text != long[:60] || g.Pass != 1 || g.Total != 1 || g.Rate != 1 {
		t.Errorf("gate 2 = %+v, want its text clipped to 60", g)
	}
	if q.Reviewed != 1 || q.Findings != 2 || q.Blocking != 1 || q.ReviewFindRate != 2 || q.BlockingShare != 0.5 {
		t.Errorf("review = %+v", q)
	}
	if q.Escapes != 1 {
		t.Errorf("escapes = %d, want 1 (a-first re-planned after landing)", q.Escapes)
	}
}

func TestMetricsReliability(t *testing.T) {
	t.Parallel()
	r := Metrics(metricsFixture(t), metricsConfig, metricsWindow(t)).Reliability
	// c-stalled's stalled finish is also a stalled signal: one andon.
	if r.AndonTotal != 2 || r.Andons["stalled"] != 1 || r.Andons["rate-limited"] != 1 || !slices.Equal(r.AndonSeries, []float64{1, 1}) {
		t.Errorf("andons = %v total %d series %v, want stalled=1 rate-limited=1 [1 1]", r.Andons, r.AndonTotal, r.AndonSeries)
	}
	// stalled at 09-01T22:00, cleared by the inspected pass at 09-02T01:10.
	mttr := 3*time.Hour + 10*time.Minute
	if r.Cleared != 1 || r.MTTR != (Dist{Count: 1, P50: mttr, P90: mttr, Max: mttr, Mean: mttr}) {
		t.Errorf("mttr = %d %+v, want 1 cleared in 3h10m", r.Cleared, r.MTTR)
	}
	if r.Frozen != (FrozenTime{Total: 2 * time.Hour, Manual: 2 * time.Hour}) {
		t.Errorf("frozen = %+v, want 2h manual", r.Frozen)
	}
	if !slices.Equal(r.Paused, []ModelPause{{Model: "m1", Paused: 2 * time.Hour}}) {
		t.Errorf("paused = %+v, want m1 2h (04:00 to the 06:00 reset)", r.Paused)
	}
}

func TestMetricsCostAndCapacity(t *testing.T) {
	t.Parallel()
	rep := Metrics(metricsFixture(t), metricsConfig, metricsWindow(t))
	c := rep.Cost
	// 09-01: 1 + 2 + 1 + 0.5 finished, 0.3 reviewed; 09-02: 0.5 + 0.25.
	if c.Spend != 5.55 || !slices.Equal(c.SpendSeries, []float64{4.8, 0.75}) {
		t.Errorf("spend = %v %v, want 5.55 [4.8 0.75]", c.Spend, c.SpendSeries)
	}
	if c.Units != 4 || c.CostPerUnit != 1.3875 || c.CostPerLanded != 1.85 {
		t.Errorf("cost per unit = %d %v, per landed %v", c.Units, c.CostPerUnit, c.CostPerLanded)
	}
	if c.Steps != 40 || c.Tokens.Input != 400 || c.Tokens.Output != 200 || c.TokensPerStep != 15 {
		t.Errorf("tokens = %+v steps %d per step %v", c.Tokens, c.Steps, c.TokensPerStep)
	}
	if len(c.ByModel) != 1 || c.ByModel[0].Model != "m1" || c.ByModel[0].Attempts != 7 || c.ByModel[0].Spend != 5.25 {
		t.Errorf("by model = %+v, want m1 with 7 attempts and 5.25 spend", c.ByModel)
	}
	// busy 30m + 1h30m + 2h + 30m + 15h (f-running to the window end) =
	// 19.5h of 2 slots × 48h.
	cp := rep.Capacity
	if len(cp.Workers) != 1 || cp.Workers[0] != (WorkerLoad{Worker: "w1", MaxParallel: 2, BusySeconds: 70200, Utilization: 0.2031}) {
		t.Errorf("workers = %+v", cp.Workers)
	}
	if cp.Utilization != 0.2031 || cp.IdleShare != 0.7969 {
		t.Errorf("utilization = %v idle %v, want 0.2031 0.7969", cp.Utilization, cp.IdleShare)
	}
}

func TestMetricsWindowBoundaries(t *testing.T) {
	t.Parallel()
	events := metricsFixture(t)
	w := MetricsWindow{Since: metricsTS(t, "2026-09-02T00:00:00Z"), Until: metricsTS(t, "2026-09-03T00:00:00Z"), Bucket: time.Hour}
	rep := Metrics(events, metricsConfig, w)
	if len(rep.Buckets) != 24 || len(rep.Flow.ThroughputSeries) != 24 || len(rep.Cost.SpendSeries) != 24 {
		t.Fatalf("buckets = %d, series %d/%d, want 24", len(rep.Buckets), len(rep.Flow.ThroughputSeries), len(rep.Cost.SpendSeries))
	}
	if rep.Flow.Throughput != 1 || rep.Flow.ThroughputSeries[2] != 1 || rep.Quality.Landed != 1 || rep.Cost.Spend != 0.75 {
		t.Errorf("09-02 only: throughput %d landed %d spend %v, want 1 1 0.75", rep.Flow.Throughput, rep.Quality.Landed, rep.Cost.Spend)
	}
	if rep.Reliability.Andons["stalled"] != 0 || rep.Reliability.AndonTotal != 1 || len(rep.Quality.Gates) != 1 {
		t.Errorf("09-01 andons and gates leaked in: %+v %+v", rep.Reliability.Andons, rep.Quality.Gates)
	}
	prev := Metrics(events, metricsConfig, metricsWindow(t).Previous())
	if prev.Flow.Throughput != 1 || prev.Cost.Spend != 5 || prev.Flow.LeadTime.Max != 3*time.Hour {
		t.Errorf("previous window = throughput %d spend %v lead %v, want g-old only", prev.Flow.Throughput, prev.Cost.Spend, prev.Flow.LeadTime.Max)
	}
	if _, err := WindowFor("1y", w.Until); err == nil {
		t.Error("WindowFor(1y) accepted")
	}
	if d, _ := WindowFor("7d", w.Until); d.Bucket != 24*time.Hour || len(d.buckets()) != 7 {
		t.Errorf("7d = %+v", d)
	}
}

func TestMetricsEmptyLedger(t *testing.T) {
	t.Parallel()
	w := metricsWindow(t)
	rep := Metrics(nil, Config{}, w)
	if rep.Flow.Throughput != 0 || rep.Flow.LeadTime != (Dist{}) || rep.Quality.FirstPassYield != 0 || rep.Cost.Spend != 0 || rep.Capacity.Utilization != 0 {
		t.Errorf("empty ledger = %+v", rep)
	}
	if !slices.Equal(rep.Flow.WIPSeries, []float64{0, 0}) || rep.Reliability.AndonTotal != 0 {
		t.Errorf("empty series = %v andons %d", rep.Flow.WIPSeries, rep.Reliability.AndonTotal)
	}
	_ = Metrics(nil, Config{}, MetricsWindow{}) // a zero window must not panic
}

func TestMetricsDeterministic(t *testing.T) {
	t.Parallel()
	events := metricsFixture(t)
	w := metricsWindow(t)
	first := metricsJSON(t, Metrics(events, metricsConfig, w))
	if again := metricsJSON(t, Metrics(events, metricsConfig, w)); again != first {
		t.Fatal("two runs differ")
	}
	rev := slices.Clone(events)
	slices.Reverse(rev)
	if got := metricsJSON(t, Metrics(rev, metricsConfig, w)); got != first {
		t.Errorf("input order changed the report:\n%s\nvs\n%s", got, first)
	}
}

// TestMetricsEvidence checks every metric drills to its units (issue #583
// k4): each id has evidence on the fixture, first-pass yield splits into its
// groups as its value does, and lead time lists the slowest unit first.
func TestMetricsEvidence(t *testing.T) {
	t.Parallel()
	rep := Metrics(metricsFixture(t), metricsConfig, metricsWindow(t))
	for _, id := range MetricIDs {
		if len(rep.Evidence[id]) == 0 {
			t.Errorf("%s has no evidence", id)
		}
	}
	for id := range rep.Evidence {
		if !slices.Contains(MetricIDs, id) {
			t.Errorf("evidence under %s, not in MetricIDs", id)
		}
	}
	groups := map[string][]string{}
	for _, u := range rep.Evidence["quality.first_pass_yield"] {
		groups[u.Group] = append(groups[u.Group], u.Task+" "+u.Value)
	}
	q := rep.Quality
	if len(groups["first pass"]) != q.FirstPass || len(groups["corrected"]) != q.Landed-q.FirstPass || len(groups) != 2 {
		t.Errorf("first-pass groups = %v, want %d first pass and %d corrected", groups, q.FirstPass, q.Landed-q.FirstPass)
	}
	if !slices.Equal(groups["first pass"], []string{"a-first first pass"}) {
		t.Errorf("first pass = %v, want a-first", groups["first pass"])
	}
	lead := rep.Evidence["flow.lead_time"]
	var tasks []string
	for i, u := range lead {
		tasks = append(tasks, u.Task)
		if i > 0 && u.Sort > lead[i-1].Sort {
			t.Errorf("lead time evidence not slowest first: %+v", lead)
		}
	}
	// b and c 6h (ties by task), a 2h.
	if !slices.Equal(tasks, []string{"b-corrected", "c-stalled", "a-first"}) || lead[0].Value != "lead "+whyDur(6*time.Hour) {
		t.Errorf("lead evidence = %+v", lead)
	}
	if a := rep.Evidence["reliability.andons"]; len(a) != rep.Reliability.AndonTotal {
		t.Errorf("andon evidence = %+v, want %d", a, rep.Reliability.AndonTotal)
	}
	if f := rep.Evidence["reliability.frozen"]; len(f) != 1 || f[0].Task != "f-running" {
		t.Errorf("frozen evidence = %+v, want f-running held", f)
	}
}

// metricsJSON marshals rep or fails the test.
func metricsJSON(t *testing.T, rep MetricsReport) string {
	t.Helper()
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
