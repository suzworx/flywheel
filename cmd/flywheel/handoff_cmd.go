package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("handoff", "print the handoff summary for a new head", runHandoff)
	registerHelp("handoff", "flywheel handoff [--dir DIR] [--stdout]", func() *flag.FlagSet { fs, _ := handoffFlags(); return fs })
}

// handoffOptions holds the parsed handoff flags.
type handoffOptions struct {
	dir    string
	stdout bool
}

// handoffFlags defines handoff's flags once, so help and run share them.
func handoffFlags() (*flag.FlagSet, *handoffOptions) {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &handoffOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.stdout, "stdout", false, "print the summary to stdout instead of flywheel.md")
	return fs, o
}

// handoffUsage prints the flywheel handoff usage line.
func handoffUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel handoff [--dir DIR] [--stdout]")
}

// runHandoff implements `flywheel handoff`: it derives the handoff summary
// from the event log and config. With --stdout the summary prints to stdout;
// without it, it is written into flywheel.md between the handoff markers.
// Exit 0, 1 on error, 2 on usage. An empty log is not an error.
func runHandoff(args []string) {
	fs, o := handoffFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel handoff: %v\n", perr)
		handoffUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel handoff: unexpected argument %q\n", pos[0])
		handoffUsage(os.Stderr)
		os.Exit(2)
	}
	h, err := flywheel.HandoffSummary(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel handoff: %v\n", err)
		os.Exit(1)
	}
	if o.stdout {
		fmt.Println(flywheel.HandoffText(h))
		return
	}
	if err := flywheel.WriteHandoff(o.dir, h); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel handoff: %v\n", err)
		os.Exit(1)
	}
}
