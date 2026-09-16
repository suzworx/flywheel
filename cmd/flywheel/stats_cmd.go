package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"flywheel/internal/flywheel"
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
}
