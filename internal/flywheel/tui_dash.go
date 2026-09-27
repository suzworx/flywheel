package flywheel

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

// The factory's metrics as a person reads them (issue #583 k4): the header
// stats, the :pulse dashboard, the :metrics table and the metric drill-down,
// every number opening its chart and then the units behind it.

// TUIMetrics is one window's MetricsReport and the previous window's, for
// the trends, and the window split by worker (issue #583 k7).
type TUIMetrics struct {
	Cur, Prev MetricsReport
	Workers   []TUIWorkerStat
}

// metricDef is one metric as the view shows it: its id (MetricIDs), name,
// how its value reads (kind: n, pct, dur, usd, num), which chart its
// drill-down draws (hist, control, flow, bars, groups, spark), and its
// one-line definition from docs/metrics.md.
type metricDef struct {
	id, name, kind, chart, def string
	value                      func(r MetricsReport) float64
	series                     func(r MetricsReport) []float64 // nil: none
	dist                       func(r MetricsReport) Dist      // the time distribution, for hist and control
	bars                       func(r MetricsReport) []BarRow  // for bars
}

// family is the metric's family, the id's prefix.
func (d metricDef) family() string { f, _, _ := strings.Cut(d.id, "."); return f }

// distDef is a time-distribution metric valued at its p50.
func distDef(id, name, chart, def string, dist func(r MetricsReport) Dist) metricDef {
	return metricDef{id: id, name: name, kind: "dur", chart: chart, def: def, dist: dist,
		value: func(r MetricsReport) float64 { return dist(r).P50.Seconds() }}
}

