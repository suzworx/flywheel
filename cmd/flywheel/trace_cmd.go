package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("trace", "print everything one session did, across tasks", runTrace)
	registerHelp("trace", "flywheel trace <session> [--dir DIR]", func() *flag.FlagSet { fs, _ := traceFlags(); return fs })
}

// traceOptions holds the parsed trace flags.
type traceOptions struct {
	dir string
}

// traceFlags defines trace's flags once, so help and run share them.
func traceFlags() (*flag.FlagSet, *traceOptions) {
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &traceOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	return fs, o
}

// traceUsage prints the flywheel trace usage line.
func traceUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel trace <session> [--dir DIR]")
}

// runTrace implements `flywheel trace <session>`: one line per event in log
// order whose session matches the argument, across every task, in the form
// "<ts> <task|-> <kind> (detail)" (the parenthetical omitted when the event
// carries none of attempt, verdict, model, staffed role or session_command
// note). It is read-only: it never derives state or writes. A session with no
// matching events prints "no events for session <id>" and exits 0. Exit 2 on
// a missing session argument, 1 on any other error.
func runTrace(args []string) {
	fs, o := traceFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel trace: %v\n", err)
		traceUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel trace: exactly one session id is required\n")
		traceUsage(os.Stderr)
		os.Exit(2)
	}
	session := pos[0]
	lines, terr := flywheel.Trace(o.dir, session)
	if terr != nil {
		fmt.Fprintf(os.Stderr, "flywheel trace: %v\n", terr)
		os.Exit(1)
	}
	if len(lines) == 0 {
		fmt.Printf("no events for session %s\n", session)
		return
	}
	for _, l := range lines {
		task := l.Task
		if task == "" {
			task = "-"
		}
		if l.Detail == "" {
			fmt.Printf("%s %s %s\n", l.TS, task, l.Kind)
			continue
		}
		fmt.Printf("%s %s %s (%s)\n", l.TS, task, l.Kind, l.Detail)
	}
}
