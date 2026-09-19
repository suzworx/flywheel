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
	register("explain", "tell one task's whole story from the ledger", runExplain)
	registerHelp("explain", "flywheel explain <task> [--json] [--dir DIR]", func() *flag.FlagSet { fs, _ := explainFlags(); return fs })
}

// explainOptions holds the parsed explain flags.
type explainOptions struct {
	dir  string
	json bool
}

// explainFlags defines explain's flags once, so help and run share them.
func explainFlags() (*flag.FlagSet, *explainOptions) {
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &explainOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.json, "json", false, "print the explanation as JSON")
	return fs, o
}

// explainUsage prints the flywheel explain usage line.
func explainUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel explain <task> [--json] [--dir DIR]")
}

// runExplain implements `flywheel explain <task>`: one task's whole story
// from the ledger, in Markdown by default or JSON with --json. It is
// read-only: it never derives state or writes. Exit 2 on a missing task
// argument, 1 on any other error.
func runExplain(args []string) {
	fs, o := explainFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel explain: %v\n", err)
		explainUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel explain: exactly one task id is required\n")
		explainUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	events, err := flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel explain: %v\n", err)
		os.Exit(1)
	}
	x, err := flywheel.Explain(events, task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel explain: %v\n", err)
		os.Exit(1)
	}
	if o.json {
		b, err := json.MarshalIndent(x, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel explain: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s\n", b)
	} else {
		if err := flywheel.RenderExplanation(os.Stdout, x); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel explain: %v\n", err)
			os.Exit(1)
		}
	}
}
