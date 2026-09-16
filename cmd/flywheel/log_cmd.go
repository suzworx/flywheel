package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"flywheel/internal/flywheel"
)

func init() {
	register("log", "append an event to the flywheel event log", runLog)
	registerHelp("log", "flywheel log [flags]", func() *flag.FlagSet { fs, _ := logFlags(); return fs })
}

// logOptions holds the parsed log flags.
type logOptions struct {
	dir     string
	jsonIn  string
	task    string
	kind    string
	session string
	model   string
	attempt string
	rc      string
	reason  string
	verdict string
	brief   string
	commit  string
	note    string
	goal    string
	noState bool
}

// logFlags defines log's flags once, so help and run share them.
func logFlags() (*flag.FlagSet, *logOptions) {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &logOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.jsonIn, "json", "", "file of events to append, or - for stdin")
	fs.StringVar(&o.task, "task", "", "task id")
	fs.StringVar(&o.kind, "kind", "", "event kind")
	fs.StringVar(&o.session, "session", "", "session id")
	fs.StringVar(&o.model, "model", "", "model name")
	fs.StringVar(&o.attempt, "attempt", "", "attempt (r1, c1, ...)")
	fs.StringVar(&o.rc, "rc", "", "exit code")
	fs.StringVar(&o.reason, "reason", "", "finish reason or classification")
	fs.StringVar(&o.verdict, "verdict", "", "inspected verdict (pass, rework, scrap, or escalate) or reviewed verdict (pass, correct, or reject)")
	fs.StringVar(&o.brief, "brief", "", "brief file")
	fs.StringVar(&o.commit, "commit", "", "commit id")
	fs.StringVar(&o.note, "note", "", "free-form note")
	fs.StringVar(&o.goal, "goal", "", "goal id a planned event links to")
	fs.BoolVar(&o.noState, "no-state", false, "skip state derivation after appending")
	return fs, o
}

// appendEvents appends each event and then derives state unless noState.
func appendEvents(dir string, events []flywheel.Event, noState bool) {
	for _, e := range events {
		if err := flywheel.AppendEvent(dir, e); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
			os.Exit(1)
		}
	}
	if noState {
		return
	}
	if _, err := flywheel.WriteState(dir); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		os.Exit(1)
	}
}

// runLogJSON appends events read (strictly) from a file or stdin, one JSON
// object per line.
func runLogJSON(dir, path string, noState bool) {
	if path == "-" {
		events, err := flywheel.ParseEvents(os.Stdin, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
			os.Exit(1)
		}
		appendEvents(dir, events, noState)
		return
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		os.Exit(1)
	}
	events, err := flywheel.ParseEvents(f, true)
	f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		os.Exit(1)
	}
	appendEvents(dir, events, noState)
}

func runLog(args []string) {
	fs, o := logFlags()
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		usage(os.Stderr)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "flywheel log: unexpected argument %q\n", fs.Arg(0))
		usage(os.Stderr)
		os.Exit(2)
	}
	if o.jsonIn != "" {
		runLogJSON(o.dir, o.jsonIn, o.noState)
		return
	}

	var e flywheel.Event
	e.Task = o.task
	e.Kind = o.kind
	e.Session = o.session
	e.Model = o.model
	e.Attempt = o.attempt
	e.Reason = o.reason
	e.Verdict = o.verdict
	e.Brief = o.brief
	e.Commit = o.commit
	e.Note = o.note
	if o.goal != "" {
		if _, ok := findGoal(o.dir, o.goal); !ok {
			fmt.Fprintf(os.Stderr, "flywheel log: unknown goal %q\n", o.goal)
			if gs := knownGoals(o.dir); len(gs) > 0 {
				ids := make([]string, len(gs))
				for i, g := range gs {
					ids[i] = g.ID
				}
				fmt.Fprintf(os.Stderr, "known goals: %s\n", strings.Join(ids, ", "))
			}
			os.Exit(1)
		}
	}
	e.GoalID = o.goal
	if o.rc != "" {
		v, err := strconv.ParseInt(o.rc, 10, strconv.IntSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: invalid --rc %q: %v\n", o.rc, err)
			os.Exit(1)
		}
		p := new(int)
		*p = int(v)
		e.RC = p
	}
	appendEvents(o.dir, []flywheel.Event{e}, o.noState)
}
