package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// fleetUsageLine is fleet's usage line, shared by help and errors.
const fleetUsageLine = "flywheel fleet add <path> [--name N] | remove <name> | list [--json] | status [--json] [--all] [--idle-after D]"

func init() {
	register("fleet", "one merged view of every factory root and its ledgers\n    add <path>         register a root (--name, default its base)\n    remove <name>      unregister a root\n    list               the roots and the ledgers discovered under them\n    status             one row per ledger: units, andon, state, health, last event;\n                       idle worktree ledgers fold into one row per root (--all lists them)", runFleet)
	registerHelp("fleet", fleetUsageLine, func() *flag.FlagSet { fs, _ := fleetFlags("add"); return fs })
}

// fleetOptions holds the parsed fleet flags.
type fleetOptions struct {
	name      string
	json      bool
	all       bool
	idleAfter time.Duration
}

// fleetFlags defines a fleet subcommand's flags once, so help and run share
// them: add takes --name, list and status take --json, status also takes
// --all and --idle-after.
func fleetFlags(sub string) (*flag.FlagSet, *fleetOptions) {
	fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &fleetOptions{}
	switch sub {
	case "add":
		fs.StringVar(&o.name, "name", "", "the root's name (default the path's base, made unique)")
	case "list", "status":
		fs.BoolVar(&o.json, "json", false, "print JSON")
	}
	if sub == "status" {
		fs.BoolVar(&o.all, "all", false, "list every ledger; do not fold idle worktree ledgers")
		fs.DurationVar(&o.idleAfter, "idle-after", flywheel.DefaultIdleAfter, "fold a worktree ledger whose latest event is older than this")
	}
	return fs, o
}

// runFleet implements `flywheel fleet <add|remove|list|status>` against the
// registry at FleetPath.
func runFleet(args []string) {
	file, err := flywheel.FleetPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel fleet: %v\n", err)
		os.Exit(1)
	}
	os.Exit(fleetMain(args, os.Stdout, os.Stderr, file, time.Now()))
}

// fleetMain runs fleet with the registry at file; exit 0, 1 on error, 2 on
// usage.
func fleetMain(args []string, stdout, stderr io.Writer, file string, now time.Time) int {
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "flywheel fleet: "+format+"\n", a...)
		fmt.Fprintln(stderr, "usage: "+fleetUsageLine)
		return 2
	}
	if len(args) == 0 {
		return usage("missing subcommand (add, remove, list or status)")
	}
	sub := args[0]
	want := map[string]int{"add": 1, "remove": 1, "list": 0, "status": 0}
	n, ok := want[sub]
	if !ok {
		return usage("unknown subcommand %q", sub)
	}
	fs, o := fleetFlags(sub)
	pos, err := parseFlags(fs, args[1:])
	if err != nil {
		return usage("%s: %v", sub, err)
	}
	if len(pos) != n {
		return usage("%s: want %d argument(s), got %d", sub, n, len(pos))
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "flywheel fleet %s: %v\n", sub, err)
		return 1
	}
	f, err := flywheel.LoadFleet(file)
	if err != nil {
		return fail(err)
	}
	switch sub {
	case "add", "remove":
		msg := ""
		if sub == "add" {
			r, err := f.AddRoot(pos[0], o.name)
			if err != nil {
				return fail(err)
			}
			msg = fmt.Sprintf("added %s %s", r.Name, r.Path)
		} else if err := f.RemoveRoot(pos[0]); err != nil {
			return fail(err)
		} else {
			msg = "removed " + pos[0]
		}
		if err := flywheel.SaveFleet(file, f); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, msg)
	case "list":
		ledgers := flywheel.FleetLedgers(f)
		if o.json {
			return fleetJSON(stdout, stderr, map[string]any{"roots": f.Roots, "ledgers": ledgers})
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		for _, r := range f.Roots {
			fmt.Fprintf(tw, "%s\t%s\n", r.Name, r.Path)
			for _, l := range ledgers {
				if l.Root != r.Name {
					continue
				}
				if l.Error != "" {
					fmt.Fprintf(tw, "  %s\t%s\n", "error", l.Error)
					continue
				}
				fmt.Fprintf(tw, "  %s\t%s\t%s\n", l.Kind, l.Name, l.Path)
			}
		}
		tw.Flush()
	case "status":
		if o.idleAfter < 0 {
			return usage("status: --idle-after must not be negative")
		}
		rows := flywheel.FleetStatus(f, now)
		if !o.all {
			rows = flywheel.FoldIdle(rows, o.idleAfter)
		}
		if o.json {
			return fleetJSON(stdout, stderr, rows)
		}
		writeFleetTable(stdout, rows)
	}
	return 0
}

// fleetJSON prints v as indented JSON; exit 0, or 1 when it fails.
func fleetJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "flywheel fleet: %v\n", err)
		return 1
	}
	return 0
}

// writeFleetTable prints rows as an aligned table. RUNNING counts the
// dispatched and running units; HEALTH and LAST are ages, "-" when none; a
// row that failed to read shows its error as its STATE.
func writeFleetTable(w io.Writer, rows []flywheel.FleetRow) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tRUNNING\tPASSED\tFINISHED\tANDON\tSTATE\tHEALTH\tLAST")
	for _, r := range rows {
		if r.Kind == flywheel.FleetKindIdle {
			fmt.Fprintf(tw, "%s (oldest %s)\t%s\t\t\t\t\t\t\t\n", r.Name, fleetAge(r.LastAge), r.Kind)
			continue
		}
		state := r.State()
		if r.Error != "" {
			state = "error: " + r.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n", r.Name, r.Kind, r.Tasks.Dispatched+r.Tasks.Running,
			r.Tasks.Passed, r.Tasks.Finished, r.Andon, state, fleetAge(r.HealthAge), fleetAge(r.LastAge))
	}
	tw.Flush()
}

// fleetAge renders an age in seconds compactly (45s, 12m, 3h, 2d); "-" for nil.
func fleetAge(sec *int) string {
	if sec == nil {
		return "-"
	}
	d := time.Duration(*sec) * time.Second
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", *sec)
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
