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
	register("status", "print the factory's deterministic summary", runStatus)
	registerHelp("status", "flywheel status [--dir DIR] [--now RFC3339] [--json]", func() *flag.FlagSet { fs, _ := statusFlags(); return fs })
}

// statusOptions holds the parsed status flags.
type statusOptions struct {
	dir     string
	now     string
	jsonOut bool
}

// statusFlags defines status's flags once, so help and run share them.
func statusFlags() (*flag.FlagSet, *statusOptions) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &statusOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.now, "now", "", "RFC3339 instant to measure at; makes output reproducible")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	return fs, o
}

// statusUsage prints the flywheel status usage line.
func statusUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel status [--dir DIR] [--now RFC3339] [--json]")
}

// runStatus implements `flywheel status`: a read-only summary of the factory.
// Exit 0, 1 on error, 2 on usage. --json prints the StatusReport instead of
// the text lines.
func runStatus(args []string) {
	fs, o := statusFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel status: %v\n", perr)
		statusUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel status: unexpected argument %q\n", pos[0])
		statusUsage(os.Stderr)
		os.Exit(2)
	}
	clock, err := clockFor(o.now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel status: --now %q is not RFC3339: %v\n", o.now, err)
		statusUsage(os.Stderr)
		os.Exit(2)
	}
	rep, err := flywheel.Status(o.dir, clock())
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel status: %v\n", err)
		os.Exit(1)
	}
	if o.jsonOut {
		b, _ := json.Marshal(rep)
		fmt.Println(string(b))
		return
	}
	printStatus(rep)
}

// printStatus writes the text summary, one line per group.
func printStatus(rep flywheel.StatusReport) {
	fmt.Printf("Factory: %s\n", rep.Factory)
	if len(rep.Goals.List) == 0 {
		fmt.Println("Goals: none")
	} else {
		fmt.Printf("Goals: active %d  met %d  failed %d  abandoned %d\n",
			rep.Goals.Active, rep.Goals.Met, rep.Goals.Failed, rep.Goals.Abandoned)
		for _, g := range rep.Goals.List {
			if g.Status != "active" {
				continue
			}
			fmt.Printf("  %s  %s  %s\n", g.ID, g.Progress, g.Title)
		}
	}
	fmt.Printf("Tasks: total %d", rep.Tasks.Total)
	for _, c := range []struct {
		name  string
		count int
	}{
		{"planned", rep.Tasks.Planned},
		{"dispatched", rep.Tasks.Dispatched},
		{"running", rep.Tasks.Running},
		{"finished", rep.Tasks.Finished},
		{"passed", rep.Tasks.Passed},
		{"needs-correction", rep.Tasks.NeedsCorrection},
		{"rejected", rep.Tasks.Rejected},
		{"blocked", rep.Tasks.Blocked},
		{"landed", rep.Tasks.Landed},
	} {
		if c.count > 0 {
			fmt.Printf(", %s %d", c.name, c.count)
		}
	}
	fmt.Println()
	fmt.Printf("Leases: live %d  expired %d\n", rep.Leases.Live, rep.Leases.Expired)
	fmt.Printf("Attempts: live %d  lost %d  stale %d\n", rep.Attempts.Live, rep.Attempts.Lost, rep.Attempts.Stale)
	for _, l := range rep.Attempts.LostList {
		fmt.Printf("  lost %s %s: lease expired at %s\n", l.Task, l.Attempt, l.ExpiresAt)
	}
	if rep.Leases.Skipped > 0 {
		fmt.Printf("  skipped %d malformed lease file(s)\n", rep.Leases.Skipped)
	}
	fmt.Printf("Last event: %s\n", describeLast(rep.LastEventAt))
	fmt.Printf("Last meaningful progress: %s\n", describeLast(rep.LastProgressAt))
	fmt.Printf("Andon: %d\n", rep.Andon)
	fmt.Printf("Attention: %d\n", len(rep.Attention))
	for _, a := range rep.Attention {
		fmt.Printf("  %s %s %s\n", a.Task, a.Attempt, a.Reason)
	}
}

// describeLast renders a LastEvent as "ts (Ns ago)", or "(none)" when nil.
func describeLast(l *flywheel.LastEvent) string {
	if l == nil {
		return "(none)"
	}
	return fmt.Sprintf("%s (%s ago)", l.TS, flywheel.HumanAge(l.Age))
}
