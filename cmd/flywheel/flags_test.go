package main

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// flagsAny is any command's flags builder keeping its options pointer.
type flagsAny func() (*flag.FlagSet, any)

// allFlagsFuncs pairs every registered command with its flags builder,
// keeping the options pointer that registerHelp's help hook discards, so
// guards below can read each command's own bound values. Add new commands.
var allFlagsFuncs = map[string]flagsAny{
	"init":       func() (*flag.FlagSet, any) { fs, o := initFlags(); return fs, o },
	"log":        func() (*flag.FlagSet, any) { fs, o := logFlags(); return fs, o },
	"state":      func() (*flag.FlagSet, any) { fs, o := stateFlags(); return fs, o },
	"factory":    func() (*flag.FlagSet, any) { fs, o := factoryFlags(); return fs, o },
	"run":        func() (*flag.FlagSet, any) { fs, o := runFlags(); return fs, o },
	"validate":   func() (*flag.FlagSet, any) { fs, o := validateFlags(); return fs, o },
	"inspect":    func() (*flag.FlagSet, any) { fs, o := inspectFlags(); return fs, o },
	"verify":     func() (*flag.FlagSet, any) { fs, o := verifyFlags(); return fs, o },
	"staff":      func() (*flag.FlagSet, any) { fs, o := staffFlags(); return fs, o },
	"land":       func() (*flag.FlagSet, any) { fs, o := landFlags(); return fs, o },
	"attest":     func() (*flag.FlagSet, any) { fs, o := attestFlags(); return fs, o },
	"status":     func() (*flag.FlagSet, any) { fs, o := statusFlags(); return fs, o },
	"next":       func() (*flag.FlagSet, any) { fs, o := nextFlags(); return fs, o },
	"goal":       func() (*flag.FlagSet, any) { fs, o := goalFlags(); return fs, o },
	"controller": func() (*flag.FlagSet, any) { fs, o := controllerFlags(); return fs, o },
	"lint":       func() (*flag.FlagSet, any) { fs, o := lintFlags(); return fs, o },
	"handoff":    func() (*flag.FlagSet, any) { fs, o := handoffFlags(); return fs, o },
	"cost":       func() (*flag.FlagSet, any) { fs, o := costFlags(); return fs, o },
	"stats":      func() (*flag.FlagSet, any) { fs, o := statsFlags(); return fs, o },
	"supervise":  func() (*flag.FlagSet, any) { fs, o := superviseFlags(); return fs, o },
	"trace":      func() (*flag.FlagSet, any) { fs, o := traceFlags(); return fs, o },
	"explain":    func() (*flag.FlagSet, any) { fs, o := explainFlags(); return fs, o },
	"claim":      func() (*flag.FlagSet, any) { fs, o := claimFlags(); return fs, o },
	"release":    func() (*flag.FlagSet, any) { fs, o := releaseFlags(); return fs, o },
	"claims":     func() (*flag.FlagSet, any) { fs, o := claimsFlags(); return fs, o },
	"claim-edit": func() (*flag.FlagSet, any) { fs, o := claimEditFlags(); return fs, o },
	"review":     func() (*flag.FlagSet, any) { fs, o := reviewFlags(); return fs, o },
	"audit":      func() (*flag.FlagSet, any) { fs, o := auditFlags(); return fs, o },
	"doctor":     func() (*flag.FlagSet, any) { fs, o := doctorFlags(); return fs, o },
	"feedback":   func() (*flag.FlagSet, any) { fs, o := feedbackFlags(); return fs, o },
	"gate":       func() (*flag.FlagSet, any) { fs, o := gateFlags(); return fs, o },
	"watch":      func() (*flag.FlagSet, any) { fs, o := watchFlags(); return fs, o },
	"wait":       func() (*flag.FlagSet, any) { fs, o := waitFlags(); return fs, o },
	"context":    func() (*flag.FlagSet, any) { fs, o := contextFlags(); return fs, o },
	"upgrade":    func() (*flag.FlagSet, any) { fs, o := upgradeFlags(); return fs, o },
	"rebase":     func() (*flag.FlagSet, any) { fs, o := rebaseFlags(); return fs, o },
	"recover":    func() (*flag.FlagSet, any) { fs, o := recoverFlags(); return fs, o },
	"checkpoint": func() (*flag.FlagSet, any) { fs, o := checkpointFlags(); return fs, o },
}