// metricDefs are every metric, in MetricIDs' order.
var metricDefs = []metricDef{
	{id: "flow.throughput", name: "throughput", kind: "n", chart: "spark", def: "landed units: a first landed event in the window",
		value: func(r MetricsReport) float64 { return float64(r.Flow.Throughput) }, series: func(r MetricsReport) []float64 { return r.Flow.ThroughputSeries }},
	{id: "flow.wip", name: "wip", kind: "n", chart: "flow", def: "units dispatched, running, finished or passed at the window's end",
		value: func(r MetricsReport) float64 { return float64(r.Flow.WIP) }, series: func(r MetricsReport) []float64 { return r.Flow.WIPSeries }},
	distDef("flow.lead_time", "lead time", "hist", "per landed unit: first planned to first landed (p50)", func(r MetricsReport) Dist { return r.Flow.LeadTime }),
	distDef("flow.cycle_time", "cycle time", "control", "per landed unit: first dispatched to first landed (p50)", func(r MetricsReport) Dist { return r.Flow.CycleTime }),
	distDef("flow.queue_time", "queue time", "hist", "per landed unit: first planned to first dispatched (p50)", func(r MetricsReport) Dist { return r.Flow.QueueTime }),
	distDef("flow.touch_time", "touch time", "hist", "per landed unit: its attempt spans a finish closed, summed (p50)", func(r MetricsReport) Dist { return r.Flow.TouchTime }),
	{id: "flow.flow_efficiency", name: "flow efficiency", kind: "pct", chart: "spark", def: "mean over landed units of touch time / cycle time",
		value: func(r MetricsReport) float64 { return r.Flow.FlowEfficiency }},
	{id: "quality.first_pass_yield", name: "first-pass yield", kind: "pct", chart: "groups", def: "landed units on r1 alone with no correct or rework verdict / landed",
		value: func(r MetricsReport) float64 { return r.Quality.FirstPassYield }},
	{id: "quality.rework_rate", name: "rework", kind: "num", chart: "groups", def: "attempts beyond the first / landed units",
		value: func(r MetricsReport) float64 { return r.Quality.ReworkRate }},
	{id: "quality.gates", name: "gate fail rate", kind: "pct", chart: "bars", def: "conclusive gate readings with a non-zero rc / all conclusive readings",
		value: gateFailRate, bars: gateBars},
	{id: "quality.review_find_rate", name: "findings/unit", kind: "num", chart: "groups", def: "review findings / units an agent reviewed",
		value: func(r MetricsReport) float64 { return r.Quality.ReviewFindRate }},
	{id: "quality.blocking_share", name: "blocking share", kind: "pct", chart: "groups", def: "blocker or major findings / findings",
		value: func(r MetricsReport) float64 { return r.Quality.BlockingShare }},
	{id: "quality.escapes", name: "escapes", kind: "n", chart: "groups", def: "landed units planned again after landing",
		value: func(r MetricsReport) float64 { return float64(r.Quality.Escapes) }},
	{id: "reliability.andons", name: "andons", kind: "n", chart: "bars", def: "signals, and stalled, silent, capped or rate-limited finishes",
		value: func(r MetricsReport) float64 { return float64(r.Reliability.AndonTotal) }, series: func(r MetricsReport) []float64 { return r.Reliability.AndonSeries }, bars: andonBars},
	distDef("reliability.mttr", "MTTR", "hist", "per cleared andon: the andon to its first clearing event (p50)", func(r MetricsReport) Dist { return r.Reliability.MTTR }),
	{id: "reliability.frozen", name: "frozen", kind: "dur", chart: "groups", def: "time inside the window the factory was suspended",
		value: func(r MetricsReport) float64 { return r.Reliability.Frozen.Total.Seconds() }},
	{id: "reliability.paused", name: "paused", kind: "dur", chart: "bars", def: "time inside the window a rate limit paused a model, summed over models",
		value: pausedTotal, bars: pausedBars},
	{id: "cost.spend", name: "spend", kind: "usd", chart: "spark", def: "cost of the window's finished and reviewed events",
		value: func(r MetricsReport) float64 { return r.Cost.Spend }, series: func(r MetricsReport) []float64 { return r.Cost.SpendSeries }},
	{id: "cost.cost_per_unit", name: "per unit", kind: "usd", chart: "groups", def: "spend / units with a finish in the window",
		value: func(r MetricsReport) float64 { return r.Cost.CostPerUnit }},
	{id: "cost.cost_per_landed", name: "per landed", kind: "usd", chart: "groups", def: "spend / landed units",
		value: func(r MetricsReport) float64 { return r.Cost.CostPerLanded }},
	{id: "cost.tokens_per_step", name: "tokens/step", kind: "num", chart: "groups", def: "input + output + reasoning tokens / steps of the window's finishes",
		value: func(r MetricsReport) float64 { return r.Cost.TokensPerStep }},
	{id: "cost.by_model", name: "by model", kind: "n", chart: "bars", def: "the stats scoreboard over the window's events: models, cost per accepted unit",
		value: func(r MetricsReport) float64 { return float64(len(r.Cost.ByModel)) }, bars: modelBars},
	{id: "capacity.utilization", name: "utilization", kind: "pct", chart: "bars", def: "busy seconds / capacity seconds over every worker",
		value: func(r MetricsReport) float64 { return r.Capacity.Utilization }, bars: workerBars},
	{id: "capacity.idle_share", name: "idle", kind: "pct", chart: "groups", def: "1 - utilization, never below 0",
		value: func(r MetricsReport) float64 { return r.Capacity.IdleShare }},
}

// metricByID is the metric with id; ok false when none.
func metricByID(id string) (metricDef, bool) {
	for _, d := range metricDefs {
		if d.id == id {
			return d, true
		}
	}
	return metricDef{}, false
}

// fmtMetric is v as the kind reads.
func fmtMetric(kind string, v float64) string {
	switch kind {
	case "n":
		return fmt.Sprintf("%.0f", v)
	case "pct":
		return fmt.Sprintf("%.0f%%", v*100)
	case "dur":
		return whyDur(time.Duration(v * float64(time.Second)))
	case "usd":
		return fmt.Sprintf("$%.2f", v)
	}
	return chartNum(math.Round(v*10) / 10)
}

// fmtDelta is cur - prev with its sign, "=" when equal.
func fmtDelta(kind string, cur, prev float64) string {
	d := cur - prev
	switch {
	case d > 0:
		return "+" + fmtMetric(kind, d)
	case d < 0:
		return "-" + fmtMetric(kind, -d)
	}
	return "="
}

// trendArrow is ↑ higher, ↓ lower, → equal than the previous window.
func trendArrow(cur, prev float64) string {
	switch {
	case cur > prev:
		return "↑"
	case cur < prev:
		return "↓"
	}
	return "→"
}

