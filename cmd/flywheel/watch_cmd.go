package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("watch", "print the factory's events as readable lines while agents work", runWatch)
	registerHelp("watch", "flywheel watch [--once] [--last N] [--interval D] [--dir DIR]", func() *flag.FlagSet { fs, _ := watchFlags(); return fs })
}

// watchOptions holds the parsed watch flags.
type watchOptions struct {
	dir      string
	once     bool
	last     int
	interval time.Duration
}

// watchFlags defines watch's flags once, so help and run share them.
func watchFlags() (*flag.FlagSet, *watchOptions) {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &watchOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.once, "once", false, "print the last N events and exit")
	fs.IntVar(&o.last, "last", 20, "how many recent events to print first")
	fs.DurationVar(&o.interval, "interval", time.Second, "how often to look for new events")
	return fs, o
}

// watchUsage prints the flywheel watch usage line.
func watchUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel watch [--once] [--last N] [--interval D] [--dir DIR]")
}

// runWatch implements `flywheel watch [--once] [--last N] [--interval D] [--dir DIR]`:
// prints one human line per event as it is appended — the same one-line summaries
// `flywheel explain` uses. Read-only: it never writes events or state.
func runWatch(args []string) {
	fs, o := watchFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel watch: %v\n", err)
		watchUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel watch: no positional arguments\n")
		watchUsage(os.Stderr)
		os.Exit(2)
	}

	if o.last < 0 {
		fmt.Fprintf(os.Stderr, "flywheel watch: --last must be >= 0\n")
		watchUsage(os.Stderr)
		os.Exit(2)
	}

	if o.interval <= 0 {
		fmt.Fprintf(os.Stderr, "flywheel watch: --interval must be > 0\n")
		watchUsage(os.Stderr)
		os.Exit(2)
	}

	events, off, err := flywheel.TailEvents(o.dir, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel watch: %v\n", err)
		os.Exit(1)
	}

	start := len(events) - o.last
	if start < 0 {
		start = 0
	}

	for i := start; i < len(events); i++ {
		fmt.Println(flywheel.HumanLine(events[i]))
	}

	if o.once {
		os.Exit(0)
	}

	for {
		time.Sleep(o.interval)

		newEvents, newOff, err := flywheel.TailEvents(o.dir, off)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel watch: %v\n", err)
			os.Exit(1)
		}

		for _, e := range newEvents {
			fmt.Println(flywheel.HumanLine(e))
		}

		off = newOff
	}
}
