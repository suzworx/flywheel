package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("status", "print the factory's deterministic summary", runStatus)
	registerHelp("status", "flywheel status [--dir DIR] [--now RFC3339] [--json] [--health [--stale-after D]]", func() *flag.FlagSet { fs, _ := statusFlags(); return fs })
}

// statusOptions holds the parsed status flags.
type statusOptions struct {
	dir     string
	now     string
	jsonOut bool
	// health prints the latest health event instead of the summary; a record
	// older than staleAfter is an andon (exit 1).
	health     bool
	staleAfter time.Duration
}

// statusFlags defines status's flags once, so help and run share them.
func statusFlags() (*flag.FlagSet, *statusOptions) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &statusOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.now, "now", "", "RFC3339 instant to measure at; makes output reproducible")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	fs.BoolVar(&o.health, "health", false, "print the latest health event the controller recorded")
	fs.DurationVar(&o.staleAfter, "stale-after", 10*time.Minute, "with --health, a record older than this is stale (exit 1)")
	return fs, o
}

// statusUsage prints the flywheel status usage line.
func statusUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel status [--dir DIR] [--now RFC3339] [--json] [--health [--stale-after D]]")
}

// statusHealth implements status --health: it writes the latest health event
// as one line (or, with --json, the HealthReport; null when none) to w and
// returns the exit code: 1 when the record is stale or unreadable, else 0.
func statusHealth(o *statusOptions, now time.Time, w io.Writer) int {
	rep, err := flywheel.LatestHealthReport(o.dir, now, o.staleAfter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel status: %v\n", err)
		return 1
	}
	if o.jsonOut {
		b, _ := json.Marshal(rep)
		fmt.Fprintln(w, string(b))
	} else {
		fmt.Fprintln(w, flywheel.HealthLine(rep))
	}
	if rep != nil && rep.Stale {
		return 1
	}
	return 0
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
	if o.health {
		os.Exit(statusHealth(o, clock(), os.Stdout))
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
	// A read error here only loses the refused lines: Status above already
	// read the same log.
	if evs, err := flywheel.ReadEvents(o.dir); err == nil {
		printRefused(os.Stdout, flywheel.Derive(evs).Tasks)
	}
}

// printRefused writes one line per unit whose newest dispatch was refused
// before dispatched (issue #651), "  <task> <status> refused: <rule>", under a
// "Refused: N" header; nothing when no unit carries one.
func printRefused(w io.Writer, tasks []flywheel.TaskState) {
	var lines []string
	for _, ts := range tasks {
		if ts.Refused == "" {
			continue
		}
		rule, _, _ := strings.Cut(ts.Refused, ": ")
		lines = append(lines, fmt.Sprintf("  %s %s refused: %s", ts.ID, ts.Status, rule))
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "Refused: %d\n", len(lines))
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

// printStatus writes the text summary, one line per group.
func printStatus(rep flywheel.StatusReport) {
	if s := rep.Suspended; s != nil {
		until := ""
		if s.Until != "" {
			until = " until " + s.Until
		}
		fmt.Printf("SUSPENDED since %s by %s: %s%s\n", s.Since, s.By, s.Reason, until)
	}
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