// metricsEvery is the longest the live view keeps a window's metrics: they
// are computed at most this often, so they never slow a key press between.
const metricsEvery = 30 * time.Second

// tuiMetricsHook, when set, is called each time the live fetch computes a
// window's metrics: the tests' counter.
var tuiMetricsHook func(window string)

// metricsCache holds each window's metrics and when they were computed.
type metricsCache struct {
	at   map[string]time.Time
	reps map[string]TUIMetrics
}

// get is the named window's metrics at now, computed afresh (this window and
// the previous one) when none are held or they are metricsEvery old.
func (c *metricsCache) get(name string, now time.Time, events []Event, cfg Config) (TUIMetrics, bool) {
	if at, ok := c.at[name]; ok && !now.Before(at) && now.Sub(at) < metricsEvery {
		return c.reps[name], true
	}
	w, err := WindowFor(name, now)
	if err != nil {
		return TUIMetrics{}, false
	}
	if tuiMetricsHook != nil {
		tuiMetricsHook(name)
	}
	if c.at == nil {
		c.at, c.reps = map[string]time.Time{}, map[string]TUIMetrics{}
	}
	c.at[name] = now
	c.reps[name] = TUIMetrics{Cur: Metrics(events, cfg, w), Prev: Metrics(events, cfg, w.Previous()), Workers: workerStats(events, w)}
	return c.reps[name], true
}

// MetricsWindow is the window the metrics views show, "" when none is shown:
// the live fetch computes it only then.
func (m *TUI) MetricsWindow() string {
	if m.view == "pulse" || m.view == "metrics" || m.drillKind == "metric" {
		return m.window
	}
	return ""
}

// pulsePanels are the :pulse dashboard's panels: each lists metrics (a label
// and a metric id) and Enter opens its first metric's drill-down, or open.
var pulsePanels = []struct {
	title string
	lines [][2]string
	open  string
}{
	{"FLOW", [][2]string{{"landed", "flow.throughput"}, {"wip", "flow.wip"}, {"lead p50/90", "flow.lead_time"}}, "flow.lead_time"},
	{"QUALITY", [][2]string{{"first-pass", "quality.first_pass_yield"}, {"rework", "quality.rework_rate"}, {"gate fail", "quality.gates"}, {"finds/unit", "quality.review_find_rate"}}, ""},
	{"RELIABILITY", [][2]string{{"andons", "reliability.andons"}, {"MTTR", "reliability.mttr"}, {"frozen", "reliability.frozen"}, {"paused", "reliability.paused"}}, ""},
	{"COST", [][2]string{{"spend", "cost.spend"}, {"per landed", "cost.cost_per_landed"}, {"tokens/step", "cost.tokens_per_step"}}, "cost.cost_per_landed"},
	{"CAPACITY", [][2]string{{"utilization", "capacity.utilization"}, {"idle", "capacity.idle_share"}}, ""},
	{"BY MODEL", nil, "cost.by_model"},
}

// pulseColumns is how many panels a row of the grid holds at width.
func pulseColumns(width int) int {
	switch {
	case width >= 110:
		return 3
	case width >= 72:
		return 2
	}
	return 1
}

// pulseLines draws the six panels in a grid that adapts to width, the panel
// under the cursor marked ▶. Each line is a value, the sparkline of its
// series when it has one, and its trend against the previous window.
func (m *TUI) pulseLines(d TUIData, width int) []string {
	tm, ok := d.Metrics[m.window]
	if !ok {
		return []string{"computing the " + m.window + " metrics…"}
	}
	cols := pulseColumns(width)
	m.pulseCols = cols
	pw := (width - 2*(cols-1)) / cols
	var out []string
	for row := 0; row < len(pulsePanels); row += cols {
		var blocks [][]string
		h := 0
		for i := row; i < min(row+cols, len(pulsePanels)); i++ {
			b := m.pulsePanel(i, tm, pw)
			blocks, h = append(blocks, b), max(h, len(b))
		}
		for j := 0; j < h; j++ {
			line := ""
			for bi, b := range blocks {
				cell := ""
				if j < len(b) {
					cell = b[j]
				}
				// No panel draws past its column, the last one included.
				if cell = fitCells(cell, pw); bi < len(blocks)-1 {
					cell = padCells(cell, pw) + "  "
				}
				line += cell
			}
			out = append(out, strings.TrimRight(line, " "))
		}
		out = append(out, "")
	}
	return out
}

