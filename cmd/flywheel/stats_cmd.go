package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("stats", "print the factory's own numbers: rates, corrections, cost", runStats)
	registerHelp("stats", "flywheel stats [--dir DIR] [--json]", func() *flag.FlagSet { fs, _ := statsFlags(); return fs })
}

// statsOptions holds the parsed stats flags.
type statsOptions struct {
	dir     string
	jsonOut bool
}

// statsFlags defines stats's flags once, so help and run share them.
func statsFlags() (*flag.FlagSet, *statsOptions) {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &statsOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	return fs, o
}

// statsUsage prints the flywheel stats usage line.
func statsUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel stats [--dir DIR] [--json]")
}

// runStats implements `flywheel stats`: the factory's health as numbers that
// trend, derived only from the event log. Exit 0, 1 on error, 2 on usage.
// --json prints the StatsReport instead of the text lines.
func runStats(args []string) {
	fs, o := statsFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel stats: %v\n", perr)
		statsUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel stats: unexpected argument %q\n", pos[0])
		statsUsage(os.Stderr)
		os.Exit(2)
	}
	rep, err := flywheel.Stats(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel stats: %v\n", err)
		os.Exit(1)
	}
	if o.jsonOut {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		return
	}
	printStats(rep)
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
