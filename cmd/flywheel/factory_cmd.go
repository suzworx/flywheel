package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
	"github.com/suzworx/flywheel/internal/term"
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
	plain    bool
	interval time.Duration
	width    int
	height   int
	now      string
	keys     string
	frames   bool
	ctx      string
}

// factoryFlags defines factory's flags once, so help and run share them.
func factoryFlags() (*flag.FlagSet, *factoryOptions) {
	fs := flag.NewFlagSet("factory", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &factoryOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.once, "once", false, "render the floor once and exit")
	fs.BoolVar(&o.asJSON, "json", false, "print one JSON snapshot and exit")
	fs.BoolVar(&o.plain, "plain", false, "redraw the plain floor instead of the interactive view")
	fs.DurationVar(&o.interval, "interval", 2*time.Second, "redraw interval in live mode")
	fs.IntVar(&o.width, "width", 100, "render width in columns")
	fs.IntVar(&o.height, "height", 30, "render height in rows, for --frames")
	fs.StringVar(&o.keys, "keys", "", `keys to press, for --frames: space-separated j, :, <enter>, <esc>, <ctrl-a>, "typed run"`)
	fs.BoolVar(&o.frames, "frames", false, "render the interactive view headlessly after each --keys token and print the frames as JSON")
	fs.StringVar(&o.now, "now", "", "RFC3339 instant to render at; makes a screenshot reproducible")
	fs.StringVar(&o.ctx, "ctx", "", "start in this fleet ledger, named as flywheel fleet status names it (overrides --dir)")
	return fs, o
}

// factoryCtx is the fleet ledger named name in the registry at file, as
// flywheel fleet status names it; ok false when none is, with every name
// the registry has.
func factoryCtx(file, name string) (l flywheel.FleetLedger, names []string, ok bool, err error) {
	f, err := flywheel.LoadFleet(file)
	if err != nil {
		return l, nil, false, err
	}
	for _, c := range flywheel.FleetLedgers(f) {
		if c.Name == name {
			return c, nil, true, nil
		}
		names = append(names, c.Name)
	}
	return l, names, false, nil
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
	if o.frames {
		if o.width <= 0 || o.height <= 0 {
			fmt.Fprintf(os.Stderr, "flywheel factory: --width and --height must be positive, got %dx%d\n", o.width, o.height)
			usage(os.Stderr)
			os.Exit(2)
		}
		if _, err := flywheel.ParseKeySeq(o.keys); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel factory: --keys: %v\n", err)
			usage(os.Stderr)
			os.Exit(2)
		}
	} else if o.keys != "" {
		fmt.Fprintln(os.Stderr, "flywheel factory: --keys needs --frames")
		usage(os.Stderr)
		os.Exit(2)
	}
	// --ctx (issue #585 f4): the view starts in that fleet ledger.
	var place flywheel.TUIFetchOptions
	if o.ctx != "" {
		file, err := flywheel.FleetPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
			os.Exit(1)
		}
		l, names, ok, err := factoryCtx(file, o.ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
			os.Exit(1)
		}
		if !ok {
			known := "none registered: flywheel fleet add <path> registers a root"
			if len(names) > 0 {
				known = strings.Join(names, ", ")
			}
			fmt.Fprintf(os.Stderr, "flywheel factory: --ctx %q is no fleet ledger; known: %s\n", o.ctx, known)
			usage(os.Stderr)
			os.Exit(2)
		}
		o.dir, place = l.Path, flywheel.TUIFetchOptions{FleetFile: file, Name: l.Name, Kind: l.Kind}
	}
	if o.frames {
		if o.now != "" {
			// A fixed --now also fixes the zone the frames show times in, so
			// the same ledger renders the same frames on any host.
			time.Local = clock().Location()
		}
		frames, err := flywheel.TUIFrames(o.dir, o.keys, o.width, o.height, clock())
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
			os.Exit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(frames); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
			os.Exit(1)
		}
		return
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
	if o.plain || !term.IsTerminal(os.Stdin) {
		// --plain, or no keyboard to drive the interactive view: the plain
		// redraw loop.
		pulse(w, o.dir, o.width, o.interval, color, clock)
		return
	}
	// Live interactive mode.
	if err := flywheel.RunTUI(os.Stdin, os.Stdout, o.dir, o.interval, clock, place); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel factory: %v\n", err)
		os.Exit(1)
	}
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