// pulsePanel is panel i's lines, each at most w cells.
func (m *TUI) pulsePanel(i int, tm TUIMetrics, w int) []string {
	p := pulsePanels[i]
	mark := "  "
	if i == m.panel {
		mark = "▶ "
	}
	out := []string{mark + p.title}
	for _, l := range p.lines {
		def, _ := metricByID(l[1])
		cur, prev := def.value(tm.Cur), def.value(tm.Prev)
		val := fmtMetric(def.kind, cur)
		if def.dist != nil {
			val = whyDur(def.dist(tm.Cur).P50) + "/" + whyDur(def.dist(tm.Cur).P90)
		}
		spark := ""
		if def.series != nil {
			spark = Sparkline(def.series(tm.Cur), 6)
		}
		out = append(out, fitCells("  "+padCells(l[0], 11)+" "+padCells(val, 11)+" "+padCells(spark, 6)+" "+trendArrow(cur, prev), w))
	}
	if p.lines == nil {
		bars := fitBars(modelBars(tm.Cur), w-2)
		if len(bars) == 0 {
			bars = []string{"no model finished"}
		}
		for _, b := range bars {
			out = append(out, "  "+b)
		}
	}
	return out
}

// pulseKey handles a key of the metrics views: 1 2 3 pick the window; on
// :pulse the arrows and h j k l move between panels and Enter opens the
// panel's metric. It reports whether it used the key.
func (m *TUI) pulseKey(k term.Key) bool {
	if w, ok := map[rune]string{'1': "24h", '2': "7d", '3': "30d"}[k.Rune]; ok && k.Kind == term.KeyRune {
		m.window = w
		return true
	}
	if m.view != "pulse" {
		return false
	}
	cols := max(m.pulseCols, 1)
	move := map[term.KeyKind]int{term.KeyLeft: -1, term.KeyRight: 1, term.KeyUp: -cols, term.KeyDown: cols}[k.Kind]
	if k.Kind == term.KeyRune {
		move = map[rune]int{'h': -1, 'l': 1, 'k': -cols, 'j': cols}[k.Rune]
	}
	switch {
	case move != 0:
		m.panel = max(0, min(len(pulsePanels)-1, m.panel+move))
	case k.Kind == term.KeyEnter:
		id := pulsePanels[m.panel].open
		if id == "" {
			id = pulsePanels[m.panel].lines[0][1]
		}
		m.openMetric(id)
	default:
		return false
	}
	return true
}

// metricRows are the :metrics table: every metric with its value, the
// sparkline of its series and its trend arrow, the change from the previous
// window, and its definition.
func metricRows(d TUIData, window string) (header []string, rows [][]string) {
	header = []string{"FAMILY", "METRIC", "VALUE", "TREND", "Δ PREV", "DEFINITION"}
	tm, ok := d.Metrics[window]
	if !ok {
		return header, nil
	}
	for _, def := range metricDefs {
		cur, prev := def.value(tm.Cur), def.value(tm.Prev)
		trend := trendArrow(cur, prev)
		if def.series != nil {
			trend = Sparkline(def.series(tm.Cur), 12) + " " + trend
		}
		rows = append(rows, []string{def.family(), def.name, fmtMetric(def.kind, cur), trend, fmtDelta(def.kind, cur, prev), def.def})
	}
	return header, rows
}

// TUIWorkerStat is one worker's share of a window (issue #583 k7): the spend
// of its attempts' finishes, the units it landed, those landed on their first
// attempt, and its cost per landed unit.
type TUIWorkerStat struct {
	Worker            string
	Spend             float64
	Landed, FirstPass int
}

