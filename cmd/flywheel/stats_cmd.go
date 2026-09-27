package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("stats", "print the factory's own numbers: rates, corrections, cost", runStats)
	registerHelp("stats", statsUsageLine, func() *flag.FlagSet { fs, _ := statsFlags(); return fs })
}

// statsUsageLine is flywheel stats's usage.
const statsUsageLine = "flywheel stats [--dir DIR] [--by model [--kind]] [--metrics [--window 24h|7d|30d] [--wip-stale-after D]] [--json]"

// statsOptions holds the parsed stats flags.
type statsOptions struct {
	dir        string
	by         string
	kind       bool
	metrics    bool
	window     string
	staleAfter time.Duration
	jsonOut    bool
}

// statsFlags defines stats's flags once, so help and run share them.
func statsFlags() (*flag.FlagSet, *statsOptions) {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &statsOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.by, "by", "", `break the numbers down: "model" adds the per-model scoreboard`)
	fs.BoolVar(&o.kind, "kind", false, "with --by model, also break the scoreboard down per task kind (a brief's kind: header)")
	fs.BoolVar(&o.metrics, "metrics", false, "print the factory metrics over a window: flow, quality, reliability, cost, capacity (docs/metrics.md)")
	fs.StringVar(&o.window, "window", "24h", `with --metrics, the window ending now: "24h", "7d" or "30d"`)
	fs.DurationVar(&o.staleAfter, "wip-stale-after", 0, "with --metrics, a unit in progress with no event for this long counts as stale, not wip (0 means the 7d default)")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	return fs, o
}

// statsUsage prints the flywheel stats usage line.
func statsUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+statsUsageLine)
}

// runStats implements `flywheel stats`: the factory's health as numbers that
// trend, derived only from the event log. Exit 0, 1 on error, 2 on usage.
// --json prints the StatsReport instead of the text lines.
func runStats(args []string) {
	if code := statsMain(args, os.Stdout, os.Stderr, time.Now()); code != 0 {
		os.Exit(code)
	}
}

// statsMain runs flywheel stats at now and returns the exit code (the test
// entry point). --metrics and --json write to out, the text summary to
// stdout.
func statsMain(args []string, out, errw io.Writer, now time.Time) int {
	fs, o := statsFlags()
	usage := func(format string, a ...any) int {
		fmt.Fprintf(errw, "flywheel stats: "+format+"\n", a...)
		statsUsage(errw)
		return 2
	}
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		return usage("%v", perr)
	}
	if len(pos) != 0 {
		return usage("unexpected argument %q", pos[0])
	}
	if o.by != "" && o.by != "model" {
		return usage(`--by must be "model"`)
	}
	if o.kind && o.by != "model" {
		return usage("--kind needs --by model")
	}
	if o.staleAfter < 0 {
		return usage("--wip-stale-after must not be negative")
	}
	if o.metrics {
		if o.by != "" {
			return usage("--metrics does not take --by")
		}
		if _, err := flywheel.WindowFor(o.window, now); err != nil {
			return usage("--%v", err)
		}
		if err := runStatsMetrics(out, o.dir, o.window, o.staleAfter, o.jsonOut, now); err != nil {
			fmt.Fprintf(errw, "flywheel stats: %v\n", err)
			return 1
		}
		return 0
	}
	rep, err := flywheel.StatsWith(o.dir, flywheel.StatsOptions{ByModel: o.by == "model", ByKind: o.kind})
	if err != nil {
		fmt.Fprintf(errw, "flywheel stats: %v\n", err)
		return 1
	}
	if o.jsonOut {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Fprintln(out, string(b))
		return 0
	}
	printStats(rep)
	if o.by == "model" {
		printByModel("By model:", "", rep.ByModel)
	}
	if o.kind {
		printByModel("By model and kind:", "KIND", rep.ByModelKind)
	}
	return 0
}

// printByModel writes the per-model scoreboard (issue #473) under title: one
// row per adapter, model and variant, ATTEMPTS the sample size, a rate n/a
// below flywheel.StatsMinSample. A non-empty kindCol adds a first column
// holding each row's task kind (issue #475).
func printByModel(title, kindCol string, rows []flywheel.StatsModel) {
	fmt.Println(title)
	if kindCol != "" {
		fmt.Printf("  %-10s", kindCol)
	}
	fmt.Printf("  %-10s %-24s %-8s %8s %6s %6s %7s %6s %6s %7s %9s %6s %9s %10s\n",
		"ADAPTER", "MODEL", "VARIANT", "ATTEMPTS", "CLEAN%", "GATE%", "ACCEPT%", "SILENT", "FAILED", "STALLED", "CORR/TASK", "MED-S", "SPEND", "$/ACCEPTED")
	if len(rows) == 0 {
		fmt.Println("  none")
	}
	for _, r := range rows {
		model, variant := r.Model, r.Variant
		if model == "" {
			model = "-"
		}
		if variant == "" {
			variant = "-"
		}
		if kindCol != "" {
			fmt.Printf("  %-10s", r.Kind)
		}
		fmt.Printf("  %-10s %-24s %-8s %8d %6s %6s %7s %6d %6d %7d %9.2f %6.0f %9.4f %10.4f\n",
			r.Adapter, model, variant, r.Attempts, ratePct(r.CleanRate), ratePct(r.GatePassRate), ratePct(r.AcceptedRate),
			r.Silent, r.Failed, r.Stalled, r.CorrectionsPerTask, r.MedianAttemptSeconds, r.Spend, r.CostPerAccepted)
	}
}

