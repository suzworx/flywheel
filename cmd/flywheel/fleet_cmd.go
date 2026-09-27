package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// fleetUsageLine is fleet's usage line, shared by help and errors.
const fleetUsageLine = "flywheel fleet add <path> [--name N] | remove <name> | list [--json] | status [--json] [--all] [--idle-after D] | learnings [--pending|--all] [--json] [--sync=false] [--since D] [--done K|--done-all|--import-seen FILE] | watch [--once] [--interval D] [--notify CMD] [--report-all] [--registry FILE]"

func init() {
	register("fleet", "one merged view of every factory root and its ledgers\n    add <path>         register a root (--name, default its base)\n    remove <name>      unregister a root\n    list               the roots and the ledgers discovered under them\n    status             one row per ledger: units, andon, state, health, last event;\n                       idle worktree ledgers fold into one row per root (--all lists them)\n    learnings          sync every ledger's learnings into the pending queue, then list it;\n                       --all lists every learning, --done <key|title prefix> / --done-all drain;\n                       --since D bounds the first sync, --import-seen FILE marks triaged titles seen\n    watch              print what is new since the last watch: learnings, andons, state\n                       changes, stale health; --notify CMD runs per item, --once runs once;\n                       the first run only records a baseline, --report-all prints everything", runFleet)
	registerHelp("fleet", fleetUsageLine, func() *flag.FlagSet { fs, _ := fleetFlags("add"); return fs })
}

// fleetOptions holds the parsed fleet flags.
type fleetOptions struct {
	name       string
	json       bool
	all        bool
	idleAfter  time.Duration
	pending    bool
	sync       bool
	done       string
	doneAll    bool
	since      time.Duration
	importSeen string
	once       bool
	interval   time.Duration
	notify     string
	registry   string
	reportAll  bool
}

// DefaultWatchInterval is how often `fleet watch` without --once looks.
const DefaultWatchInterval = 5 * time.Minute

// fleetFlags defines a fleet subcommand's flags once, so help and run share
// them: add takes --name, list, status and learnings take --json, status
// also takes --all and --idle-after, learnings --all, --pending, --sync,
// --since, --done, --done-all and --import-seen, and watch --once,
// --interval, --notify and --registry.
func fleetFlags(sub string) (*flag.FlagSet, *fleetOptions) {
	fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &fleetOptions{}
	switch sub {
	case "add":
		fs.StringVar(&o.name, "name", "", "the root's name (default the path's base, made unique)")
	case "list", "status", "learnings":
		fs.BoolVar(&o.json, "json", false, "print JSON")
	}
	if sub == "learnings" {
		fs.BoolVar(&o.all, "all", false, "list every learning across the fleet, not the pending queue")
		fs.BoolVar(&o.pending, "pending", false, "list the pending queue (the default)")
		fs.BoolVar(&o.sync, "sync", true, "sync the queue with every ledger first")
		fs.StringVar(&o.done, "done", "", "remove the pending learnings whose key or title starts with this")
		fs.BoolVar(&o.doneAll, "done-all", false, "empty the pending queue")
		fs.DurationVar(&o.since, "since", flywheel.FirstSyncWindow, "on the first sync, only learnings newer than this become pending")
		fs.StringVar(&o.importSeen, "import-seen", "", "a JSON array of titles (or {title} objects) already triaged: mark them seen, not pending")
	}
	if sub == "watch" {
		fs.BoolVar(&o.once, "once", false, "look once and exit")
		fs.DurationVar(&o.interval, "interval", DefaultWatchInterval, "how often to look without --once")
		fs.StringVar(&o.notify, "notify", "", "run this command per item, with FLYWHEEL_FLEET_ITEM set to its line")
		fs.StringVar(&o.registry, "registry", "", "the fleet registry file (default the user's, or $FLYWHEEL_FLEET)")
		fs.BoolVar(&o.reportAll, "report-all", false, "print the whole current set once (every pending learning, open andon, non-running state, stale health), notifying nothing")
	}
	if sub == "status" {
		fs.BoolVar(&o.all, "all", false, "list every ledger; do not fold idle worktree ledgers")
		fs.DurationVar(&o.idleAfter, "idle-after", flywheel.DefaultIdleAfter, "fold a worktree ledger whose latest event is older than this")
	}
	return fs, o
}

