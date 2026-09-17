package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("factory", "live dashboard of the factory floor", runFactory)
	registerHelp("factory", "flywheel factory [flags]", func() *flag.FlagSet { fs, _ := factoryFlags(); return fs })
}

// factoryOptions holds the parsed factory flags.
type factoryOptions struct {
	dir      string
	once     bool
	asJSON   bool
	interval time.Duration
	width    int
	now      string
}

// factoryFlags defines factory's flags once, so help and run share them.
func factoryFlags() (*flag.FlagSet, *factoryOptions) {
	fs := flag.NewFlagSet("factory", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &factoryOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.once, "once", false, "render the floor once and exit")
	fs.BoolVar(&o.asJSON, "json", false, "print one JSON snapshot and exit")
	fs.DurationVar(&o.interval, "interval", 2*time.Second, "redraw interval in live mode")
	fs.IntVar(&o.width, "width", 100, "render width in columns")
	fs.StringVar(&o.now, "now", "", "RFC3339 instant to render at; makes a screenshot reproducible")
	return fs, o
}

func runFactory(args []string) {
	fs, o := factoryFlags()
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
		usage(os.Stderr)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "flywheel factory: unexpected argument %q\n", fs.Arg(0))
		usage(os.Stderr)
		os.Exit(2)
	}
	if o.interval <= 0 {
		fmt.Fprintf(os.Stderr, "flywheel factory: --interval must be positive, got %v\n", o.interval)
		usage(os.Stderr)
		os.Exit(2)
	}
	clock, err := clockFor(o.now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel factory: --now %q is not RFC3339: %v\n", o.now, err)
		usage(os.Stderr)
		os.Exit(2)
	}
	w := flywheel.NewWatcher()
	color := flywheel.EnableANSI()
	if o.asJSON || o.once {
		render(w, o.dir, o.width, o.asJSON, color, clock)
		return
	}
	if !color {
		// stdout is not an ANSI terminal (a pipe or a redirected file), so live
		// mode would never be seen and would only hang an automated caller.
		// Render once, plain text, and exit 0 exactly like --once.
		render(w, o.dir, o.width, false, color, clock)
		return
	}
	pulse(w, o.dir, o.width, o.interval, color, clock)
}

// clockFor turns the --now flag into a clock. "" returns the real clock, read
// on every call; an RFC3339 value returns a clock fixed to that instant; any
// other value returns an error.
func clockFor(nowFlag string) (func() time.Time, error) {
	if nowFlag == "" {
		return time.Now, nil
	}
	t, err := time.Parse(time.RFC3339, nowFlag)
	if err != nil {
		return nil, err
	}
	return func() time.Time { return t }, nil
}

// render draws a single snapshot and exits; json selects the JSON form.
func render(w flywheel.Watcher, dir string, width int, asJSON bool, color bool, now func() time.Time) {
	f, err := w.Refresh(dir, now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
		os.Exit(1)
	}
	if asJSON {
		flywheel.RenderJSON(os.Stdout, f)
		return
	}
	flywheel.RenderText(os.Stdout, f, width, color)
}

// pulse runs the live dashboard: clear and redraw every tick until Ctrl-C.
func pulse(w flywheel.Watcher, dir string, width int, interval time.Duration, color bool, now func() time.Time) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		fmt.Print("\x1b[H\x1b[2J")
		f, err := w.Refresh(dir, now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
			os.Exit(1)
		}
		flywheel.RenderText(os.Stdout, f, width, color)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