// optionDir reads the dir an options struct bound; "" when it has no dir.
func optionDir(o any) string {
	v := reflect.ValueOf(o).Elem()
	f := v.FieldByName("dir")
	if !f.IsValid() || f.Kind() != reflect.String {
		return ""
	}
	return f.String()
}

// TestDirFlagsReachEveryRegistryCommand parses --dir <tempdir> for every
// registry command whose flags function defines dir and asserts the command's
// own bound options hold it; copy-before-Parse keeps the default forever.
func TestDirFlagsReachEveryRegistryCommand(t *testing.T) {
	t.Parallel()
	for name, c := range commands {
		if c.flags == nil {
			continue
		}
		mk, ok := allFlagsFuncs[name]
		if !ok {
			t.Errorf("%s: flags registered but missing from allFlagsFuncs", name)
			continue
		}
		fs, o := mk()
		if fs.Lookup("dir") == nil {
			continue
		}
		want := t.TempDir()
		if err := fs.Parse([]string{"--dir", want}); err != nil {
			t.Errorf("%s: parse --dir: %v", name, err)
			continue
		}
		if got := optionDir(o); got != want {
			t.Errorf("%s: --dir %s ignored by bound options (got %q)", name, want, got)
		}
	}
}

// TestVerifyExitCodeDistinguishesInconclusive checks the three-state exit
// mapping (issue #244): 0 when every check passes, 6 for an established
// violation, 8 when every failing check is inconclusive, and a violation
// always outranks an inconclusive.
func TestVerifyExitCodeDistinguishesInconclusive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		items []flywheel.VerifyItem
		want  int
	}{
		{"all pass", []flywheel.VerifyItem{{Rule: "T3", Pass: true}}, 0},
		{"violation", []flywheel.VerifyItem{{Rule: "T3", Pass: false}}, 6},
		{"inconclusive", []flywheel.VerifyItem{{Rule: "T3", Pass: false, Inconclusive: true}}, 8},
		{"violation plus inconclusive", []flywheel.VerifyItem{
			{Rule: "T1", Pass: false},
			{Rule: "T3", Pass: false, Inconclusive: true},
		}, 6},
		{"inconclusive plus pass", []flywheel.VerifyItem{
			{Rule: "T1", Pass: true},
			{Rule: "T3", Pass: false, Inconclusive: true},
		}, 8},
	}
	for _, tc := range cases {
		if got := verifyExit(tc.items); got != tc.want {
			t.Errorf("%s: verifyExit() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestVerifyFlagsBindWorkdir checks verify's --workdir reaches the bound
// options, exactly like the other commands' flags (issue #244).
func TestVerifyFlagsBindWorkdir(t *testing.T) {
	t.Parallel()
	fs, o := verifyFlags()
	if err := fs.Parse([]string{"--workdir", "X", "--all"}); err != nil {
		t.Fatalf("verifyFlags: %v", err)
	}
	if o.workdir != "X" {
		t.Errorf("workdir = %q, want X", o.workdir)
	}
	if !o.all {
		t.Error("all = false, want true")
	}
}

// TestClaimEditRefusesUnboundedPaths checks claim-edit's argument check
// refuses any claim path that is not a literal file, with the message naming
// the offending pattern: a wildcard or directory prefix would let ownsContains
// exempt a whole tree, and a pattern cannot be bound to one content hash —
// a claim must name the files actually edited (issue #258).
func TestClaimEditRefusesUnboundedPaths(t *testing.T) {
	t.Parallel()
	for _, p := range []string{"*", "a/*.go", "dir/", "a?b", "[ab]c"} {
		got := claimPathError(p)
		if got == "" {
			t.Errorf("claimPathError(%q) = empty, want a refusal naming the pattern", p)
			continue
		}
		if !strings.Contains(got, p) {
			t.Errorf("claimPathError(%q) = %q, want it to name the offending pattern", p, got)
		}
		if !strings.Contains(got, "files actually edited") {
			t.Errorf("claimPathError(%q) = %q, want it to say claims must name the files actually edited", p, got)
		}
	}
	for _, p := range []string{"README.md", "a/b.go", "docs/PROTOCOL.md"} {
		if got := claimPathError(p); got != "" {
			t.Errorf("claimPathError(%q) = %q, want empty (a literal path is claimable)", p, got)
		}
	}
}

// TestInitFlagsBindEveryOption parses non-default values for every init flag
// and asserts the bound options hold them.
func TestInitFlagsBindEveryOption(t *testing.T) {
	t.Parallel()
	fs, o := initFlags()
	if err := fs.Parse([]string{"--dir", "X", "--force"}); err != nil {
		t.Fatalf("initFlags: %v", err)
	}
	want := initOptions{dir: "X", force: true, localURL: "http://localhost:11434/v1"}
	if *o != want {
		t.Errorf("initFlags parsed = %#v, want %#v", *o, want)
	}
}

// TestLogFlagsBindEveryOption parses non-default values for every log flag
// and asserts the bound options hold them.
func TestLogFlagsBindEveryOption(t *testing.T) {
	t.Parallel()
	args := []string{
		"--dir", "X", "--task", "T1", "--kind", "planned", "--brief", "b.txt",
		"--session", "s", "--model", "m", "--attempt", "r1", "--rc", "0",
		"--reason", "stop", "--verdict", "pass", "--commit", "c", "--note", "n",
		"--goal", "g1", "--base", "b1", "--json", "f", "--no-state", "--replan",
	}
	fs, o := logFlags()
	if err := fs.Parse(args); err != nil {
		t.Fatalf("logFlags: %v", err)
	}
	want := logOptions{dir: "X", jsonIn: "f", task: "T1", kind: "planned",
		session: "s", model: "m", attempt: "r1", rc: "0", reason: "stop",
		verdict: "pass", brief: "b.txt", commit: "c", note: "n", goal: "g1", base: "b1", noState: true, replan: true}
	if *o != want {
		t.Errorf("logFlags parsed = %#v, want %#v", *o, want)
	}
}

// TestStateFlagsBindEveryOption parses non-default values for every state
// flag and asserts the bound options hold them.
func TestStateFlagsBindEveryOption(t *testing.T) {
	t.Parallel()
	fs, o := stateFlags()
	if err := fs.Parse([]string{"--dir", "X", "--json"}); err != nil {
		t.Fatalf("stateFlags: %v", err)
	}
	want := stateOptions{dir: "X", asJSON: true}
	if *o != want {
		t.Errorf("stateFlags parsed = %#v, want %#v", *o, want)
	}
}

// TestFactoryFlagsBindEveryOption parses non-default values for every factory
// flag and asserts the bound options hold them.
func TestFactoryFlagsBindEveryOption(t *testing.T) {
	t.Parallel()
	args := []string{"--dir", "X", "--once", "--json", "--interval", "5s",
		"--width", "120", "--now", "2026-01-02T15:04:05Z"}
	fs, o := factoryFlags()
	if err := fs.Parse(args); err != nil {
		t.Fatalf("factoryFlags: %v", err)
	}
	want := factoryOptions{dir: "X", once: true, asJSON: true,
		interval: 5 * time.Second, width: 120, now: "2026-01-02T15:04:05Z"}
	if *o != want {
		t.Errorf("factoryFlags parsed = %#v, want %#v", *o, want)
	}
}

// TestReviewAgentFlags parses the review agent's flags (issue #389) and
// asserts the bound options hold them.
func TestReviewAgentFlags(t *testing.T) {
	t.Parallel()
	fs, o := reviewFlags()
	if err := fs.Parse([]string{"--agent", "--worker", "rev", "--round", "3", "--session", "s", "--workdir", "W", "--dir", "D"}); err != nil {
		t.Fatalf("reviewFlags: %v", err)
	}
	if !o.agent || o.worker != "rev" || o.round != 3 || o.session != "s" || o.workdir != "W" || o.dir != "D" {
		t.Errorf("reviewFlags parsed = %#v", *o)
	}
	if !strings.Contains(reviewUsageLine, "--agent") || !strings.Contains(reviewUsageLine, "--round N") {
		t.Errorf("usage %q does not name --agent and --round", reviewUsageLine)
	}
}

// TestCalibrateRunFlags parses `flywheel review calibrate`'s flags (issue
// #389) and asserts the defaults and the bound options.
func TestCalibrateRunFlags(t *testing.T) {
	t.Parallel()
	fs, o := reviewCalibrateFlags()
	if o.sample != 10 || o.window != 15 || o.main != "origin/main" || o.cases != "docs/calibration/external-review-bugs.json" {
		t.Errorf("calibrate defaults = %#v", *o)
	}
	if err := fs.Parse([]string{"--cases", "c.json", "--session", "rev", "--sample", "4", "--seed", "9",
		"--worker", "w", "--window", "20", "--main", "main", "--out", "r.md", "--dir", "D"}); err != nil {
		t.Fatalf("reviewCalibrateFlags: %v", err)
	}
	if o.cases != "c.json" || o.session != "rev" || o.sample != 4 || o.seed != 9 || o.worker != "w" ||
		o.window != 20 || o.main != "main" || o.out != "r.md" || o.dir != "D" {
		t.Errorf("calibrate parsed = %#v", *o)
	}
	if !strings.Contains(reviewUsageLine, "flywheel review calibrate --cases FILE") {
		t.Errorf("usage %q does not name review calibrate", reviewUsageLine)
	}
}

// TestCalibratePerPersonaFlags parses `review calibrate --panel [dims]` (issue
// #420): unset by default, bare for the configured panel, with a value for
// those dimensions; help and run share the one flag set.
func TestCalibratePerPersonaFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		set  bool
		dims string
		pos  int
	}{
		{nil, false, "", 0},
		{[]string{"--panel"}, true, "", 0},
		{[]string{"--panel=correctness, tests"}, true, "correctness,tests", 0},
		{[]string{"--panel", "tests,docs"}, true, "", 1}, // the run takes the next argument as the dims
		{[]string{"--panel=false"}, false, "", 0},
	} {
		fs, o := reviewCalibrateFlags()
		pos, err := parseFlags(fs, tc.args)
		if err != nil || o.panel.set != tc.set || o.panel.String() != tc.dims || len(pos) != tc.pos {
			t.Errorf("%v: set %v dims %q pos %v (err %v), want %v %q %d", tc.args, o.panel.set, o.panel.String(), pos, err, tc.set, tc.dims, tc.pos)
		}
	}
	if fs, _ := reviewCalibrateFlags(); fs.Lookup("panel") == nil || !strings.Contains(reviewUsageLine, "--panel [dims]") {
		t.Error("review calibrate help lacks --panel")
	}
}

// TestReviewLoopFlags parses the review loop's flags (issue #389): --fix with
// its rounds, correcting worker and worktree, and a lead's --dismiss.
func TestReviewLoopFlags(t *testing.T) {
	t.Parallel()
	fs, o := reviewFlags()
	if o.rounds != 3 {
		t.Errorf("default --rounds = %d, want 3", o.rounds)
	}
	if err := fs.Parse([]string{"--agent", "--fix", "--rounds", "5", "--fix-worker", "w", "--worktree", "--session", "rev"}); err != nil {
		t.Fatalf("reviewFlags: %v", err)
	}
	if !o.agent || !o.fix || o.rounds != 5 || o.fixWorker != "w" || !o.worktree || o.session != "rev" {
		t.Errorf("--fix parsed = %#v", *o)
	}
	fs, o = reviewFlags()
	if err := fs.Parse([]string{"--dismiss", "T1-r1-2", "--session", "lead", "--note", "by design"}); err != nil {
		t.Fatalf("reviewFlags: %v", err)
	}
	if o.dismiss != "T1-r1-2" || o.session != "lead" || o.note != "by design" {
		t.Errorf("--dismiss parsed = %#v", *o)
	}
	for _, want := range []string{"--fix", "--rounds N", "--dismiss <finding-id>"} {
		if !strings.Contains(reviewUsageLine, want) {
			t.Errorf("usage %q does not name %s", reviewUsageLine, want)
		}
	}
}

// TestReviewGroupFlags parses the group review's flags (issue #420): --group
// with --agent, a base and a worker, and names them in the usage.
func TestReviewGroupFlags(t *testing.T) {
	t.Parallel()
	fs, o := reviewFlags()
	if err := fs.Parse([]string{"--group", "tasks:A,B", "--agent", "--base", "origin/main", "--worker", "w", "--session", "rev"}); err != nil {
		t.Fatalf("reviewFlags: %v", err)
	}
	if o.group != "tasks:A,B" || !o.agent || o.base != "origin/main" || o.worker != "w" || o.session != "rev" {
		t.Errorf("--group parsed = %#v", *o)
	}
	if !strings.Contains(reviewUsageLine, "flywheel review --group <goal|tasks:a,b> --agent --session <session> [--base REF]") {
		t.Errorf("usage %q does not name --group", reviewUsageLine)
	}
}

// TestReviewPanelFlags parses the review panel's flags (issue #420): --panel
// with --agent, alone or with the loop's --fix and --rounds.
func TestReviewPanelFlags(t *testing.T) {
	t.Parallel()
	fs, o := reviewFlags()
	if o.panel {
		t.Error("--panel defaults to true")
	}
	if err := fs.Parse([]string{"--agent", "--panel", "--fix", "--rounds", "2", "--session", "rev"}); err != nil {
		t.Fatalf("reviewFlags: %v", err)
	}
	if !o.agent || !o.panel || !o.fix || o.rounds != 2 || o.session != "rev" {
		t.Errorf("--panel parsed = %#v", *o)
	}
	if !strings.Contains(reviewUsageLine, "--agent --panel --session <session>") {
		t.Errorf("usage %q does not name --agent --panel", reviewUsageLine)
	}
}

// TestRecoverJSON checks `flywheel recover --json` (issue #422): the report
// decodes with each unit's next action, it is read-only (the log is byte
// identical after), and the exit is 0 for an intact ledger with nothing to
// investigate.
func TestRecoverJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("owns: a.go\nneeds: none\ngate: true\n\n# TASK: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range []flywheel.Event{
		{TS: "2026-09-20T00:00:00Z", Task: "T", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-20T00:01:00Z", Task: "T", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-20T00:02:00Z", Task: "T", Kind: "finished", Attempt: "r1", Reason: "stop"},
	} {
		if err := flywheel.AppendEvent(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(dir, ".flywheel", "events.jsonl")
	before, _ := os.ReadFile(logPath)
	var stdout, stderr strings.Builder
	code := recoverMain([]string{"--json", "--dir", dir}, &stdout, &stderr, time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC))
	if code != 0 {
		t.Fatalf("exit = %d, stderr %q, stdout %s", code, stderr.String(), stdout.String())
	}
	var rep flywheel.RecoverReport
	if err := json.Unmarshal([]byte(stdout.String()), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if !rep.Integrity.Pass || len(rep.Tasks) != 1 || rep.Tasks[0].Next.Action != "re-validate" || rep.Tasks[0].Next.Command != "flywheel validate T" {
		t.Errorf("report = %+v", rep)
	}
	if after, _ := os.ReadFile(logPath); string(after) != string(before) {
		t.Error("recover without --apply changed the event log")
	}
	if code := recoverMain([]string{"extra"}, &stdout, &stderr, time.Now()); code != 2 {
		t.Errorf("a positional argument exits %d, want 2", code)
	}
	fs, o := recoverFlags()
	if o.dormantAfter != 168*time.Hour || o.all {
		t.Errorf("defaults: dormant-after %s, all %v; want 168h, false", o.dormantAfter, o.all)
	}
	if err := fs.Parse([]string{"--dormant-after", "0", "--all"}); err != nil || o.dormantAfter != 0 || !o.all {
		t.Errorf("parse --dormant-after 0 --all: %v, %+v", err, *o)
	}
}

// TestAcknowledgeRequiresNote checks the acknowledgement of a lost delta
// (`flywheel log --kind amended --attempt cN --note`, issue #452) needs a
// note and a dispatched cN, carries no brief, and lands as an amended event
// naming the attempt, on the JSON route too.
func TestAcknowledgeRequiresNote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := flywheel.AppendEvent(dir, flywheel.Event{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "d.txt", SHA256: "aaaaaaaaaaaaaaaa"}); err != nil {
		t.Fatalf("AppendEvent(dispatched) error = %v", err)
	}
	if err := appendAmended(dir, flywheel.Event{Task: "T1", Kind: "amended", Attempt: "c1"}); err == nil {
		t.Error("acknowledgement without a note was appended")
	}
	if err := appendAmended(dir, flywheel.Event{Task: "T1", Kind: "amended", Attempt: "c2", Note: "lost"}); err == nil || !strings.Contains(err.Error(), "no dispatched c2") {
		t.Errorf("acknowledging an undispatched c2 = %v, want an error naming it", err)
	}
	if err := appendAmended(dir, flywheel.Event{Task: "T1", Kind: "amended", Attempt: "c1", Brief: "b.txt", Note: "lost"}); err == nil {
		t.Error("acknowledgement carrying a brief was appended")
	}
	if err := appendAmended(dir, flywheel.Event{Task: "T1", Kind: "amended", Attempt: "c1", Note: "overwritten before #452"}); err != nil {
		t.Fatalf("acknowledging c1 = %v, want nil", err)
	}
	evs, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	last := evs[len(evs)-1]
	if len(evs) != 2 || last.Kind != "amended" || last.Attempt != "c1" || last.Brief != "" || last.Header != nil {
		t.Errorf("events = %+v, want one acknowledgement of c1 with no brief after the dispatch", evs)
	}
}
