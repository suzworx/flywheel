package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	base    string
	noState bool
	shard   bool
	// replan silences the re-plan warning on --kind planned (issue #476).
	replan bool
	// reanchor and force drive flywheel log --reanchor (issue #436).
	reanchor bool
	force    bool
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
	fs.StringVar(&o.base, "base", "", "with --kind rebased: the new base commit or ref (resolved to a full commit in --dir)")
	fs.BoolVar(&o.noState, "no-state", false, "skip state derivation after appending")
	fs.BoolVar(&o.replan, "replan", false, "with --kind planned: silence the warnings that the task already has attempts or its fw/<task> branch exists")
	fs.BoolVar(&o.shard, "shard", false, "switch this repository's event log to per-task shards under .flywheel/events/ (one-way)")
	fs.BoolVar(&o.reanchor, "reanchor", false, "acknowledge the log chain's first unacknowledged break with an appended reanchored event (requires --note)")
	fs.BoolVar(&o.force, "force", false, "with --reanchor: acknowledge a break that classifies as removed (a possible real edit or deletion)")
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
		{"--base", o.base},
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
func runLogShard(dir string, stdout, stderr io.Writer, now time.Time) error {
	// Everything that can fail happens BEFORE the layout changes: a config
	// that does not parse, a config that cannot be written, or an ignore file
	// that cannot be updated must not leave a migrated repository without its
	// fence (#351 review). The config is restored when the migration fails.
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		return err
	}
	if err := flywheel.EnsureLocksIgnored(dir); err != nil {
		return err
	}
	had := cfg.Log
	if cfg.Log == nil {
		cfg.Log = &flywheel.LogConfig{}
	}
	cfg.Log.Shards = true
	if err := flywheel.WriteConfig(dir, cfg); err != nil {
		return err
	}
	sealed, legacyLines, err := flywheel.EnableShards(dir, now)
	if err != nil {
		cfg.Log = had
		if werr := flywheel.WriteConfig(dir, cfg); werr != nil {
			return fmt.Errorf("%w (and restoring the config failed: %v)", err, werr)
		}
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
	// The workflow lives at the repository root, which a nested factory does
	// not share (#351 review); fall back to dir when git cannot say.
	root := dir
	if r, err := flywheel.RepoRoot(dir); err == nil {
		root = r
	}
	auditPath := filepath.Join(root, ".github", "workflows", "flywheel-audit.yml")
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
			if e.Kind == "amended" || e.Kind == "withdrawn" {
				if err = appendChecked(dir, e); err != nil {
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
			err = appendChecked(dir, e)
			if err != nil {
				break
			}
		}
	}
	finishLog(dir, err, noState)
}

// appendChecked appends one event through the check its kind carries: an
// amended event through appendAmended, a withdrawn one through
// flywheel.AppendWithdrawnEvent's refusal on a live attempt (issue #479), any
// other as is.
func appendChecked(dir string, e flywheel.Event) error {
	switch e.Kind {
	case "amended":
		return appendAmended(dir, e)
	case "withdrawn":
		return flywheel.AppendWithdrawnEvent(dir, e)
	}
	return flywheel.AppendEvent(dir, e)
}

// appendAmended appends one amended event: an acknowledgement (it names an
// attempt) through appendAcknowledgement, any other through
// flywheel.AppendAmendedEvent's inert-amendment check.
func appendAmended(dir string, e flywheel.Event) error {
	if e.Attempt != "" {
		return appendAcknowledgement(dir, e)
	}
	return flywheel.AppendAmendedEvent(dir, e)
}

