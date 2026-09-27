package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

// dashData is the test frame with the fixture ledger's metrics at
// 2026-09-03T00:00Z in every window, and its units on the floor.
func dashData(t *testing.T) TUIData {
	t.Helper()
	events := metricsFixture(t)
	now := metricsTS(t, "2026-09-03T00:00:00Z")
	d := makeTestTUIData()
	d.Floor.Units = nil
	for _, task := range []string{"g-old", "a-first", "b-corrected", "c-stalled", "d-limited", "e-withdrawn", "f-running"} {
		d.Floor.Units = append(d.Floor.Units, Unit{Task: task, Stage: "landed"})
	}
	d.Metrics = map[string]TUIMetrics{}
	for _, name := range []string{"24h", "7d", "30d"} {
		w, err := WindowFor(name, now)
		if err != nil {
			t.Fatal(err)
		}
		d.Metrics[name] = TUIMetrics{Cur: Metrics(events, metricsConfig, w), Prev: Metrics(events, metricsConfig, w.Previous())}
	}
	return d
}

// TestTUIPolishPulseAndSpend checks the lead's k4 polish list (issue #583
// k6): at 110 and 120 columns no pulse panel draws past its column and the
// BY MODEL rows never show an amount cut in two; a model with no landed
// unit shows "–", not "$0.00/unit"; the header formats large spend
// compactly ($711, $1.2k) instead of cutting it.
func TestTUIPolishPulseAndSpend(t *testing.T) {
	t.Parallel()
	d := dashData(t)
	tm := d.Metrics["24h"]
	tm.Cur.Cost.Spend = 711.23
	tm.Cur.Cost.ByModel = []StatsModel{
		{Model: "claude-opus-5-5-20260901", Accepted: 3, Spend: 711.23, CostPerAccepted: 237.08},
		{Model: "gpt-5-codex-high-reasoning", Accepted: 0, Spend: 1234.5},
		{Model: "sim", Accepted: 12, Spend: 7.25, CostPerAccepted: 0.6},
	}
	d.Metrics["24h"] = tm
	d.Floor.Output.Cost = 1234.5
	m := NewTUI()
	typed(m, d, ":p")
	for _, width := range []int{110, 120} {
		pw := (width - 4) / 3
		for i := range pulsePanels {
			for _, l := range m.pulsePanel(i, tm, pw) {
				if cellCount(l) > pw {
					t.Errorf("%d cols: panel %s line %q is %d cells, over its column's %d", width, pulsePanels[i].title, l, cellCount(l), pw)
				}
			}
		}
		for _, l := range strings.Split(m.View(d, width, 60, false), "\n") {
			if cellCount(l) > width {
				t.Errorf("%d cols: line over the frame: %q", width, l)
			}
		}
		byModel := m.pulsePanel(5, tm, pw)[1:]
		for _, l := range byModel {
			if !strings.HasSuffix(l, "spent") && !strings.HasSuffix(l, "/unit") && !strings.HasSuffix(l, "–") {
				t.Errorf("%d cols: BY MODEL row %q ends in a cut amount", width, l)
			}
		}
		if l := lineWith(strings.Join(byModel, "\n"), "gpt-5-codex"); !strings.Contains(l, "–") || strings.Contains(l, "$0.00") {
			t.Errorf("%d cols: the model with no landed unit reads %q, want –", width, l)
		}
	}
	for v, want := range map[float64]string{0: "$0.00", 7.25: "$7.25", 99.99: "$99.99", 711.23: "$711", 1234.5: "$1.2k", 3.4e6: "$3.4M", -12: "-$12.00"} {
		if got := fmtUSD(v); got != want {
			t.Errorf("fmtUSD(%v) = %q, want %q", v, got, want)
		}
	}
	if s := strings.Join(headerStats(d), "\n"); !strings.Contains(s, "spend 24h $711\n") {
		t.Errorf("header stats spend not compact:\n%s", s)
	}
	if c := strings.Join(contextLines(d), "\n"); !strings.Contains(c, " · $1.2k\n") {
		t.Errorf("header context spend not compact:\n%s", c)
	}
}

// lineWith is the first line of view holding every one of words, "" when none.
func lineWith(view string, words ...string) string {
	for _, l := range strings.Split(view, "\n") {
		all := true
		for _, w := range words {
			all = all && strings.Contains(l, w)
		}
		if all {
			return l
		}
	}
	return ""
}