// ratePct renders a per-model rate as a whole percentage, or n/a when its
// sample was under the minimum.
func ratePct(r *float64) string {
	if r == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", *r*100)
}

// printStats writes the text summary, one metric per line in the order the
// brief lists them.
func printStats(rep flywheel.StatsReport) {
	fmt.Printf("Tasks: total %d, landed %d, passed %d, rejected %d\n",
		rep.Tasks.Total, rep.Tasks.Landed, rep.Tasks.Passed, rep.Tasks.Rejected)
	fmt.Printf("Lead-implemented: %d of %d landed\n", rep.Tasks.LeadImplemented, rep.Tasks.Landed)
	fmt.Printf("First-pass rate: %.2f (%d/%d)\n", rep.FirstPassRate, rep.FirstPassCount, rep.FirstPassTotal)
	fmt.Printf("Corrections per task: %.2f\n", rep.CorrectionsPerTask)
	reasons := make([]string, 0, len(rep.FinishReasons))
	for r := range rep.FinishReasons {
		reasons = append(reasons, r)
	}
	slices.Sort(reasons)
	fmt.Print("Finish reasons:")
	if len(reasons) == 0 {
		fmt.Print(" none")
	}
	for _, r := range reasons {
		fmt.Printf(" %s=%d", r, rep.FinishReasons[r])
	}
	fmt.Printf(" (unclean %.2f per 100)\n", rep.UncleanPer100)
	fmt.Printf("Mean attempt seconds: %d\n", rep.MeanAttemptSeconds)
	fmt.Printf("Cost per landed task: $%.4f\n", rep.CostPerLandedTask)
	fmt.Printf("Tokens: input %d, output %d, reasoning %d, cache read %d, cache write %d\n",
		rep.Tokens.Input, rep.Tokens.Output, rep.Tokens.Reasoning, rep.Tokens.CacheRead, rep.Tokens.CacheWrite)
	fmt.Printf("Spend: $%.4f\n", rep.Spend)
	if rep.Baseline != nil {
		fmt.Printf("Frontier baseline (%s): $%.4f for the same tokens; spend is %.1f%% of it\n",
			rep.Baseline.Model, rep.Baseline.Cost, rep.Baseline.Ratio*100)
	} else {
		fmt.Printf("Frontier baseline: not configured (add \"baseline\" to .flywheel/config.json)\n")
	}
	rv := rep.Review
	fmt.Printf("Review: %d round(s), %d finding(s), %d dismissed\n", rv.Reviews, rv.Findings, rv.Dismissals)
	fmt.Printf("Review findings by severity:%s\n", countLine(rv.BySeverity))
	fmt.Printf("Review findings by category:%s\n", countLine(rv.ByCategory))
	fmt.Printf("Review median rounds to clean: %.1f (%d clean unit(s))\n", rv.MedianRoundsToClean, rv.CleanUnits)
	fmt.Printf("Review caught what gates missed: %d finding(s) on units whose gates had all passed\n", rv.CaughtAfterGates)
	for _, l := range rv.ByLevel {
		fmt.Printf("Review level %-7s %d round(s), %d not pass, %d finding(s), %d blocking\n", l.Level+":", l.Rounds, l.NotPass, l.Findings, l.Blocking)
	}
	if len(rv.ByPersona) > 0 {
		fmt.Printf("  %-12s %-5s %7s %8s %5s %8s %9s  %s\n", "PERSONA", "LEVEL", "REVIEWS", "FINDINGS", "FIXED", "DISPUTED", "DISMISSED", "BY SEVERITY")
		for _, p := range rv.ByPersona {
			fmt.Printf("  %-12s %-5s %7d %8d %5d %8d %9d %s\n", p.Persona, p.Level, p.Reviews, p.Findings, p.Fixed, p.Disputed, p.Dismissed, countLine(p.BySeverity))
		}
	}
}