// workerStats splits the window's spend and landings by worker: an attempt's
// worker is its dispatched event's (older dispatches name none: "-"), and a
// landed unit counts for the worker of its latest dispatch. Workers sorted.
func workerStats(events []Event, w MetricsWindow) []TUIWorkerStat {
	in := func(ts string) bool {
		t, err := time.Parse(time.RFC3339Nano, ts)
		return err == nil && !t.Before(w.Since) && t.Before(w.Until)
	}
	worker, latest, dispatches := map[string]string{}, map[string]string{}, map[string]int{}
	stats := map[string]*TUIWorkerStat{}
	stat := func(name string) *TUIWorkerStat {
		if stats[name] == nil {
			stats[name] = &TUIWorkerStat{Worker: name}
		}
		return stats[name]
	}
	for _, e := range events {
		switch e.Kind {
		case "dispatched":
			name := cmp.Or(e.Worker, "-")
			worker[e.Task+"\x00"+e.Attempt], latest[e.Task] = name, name
			dispatches[e.Task]++
		case "finished":
			if in(e.TS) {
				stat(cmp.Or(worker[e.Task+"\x00"+e.Attempt], "-")).Spend += e.Cost
			}
		case "landed":
			if in(e.TS) {
				s := stat(cmp.Or(latest[e.Task], "-"))
				s.Landed++
				if dispatches[e.Task] == 1 {
					s.FirstPass++
				}
			}
		}
	}
	var out []TUIWorkerStat
	for _, s := range stats {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b TUIWorkerStat) int { return strings.Compare(a.Worker, b.Worker) })
	return out
}

// splitRows are the selected metric's window split by model (MetricsReport's
// by-model scoreboard: first-pass is accepted of inspected, landed is
// accepted) or by worker (workerStats).
func splitRows(d TUIData, window, by string) (header []string, rows [][]string) {
	tm, ok := d.Metrics[window]
	pct := func(num, den int) string {
		if den == 0 {
			return "–"
		}
		return fmtMetric("pct", float64(num)/float64(den))
	}
	per := func(spend float64, landed int) string {
		if landed == 0 {
			return "–"
		}
		return fmtUSD(spend / float64(landed))
	}
	if by == "worker" {
		header = []string{"WORKER", "SPEND", "LANDED", "FIRST-PASS", "PER LANDED"}
		if ok {
			for _, s := range tm.Workers {
				rows = append(rows, []string{s.Worker, fmtUSD(s.Spend), fmt.Sprint(s.Landed), pct(s.FirstPass, s.Landed), per(s.Spend, s.Landed)})
			}
		}
		return header, rows
	}
	header = []string{"MODEL", "SPEND", "LANDED", "FIRST-PASS", "PER LANDED"}
	if ok {
		for _, s := range tm.Cur.Cost.ByModel {
			rows = append(rows, []string{s.Model, fmtUSD(s.Spend), fmt.Sprint(s.Accepted), pct(s.Accepted, s.Inspected), per(s.Spend, s.Accepted)})
		}
	}
	return header, rows
}

// splitKey handles the :metrics split keys (issue #583 k7): Shift-M the
// metric under the cursor by model, Shift-W by worker (the same key again
// closes it), Esc back to every metric. It reports whether it used the key.
func (m *TUI) splitKey(k term.Key, rows [][]string) bool {
	by := map[rune]string{'M': "model", 'W': "worker"}[k.Rune]
	switch {
	case k.Kind == term.KeyEsc && m.split != "" && m.filter == "":
		m.split, m.cursor = "", m.splitRow
	case k.Kind != term.KeyRune || by == "":
		return false
	case m.split == by:
		m.split, m.cursor = "", m.splitRow
	case m.split != "":
		m.split = by
	case m.cursor < len(rows):
		m.split, m.splitMetric, m.splitRow, m.cursor = by, metricOfRow(rows[m.cursor]), m.cursor, 0
	}
	return true
}

// metricOfRow is the id of the metric a :metrics row shows.
func metricOfRow(row []string) string {
	for _, def := range metricDefs {
		if len(row) > 1 && def.family() == row[0] && def.name == row[1] {
			return def.id
		}
	}
	return ""
}