func TestTUIPulse(t *testing.T) {
	t.Parallel()
	d := dashData(t)
	m := NewTUI()
	typed(m, d, ":p")
	view := m.View(d, 110, 60, false)
	if !strings.Contains(view, "── Pulse 24h ──") {
		t.Fatalf("no pulse title:\n%s", view)
	}
	if lineWith(view, "FLOW", "QUALITY", "RELIABILITY") == "" || lineWith(view, "COST", "CAPACITY", "BY MODEL") == "" {
		t.Errorf("110 cols: want 3 columns of panels:\n%s", view)
	}
	view = m.View(d, 72, 60, false)
	if lineWith(view, "FLOW", "QUALITY") == "" || lineWith(view, "RELIABILITY", "COST") == "" || lineWith(view, "CAPACITY", "BY MODEL") == "" ||
		lineWith(view, "QUALITY", "RELIABILITY") != "" {
		t.Errorf("72 cols: want 2 columns of panels:\n%s", view)
	}
	for _, title := range []string{"FLOW", "QUALITY", "RELIABILITY", "COST", "CAPACITY", "BY MODEL"} {
		if !strings.Contains(view, title) {
			t.Errorf("no %s panel:\n%s", title, view)
		}
	}
	// landed: 1 unit on 09-02, 4 over the week (g-old, a, b, c).
	landed := func() string { return strings.Fields(m.pulsePanel(0, d.Metrics[m.window], 40)[1])[1] }
	if got := landed(); got != "1" {
		t.Errorf("24h landed = %s, want 1", got)
	}
	press(m, d, "2")
	if view = m.View(d, 110, 60, false); !strings.Contains(view, "── Pulse 7d ──") || landed() != "4" {
		t.Errorf("7d: landed %s, title:\n%s", landed(), view)
	}
	press(m, d, "3")
	if !strings.Contains(m.View(d, 110, 60, false), "── Pulse 30d ──") {
		t.Error("3 does not show 30d")
	}
	// l to QUALITY, Enter opens its first metric.
	press(m, d, "l")
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if m.drillKind != "metric" || m.drillTask != "quality.first_pass_yield" {
		t.Errorf("enter on QUALITY opened %q %q", m.drillKind, m.drillTask)
	}
	// j moves down a row of panels: FLOW to COST at 3 columns.
	m = NewTUI()
	typed(m, d, ":pulse")
	m.View(d, 110, 60, false)
	press(m, d, "j")
	if m.panel != 3 {
		t.Errorf("j moved to panel %d, want 3 (COST)", m.panel)
	}
}

func TestTUIMetricDrill(t *testing.T) {
	t.Parallel()
	d := dashData(t)
	m := NewTUI()
	typed(m, d, ":p")
	press(m, d, "2")
	m.Update(term.Key{Kind: term.KeyEnter}, d) // FLOW: lead time
	// The path starts at the view below :pulse, units.
	crumbs := func() []string { return m.Crumbs()[1:] }
	view := m.View(d, 120, 50, false)
	if !slices.Equal(crumbs(), []string{"pulse", "lead time", "chart"}) || !strings.Contains(view, "── Metric lead time 7d ──") {
		t.Fatalf("crumbs %v, view:\n%s", m.Crumbs(), view)
	}
	// Over the week: lead a 2h, g-old 3h, b 6h, c 6h; p50 3h, p90 6h.
	if lineWith(view, "▲", "p50 "+whyDur(3*3600e9)) == "" || lineWith(view, "▲", "p90 "+whyDur(6*3600e9)) == "" {
		t.Errorf("no histogram with p50 and p90:\n%s", view)
	}
	press(m, d, "u")
	view = m.View(d, 120, 50, false)
	if !slices.Equal(crumbs(), []string{"pulse", "lead time", "units"}) || lineWith(view, "TASK", "VALUE", "GROUP") == "" {
		t.Fatalf("crumbs %v, view:\n%s", m.Crumbs(), view)
	}
	if lineWith(view, "> b-corrected", "lead "+whyDur(6*3600e9)) == "" || lineWith(view, "  a-first", "lead 2h") == "" {
		t.Errorf("evidence not slowest first with the cursor on b-corrected:\n%s", view)
	}
	press(m, d, "j")
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if m.drillKind != "why" || m.drillTask != "c-stalled" {
		t.Fatalf("enter on the second row opened %q %q", m.drillKind, m.drillTask)
	}
	if want := []string{"pulse", "lead time", "units", "c-stalled", "why"}; !slices.Equal(crumbs(), want) {
		t.Errorf("crumbs = %v, want %v", m.Crumbs(), want)
	}
	esc(m, d)
	if m.drillKind != "metric" || m.metricPart != "units" || m.evCursor != 1 || crumbs()[2] != "units" {
		t.Errorf("esc from the unit: %q %q row %d %v, want back on the evidence", m.drillKind, m.metricPart, m.evCursor, m.Crumbs())
	}
	press(m, d, "h")
	if !strings.Contains(m.View(d, 120, 50, false), "▲") {
		t.Error("h does not show the chart again")
	}
	esc(m, d)
	if m.drillKind != "" || m.view != "pulse" {
		t.Errorf("esc from the metric: %q view %q, want the pulse", m.drillKind, m.view)
	}
}

