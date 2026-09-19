package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("supervise", "validate every finished unit that has not been measured yet", runSupervise)
	registerHelp("supervise", "flywheel supervise [--once] [--interval D] [--json] [--dir DIR]", func() *flag.FlagSet { fs, _ := superviseFlags(); return fs })
}

// superviseOptions holds the parsed supervise flags.
type superviseOptions struct {
	dir      string
	once     bool
	interval time.Duration
	json     bool
}

// superviseFlags defines supervise's flags once, so help and run share them.
func superviseFlags() (*flag.FlagSet, *superviseOptions) {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &superviseOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.once, "once", false, "run one pass and exit")
	fs.DurationVar(&o.interval, "interval", 0, "repeat a pass every D until interrupted (e.g. 60s)")
	fs.BoolVar(&o.json, "json", false, "print each pass as JSON")
	return fs, o
}

// superviseUsage prints the flywheel supervise usage line.
func superviseUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel supervise [--once] [--interval D] [--json] [--dir DIR]")
}

// runSupervise implements `flywheel supervise [--once] [--interval D] [--json] [--dir DIR]`:
// finds every task whose worker finished and whose current attempt has not been
// measured since, and runs flywheel validate on it. --once runs one pass (exit 5
// if any unit's gauges fail); --interval D repeats forever. Never inspects or lands.
func runSupervise(args []string) {
	fs, o := superviseFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel supervise: %v\n", err)
		superviseUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel supervise: no positional arguments\n")
		superviseUsage(os.Stderr)
		os.Exit(2)
	}

	// Exactly one of --once or --interval D must be given
	// A zero or negative interval would spin without sleeping (#298 review).
	if (!o.once && o.interval <= 0) || (o.once && o.interval != 0) {
		fmt.Fprintf(os.Stderr, "flywheel supervise: exactly one of --once or --interval D (D > 0) must be given\n")
		superviseUsage(os.Stderr)
		os.Exit(2)
	}

	if o.once {
		// Run one pass
		result, err := flywheel.Supervise(o.dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel supervise: %v\n", err)
			os.Exit(1)
		}

		if o.json {
			b, jerr := json.MarshalIndent(result, "", "  ")
			if jerr != nil {
				fmt.Fprintf(os.Stderr, "flywheel supervise: encode JSON: %v\n", jerr)
				os.Exit(1)
			}
			fmt.Printf("%s\n", b)
		} else {
			printSuperviseResult(result)
		}

		// Exit 5 if any measured task failed or errored
		for _, st := range result.Measured {
			if st.Error != "" || !st.OK {
				os.Exit(5)
			}
		}
		os.Exit(0)
	} else {
		// Loop with --interval D until interrupted
		for {
			result, err := flywheel.Supervise(o.dir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "flywheel supervise: %v\n", err)
				os.Exit(1)
			}

			if o.json {
				b, jerr := json.MarshalIndent(result, "", "  ")
				if jerr != nil {
					fmt.Fprintf(os.Stderr, "flywheel supervise: encode JSON: %v\n", jerr)
					os.Exit(1)
				}
				fmt.Printf("%s\n", b)
			} else {
				printSuperviseResult(result)
			}

			time.Sleep(o.interval)
		}
	}
}

// printSuperviseResult prints a supervise result in human-readable format.
func printSuperviseResult(result flywheel.SuperviseResult) {
	if len(result.Measured) == 0 {
		fmt.Println("supervise: nothing to measure")
		return
	}

	for _, st := range result.Measured {
		if st.Error != "" {
			fmt.Printf("%s error: %s\n", st.Task, st.Error)
		} else if st.OK {
			fmt.Printf("%s %s pass\n", st.Task, st.Attempt)
		} else {
			fmt.Printf("%s %s fail\n", st.Task, st.Attempt)
		}
	}
}