// metricLines are the metric drill-down's lines: the value and its trend,
// the definition, then the chart (h) or the evidence table (u, worst
// first); cur is the line of the evidence row under the cursor, -1 when none.
func (m *TUI) metricLines(d TUIData, width int) (lines []string, cur int) {
	def, _ := metricByID(m.drillTask)
	tm, ok := d.Metrics[m.window]
	if !ok {
		return []string{"computing the " + m.window + " metrics…"}, -1
	}
	c, p := def.value(tm.Cur), def.value(tm.Prev)
	lines = []string{
		fmt.Sprintf("%s %s  %s %s vs the previous %s", def.name, fmtMetric(def.kind, c), trendArrow(c, p), fmtDelta(def.kind, c, p), m.window),
		def.def, "",
	}
	units := tm.Cur.Evidence[def.id]
	if m.metricPart != "units" {
		lines = append(lines, metricChart(def, tm.Cur, units, width)...)
		return append(lines, "", fmt.Sprintf("<u> the %d units behind it", len(units))), -1
	}
	if len(units) == 0 {
		return append(lines, "no units behind this number in the "+m.window+" window"), -1
	}
	tw, vw := len("TASK"), len("VALUE")
	for _, u := range units {
		tw, vw = max(tw, cellCount(u.Task)), max(vw, cellCount(u.Value))
	}
	lines = append(lines, "  "+padCells("TASK", tw)+"  "+padCells("VALUE", vw)+"  GROUP")
	m.evCursor = max(0, min(m.evCursor, len(units)-1))
	for i, u := range units {
		mark := "  "
		if i == m.evCursor {
			mark = "> "
		}
		lines = append(lines, mark+padCells(u.Task, tw)+"  "+padCells(u.Value, vw)+"  "+u.Group)
	}
	return lines, len(lines) - len(units) + m.evCursor
}

// metricChart draws the metric's chart: a histogram with its p50 and p90
// for a time distribution, a control chart for cycle time, the flow of WIP
// over time, bars for a breakdown, else its series as a wide sparkline.
func metricChart(def metricDef, r MetricsReport, units []MetricUnit, width int) []string {
	var bars []BarRow
	switch def.chart {
	case "hist":
		return distHistogram(units, def.dist(r), width)
	case "control":
		landed := map[string]float64{}
		for _, u := range r.Evidence["flow.throughput"] {
			landed[u.Task] = u.Sort
		}
		order := slices.Clone(units)
		slices.SortStableFunc(order, func(a, b MetricUnit) int { return cmp.Compare(landed[a.Task], landed[b.Task]) })
		var pts []float64
		var labels []string
		for _, u := range order {
			pts, labels = append(pts, u.Sort/3600), append(labels, u.Task)
		}
		mean, sigma := meanSigma(pts)
		lines, _ := ControlChart(pts, mean, sigma, width, 10, labels)
		return append([]string{"cycle time per landed unit in landing order, hours"}, lines...)
	case "flow":
		lines, err := StackedFlow([][]float64{def.series(r)}, []string{def.name}, width, 10)
		if err != nil {
			return []string{err.Error()}
		}
		return lines
	case "bars":
		bars = def.bars(r)
	case "groups":
		bars = groupBars(units)
	default:
		if def.series == nil {
			return []string{"no per-bucket series: <u> lists the units"}
		}
		s := def.series(r)
		return []string{Sparkline(s, width-2), fmt.Sprintf("%d buckets of %s, max %s", len(s), whyDur(r.Window.Bucket), fmtMetric(def.kind, slices.Max(append([]float64{0}, s...))))}
	}
	if len(bars) == 0 {
		return []string{"nothing to chart in the window"}
	}
	return HBars(bars, width)
}

// distHistogram buckets the units' times (their Sort, in seconds) into at
// most six equal ranges up to the slowest, marking the buckets of the p50
// and the p90.
func distHistogram(units []MetricUnit, dist Dist, width int) []string {
	if len(units) == 0 {
		return []string{"no units in the window"}
	}
	hi := 0.0
	for _, u := range units {
		hi = max(hi, u.Sort)
	}
	n := min(6, len(units))
	step := max(hi/float64(n), 1)
	at := func(secs float64) int { return max(0, min(n-1, int(math.Ceil(secs/step))-1)) }
	buckets := make([]HistBucket, n)
	for i := range buckets {
		buckets[i].Label = "≤ " + whyDur(time.Duration(float64(i+1)*step*float64(time.Second)))
	}
	for _, u := range units {
		buckets[at(u.Sort)].Count++
	}
	markers := []Marker{
		{Label: "p50 " + whyDur(dist.P50), BucketIndex: at(dist.P50.Seconds())},
		{Label: "p90 " + whyDur(dist.P90), BucketIndex: at(dist.P90.Seconds())},
	}
	return Histogram(buckets, markers, width)
}