// not parallel: sets tuiMetricsHook, a package variable the live fetch reads.
func TestTUIHeaderStats(t *testing.T) {
	d := dashData(t)
	view := NewTUI().View(d, 200, 40, false)
	for _, want := range []string{"throughput 1/24h", "wip 2", "first-pass 0%", "andons 1", "spend 24h $0.75", "workers 3/5 busy"} {
		if !strings.Contains(view, want) {
			t.Errorf("header lacks %q:\n%s", want, view)
		}
	}
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatal(err)
	}
	ledger, err := os.ReadFile("testdata/metrics-ledger.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), ledger, 0o644); err != nil {
		t.Fatal(err)
	}
	computed := 0
	tuiMetricsHook = func(string) { computed++ }
	t.Cleanup(func() { tuiMetricsHook = nil })
	clock := metricsTS(t, "2026-09-03T00:00:00Z")
	fetch := TUIFetcher(dir, func() time.Time { return clock })
	m := NewTUI()
	data, err := fetch(m)
	if err != nil {
		t.Fatal(err)
	}
	if computed != 1 || data.Metrics["24h"].Cur.Flow.Throughput != 1 {
		t.Fatalf("first fetch computed %d times, throughput %d; want 1 and 1", computed, data.Metrics["24h"].Cur.Flow.Throughput)
	}
	for _, r := range "jkj" {
		clock = clock.Add(5 * time.Second)
		m.Update(term.Key{Kind: term.KeyRune, Rune: r}, data)
		if data, err = fetch(m); err != nil {
			t.Fatal(err)
		}
	}
	if computed != 1 || !strings.Contains(m.View(data, 200, 40, false), "throughput 1/24h") {
		t.Errorf("key presses recomputed the metrics: %d computations", computed)
	}
	clock = clock.Add(metricsEvery)
	if _, err = fetch(m); err != nil || computed != 2 {
		t.Errorf("after %s: %d computations (%v), want 2", metricsEvery, computed, err)
	}
}

func TestTUIMetricsView(t *testing.T) {
	t.Parallel()
	d := dashData(t)
	m := NewTUI()
	typed(m, d, ":m")
	header, rows := m.Rows(d)
	if !slices.Equal(header, []string{"FAMILY", "METRIC", "VALUE", "TREND", "Δ PREV", "DEFINITION"}) || len(rows) != len(MetricIDs) {
		t.Fatalf("metrics table = %v with %d rows, want %d", header, len(rows), len(MetricIDs))
	}
	for i, id := range MetricIDs {
		def, ok := metricByID(id)
		if !ok || metricOfRow(rows[i]) != id || rows[i][2] == "" || rows[i][3] == "" || rows[i][5] != def.def || def.def == "" {
			t.Errorf("row %d for %s = %q", i, id, rows[i])
		}
	}
	if r := rows[0]; r[1] != "throughput" || r[2] != "1" || !strings.ContainsAny(r[3], "▁▂▃▄▅▆▇█") || !strings.ContainsAny(r[3], "↑↓→") {
		t.Errorf("throughput row = %q, want value 1 and a sparkline with its arrow", r)
	}
	if view := m.View(d, 250, 50, false); !strings.Contains(view, "Metrics 24h") || !strings.Contains(view, "landed units on r1 alone") {
		t.Errorf("metrics view:\n%s", view)
	}
	names := func() []string {
		_, rows := m.Rows(d)
		var out []string
		for _, r := range rows {
			out = append(out, r[1])
		}
		return out
	}
	press(m, d, "N")
	if n := names(); !slices.IsSorted(n) {
		t.Errorf("N: not sorted by metric: %v", n)
	}
	press(m, d, "N")
	if n := names(); !slices.IsSortedFunc(n, func(a, b string) int { return strings.Compare(b, a) }) {
		t.Errorf("N again: not sorted descending: %v", n)
	}
	typed(m, d, "/lead time")
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if m.drillKind != "metric" || m.drillTask != "flow.lead_time" {
		t.Errorf("enter on lead time opened %q %q", m.drillKind, m.drillTask)
	}
}

