package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("next", "print the reconciler's next actions", runNext)
	registerHelp("next", "flywheel next [--dir DIR] [--now RFC3339] [--json]", func() *flag.FlagSet { fs, _ := nextFlags(); return fs })
}

// nextOptions holds the parsed next flags.
type nextOptions struct {
	dir     string
	now     string
	jsonOut bool
}

// nextFlags defines next's flags once, so help and run share them.
func nextFlags() (*flag.FlagSet, *nextOptions) {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &nextOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.now, "now", "", "RFC3339 instant to reconcile at; makes output reproducible")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	return fs, o
}

// nextUsage prints the flywheel next usage line.
func nextUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel next [--dir DIR] [--now RFC3339] [--json]")
}

// runNext implements `flywheel next`: a read-only reconcile. It prints one
// line per action, or "nothing to do". Exit 0, 1 on error, 2 on usage.
func runNext(args []string) {
	fs, o := nextFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel next: %v\n", perr)
		nextUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel next: unexpected argument %q\n", pos[0])
		nextUsage(os.Stderr)
		os.Exit(2)
	}
	clock, err := clockFor(o.now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel next: --now %q is not RFC3339: %v\n", o.now, err)
		nextUsage(os.Stderr)
		os.Exit(2)
	}
	actions, err := flywheel.NextActions(o.dir, clock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel next: %v\n", err)
		os.Exit(1)
	}
	if o.jsonOut {
		b, _ := json.Marshal(actions)
		fmt.Println(string(b))
		return
	}
	printNext(actions)
}

// printNext writes the actions one line each:
// "<n>. <KIND>  <task> <attempt>  <reason>", plus an indented evidence line
// when set, or "nothing to do" for an empty list.
func printNext(actions []flywheel.Action) {
	if len(actions) == 0 {
		fmt.Println("nothing to do")
		return
	}
	for i, a := range actions {
		var b strings.Builder
		fmt.Fprintf(&b, "%d. %s  %s", i+1, a.Kind, a.Task)
		if a.Attempt != "" {
			fmt.Fprintf(&b, " %s", a.Attempt)
		}
		if a.Reason != "" {
			fmt.Fprintf(&b, "  %s", a.Reason)
		}
		fmt.Println(b.String())
		if a.Evidence != "" {
			fmt.Printf("  evidence: %s\n", a.Evidence)
		}
	}
}