// appendAcknowledgement appends an amended event that acknowledges
// correction e.Attempt's delta is not retained (issue #452). It amends no
// brief, so it carries none; the attempt must already have a dispatched
// event for the task (Validate, per event, checks the c* attempt and the
// note). rule T1 then passes that correction's mismatched or unreadable
// delta, naming the note, and nothing else.
func appendAcknowledgement(dir string, e flywheel.Event) error {
	if e.Brief != "" || e.Header != nil {
		return fmt.Errorf("amended event acknowledging %s must not carry a brief or header", e.Attempt)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		return err
	}
	for _, d := range events {
		if d.Task == e.Task && d.Kind == "dispatched" && d.Attempt == e.Attempt {
			return flywheel.AppendEvent(dir, e)
		}
	}
	return fmt.Errorf("task %q has no dispatched %s to acknowledge", e.Task, e.Attempt)
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
	if fs.NFlag() == 0 {
		fmt.Fprintf(os.Stderr, "flywheel log: no flags given\n")
		usage(os.Stderr)
		os.Exit(2)
	}
	if o.reanchor {
		runLogReanchor(fs, args, o)
		return
	}
	if o.force {
		logFlagError(fs, args, "--force applies to --reanchor only", logWithout("force"), "")
	}
	if o.shard {
		if err := logShardConflicts(o); err != nil {
			logFlagError(fs, args, err.Error(), func(n string) bool { return n == "dir" || n == "shard" }, "")
		}
		if err := runLogShard(o.dir, os.Stdout, os.Stderr, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if o.jsonIn != "" {
		runLogJSON(o.dir, o.jsonIn, o.noState)
		return
	}
	// A missing or misplaced flag is a usage error (exit 2) reported before
	// anything is read: the error, then the same command fixed (issue #392).
	switch {
	case o.kind == "":
		logFlagError(fs, args, "--kind is required", nil, "--kind <kind>")
	case o.replan && o.kind != "planned":
		logFlagError(fs, args, "--replan applies to --kind planned only", logWithout("replan"), "")
	case (o.kind == "planned" || o.kind == "amended" || o.kind == "withdrawn") && o.task == "":
		logFlagError(fs, args, "--kind "+o.kind+" requires --task <id>", nil, "--task <id>")
	case o.kind == "amended" && o.goal != "":
		// --goal is legal only for planned: refuse it before asking whether
		// the goal exists.
		logFlagError(fs, args, "--goal applies to --kind planned only", logWithout("goal"), "")
	case o.kind == "amended" && o.note == "":
		logFlagError(fs, args, "--kind amended requires --note <why>", nil, `--note "<why>"`)
	case o.kind == "withdrawn" && strings.TrimSpace(o.note) == "":
		// Taking a plan back says why (issue #479).
		logFlagError(fs, args, "--kind withdrawn requires --note <why>", logWithout("note"), `--note "<why>"`)
	case o.kind == "note" && o.note == "":
		// A note is a journal line (issue #409): the text is the whole event.
		logFlagError(fs, args, "--kind note requires --note <text>", logWithout("note"), `--note "<text>"`)
	case o.base != "" && o.kind != "rebased":
		logFlagError(fs, args, "--base applies to --kind rebased only", logWithout("base"), "")
	case o.kind == "rebased" && o.task == "":
		logFlagError(fs, args, "--kind rebased requires --task <id>", nil, "--task <id>")
	case o.kind == "rebased" && o.base == "":
		// A hand rebase records its new base like flywheel rebase does (issue #498).
		logFlagError(fs, args, "--kind rebased requires --base <ref>", nil, "--base <ref>")
	case o.kind == "rebased" && strings.TrimSpace(o.note) == "":
		logFlagError(fs, args, "--kind rebased requires --note <old base> onto <ref>", logWithout("note"), `--note "<old base> onto <ref>"`)
	}
	if o.base != "" {
		sha, err := resolveLogBase(o.dir, o.base)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel log: %v\n", err)
			os.Exit(1)
		}
		o.base = sha
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
	if o.kind == "amended" && o.attempt != "" {
		if o.brief != "" {
			logFlagError(fs, args, "--kind amended --attempt acknowledges a lost delta and takes no --brief", logWithout("brief"), "")
		}
		finishLog(o.dir, appendAcknowledgement(o.dir, flywheel.Event{Task: o.task, Kind: "amended", Attempt: o.attempt,
			Note: o.note, Session: o.session, Model: o.model}), o.noState)
		return
	}
	if o.kind == "amended" {
		finishLog(o.dir, flywheel.RecordAmendedBy(o.dir, o.task, o.brief, o.note, flywheel.PlanMeta{Session: o.session, Model: o.model}), o.noState)
		return
	}
	// Re-planning an id with attempts starts a new plan (issue #476): warn,
	// unless --replan says it is meant, once the event is recorded.
	// A branch fw/<id> another worktree made means another root may own the
	// id (issue #479): the same warning, the same --replan.
	var warns []string
	if o.kind == "planned" && !o.replan {
		for _, w := range []string{replanWarning(o.dir, o.task), branchWarning(o.dir, o.task)} {
			if w != "" {
				warns = append(warns, w)
			}
		}
	}
	rw := strings.Join(warns, "\nflywheel log: ")
	if o.kind == "planned" && o.brief != "" {
		// A re-plan cannot change a dispatched attempt's gates (issue #366):
		// compute the warning against the log before this event lands, and
		// print it once the event is recorded; the exit status is unchanged.
		w := flywheel.PlanDriftWarning(o.dir, o.task, o.brief)
		err := flywheel.RecordPlannedBy(o.dir, o.task, o.brief, flywheel.PlanMeta{Session: o.session, Model: o.model, GoalID: o.goal, Note: o.note})
		if err == nil && w != "" {
			fmt.Fprintf(os.Stderr, "flywheel log: warning: %s\n", w)
		}
		if err == nil && rw != "" {
			fmt.Fprintf(os.Stderr, "flywheel log: %s\n", rw)
		}
		finishLog(o.dir, err, o.noState)
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
	e.Base = o.base
	if o.rc != "" {
		v, err := strconv.ParseInt(o.rc, 10, strconv.IntSize)
		if err != nil {
			logFlagError(fs, args, fmt.Sprintf("invalid --rc %q: %v", o.rc, err), logWithout("rc"), "--rc <exit-code>")
		}
		p := new(int)
		*p = int(v)
		e.RC = p
	}
	appendEvents(o.dir, []flywheel.Event{e}, o.noState)
	if rw != "" {
		fmt.Fprintf(os.Stderr, "flywheel log: %s\n", rw)
	}
}

// fullSHA matches a full 40-hex commit id, which --base passes through as is.
var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// resolveLogBase returns --base as a full commit id: a full sha as given, any
// other ref resolved read-only with git rev-parse --verify in dir (issue #498).
func resolveLogBase(dir, ref string) (string, error) {
	if fullSHA.MatchString(ref) {
		return ref, nil
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("--base %q does not resolve to a commit in %s", ref, dir)
	}
	return strings.TrimSpace(string(out)), nil
}

// replanWarning returns the warning for a planned event on a task that
// already has dispatched attempts (issue #476), or "" for a fresh id. An
// unreadable log gives "": the append that follows reports it.
func replanWarning(dir, task string) string {
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		return ""
	}
	n := 0
	for _, e := range events {
		if e.Task == task && e.Kind == "dispatched" {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("warning: task %s already has %d attempt(s); this starts a new plan, the old attempts stay in the ledger (--replan silences this)", task, n)
}

// branchWarning returns the warning for a planned event on a task whose
// branch fw/<task> already exists in the repository (issue #479), naming the
// worktree that has it checked out, or "". This root's own task worktree is
// not another root: replanWarning covers a re-plan here.
func branchWarning(dir, task string) string {
	exists, wt := flywheel.TaskBranch(dir, task)
	if !exists {
		return ""
	}
	if wt != "" {
		if a, err := os.Stat(wt); err == nil {
			if b, err := os.Stat(filepath.Join(dir, ".flywheel", "worktrees", task)); err == nil && os.SameFile(a, b) {
				return ""
			}
		}
		return fmt.Sprintf("warning: branch fw/%s already exists (checked out in %s); another root may own this id (--replan silences this)", task, wt)
	}
	return fmt.Sprintf("warning: branch fw/%s already exists; another root may own this id (--replan silences this)", task)
}

// runLogReanchor implements flywheel log --reanchor (issue #436): it
// acknowledges the log chain's first unacknowledged break with an appended
// reanchored event and prints what it acknowledged. --kind, --task, --json and
// --shard are usage errors, --note is required (exit 2); a refusal exits 6.
func runLogReanchor(fs *flag.FlagSet, args []string, o *logOptions) {
	keep := func(n string) bool {
		return n == "reanchor" || n == "note" || n == "force" || n == "session" || n == "dir" || n == "no-state"
	}
	for _, c := range []struct{ name, val string }{{"--kind", o.kind}, {"--task", o.task}, {"--json", o.jsonIn}} {
		if c.val != "" {
			logFlagError(fs, args, c.name+" cannot be used with --reanchor", keep, "")
		}
	}
	if o.shard {
		logFlagError(fs, args, "--shard cannot be used with --reanchor", keep, "")
	}
	if strings.TrimSpace(o.note) == "" {
		logFlagError(fs, args, "--reanchor requires --note <why>", logWithout("note"), `--note "<why>"`)
	}
	ack, err := flywheel.Reanchor(o.dir, o.note, o.session, o.force)
	if err == nil {
		fmt.Printf("acknowledged %s line %d (%s): %s\n", ack.File, ack.Line, ack.Reason, ack.Note)
	}
	finishLog(o.dir, err, o.noState)
}

// logFlagError reports an error about one missing or invalid flag and exits 2:
// the error line, then "try:" and the same command fixed — the flags the user
// gave that keep accepts (all when keep is nil), plus add. It never prints the
// full usage, which buried the one line that mattered (issue #392).
func logFlagError(fs *flag.FlagSet, args []string, msg string, keep func(name string) bool, add string) {
	fmt.Fprintf(os.Stderr, "flywheel log: %s\n", msg)
	fmt.Fprintf(os.Stderr, "try: %s\n", logTryCommand(fs, args, keep, add))
	os.Exit(2)
}

// logWithout keeps every flag except the named ones.
func logWithout(names ...string) func(string) bool {
	return func(n string) bool {
		for _, x := range names {
			if n == x {
				return false
			}
		}
		return true
	}
}

// logTryCommand rebuilds "flywheel log ..." from the parsed flags in the order
// the user gave them (each once, with its final value), dropping those keep
// rejects, and appends add verbatim.
func logTryCommand(fs *flag.FlagSet, args []string, keep func(string) bool, add string) string {
	parts := []string{"flywheel log"}
	seen := map[string]bool{}
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		f := fs.Lookup(name)
		if f == nil || seen[name] || (keep != nil && !keep(name)) {
			continue
		}
		seen[name] = true
		v := f.Value.String()
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			if v == "true" {
				parts = append(parts, "--"+name)
			}
			continue
		}
		parts = append(parts, "--"+name, logQuote(v))
	}
	if add != "" {
		parts = append(parts, add)
	}
	return strings.Join(parts, " ")
}

// logQuote double-quotes a value that is empty or holds whitespace or quotes,
// so the try line can be pasted back into a shell.
func logQuote(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\"'") {
		return v
	}
	return `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
}
