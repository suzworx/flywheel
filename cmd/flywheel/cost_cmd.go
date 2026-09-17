package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("cost", "sum finished events' tokens and cost per task and model", runCost)
	registerHelp("cost", "flywheel cost [--dir DIR] [--json]", func() *flag.FlagSet { fs, _ := costFlags(); return fs })
}

// costOptions holds the parsed cost flags.
type costOptions struct {
	dir     string
	jsonOut bool
}

// costFlags defines cost's flags once, so help and run share them.
func costFlags() (*flag.FlagSet, *costOptions) {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &costOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	return fs, o
}

// costUsage prints the flywheel cost usage line.
func costUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel cost [--dir DIR] [--json]")
}

// runCost implements `flywheel cost`: a read-only sum of the finished
// events' tokens and cost, grouped per task and per model. Exit 0, 1 on
// error, 2 on usage. --json prints the CostReport instead of the text lines.
func runCost(args []string) {
	fs, o := costFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel cost: %v\n", perr)
		costUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel cost: unexpected argument %q\n", pos[0])
		costUsage(os.Stderr)
		os.Exit(2)
	}
	rep, err := flywheel.Cost(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel cost: %v\n", err)
		os.Exit(1)
	}
	if o.jsonOut {
		b, _ := json.Marshal(rep)
		fmt.Println(string(b))
		return
	}
	printCost(rep)
}

// printCost writes the text cost summary: one row per task and per model,
// then the total.
func printCost(rep flywheel.CostReport) {
	fmt.Println("per task:")
	for _, r := range rep.Tasks {
		fmt.Printf("%s tokens=%d cost=$%.4f\n", r.ID, r.Count(), r.Cost)
	}
	fmt.Println("per model:")
	for _, r := range rep.Models {
		fmt.Printf("%s tokens=%d cost=$%.4f\n", r.ID, r.Count(), r.Cost)
	}
	fmt.Printf("total tokens=%d cost=$%.4f\n", rep.Total.Count(), rep.Total.Cost)
}