// runFleet implements `flywheel fleet <add|remove|list|status|learnings>` against the
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
		return usage("missing subcommand (add, remove, list, status, learnings or watch)")
	}
	sub := args[0]
	want := map[string]int{"add": 1, "remove": 1, "list": 0, "status": 0, "learnings": 0, "watch": 0}
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
	if sub == "watch" {
		if o.interval < time.Second {
			return usage("watch: --interval must be at least 1s, got %v", o.interval)
		}
		if o.registry != "" {
			file = o.registry
		}
		for reportAll := o.reportAll; ; reportAll = false {
			if err := fleetWatchOnce(stdout, stderr, file, o.notify, reportAll, now, flywheel.ExecNotify); err != nil {
				return fail(err)
			}
			if o.once {
				return 0
			}
			time.Sleep(o.interval)
			now = time.Now()
		}
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
	case "learnings":
		return fleetLearnings(stdout, stderr, file, f, o, now, fail, usage)
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
// dispatched and running units; EVENTS is the events summarised, "+N" for a
// worktree row counting only its N events after the fork from its root
// (issue #608); HEALTH and LAST are ages, "-" when none; a row that failed to
// read shows its error as its STATE.
func writeFleetTable(w io.Writer, rows []flywheel.FleetRow) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tKIND\tEVENTS\tRUNNING\tPASSED\tFINISHED\tANDON\tSTATE\tHEALTH\tLAST")
	for _, r := range rows {
		if r.Kind == flywheel.FleetKindIdle {
			fmt.Fprintf(tw, "%s (oldest %s)\t%s\t\t\t\t\t\t\t\t\n", r.Name, fleetAge(r.LastAge), r.Kind)
			continue
		}
		state := r.State()
		if r.Error != "" {
			state = "error: " + r.Error
		}
		events := fmt.Sprint(r.Events)
		if r.Inherited > 0 {
			events = "+" + events
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n", r.Name, r.Kind, events, r.Tasks.Dispatched+r.Tasks.Running,
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

// fleetWatchOnce runs one `fleet watch` look against the registry at file:
// it compares the fleet with the state beside it (fleet-watch.json), prints
// one line per item, saves the new state, then, with notify set, runs notify
// per line through run; a notify failure only warns. With no state file yet
// it records the fleet as the baseline and prints only the WatchBaseline
// line, so history never floods; reportAll prints the whole current set
// instead (FleetWatchAll). Neither notifies.
func fleetWatchOnce(stdout, stderr io.Writer, file, notify string, reportAll bool, now time.Time, run flywheel.NotifyRunner) error {
	f, err := flywheel.LoadFleet(file)
	if err != nil {
		return err
	}
	stateFile := flywheel.WatchStatePath(file)
	prev, err := flywheel.LoadWatchState(stateFile)
	if err != nil {
		return err
	}
	watch := flywheel.FleetWatch
	if reportAll {
		watch = flywheel.FleetWatchAll
	}
	items, next := watch(f, prev, now)
	if err := flywheel.SaveWatchState(stateFile, next); err != nil {
		return err
	}
	if prev.First && !reportAll {
		q, err := flywheel.LoadLearningsQueue(next.Queue)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, flywheel.WatchBaseline(next, len(q.Pending)))
		return nil
	}
	lines := make([]string, len(items))
	for i, it := range items {
		lines[i] = flywheel.WatchLine(it, now)
		fmt.Fprintln(stdout, lines[i])
	}
	if notify != "" && !reportAll {
		for _, err := range flywheel.NotifyWatch(lines, notify, run) {
			fmt.Fprintf(stderr, "flywheel fleet watch: warning: %v\n", err)
		}
	}
	return nil
}

// fleetLearnings runs `fleet learnings` against the queue beside the registry
// at file: --done and --done-all drain pending and sync nothing; otherwise it
// syncs (unless --sync=false), noting on stderr what the sync added, then
// lists the pending queue, or with --all every learning, oldest first.
func fleetLearnings(stdout, stderr io.Writer, file string, f flywheel.Fleet, o *fleetOptions, now time.Time,
	fail func(error) int, usage func(string, ...any) int) int {
	queue := flywheel.LearningsQueuePath(file)
	switch {
	case o.done != "" && o.doneAll:
		return usage("learnings: --done and --done-all are exclusive")
	case o.all && o.pending:
		return usage("learnings: --all and --pending are exclusive")
	case o.since <= 0:
		return usage("learnings: --since must be positive, got %v", o.since)
	case o.importSeen != "" && (o.done != "" || o.doneAll):
		return usage("learnings: --import-seen and --done/--done-all are exclusive")
	case o.importSeen != "":
		data, err := os.ReadFile(o.importSeen)
		if err != nil {
			return fail(err)
		}
		n, err := flywheel.ImportSeenLearnings(queue, f, now, o.since, data)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "imported: %d learnings matched, marked seen and not pending\n", n)
		return 0
	case o.doneAll:
		n, err := flywheel.DoneAllLearnings(queue)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "done %d\n", n)
		return 0
	case o.done != "":
		n, err := flywheel.DoneLearning(queue, o.done)
		if err != nil {
			return fail(err)
		}
		if n == 0 {
			return fail(fmt.Errorf("no pending learning's key or title starts with %q", o.done))
		}
		fmt.Fprintf(stdout, "done %d\n", n)
		return 0
	}
	if o.sync {
		newly, first, err := flywheel.SyncLearningsSince(queue, f, now, o.since)
		if err != nil {
			return fail(err)
		}
		if first {
			fmt.Fprintf(stderr, "first sync: every learning marked seen; only the %d newer than %s are pending\n", len(newly), o.since)
		} else if len(newly) > 0 {
			fmt.Fprintf(stderr, "synced: %d new pending\n", len(newly))
		}
	}
	var ls []flywheel.FleetLearning
	if o.all {
		ls = flywheel.FleetLearnings(f, now)
	} else {
		q, err := flywheel.LoadLearningsQueue(queue)
		if err != nil {
			return fail(err)
		}
		ls = slices.SortedFunc(maps.Values(q.Pending), func(a, b flywheel.FleetLearning) int {
			return cmp.Or(strings.Compare(a.TS, b.TS), strings.Compare(a.Key, b.Key))
		})
	}
	if ls == nil {
		ls = []flywheel.FleetLearning{}
	}
	if o.json {
		return fleetJSON(stdout, stderr, ls)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tSEVERITY\tAGE\tROOT\tTASK\tTITLE")
	for _, l := range ls {
		var age *int
		if t, err := time.Parse(time.RFC3339Nano, l.TS); err == nil {
			sec := int(now.Sub(t).Seconds())
			age = &sec
		}
		title := l.Title
		if l.Dismissed {
			title += " (dismissed)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", l.Key[:12], l.Severity, fleetAge(age), l.Root, l.Task, title)
	}
	tw.Flush()
	return 0
}
