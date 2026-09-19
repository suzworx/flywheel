package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("gate", "exit 6 while work is left unjudged: finished units not inspected, untriaged signals", runGate)
	registerHelp("gate", "flywheel gate [--json] [--dir DIR]", func() *flag.FlagSet { fs, _ := gateFlags(); return fs })
}

// gateOptions holds the parsed gate flags.
type gateOptions struct {
	dir  string
	json bool
}

// gateFlags defines gate's flags once, so help and run share them.
func gateFlags() (*flag.FlagSet, *gateOptions) {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &gateOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.json, "json", false, "print the blockers as JSON")
	return fs, o
}

// gateUsage prints the flywheel gate usage line.
func gateUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel gate [--json] [--dir DIR]")
}

// runGate implements `flywheel gate [--json] [--dir DIR]`: reads the event log
// and exits 6 if any work is left unjudged (finished tasks not inspected, or
// untriaged signals), else exits 0 when clear. Output is human-readable by
// default, JSON with --json. Read-only: it never writes events or state.
func runGate(args []string) {
	fs, o := gateFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel gate: %v\n", err)
		gateUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel gate: no positional arguments\n")
		gateUsage(os.Stderr)
		os.Exit(2)
	}

	events, terr := flywheel.ReadEvents(o.dir)
	if terr != nil {
		fmt.Fprintf(os.Stderr, "flywheel gate: %v\n", terr)
		os.Exit(1)
	}

	result := flywheel.Gate(events)

	if o.json {
		b, jerr := json.MarshalIndent(result, "", "  ")
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "flywheel gate: encode JSON: %v\n", jerr)
			os.Exit(1)
		}
		fmt.Printf("%s\n", b)
	} else {
		if result.OK() {
			fmt.Println("gate: clear")
		} else {
			fmt.Printf("gate: %d blocker(s)\n", len(result.Blockers))
			for _, b := range result.Blockers {
				fmt.Printf("  %s %s: %s\n", b.Task, b.Kind, b.Detail)
			}
			fmt.Println("  inspect: flywheel validate <task> && flywheel inspect <task> --verdict ... --session <s>; triage: flywheel feedback add --task <task> ... --signals <signal>")
		}
	}

	if result.OK() {
		os.Exit(0)
	}
	os.Exit(6)
}