// meanSigma is the mean and population standard deviation of xs.
func meanSigma(xs []float64) (mean, sigma float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		sigma += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(sigma / float64(len(xs)))
}

// openMetric opens metric id's drill-down at its chart.
func (m *TUI) openMetric(id string) {
	def, ok := metricByID(id)
	if !ok {
		m.flash("no metric " + id)
		return
	}
	m.drillKind, m.drillTask, m.detailTop = "metric", id, 0
	m.metricPart, m.evCursor, m.metricBack = "chart", 0, nil
	m.pushCrumbs(def.name, "chart")
}

// metricKey handles a key of the metric drill-down: h the chart, u the
// evidence, j k (and the arrows) move along the evidence, Enter opens the
// unit under the cursor at its why. It reports whether it used the key.
func (m *TUI) metricKey(k term.Key, d TUIData) bool {
	units := d.Metrics[m.window].Cur.Evidence[m.drillTask]
	part := map[rune]string{'h': "chart", 'u': "units"}[k.Rune]
	switch {
	case k.Kind == term.KeyRune && part != "":
		m.metricPart, m.detailTop = part, 0
		m.crumbs[len(m.crumbs)-1] = part
	case m.metricPart != "units":
		return false
	case k.Kind == term.KeyDown || (k.Kind == term.KeyRune && k.Rune == 'j'):
		m.evCursor = min(m.evCursor+1, max(len(units)-1, 0))
	case k.Kind == term.KeyUp || (k.Kind == term.KeyRune && k.Rune == 'k'):
		m.evCursor = max(m.evCursor-1, 0)
	case k.Kind == term.KeyEnter:
		if m.evCursor >= len(units) {
			return true
		}
		back := &metricReturn{id: m.drillTask, part: m.metricPart, row: m.evCursor, crumbs: m.Crumbs()}
		m.drillUnit(d, units[m.evCursor].Task, "why")
		if m.drillKind == "why" {
			m.metricBack = back
		}
	default:
		return false
	}
	return true
}

// metricReturnTo reopens the metric drill-down a unit was opened from (Esc
// on the unit); false when it was not opened from one.
func (m *TUI) metricReturnTo() bool {
	b := m.metricBack
	if b == nil {
		return false
	}
	m.drillKind, m.drillTask, m.detailTop = "metric", b.id, 0
	m.metricPart, m.evCursor, m.metricBack = b.part, b.row, nil
	m.crumbs = b.crumbs
	return true
}

// headerStats are the header's middle column: the last 24h's throughput
// with its sparkline, WIP, first-pass yield, andons, spend, and the workers
// busy of the most; nil before the metrics arrive.
func headerStats(d TUIData) []string {
	tm, ok := d.Metrics["24h"]
	if !ok {
		return nil
	}
	r := tm.Cur
	busy, most := 0, 0
	for _, l := range d.Floor.Lines {
		busy, most = busy+l.Busy, most+l.MaxParallel
	}
	return []string{
		strings.TrimSpace(fmt.Sprintf("throughput %d/24h %s", r.Flow.Throughput, Sparkline(r.Flow.ThroughputSeries, 12))),
		fmt.Sprintf("wip %d", r.Flow.WIP),
		"first-pass " + fmtMetric("pct", r.Quality.FirstPassYield),
		fmt.Sprintf("andons %d", r.Reliability.AndonTotal),
		"spend 24h " + fmtUSD(r.Cost.Spend),
		fmt.Sprintf("workers %d/%d busy", busy, most),
	}
}

// gateFailRate is the share of the window's conclusive gate readings that
// failed, over every gate.
func gateFailRate(r MetricsReport) float64 {
	pass, total := 0, 0
	for _, g := range r.Quality.Gates {
		pass, total = pass+g.Pass, total+g.Total
	}
	if total == 0 {
		return 0
	}
	return round(float64(total-pass)/float64(total), 4)
}

