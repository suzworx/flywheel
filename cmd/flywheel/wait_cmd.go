package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("wait", "block until the named tasks finish, printing each finish as it lands", runWait)
	registerHelp("wait", "flywheel wait <task>... [--timeout D] [--interval D] [--dir DIR]", func() *flag.FlagSet { fs, _ := waitFlags(); return fs })
}

// waitOptions holds the parsed wait flags.
type waitOptions struct {
	dir      string
	timeout  time.Duration
	interval time.Duration
}

// waitFlags defines wait's flags once, so help and run share them.
func waitFlags() (*flag.FlagSet, *waitOptions) {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &waitOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.DurationVar(&o.timeout, "timeout", 0, "give up after this long (0 = wait forever)")
	fs.DurationVar(&o.interval, "interval", 2*time.Second, "how often to re-read the event log")
	return fs, o
}

// waitUsage prints the flywheel wait usage line.
func waitUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel wait <task>... [--timeout D] [--interval D] [--dir DIR]")
}

// runWait implements `flywheel wait <task>...` (issue #393).
func runWait(args []string) {
	os.Exit(waitMain(args, os.Stdout, os.Stderr))
}

// waitMain blocks until every named task finishes its current (or first)
// attempt and returns the exit code: 0 every finish clean (reason stop), 4
// any unclean, 8 timeout, 2 usage, 1 any other error. Read-only.
func waitMain(args []string, stdout, stderr io.Writer) int {
	fs, o := waitFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "flywheel wait: %v\n", err)
		waitUsage(stderr)
		return 2
	}
	if len(pos) == 0 {
		fmt.Fprintf(stderr, "flywheel wait: at least one task id is required\n")
		waitUsage(stderr)
		return 2
	}
	if o.timeout < 0 || o.interval <= 0 {
		fmt.Fprintf(stderr, "flywheel wait: --timeout must be >= 0 and --interval > 0\n")
		waitUsage(stderr)
		return 2
	}
	clean, err := flywheel.WaitFor(o.dir, pos, o.timeout, o.interval, time.Now, time.Sleep, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "flywheel wait: %v\n", err)
		var wt *flywheel.WaitTimeout
		if errors.As(err, &wt) {
			return 8
		}
		return 1
	}
	if !clean {
		return 4
	}
	return 0
}