// TestTUIMetricsSplitByModelAndWorker checks the :metrics split (issue #583
// k7): Shift-M on a metric shows a row per model, Shift-W a row per worker
// (the dispatched events' worker), and Esc returns to every metric.
func TestTUIMetricsSplitByModelAndWorker(t *testing.T) {
	t.Parallel()
	at := func(h int) string { return time.Date(2026, 9, 2, h, 0, 0, 0, time.UTC).Format(time.RFC3339) }
	events := []Event{
		{TS: at(1), Task: "T1", Kind: "dispatched", Attempt: "r1", Worker: "fast"},
		{TS: at(2), Task: "T1", Kind: "finished", Attempt: "r1", Cost: 1.5},
		{TS: at(3), Task: "T1", Kind: "landed"},
		{TS: at(4), Task: "T2", Kind: "dispatched", Attempt: "r1", Worker: "deep"},
		{TS: at(5), Task: "T2", Kind: "finished", Attempt: "r1", Cost: 2},
		{TS: at(6), Task: "T2", Kind: "dispatched", Attempt: "c1"},
		{TS: at(7), Task: "T2", Kind: "finished", Attempt: "c1", Cost: 1},
		{TS: at(8), Task: "T2", Kind: "landed"},
	}
	w, _ := WindowFor("24h", time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	workers := workerStats(events, w)
	want := []TUIWorkerStat{{Worker: "-", Spend: 1, Landed: 1}, {Worker: "deep", Spend: 2}, {Worker: "fast", Spend: 1.5, Landed: 1, FirstPass: 1}}
	if !slices.Equal(workers, want) {
		t.Errorf("workerStats = %+v, want %+v", workers, want)
	}

	d := makeTestTUIData()
	var tm TUIMetrics
	tm.Cur.Cost.ByModel = []StatsModel{
		{Model: "claude-opus-5-5", Spend: 6, Accepted: 3, Inspected: 4},
		{Model: "gpt-5", Spend: 2, Inspected: 1},
	}
	tm.Workers = workers
	d.Metrics = map[string]TUIMetrics{"24h": tm}
	m := NewTUI()
	typed(m, d, ":m")
	for m.cursor < 30 && cell(func() []string { _, r := m.Rows(d); return r[m.cursor] }(), 1) != "spend" {
		press(m, d, "j")
	}
	press(m, d, "M")
	header, rows := m.Rows(d)
	got := strings.Join(header, " ")
	for _, r := range rows {
		got += " | " + strings.Join(r, " ")
	}
	if want := "MODEL SPEND LANDED FIRST-PASS PER LANDED | claude-opus-5-5 $6.00 3 75% $2.00 | gpt-5 $2.00 0 0% –"; got != want {
		t.Errorf("by model = %q\nwant       %q", got, want)
	}
	if frame := m.View(d, 120, 20, false); !strings.Contains(frame, "Metrics 24h · spend by model") {
		t.Errorf("split title lacks the metric:\n%s", frame)
	}
	press(m, d, "W")
	if got := firstCells(m, d); got != "- deep fast" {
		t.Errorf("by worker rows = %q, want - deep fast", got)
	}
	esc(m, d)
	if header, _ := m.Rows(d); m.view != "metrics" || header[1] != "METRIC" || m.split != "" {
		t.Errorf("after Esc: view %q header %q; want every metric", m.view, header)
	}
	if _, rows := m.Rows(d); cell(rows[m.cursor], 1) != "spend" {
		t.Errorf("after Esc the cursor is on %q, want spend", rows[m.cursor])
	}
}
