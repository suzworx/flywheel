package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("context", "print a compact pack of the factory's state for a joining agent", runContext)
	registerHelp("context", "flywheel context [--json] [--learnings N] [--role R] [--dir DIR]", func() *flag.FlagSet { fs, _ := contextFlags(); return fs })
}

// contextOptions holds the parsed context flags.
type contextOptions struct {
	dir       string
	json      bool
	learnings int
	role      string
}

// contextFlags defines context's flags once, so help and run share them.
func contextFlags() (*flag.FlagSet, *contextOptions) {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &contextOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.json, "json", false, "print the pack as JSON")
	fs.IntVar(&o.learnings, "learnings", 5, "how many recent undismissed learnings to include")
	fs.StringVar(&o.role, "role", "", "only this role's open work: lead, planner, foreman, inspector, steward or auditor")
	return fs, o
}

// contextUsage prints the flywheel context usage line.
func contextUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel context [--json] [--learnings N] [--role R] [--dir DIR]")
}

// runContext implements `flywheel context [--json] [--learnings N] [--dir DIR]`:
// reads the event log and config, builds a ContextPack, and prints it as
// JSON or Markdown. Read-only: it never writes events or state.
func runContext(args []string) {
	fs, o := contextFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel context: %v\n", err)
		contextUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel context: no positional arguments\n")
		contextUsage(os.Stderr)
		os.Exit(2)
	}
	if o.learnings < 0 {
		fmt.Fprintf(os.Stderr, "flywheel context: --learnings must be non-negative\n")
		contextUsage(os.Stderr)
		os.Exit(2)
	}

	// The role is a CLI argument: check it before touching any factory file,
	// so a bad role is always a usage error (#302 review).
	if o.role != "" && !flywheel.ValidRole(o.role) {
		fmt.Fprintf(os.Stderr, "flywheel context: unknown role %q: one of %s\n", o.role, strings.Join(flywheel.ContextRoles, ", "))
		contextUsage(os.Stderr)
		os.Exit(2)
	}

	events, terr := flywheel.ReadEvents(o.dir)
	if terr != nil {
		fmt.Fprintf(os.Stderr, "flywheel context: %v\n", terr)
		os.Exit(1)
	}

	cfg, _, cerr := flywheel.LoadConfig(o.dir)
	if cerr != nil {
		fmt.Fprintf(os.Stderr, "flywheel context: %v\n", cerr)
		os.Exit(1)
	}

	pack, rerr := flywheel.BuildRoleContext(events, cfg, o.learnings, o.role)
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "flywheel context: %v\n", rerr)
		contextUsage(os.Stderr)
		os.Exit(2)
	}

	if o.json {
		b, jerr := json.MarshalIndent(pack, "", "  ")
		if jerr != nil {
			fmt.Fprintf(os.Stderr, "flywheel context: encode JSON: %v\n", jerr)
			os.Exit(1)
		}
		if _, werr := fmt.Printf("%s\n", b); werr != nil {
			fmt.Fprintf(os.Stderr, "flywheel context: write JSON: %v\n", werr)
			os.Exit(1)
		}
	} else {
		if rerr := flywheel.RenderContext(os.Stdout, pack); rerr != nil {
			fmt.Fprintf(os.Stderr, "flywheel context: %v\n", rerr)
			os.Exit(1)
		}
	}

	os.Exit(0)
}
