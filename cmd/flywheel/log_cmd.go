package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("log", "append an event to the flywheel event log", runLog)
	registerHelp("log", "flywheel log [flags]", func() *flag.FlagSet { fs, _ := logFlags(); return fs })
}

// logOptions holds the parsed log flags.
type logOptions struct {
	dir     string
	jsonIn  string
	task    string
	kind    string
	session string
	model   string
	attempt string
	rc      string
	reason  string
	verdict string
	brief   string
	commit  string
	note    string
	goal    string
	noState bool
	shard   bool
}

// logFlags defines log's flags once, so help and run share them.
func logFlags() (*flag.FlagSet, *logOptions) {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &logOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.jsonIn, "json", "", "file of events to append, or - for stdin")
	fs.StringVar(&o.task, "task", "", "task id")
	fs.StringVar(&o.kind, "kind", "", "event kind")
	fs.StringVar(&o.session, "session", "", "session id")
	fs.StringVar(&o.model, "model", "", "model name")
	fs.StringVar(&o.attempt, "attempt", "", "attempt (r1, c1, ...)")
	fs.StringVar(&o.rc, "rc", "", "exit code")
	fs.StringVar(&o.reason, "reason", "", "finish reason or classification")
	fs.StringVar(&o.verdict, "verdict", "", "inspected verdict (pass, rework, scrap, or escalate) or reviewed verdict (pass, correct, or reject)")
	fs.StringVar(&o.brief, "brief", "", "brief file")
	fs.StringVar(&o.commit, "commit", "", "commit id")
	fs.StringVar(&o.note, "note", "", "free-form note")
	fs.StringVar(&o.goal, "goal", "", "goal id a planned event links to")
	fs.BoolVar(&o.noState, "no-state", false, "skip state derivation after appending")
	fs.BoolVar(&o.shard, "shard", false, "switch this repository's event log to per-task shards under .flywheel/events/ (one-way)")
	return fs, o
}

// logShardConflicts checks that no incompatible flags were passed with --shard.
func logShardConflicts(o *logOptions) error {
	conflicting := []struct {
		field string
		val   string
	}{
		{"--task", o.task},
		{"--kind", o.kind},
		{"--brief", o.brief},
		{"--json", o.jsonIn},
		{"--commit", o.commit},
		{"--note", o.note},
		{"--session", o.session},
		{"--model", o.model},
		{"--rc", o.rc},
		{"--reason", o.reason},
		{"--goal", o.goal},
		{"--verdict", o.verdict},
		{"--attempt", o.attempt},
	}
	for _, c := range conflicting {
		if c.val != "" {
			return fmt.Errorf("%s cannot be used with --shard", c.field)
		}
	}
	if o.noState {
		return fmt.Errorf("--no-state cannot be used with --shard")
	}
	return nil
}

// runLogShard switches dir to the sharded log layout and marks it in the
// config: EnableShards, then LoadConfig/WriteConfig with Log.Shards = true
// (creating the Log section when missing). It prints
// "sealed <dir>/.flywheel/events.jsonl at N lines; new events go to .flywheel/events/"
// (or "<dir> already uses the sharded log" when nothing was sealed), and
// warns on stderr when .github/workflows/flywheel-audit.yml pins a flywheel
// version (a line containing "version:" under the flywheel action/step, or
// any "flywheel@v" reference): an older binary in CI verifies only the
// legacy file.
func runLogShard(dir string, stdout, stderr io.Writer) error {
	sealed, legacyLines, err := flywheel.EnableShards(dir, time.Now())
	if err != nil {
		return err
	}
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		return err
	}
	if cfg.Log == nil {
		cfg.Log = &flywheel.LogConfig{}
	}
	cfg.Log.Shards = true
	if err := flywheel.WriteConfig(dir, cfg); err != nil {
		return err
	}
	if sealed {
		fmt.Fprintf(stdout, "sealed %s/.flywheel/events.jsonl at %d lines; new events go to .flywheel/events/\n", dir, legacyLines)
	} else {
		fmt.Fprintf(stdout, "%s already uses the sharded log\n", dir)
	}
	warnPinnedAuditWorkflow(dir, stderr)
	return nil
}

// pinnedFlywheel matches an audit workflow that installs a PINNED flywheel
// release (…/cmd/flywheel@v0.17.0), as opposed to @latest. The workflow always
// carries a go-version line, so the word "version" alone means nothing.
var pinnedFlywheel = regexp.MustCompile(`flywheel@v?[0-9]`)

// warnPinnedAuditWorkflow warns on stderr when
// .github/workflows/flywheel-audit.yml installs a pinned flywheel release:
// that binary may be older than the shards and would verify only the legacy
// log, reporting a pass over records it cannot see.
func warnPinnedAuditWorkflow(dir string, stderr io.Writer) {
	auditPath := filepath.Join(dir, ".github", "workflows", "flywheel-audit.yml")
	b, err := os.ReadFile(auditPath)
	if err != nil {
		return // no audit workflow, or unreadable: nothing to warn about
	}
	if m := pinnedFlywheel.FindString(string(b)); m != "" {
		fmt.Fprintf(stderr, "warning: %s pins a flywheel release (%s...); a binary older than the sharded log verifies only .flywheel/events.jsonl — bump the pin\n", auditPath, m)
	}
}