// gateBars are each gate's fail rate.
func gateBars(r MetricsReport) []BarRow {
	var out []BarRow
	for _, g := range r.Quality.Gates {
		fail := 1 - g.Rate
		out = append(out, BarRow{Label: "gate " + g.Gate, Value: fail, Text: fmt.Sprintf("%.0f%% of %d  %s", fail*100, g.Total, g.Text)})
	}
	return out
}

// andonBars are the window's andons by kind, most first.
func andonBars(r MetricsReport) []BarRow {
	var out []BarRow
	for k, n := range r.Reliability.Andons {
		out = append(out, BarRow{Label: k, Value: float64(n), Text: fmt.Sprint(n)})
	}
	sortBars(out)
	return out
}

// pausedTotal is the pause time summed over models, in seconds.
func pausedTotal(r MetricsReport) float64 {
	var d time.Duration
	for _, p := range r.Reliability.Paused {
		d += p.Paused
	}
	return d.Seconds()
}

// pausedBars are each model's pause time.
func pausedBars(r MetricsReport) []BarRow {
	var out []BarRow
	for _, p := range r.Reliability.Paused {
		out = append(out, BarRow{Label: p.Model, Value: p.Paused.Seconds(), Text: whyDur(p.Paused)})
	}
	return out
}

// modelBars are each model's cost per accepted (landed) unit, "–" for a
// model none of whose units landed, and its spend.
func modelBars(r MetricsReport) []BarRow {
	var out []BarRow
	for _, s := range r.Cost.ByModel {
		per := "–"
		if s.Accepted > 0 {
			per = fmtUSD(s.CostPerAccepted) + "/unit"
		}
		out = append(out, BarRow{Label: s.Model, Value: s.CostPerAccepted, Text: per + "  " + fmtUSD(s.Spend) + " spent"})
	}
	sortBars(out)
	return out
}

// fitBars is HBars of rows at width with every row's text whole: the full
// text when it fits, else its part before the first double space, else none,
// so a narrow panel never shows an amount cut in two.
func fitBars(rows []BarRow, width int) []string {
	for _, cut := range []func(string) string{
		func(s string) string { return s },
		func(s string) string { s, _, _ = strings.Cut(s, "  "); return s },
		func(string) string { return "" },
	} {
		rs := slices.Clone(rows)
		for i := range rs {
			rs[i].Text = cut(rs[i].Text)
		}
		lines, whole := HBars(rs, width), true
		for i, l := range lines {
			whole = whole && strings.HasSuffix(l, rs[i].Text)
		}
		if whole {
			return lines
		}
	}
	return HBars(nil, width)
}

// fmtUSD is an amount as compact as it reads: $7.25 under $100, then whole
// dollars ($711), then thousands ($1.2k) and millions ($3.4M).
func fmtUSD(v float64) string {
	sign, a := "", math.Abs(v)
	if v < 0 {
		sign = "-"
	}
	switch {
	case a >= 999_950:
		return fmt.Sprintf("%s$%.1fM", sign, a/1e6)
	case a >= 999.5:
		return fmt.Sprintf("%s$%.1fk", sign, a/1e3)
	case a >= 100:
		return fmt.Sprintf("%s$%.0f", sign, a)
	}
	return fmt.Sprintf("%s$%.2f", sign, a)
}

// workerBars are each worker's utilization.
func workerBars(r MetricsReport) []BarRow {
	var out []BarRow
	for _, w := range r.Capacity.Workers {
		out = append(out, BarRow{Label: w.Worker, Value: w.Utilization, Text: fmt.Sprintf("%.0f%% of %d", w.Utilization*100, w.MaxParallel)})
	}
	return out
}

// groupBars count a metric's evidence by group, most first.
func groupBars(units []MetricUnit) []BarRow {
	n := map[string]int{}
	var order []string
	for _, u := range units {
		if n[u.Group] == 0 {
			order = append(order, u.Group)
		}
		n[u.Group]++
	}
	var out []BarRow
	for _, g := range order {
		out = append(out, BarRow{Label: g, Value: float64(n[g]), Text: fmt.Sprintf("%d units", n[g])})
	}
	sortBars(out)
	return out
}

// sortBars orders rows by value, largest first, then by label.
func sortBars(rows []BarRow) {
	slices.SortStableFunc(rows, func(a, b BarRow) int {
		if a.Value != b.Value {
			if a.Value > b.Value {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Label, b.Label)
	})
}