// runStatsMetrics writes flywheel stats --metrics for dir to out: the report
// over the named window ending at now, as JSON (every series included), or
// as the family table with a trend arrow against the previous equal window.
// staleAfter is the window's WIP stale threshold, 0 for the default.
func runStatsMetrics(out io.Writer, dir, window string, staleAfter time.Duration, jsonOut bool, now time.Time) error {
	w, err := flywheel.WindowFor(window, now)
	if err != nil {
		return err
	}
	w.StaleAfter = staleAfter
	cur, err := flywheel.MetricsFor(dir, w)
	if err != nil {
		return err
	}
	if jsonOut {
		b, err := json.MarshalIndent(cur, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	prev, err := flywheel.MetricsFor(dir, w.Previous())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Metrics over the last %s (%s to %s); trend vs the previous %s\n",
		window, w.Since.UTC().Format(time.RFC3339), w.Until.UTC().Format(time.RFC3339), window)
	pf := metricFamilies(prev)
	for i, f := range metricFamilies(cur) {
		fmt.Fprintf(out, "%s:\n", f.name)
		for j, r := range f.rows {
			fmt.Fprintf(out, "  %-22s %-32s %s\n", r.name, r.value, trendArrow(r.num, pf[i].rows[j].num))
		}
	}
	return nil
}

// metricFamily is one group of the metrics table.
type metricFamily struct {
	name string
	rows []metricRow
}

// metricRow is one metric: its label, rendered value and the number its trend
// compares.
type metricRow struct {
	name, value string
	num         float64
}

// metricFamilies lays rep out as the table's families, in docs/metrics.md order.
func metricFamilies(rep flywheel.MetricsReport) []metricFamily {
	dur := func(name string, d time.Duration) metricRow {
		return metricRow{name, d.Round(time.Second).String(), d.Seconds()}
	}
	count := func(name string, n int) metricRow { return metricRow{name, fmt.Sprintf("%d", n), float64(n)} }
	pct := func(name string, x float64) metricRow { return metricRow{name, fmt.Sprintf("%.1f%%", x*100), x} }
	num := func(name, format string, x float64) metricRow { return metricRow{name, fmt.Sprintf(format, x), x} }
	fl, q, rl, c, cp := rep.Flow, rep.Quality, rep.Reliability, rep.Cost, rep.Capacity
	pass, total := 0, 0
	for _, g := range q.Gates {
		pass, total = pass+g.Pass, total+g.Total
	}
	gateRate := 0.0
	if total > 0 {
		gateRate = float64(pass) / float64(total)
	}
	var paused time.Duration
	for _, p := range rl.Paused {
		paused += p.Paused
	}
	andons := count("andons", rl.AndonTotal)
	if rl.AndonTotal > 0 {
		andons.value += " (" + countLine(rl.Andons)[1:] + ")"
	}
	// The oldest stale age rides in the stale row, so prev and cur keep the
	// same rows for the trend.
	stale := count("stale", fl.Stale)
	if fl.Stale > 0 {
		stale.value += " (oldest " + fl.StaleOldest.Round(time.Second).String() + ")"
	}
	return []metricFamily{
		{"Flow", []metricRow{count("throughput", fl.Throughput), count("wip", fl.WIP), stale,
			dur("lead time p50", fl.LeadTime.P50), dur("lead time p90", fl.LeadTime.P90),
			dur("cycle time p50", fl.CycleTime.P50), dur("cycle time p90", fl.CycleTime.P90),
			dur("queue time p50", fl.QueueTime.P50), dur("touch time mean", fl.TouchTime.Mean),
			pct("flow efficiency", fl.FlowEfficiency)}},
		{"Quality", []metricRow{pct("first-pass yield", q.FirstPassYield), num("rework rate", "%.2f", q.ReworkRate),
			pct("gate pass rate", gateRate), num("review find rate", "%.2f", q.ReviewFindRate),
			pct("blocking share", q.BlockingShare), count("escapes", q.Escapes)}},
		{"Reliability", []metricRow{andons, count("andons cleared", rl.Cleared), dur("mttr p50", rl.MTTR.P50),
			dur("frozen", rl.Frozen.Total), dur("rate-limit paused", paused)}},
		{"Cost", []metricRow{num("spend", "$%.4f", c.Spend), num("cost per unit", "$%.4f", c.CostPerUnit),
			num("cost per landed", "$%.4f", c.CostPerLanded), num("tokens per step", "%.1f", c.TokensPerStep)}},
		{"Capacity", []metricRow{pct("utilization", cp.Utilization), pct("idle share", cp.IdleShare)}},
	}
}

// trendArrow is ↑ when cur is above prev, ↓ when below, → when equal.
func trendArrow(cur, prev float64) string {
	switch {
	case cur > prev:
		return "↑"
	case cur < prev:
		return "↓"
	}
	return "→"
}

// countLine renders counts as " k=v ..." in key order, or " none".
func countLine(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) == 0 {
		return " none"
	}
	s := ""
	for _, k := range keys {
		s += fmt.Sprintf(" %s=%d", k, m[k])
	}
	return s
}
