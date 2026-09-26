package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("controller", "run the controller loop: lock, reconcile, mark lost and blocked, resume rate-limited units", runController)
	registerHelp("controller", "flywheel controller [--once] [--interval D] [--dir DIR] [--now RFC3339]", func() *flag.FlagSet { fs, _ := controllerFlags(); return fs })
}

// controllerOptions holds the parsed controller flags.
type controllerOptions struct {
	dir      string
	once     bool
	interval time.Duration
	now      string
}

// controllerFlags defines controller's flags once, so help and run share
// them. The interval defaults to 0 so the config's controller.interval (10s)
// applies unless the flag is given.
func controllerFlags() (*flag.FlagSet, *controllerOptions) {
	fs := flag.NewFlagSet("controller", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &controllerOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.once, "once", false, "run one tick and release the lock")
	fs.DurationVar(&o.interval, "interval", 0, "tick interval in live mode; default from config")
	fs.StringVar(&o.now, "now", "", "RFC3339 instant to tick at; makes a run reproducible")
	return fs, o
}

// controllerUsage prints the flywheel controller usage line.
func controllerUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel controller [--once] [--interval D] [--dir DIR] [--now RFC3339]")
}

// runController implements `flywheel controller`: it acquires the controller
// lock, then ticks — once with --once, or every interval until Ctrl-C. Each
// tick renews the lock. Exit 0, 1 on error, 2 on usage, 6 when the lock is
// held elsewhere.
func runController(args []string) {
	fs, o := controllerFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel controller: %v\n", perr)
		controllerUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel controller: unexpected argument %q\n", pos[0])
		controllerUsage(os.Stderr)
		os.Exit(2)
	}
	clock, err := clockFor(o.now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel controller: --now %q is not RFC3339: %v\n", o.now, err)
		controllerUsage(os.Stderr)
		os.Exit(2)
	}
	cfg, _, cerr := flywheel.LoadConfig(o.dir)
	if cerr != nil {
		fmt.Fprintf(os.Stderr, "flywheel controller: %v\n", cerr)
		os.Exit(1)
	}
	interval, ttl, _ := flywheel.ControllerTimings(cfg)
	if o.interval > 0 {
		interval = o.interval
	}
	if interval <= 0 {
		fmt.Fprintf(os.Stderr, "flywheel controller: --interval must be positive, got %v\n", o.interval)
		controllerUsage(os.Stderr)
		os.Exit(2)
	}
	lock, lerr := flywheel.AcquireLock(o.dir, clock(), ttl)
	if lerr != nil {
		fmt.Fprintf(os.Stderr, "flywheel controller: %v\n", lerr)
		if flywheel.IsRuleRefusal(lerr) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	if o.once {
		res, terr := flywheel.TickWith(o.dir, clock(), controllerTickOptions(o.dir, false))
		relErr := flywheel.ReleaseLock(o.dir, lock)
		if terr != nil {
			fmt.Fprintf(os.Stderr, "flywheel controller: %v\n", terr)
			if flywheel.IsRuleRefusal(terr) {
				os.Exit(6)
			}
			os.Exit(1)
		}
		if relErr != nil {
			fmt.Fprintf(os.Stderr, "flywheel controller: %v\n", relErr)
			os.Exit(1)
		}
		printTick(res)
		return
	}
	code := controllerLoop(o.dir, clock, interval)
	if relErr := flywheel.ReleaseLock(o.dir, lock); relErr != nil {
		fmt.Fprintf(os.Stderr, "flywheel controller: %v\n", relErr)
	}
	os.Exit(code)
}

// controllerLoop ticks every interval until Ctrl-C; each tick renews the
// lock. It returns the exit code: 0 on Ctrl-C, 6 on a rule refusal, 1 on
// error.
func controllerLoop(dir string, clock func() time.Time, interval time.Duration) int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	t := time.NewTicker(interval)
	defer t.Stop()
	opts := controllerTickOptions(dir, true)
	for {
		res, terr := flywheel.TickWith(dir, clock(), opts)
		if terr != nil {
			fmt.Fprintf(os.Stderr, "flywheel controller: %v\n", terr)
			if flywheel.IsRuleRefusal(terr) {
				return 6
			}
			return 1
		}
		printTick(res)
		select {
		case <-ctx.Done():
			return 0
		case <-t.C:
		}
	}
}

// controllerTickOptions are the tick options the controller runs with: it
// resumes rate-limited units through the same starter supervise
// --resume-limited uses, recording session $FLYWHEEL_SESSION, else
// controller. With reap (live mode) each child is waited for; with --once it
// outlives the tick.
func controllerTickOptions(dir string, reap bool) flywheel.TickOptions {
	session := os.Getenv("FLYWHEEL_SESSION")
	if session == "" {
		session = "controller"
	}
	return flywheel.TickOptions{Start: superviseStarter(dir, session, reap), Session: session}
}

// printTick writes the one-line tick summary, then one line per unit the
// auto-resume pass acted on; notify failures and a skipped pass go to stderr.
func printTick(res flywheel.TickResult) {
	fmt.Printf("tick %s: %d actions (%d lost, %d blocked, %d proposed)\n",
		res.TS, res.Actions, res.Lost, res.Blocked, res.Proposed)
	for _, r := range res.Resumed {
		if r.Started {
			fmt.Printf("resumed %s %s (log %s)\n", r.Task, r.Attempt, filepath.ToSlash(autoResumeLog(r.Task)))
		} else {
			fmt.Printf("not resumed %s: %s\n", r.Task, r.Reason)
		}
	}
	if res.ResumeSkipped != "" {
		fmt.Fprintf(os.Stderr, "flywheel controller: auto-resume skipped this tick: %s\n", res.ResumeSkipped)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "flywheel controller: warning: %s\n", w)
	}
}