// appendEvents appends each event and then derives state unless noState. A
// batch carrying a learning or dismissed event participates in the feedback
// transaction: flywheel.AppendLearningEvents holds feedback.lock across the
// whole batch — append every event, rebuild learnings.md, release — so the
// generic JSON log path serialises against the feedback commands exactly as
// they serialise against each other (issue #260). A batch with no learning or
// dismissed event takes no feedback lock at all: logging a planned event must
// never wait on a feedback command. An amended event never bypasses the
// inert-amendment refusal (issue #272): it is routed through
// flywheel.AppendAmendedEvent, which holds the dispatch lock and runs the
// same check as --kind amended, whatever else the batch carries. In a
// feedback batch the amended events land first, before the feedback
// transaction appends the rest; the transaction re-reads the log for its
// artifact rebuild, so a landed amendment is included exactly as a separate
// append would be.
func appendEvents(dir string, events []flywheel.Event, noState bool) {
	var err error
	if batchHasLearning(events) {
		var rest []flywheel.Event
		for _, e := range events {
			if e.Kind == "amended" {
				if err = flywheel.AppendAmendedEvent(dir, e); err != nil {
					break
				}
			} else {
				rest = append(rest, e)
			}
		}
		if err == nil {
			err = flywheel.AppendLearningEvents(dir, rest)
		}
	} else {
		for _, e := range events {
			if e.Kind == "amended" {
				err = flywheel.AppendAmendedEvent(dir, e)
			} else {
				err = flywheel.AppendEvent(dir, e)
			}
			if err != nil {
				break
			}
		}
	}
	finishLog(dir, err, noState)
}

// batchHasLearning reports whether any event in the batch is a learning or
// dismissed event — the two kinds that change Learnings(events) and therefore
// must be appended under the feedback lock.
func batchHasLearning(events []flywheel.Event) bool {
	for _, e := range events {
		if e.Kind == "learning" || e.Kind == "dismissed" {
			return true
		}
	}
	return false
}

// finishLog reports a non-nil err and exits 6 for a RuleRefusal (the
// amended command refuses an amendment that would change a dispatched
// attempt's gates), 1 otherwise; then it derives state unless noState.
// Shared by appendEvents and the planned/amended fast paths so every
// flywheel log invocation refreshes state the same way.
func finishLog(dir string, err error, noState bool) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	if noState {
		return
	}
	if _, err := flywheel.WriteState(dir); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		os.Exit(1)
	}
}

// runLogJSON appends events read (strictly) from a file or stdin, one JSON
// object per line.
func runLogJSON(dir, path string, noState bool) {
	if path == "-" {
		events, err := flywheel.ParseEvents(os.Stdin, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
			os.Exit(1)
		}
		appendEvents(dir, events, noState)
		return
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		os.Exit(1)
	}
	events, err := flywheel.ParseEvents(f, true)
	f.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		os.Exit(1)
	}
	appendEvents(dir, events, noState)
}

func runLog(args []string) {
	fs, o := logFlags()
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
		usage(os.Stderr)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "flywheel log: unexpected argument %q\n", fs.Arg(0))
		usage(os.Stderr)
		os.Exit(2)
	}
	if o.shard {
		if err := logShardConflicts(o); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
			usage(os.Stderr)
			os.Exit(2)
		}
		if err := runLogShard(o.dir, os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if o.jsonIn != "" {
		runLogJSON(o.dir, o.jsonIn, o.noState)
		return
	}
	// --goal is legal only for planned: refuse it on amended as a usage
	// error (exit 2) before asking whether the goal exists.
	if o.kind == "amended" && o.goal != "" {
		fmt.Fprintf(os.Stderr, "flywheel log: --goal applies to --kind planned only\n")
		usage(os.Stderr)
		os.Exit(2)
	}
	if o.goal != "" {
		if _, ok := findGoal(o.dir, o.goal); !ok {
			fmt.Fprintf(os.Stderr, "flywheel log: unknown goal %q\n", o.goal)
			if gs := knownGoals(o.dir); len(gs) > 0 {
				ids := make([]string, len(gs))
				for i, g := range gs {
					ids[i] = g.ID
				}
				fmt.Fprintf(os.Stderr, "known goals: %s\n", strings.Join(ids, ", "))
			}
			os.Exit(1)
		}
	}
	if o.kind == "amended" {
		if o.note == "" {
			fmt.Fprintf(os.Stderr, "flywheel log: --kind amended requires --note <why>\n")
			usage(os.Stderr)
			os.Exit(2)
		}
		finishLog(o.dir, flywheel.RecordAmendedBy(o.dir, o.task, o.brief, o.note, flywheel.PlanMeta{Session: o.session, Model: o.model}), o.noState)
		return
	}
	if o.kind == "planned" && o.brief != "" {
		finishLog(o.dir, flywheel.RecordPlannedBy(o.dir, o.task, o.brief, flywheel.PlanMeta{Session: o.session, Model: o.model, GoalID: o.goal, Note: o.note}), o.noState)
		return
	}

	var e flywheel.Event
	e.Task = o.task
	e.Kind = o.kind
	e.Session = o.session
	e.Model = o.model
	e.Attempt = o.attempt
	e.Reason = o.reason
	e.Verdict = o.verdict
	e.Brief = o.brief
	e.Commit = o.commit
	e.Note = o.note
	e.GoalID = o.goal
	if o.rc != "" {
		v, err := strconv.ParseInt(o.rc, 10, strconv.IntSize)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: invalid --rc %q: %v\n", o.rc, err)
			os.Exit(1)
		}
		p := new(int)
		*p = int(v)
		e.RC = p
	}
	appendEvents(o.dir, []flywheel.Event{e}, o.noState)
}
