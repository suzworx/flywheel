package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	"ledger":     func() (*flag.FlagSet, any) { fs, o := ledgerBackupFlags(); return fs, o },
	"feedback":   func() (*flag.FlagSet, any) { fs, o := feedbackFlags(); return fs, o },
	"gate":       func() (*flag.FlagSet, any) { fs, o := gateFlags(); return fs, o },
	"watch":      func() (*flag.FlagSet, any) { fs, o := watchFlags(); return fs, o },
	"wait":       func() (*flag.FlagSet, any) { fs, o := waitFlags(); return fs, o },
	"context":    func() (*flag.FlagSet, any) { fs, o := contextFlags(); return fs, o },
	"upgrade":    func() (*flag.FlagSet, any) { fs, o := upgradeFlags(); return fs, o },
	"rebase":     func() (*flag.FlagSet, any) { fs, o := rebaseFlags(); return fs, o },
	"recover":    func() (*flag.FlagSet, any) { fs, o := recoverFlags(); return fs, o },
	"checkpoint": func() (*flag.FlagSet, any) { fs, o := checkpointFlags(); return fs, o },
	"brief":      func() (*flag.FlagSet, any) { fs, o := briefFlags(); return fs, o },
	"ship":       func() (*flag.FlagSet, any) { fs, o := shipFlags(); return fs, o },
	"schedule":   func() (*flag.FlagSet, any) { fs, o := scheduleFlags(true); return fs, o },
	"suspend":    func() (*flag.FlagSet, any) { fs, o := suspendFlags(); return fs, o },
	"resume":     func() (*flag.FlagSet, any) { fs, o := resumeFlags(); return fs, o },
	"fleet":      func() (*flag.FlagSet, any) { fs, o := fleetFlags("add"); return fs, o },
	"unstage":    func() (*flag.FlagSet, any) { fs, o := unstageFlags(); return fs, o },
}

// TestFleetFlagsBind checks add's --name and list/status's --json reach the
// bound options, and each subcommand takes only its own flags (issue #585).
func TestFleetFlagsBind(t *testing.T) {
	t.Parallel()
	fs, o := fleetFlags("add")
	if err := fs.Parse([]string{"--name", "N"}); err != nil || o.name != "N" {
		t.Errorf("fleet add --name N: parsed %#v, err %v", *o, err)
	}
	for _, sub := range []string{"list", "status"} {
		fs, o := fleetFlags(sub)
		if err := fs.Parse([]string{"--json"}); err != nil || !o.json {
			t.Errorf("fleet %s --json: parsed %#v, err %v", sub, *o, err)
		}
		if fs.Lookup("name") != nil {
			t.Errorf("fleet %s defines --name", sub)
		}
	}
	if fs, _ := fleetFlags("remove"); fs.Lookup("json") != nil || fs.Lookup("name") != nil {
		t.Error("fleet remove defines flags, want none")
	}
	fs, o = fleetFlags("status")
	if o.idleAfter != flywheel.DefaultIdleAfter {
		t.Errorf("fleet status --idle-after default = %v, want %v", o.idleAfter, flywheel.DefaultIdleAfter)
	}
	if err := fs.Parse([]string{"--all", "--idle-after", "24h"}); err != nil || !o.all || o.idleAfter != 24*time.Hour {
		t.Errorf("fleet status --all --idle-after 24h: parsed %#v, err %v", *o, err)
	}
	if fs, _ := fleetFlags("list"); fs.Lookup("all") != nil || fs.Lookup("idle-after") != nil {
		t.Error("fleet list defines --all or --idle-after, want status only")
	}
}

// TestFleetMainTable drives fleetMain against a temp registry: usage exits
// 2, add then status prints the table's columns and the root's row, list
// names the ledger, a duplicate add exits 1, and remove empties the fleet.
func TestFleetMainTable(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "fleet.json")
	root := filepath.Join(t.TempDir(), "alpha")
	if _, err := flywheel.Init(root, false); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		var out, errb strings.Builder
		code := fleetMain(args, &out, &errb, file, time.Now())
		return code, out.String() + errb.String()
	}
	for _, args := range [][]string{nil, {"bogus"}, {"add"}, {"list", "x"}, {"status", "--name", "n"}} {
		if code, out := run(args...); code != 2 {
			t.Errorf("fleet %v = %d, want 2 (usage)\n%s", args, code, out)
		}
	}
	if code, out := run("add", root, "--name", "a1"); code != 0 || !strings.Contains(out, "added a1") {
		t.Fatalf("fleet add = %d\n%s", code, out)
	}
	if code, out := run("add", root); code != 1 || !strings.Contains(out, `"a1"`) {
		t.Errorf("fleet add twice = %d, want 1 naming a1\n%s", code, out)
	}
	code, out := run("status")
	head := strings.Fields(strings.SplitN(out, "\n", 2)[0])
	if want := "NAME KIND EVENTS RUNNING PASSED FINISHED ANDON STATE HEALTH LAST"; code != 0 || strings.Join(head, " ") != want {
		t.Errorf("fleet status = %d, header %v, want %s\n%s", code, head, want, out)
	}
	if !strings.Contains(out, "a1") || !strings.Contains(out, "running") {
		t.Errorf("fleet status lacks the a1 running row:\n%s", out)
	}
	if code, out := run("list"); code != 0 || !strings.Contains(out, "root") || !strings.Contains(out, root) {
		t.Errorf("fleet list = %d\n%s", code, out)
	}
	var rows []flywheel.FleetRow
	if code, out := run("status", "--json"); code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
		t.Errorf("fleet status --json = %d, rows %d\n%s", code, len(rows), out)
	}
	if code, out := run("remove", "a1"); code != 0 {
		t.Errorf("fleet remove = %d\n%s", code, out)
	}
	if code, out := run("remove", "a1"); code != 1 {
		t.Errorf("fleet remove twice = %d, want 1\n%s", code, out)
	}
}

// TestFleetIdleFoldCLI: fleet status folds each root's idle worktree ledgers
// into one "+N idle worktree ledgers (oldest <age>)" row and keeps both roots
// (b's own ledger is idle); --all lists every ledger; --idle-after moves the
// line; a negative --idle-after is usage. Each ledger's event is its own
// (its session is its directory's base), except a/copy, a byte-identical copy
// of root a's event: it has no event after the fork, so it folds though 1h
// old and --all lists it with EVENTS +0 (issue #608).
func TestFleetIdleFoldCLI(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	ledger := func(dir string, age time.Duration, session string) {
		if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(flywheel.Event{TS: now.Add(-age).Format(time.RFC3339Nano), Kind: "staffed", Persona: "lead", Session: session})
		if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var f flywheel.Fleet
	for _, name := range []string{"a", "b"} {
		root := filepath.Join(t.TempDir(), name)
		rootAge := map[string]time.Duration{"a": time.Hour, "b": 300 * time.Hour}[name]
		ledger(root, rootAge, name)
		for unit, age := range map[string]time.Duration{"live": time.Hour, "old1": 100 * time.Hour, "old2": 200 * time.Hour} {
			ledger(filepath.Join(root, ".flywheel", "worktrees", unit), age, unit)
		}
		if name == "a" {
			ledger(filepath.Join(root, ".flywheel", "worktrees", "copy"), rootAge, name)
		}
		f.Roots = append(f.Roots, flywheel.FleetRoot{Name: name, Path: root})
	}
	file := filepath.Join(t.TempDir(), "fleet.json")
	if err := flywheel.SaveFleet(file, f); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		var out, errb strings.Builder
		code := fleetMain(args, &out, &errb, file, now)
		return code, out.String() + errb.String()
	}
	names := func(out string) string {
		var s []string
		for _, line := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
			s = append(s, strings.Fields(line)[0])
		}
		return strings.Join(s, " ")
	}
	code, out := run("status")
	if want := "a a/live +3 b b/live +2"; code != 0 || names(out) != want {
		t.Errorf("fleet status = %d, names %q, want %q (a/copy folded)\n%s", code, names(out), want, out)
	}
	if !strings.Contains(out, "+3 idle worktree ledgers (oldest 8d)") || !strings.Contains(out, "+2 idle worktree ledgers (oldest 8d)") {
		t.Errorf("fleet status lacks the per-root fold lines (+3 for a, +2 for b) with the oldest age:\n%s", out)
	}
	code, out = run("status", "--all")
	if want := "a a/copy a/live a/old1 a/old2 b b/live b/old1 b/old2"; code != 0 || names(out) != want {
		t.Errorf("fleet status --all = %d, names %q, want %q\n%s", code, names(out), want, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 2 && fields[0] == "a/copy" && fields[2] != "+0" {
			t.Errorf("fleet status --all a/copy EVENTS = %q, want +0\n%s", fields[2], out)
		}
	}
	code, out = run("status", "--idle-after", "150h")
	if want := "a a/live a/old1 +2 b b/live b/old1 +1"; code != 0 || names(out) != want {
		t.Errorf("fleet status --idle-after 150h = %d, names %q, want %q\n%s", code, names(out), want, out)
	}
	var rows []flywheel.FleetRow
	if code, out := run("status", "--json"); code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 6 || rows[2].Idle != 3 || rows[5].Idle != 2 {
		t.Errorf("fleet status --json = %d, %d rows, want 6 with fold rows of 3 and 2\n%s", code, len(rows), out)
	}
	if code, out := run("status", "--idle-after", "-1h"); code != 2 {
		t.Errorf("fleet status --idle-after -1h = %d, want 2 (usage)\n%s", code, out)
	}
}

// TestScheduleFlagsBind checks install's --every and --dir reach the bound
// options, and status/remove take no --every (issue #572).
func TestScheduleFlagsBind(t *testing.T) {
	t.Parallel()
	fs, o := scheduleFlags(true)
	if err := fs.Parse([]string{"--every", "7m", "--dir", "D"}); err != nil {
		t.Fatalf("scheduleFlags: %v", err)
	}
	if want := (scheduleOptions{dir: "D", every: 7 * time.Minute}); *o != want {
		t.Errorf("scheduleFlags parsed = %#v, want %#v", *o, want)
	}
	if fs, _ := scheduleFlags(false); fs.Lookup("every") != nil {
		t.Error("status/remove define --every")
	}
}

// fakeScheduler records calls instead of touching the OS scheduler.
type fakeScheduler struct{ calls []string }

func (f *fakeScheduler) Install(p flywheel.SchedulePlan) error {
	f.calls = append(f.calls, "install "+p.Name+" "+p.Every.String())
	return nil
}
func (f *fakeScheduler) Remove(name string) error {
	f.calls = append(f.calls, "remove "+name)
	return nil
}
func (f *fakeScheduler) Status(name string) (bool, string, error) {
	f.calls = append(f.calls, "status "+name)
	return true, "next run soon", nil
}

// TestScheduleMainExitsAndOutput checks install prints the name, interval and
// command, status prints installed plus the detail, and a bad subcommand or
// an --every under a minute is usage (exit 2) before any scheduler call.
func TestScheduleMainExitsAndOutput(t *testing.T) {
	t.Parallel()
	f := &fakeScheduler{}
	newSched := func(flywheel.Runner) (flywheel.Scheduler, error) { return f, nil }
	plan := func(dir string, every time.Duration) (flywheel.SchedulePlan, error) {
		if every == 0 {
			every = flywheel.DefaultScheduleEvery
		}
		return flywheel.SchedulePlan{Name: "flywheel-x-12345678", Every: every, Exe: "/bin/fw",
			Dir: dir, Args: []string{"controller", "--once", "--dir", dir}}, nil
	}
	dir := t.TempDir()
	var out, errb strings.Builder
	if code := scheduleMain([]string{"install", "--every", "5m", "--dir", dir}, &out, &errb, newSched, plan, noFleetPlan(t)); code != 0 {
		t.Fatalf("install = %d; stderr %s", code, errb.String())
	}
	if w := "installed flywheel-x-12345678: every 5m0s runs /bin/fw controller --once --dir"; !strings.Contains(out.String(), w) {
		t.Errorf("install output = %q, want %q", out.String(), w)
	}
	out.Reset()
	if code := scheduleMain([]string{"status", "--dir", dir}, &out, &errb, newSched, plan, noFleetPlan(t)); code != 0 ||
		out.String() != "flywheel-x-12345678: installed\nnext run soon\n" {
		t.Errorf("status = %d %q", code, out.String())
	}
	if code := scheduleMain([]string{"remove", "--dir", dir}, &out, &errb, newSched, plan, noFleetPlan(t)); code != 0 {
		t.Errorf("remove = %d", code)
	}
	calls := len(f.calls)
	for _, args := range [][]string{{}, {"bogus"}, {"install", "--every", "30s"}, {"status", "--every", "5m"}, {"remove", "extra"}} {
		if code := scheduleMain(args, &out, &errb, newSched, plan, noFleetPlan(t)); code != 2 {
			t.Errorf("scheduleMain(%v) = %d, want 2", args, code)
		}
	}
	if len(f.calls) != calls || calls != 3 {
		t.Errorf("calls = %v, want exactly install, status, remove", f.calls)
	}
}

// noFleetPlan is a fleetPlan a test that never passes --fleet must not call.
func noFleetPlan(t *testing.T) func(time.Duration, string) (flywheel.SchedulePlan, error) {
	return func(time.Duration, string) (flywheel.SchedulePlan, error) {
		t.Error("fleet plan built without --fleet")
		return flywheel.SchedulePlan{}, nil
	}
}

// TestScheduleFleetCLI: install --fleet builds the fleet plan (default every
// unless --every is given, --notify passed through) and installs it under its
// own name, never the repository plan; status and remove --fleet use the same
// name; --notify without --fleet is usage.
func TestScheduleFleetCLI(t *testing.T) {
	t.Parallel()
	f := &fakeScheduler{}
	newSched := func(flywheel.Runner) (flywheel.Scheduler, error) { return f, nil }
	repoPlan := func(string, time.Duration) (flywheel.SchedulePlan, error) {
		t.Error("repository plan built with --fleet")
		return flywheel.SchedulePlan{}, nil
	}
	var got []string
	fleetPlan := func(every time.Duration, notify string) (flywheel.SchedulePlan, error) {
		got = append(got, fmt.Sprintf("%v %q", every, notify))
		args := []string{"fleet", "watch", "--once"}
		if notify != "" {
			args = append(args, "--notify", notify)
		}
		return flywheel.SchedulePlan{Name: "flywheel-fleet-me", Every: max(every, 5*time.Minute), Exe: "/bin/fw", Args: args}, nil
	}
	var out, errb strings.Builder
	if code := scheduleMain([]string{"install", "--fleet", "--notify", "say hi"}, &out, &errb, newSched, repoPlan, fleetPlan); code != 0 {
		t.Fatalf("install --fleet = %d; stderr %s", code, errb.String())
	}
	if w := `installed flywheel-fleet-me: every 5m0s runs /bin/fw fleet watch --once --notify "say hi"`; !strings.Contains(out.String(), w) {
		t.Errorf("install output = %q, want %q", out.String(), w)
	}
	for _, args := range [][]string{{"install", "--fleet", "--every", "10m"}, {"status", "--fleet"}, {"remove", "--fleet"}} {
		if code := scheduleMain(args, &out, &errb, newSched, repoPlan, fleetPlan); code != 0 {
			t.Errorf("schedule %v = %d; stderr %s", args, code, errb.String())
		}
	}
	if want := []string{`0s "say hi"`, `10m0s ""`, `0s ""`, `0s ""`}; !slices.Equal(got, want) {
		t.Errorf("fleet plans = %q, want %q", got, want)
	}
	if want := []string{"install flywheel-fleet-me 5m0s", "install flywheel-fleet-me 10m0s", "status flywheel-fleet-me", "remove flywheel-fleet-me"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
	if code := scheduleMain([]string{"install", "--notify", "x"}, &out, &errb, newSched, repoPlan, fleetPlan); code != 2 {
		t.Errorf("install --notify without --fleet = %d, want 2", code)
	}
}

// TestSuspendFlagsBind parses every suspend and resume flag and asserts the
// bound options hold them, and --until reads HH:MM as today or tomorrow
// (issue #572).
func TestSuspendFlagsBind(t *testing.T) {
	t.Parallel()
	fs, o := suspendFlags()
	if err := fs.Parse([]string{"--reason", "r", "--until", "18:00", "--stop", "--session", "s", "--dir", "D"}); err != nil {
		t.Fatalf("suspendFlags: %v", err)
	}
	if want := (suspendOptions{dir: "D", session: "s", reason: "r", until: "18:00", stop: true}); *o != want {
		t.Errorf("suspendFlags parsed = %#v, want %#v", *o, want)
	}
	fs, o = resumeFlags()
	if err := fs.Parse([]string{"--note", "n", "--no-redispatch", "--session", "s", "--dir", "D"}); err != nil {
		t.Fatalf("resumeFlags: %v", err)
	}
	if want := (suspendOptions{dir: "D", session: "s", note: "n", noRedispatch: true}); *o != want {
		t.Errorf("resumeFlags parsed = %#v, want %#v", *o, want)
	}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"18:00":                now.Add(6 * time.Hour),
		"09:30":                now.Add(21*time.Hour + 30*time.Minute),
		"2026-09-27T01:00:00Z": now.Add(13 * time.Hour),
	} {
		if got, err := parseUntil(in, now); err != nil || !got.Equal(want) {
			t.Errorf("parseUntil(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseUntil("6pm", now); err == nil {
		t.Error("parseUntil(6pm) = nil error, want one")
	}
}

// stoppedFactory is a ledger frozen by a stopping suspension: A's latest
// finish is suspended, B's was re-dispatched since, C finished clean and D
// finished suspended before a later clean attempt.
func stoppedFactory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := flywheel.Init(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var evs []flywheel.Event
	unit := func(task string, reasons ...string) {
		evs = append(evs, flywheel.Event{Task: task, Kind: "planned", Brief: "b.md"})
		for i, r := range reasons {
			a := "r" + string(rune('1'+i))
			evs = append(evs, flywheel.Event{Task: task, Kind: "dispatched", Attempt: a, Model: "m"})
			if r != "" {
				evs = append(evs, flywheel.Event{Task: task, Kind: "finished", Attempt: a, Model: "m", Reason: r, Session: "ses_" + task})
			}
		}
	}
	unit("A", "suspended")
	unit("B", "suspended", "")
	unit("C", "stop")
	unit("D", "suspended", "stop")
	evs = append(evs, flywheel.Event{Kind: "suspended", Session: "lead", Note: "freeze", Stop: true})
	for _, e := range evs {
		if err := flywheel.AppendEvent(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestResumeRedispatchStopped: resume thaws and starts `flywheel run <task>
// --resume` for exactly the unit a stopping suspension stopped and not
// re-dispatched since, after writing its continue delta; --no-redispatch
// only thaws (issue #572).
func TestResumeRedispatchStopped(t *testing.T) {
	t.Parallel()
	for _, noRedispatch := range []bool{false, true} {
		dir := stoppedFactory(t)
		var started []string
		var out strings.Builder
		o := &suspendOptions{dir: dir, session: "lead", noRedispatch: noRedispatch}
		err := resumeFactory(o, func(task string) error { started = append(started, task); return nil }, &out)
		if err != nil {
			t.Fatalf("resumeFactory(noRedispatch=%v): %v", noRedispatch, err)
		}
		evs, _ := flywheel.ReadEvents(dir)
		if s := flywheel.FactorySuspended(evs, time.Now()); s.Suspended {
			t.Errorf("noRedispatch=%v: still suspended %+v", noRedispatch, s)
		}
		delta, derr := os.ReadFile(filepath.Join(dir, ".flywheel", "briefs", "A.delta.txt"))
		if noRedispatch {
			if len(started) != 0 || derr == nil || out.String() != "factory resumed\n" {
				t.Errorf("--no-redispatch started %q, delta err %v, out %q; want nothing started", started, derr, out.String())
			}
			continue
		}
		if !reflect.DeepEqual(started, []string{"A"}) {
			t.Errorf("started = %q, want [A]", started)
		}
		if !strings.HasPrefix(string(delta), "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK: continue") || !strings.Contains(string(delta), "stopped by a factory suspension") {
			t.Errorf("A.delta.txt = %q, %v; want the header then the continue text", delta, derr)
		}
		if want := "factory resumed\nresumed A r1 (log .flywheel/runs/A.autoresume.log)\n"; out.String() != want {
			t.Errorf("out = %q, want %q", out.String(), want)
		}
	}
}

// TestBriefFlagsBindEveryOption parses non-default values for every brief
// flag, --gate repeated, and asserts the bound options hold them (issue #457).
func TestBriefFlagsBindEveryOption(t *testing.T) {
	t.Parallel()
	fs, o := briefFlags()
	args := []string{"--from-issue", "7", "--owns", "a,b", "--gate", "x", "--gate", "y", "--repo", "o/r",
		"--needs", "t1,t2", "--kind", "feature", "--force", "--no-plan", "--dir", "D"}
	if err := fs.Parse(args); err != nil {
		t.Fatalf("briefFlags: %v", err)
	}
	want := briefOptions{dir: "D", fromIssue: 7, repo: "o/r", owns: "a,b", needs: "t1,t2",
		gates: gateList{"x", "y"}, kind: "feature", force: true, noPlan: true}
	if !reflect.DeepEqual(*o, want) {
		t.Errorf("briefFlags parsed = %#v, want %#v", *o, want)
	}
	if got := splitCSV(o.owns); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("splitCSV(owns) = %v, want [a b]", got)
	}
}

// TestBriefFlagsUsageExits checks a missing --from-issue, a bad N and a
// missing --owns are usage errors (exit 2) before gh is run.
func TestBriefFlagsUsageExits(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"t1", "--owns", "a", "--dir", dir},
		{"t1", "--from-issue", "x", "--owns", "a", "--dir", dir},
		{"t1", "--from-issue", "0", "--owns", "a", "--dir", dir},
		{"t1", "--from-issue", "7", "--dir", dir},
	} {
		var out, errb strings.Builder
		if code := briefMain(args, &out, &errb); code != 2 {
			t.Errorf("briefMain(%v) = %d, want 2; stderr:\n%s", args, code, errb.String())
		}
	}
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
	if err := fs.Parse([]string{"--dir", "X", "--force", "--adapter", "codex"}); err != nil {
		t.Fatalf("initFlags: %v", err)
	}
	want := initOptions{dir: "X", force: true, adapter: "codex", localURL: "http://localhost:11434/v1"}
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
		"--width", "120", "--height", "40", "--now", "2026-01-02T15:04:05Z",
		"--keys", "j <enter>", "--frames", "--readonly"}
	fs, o := factoryFlags()
	if err := fs.Parse(args); err != nil {
		t.Fatalf("factoryFlags: %v", err)
	}
	want := factoryOptions{dir: "X", once: true, asJSON: true,
		interval: 5 * time.Second, width: 120, height: 40, now: "2026-01-02T15:04:05Z",
		keys: "j <enter>", frames: true, readonly: true}
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

// TestRunBaseFlag parses run's --base (issue #456) next to --worktree, in
// either order around the task id, and checks the usage line names it.
func TestRunBaseFlag(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"T1", "--worktree", "--base", "main2"},
		{"--base", "main2", "--worktree", "T1"},
	} {
		fs, o := runFlags()
		pos, err := parseArgs(fs, args)
		if err != nil {
			t.Fatalf("runFlags parseArgs(%v): %v", args, err)
		}
		if len(pos) != 1 || pos[0] != "T1" || !o.worktree || o.base != "main2" {
			t.Errorf("parseArgs(%v) = %v, worktree=%v base=%q", args, pos, o.worktree, o.base)
		}
	}
	if fs, o := runFlags(); fs.Parse(nil) != nil || o.base != "" {
		t.Errorf("default --base = %q, want empty", o.base)
	}
	var b strings.Builder
	runUsage(&b)
	if !strings.Contains(b.String(), "[--base REF]") {
		t.Errorf("run usage lacks --base: %s", b.String())
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

// healthAt is the instant the status --health tests read at.
var healthAt = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// healthDir is a factory whose ledger holds one health event age before
// healthAt.
func healthDir(t *testing.T, age time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	if err := flywheel.AppendEvent(dir, flywheel.Event{TS: healthAt.Add(-age).Format(time.RFC3339Nano), Kind: "health",
		Health: &flywheel.HealthSnapshot{Running: 2, Stalled: 1, RateLimited: 1, Andon: 3, OldestInFlight: "T3 42m",
			PausedModels:         []flywheel.PausedModel{{Model: "m", ResetAt: "2026-09-25T13:00:00Z"}},
			ControllerGeneration: 4, Version: "v1.2.3"}}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	return dir
}

// runStatusHealth parses args as status flags and runs statusHealth at
// healthAt, returning its output and exit code.
func runStatusHealth(t *testing.T, args ...string) (string, int) {
	t.Helper()
	fs, o := statusFlags()
	if _, err := parseArgs(fs, args); err != nil {
		t.Fatalf("parse %q: %v", args, err)
	}
	var buf strings.Builder
	code := statusHealth(o, healthAt, &buf)
	return buf.String(), code
}

// TestStatusHealthPrintsLatest: status --health prints the latest health
// event as one line and exits 0; --json carries the snapshot, ts and age.
func TestStatusHealthPrintsLatest(t *testing.T) {
	t.Parallel()
	dir := healthDir(t, 3*time.Minute)
	out, code := runStatusHealth(t, "--dir", dir, "--health")
	paused := time.Date(2026, 9, 25, 13, 0, 0, 0, time.UTC).Local().Format("15:04")
	want := "health 3m ago: running 2, stalled 1, rate-limited 1 (paused: m until " + paused +
		"), andon 3, oldest T3 42m, controller gen 4, flywheel v1.2.3\n"
	if code != 0 || out != want {
		t.Errorf("status --health = %q (exit %d), want %q (exit 0)", out, code, want)
	}
	out, code = runStatusHealth(t, "--dir", dir, "--health", "--json")
	var rep flywheel.HealthReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil || code != 0 || rep.Age != 180 || rep.Stale ||
		rep.Health == nil || rep.Health.ControllerGeneration != 4 || rep.TS == "" {
		t.Errorf("status --health --json = %q (exit %d, err %v), want the snapshot aged 180s", out, code, err)
	}
}

// TestStatusHealthNoneRecorded: an empty log prints "health: none recorded".
func TestStatusHealthNoneRecorded(t *testing.T) {
	t.Parallel()
	out, code := runStatusHealth(t, "--dir", t.TempDir(), "--health")
	if code != 0 || out != "health: none recorded\n" {
		t.Errorf("status --health = %q (exit %d), want none recorded (exit 0)", out, code)
	}
}

// TestHealthStaleIsAndon: a health record 20m old with --stale-after 10m
// prints the STALE line and exits 1.
func TestHealthStaleIsAndon(t *testing.T) {
	t.Parallel()
	dir := healthDir(t, 20*time.Minute)
	out, code := runStatusHealth(t, "--dir", dir, "--health", "--stale-after", "10m")
	want := "health STALE (20m): the controller is not recording; run flywheel controller\n"
	if code != 1 || out != want {
		t.Errorf("status --health = %q (exit %d), want %q (exit 1)", out, code, want)
	}
}

// TestShipFlagsBindEveryOption parses every local ship flag, the task before
// or after them, checks the defaults, the help's exit codes, and a missing
// task is a usage error (issue #457).
func TestShipFlagsBindEveryOption(t *testing.T) {
	t.Parallel()
	if _, o := shipFlags(); !reflect.DeepEqual(*o, shipOptions{dir: ".", remote: "origin", ciTimeout: 45 * time.Minute, poll: 30 * time.Second}) {
		t.Errorf("shipFlags defaults = %#v", *o)
	}
	for _, args := range [][]string{
		{"T1", "--integration", "main2", "--workdir", "W", "--remote", "up", "--message", "m", "--dir", "D"},
		{"--integration", "main2", "--workdir", "W", "--remote", "up", "--message", "m", "--dir", "D", "T1"},
	} {
		fs, o := shipFlags()
		pos, err := parseArgs(fs, args)
		want := shipOptions{dir: "D", integration: "main2", workdir: "W", remote: "up", message: "m", ciTimeout: 45 * time.Minute, poll: 30 * time.Second}
		if err != nil || len(pos) != 1 || pos[0] != "T1" || !reflect.DeepEqual(*o, want) {
			t.Errorf("parseArgs(%v) = %v, %#v, %v; want [T1], %#v", args, pos, *o, err, want)
		}
	}
	var b strings.Builder
	shipUsage(&b)
	if !strings.Contains(b.String(), "--integration BRANCH") || !strings.Contains(b.String(), "5 gates or CI failed") || strings.Contains(b.String(), "not in this version yet") {
		t.Errorf("ship usage = %q", b.String())
	}
	var out, errb strings.Builder
	if code := shipMain([]string{"--dir", t.TempDir()}, &out, &errb); code != 2 {
		t.Errorf("shipMain without a task = %d, want 2", code)
	}
}

// TestShipRemoteFlags binds every remote-half ship flag, --ignore-check
// repeated, and the help names them; a missing --body-file is a usage error.
func TestShipRemoteFlags(t *testing.T) {
	t.Parallel()
	fs, o := shipFlags()
	pos, err := parseArgs(fs, []string{"T1", "--title", "Ti", "--body-file", "B.md", "--no-merge", "--ci-timeout", "10m",
		"--poll", "5s", "--ignore-check", "lint", "--ignore-check", "docs", "--repo", "o/r"})
	want := shipOptions{dir: ".", remote: "origin", title: "Ti", bodyFile: "B.md", noMerge: true, ciTimeout: 10 * time.Minute,
		poll: 5 * time.Second, ignoreChecks: repeatable{"lint", "docs"}, repo: "o/r"}
	if err != nil || len(pos) != 1 || pos[0] != "T1" || !reflect.DeepEqual(*o, want) {
		t.Errorf("parseArgs = %v, %#v, %v; want [T1], %#v", pos, *o, err, want)
	}
	for _, f := range []string{"--title", "--body-file", "--no-merge", "--ci-timeout", "--poll", "--ignore-check", "--repo"} {
		if !strings.Contains(shipUsageLine, f) {
			t.Errorf("ship usage lacks %s: %q", f, shipUsageLine)
		}
	}
	var out, errb strings.Builder
	missing := filepath.Join(t.TempDir(), "none.md")
	if code := shipMain([]string{"T1", "--body-file", missing, "--dir", t.TempDir()}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "--body-file") {
		t.Errorf("shipMain with a missing --body-file = %d, %q; want 2", code, errb.String())
	}
}

// TestFleetLearningsFlagsBind: fleet learnings binds --pending, --all,
// --json, --sync (default true), --done and --done-all (issue #585).
func TestFleetLearningsFlagsBind(t *testing.T) {
	t.Parallel()
	fs, o := fleetFlags("learnings")
	if !o.sync {
		t.Error("fleet learnings --sync defaults to false, want true")
	}
	if err := fs.Parse([]string{"--pending", "--all", "--json", "--sync=false", "--done", "K", "--done-all"}); err != nil ||
		!o.pending || !o.all || !o.json || o.sync || o.done != "K" || !o.doneAll {
		t.Errorf("fleet learnings flags: parsed %#v, err %v", *o, err)
	}
	if fs, _ := fleetFlags("status"); fs.Lookup("done") != nil || fs.Lookup("sync") != nil {
		t.Error("fleet status defines learnings flags")
	}
}

// TestFleetLearningsCLI drives fleet learnings against a temp registry: the
// first sync says so and lists only the recent learning, --json parses,
// --all --json lists both, --done drains by title prefix, an unmatched
// --done exits 1 and conflicting flags are usage.
func TestFleetLearningsCLI(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	root := filepath.Join(t.TempDir(), "a")
	if err := os.MkdirAll(filepath.Join(root, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for title, age := range map[string]time.Duration{"old": 400 * time.Hour, "recent": time.Hour} {
		b, _ := json.Marshal(flywheel.Event{TS: now.Add(-age).Format(time.RFC3339Nano), Task: "t1", Kind: "learning", Severity: "P1", Title: title, Observed: "o"})
		lines = append(append(lines, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(root, ".flywheel", "events.jsonl"), lines, 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "fleet.json")
	if err := flywheel.SaveFleet(file, flywheel.Fleet{Roots: []flywheel.FleetRoot{{Name: "a", Path: root}}}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string, string) {
		var out, errb strings.Builder
		code := fleetMain(append([]string{"learnings"}, args...), &out, &errb, file, now)
		return code, out.String(), errb.String()
	}
	code, out, errs := run()
	if head := strings.Join(strings.Fields(strings.SplitN(out, "\n", 2)[0]), " "); code != 0 || head != "KEY SEVERITY AGE ROOT TASK TITLE" ||
		!strings.Contains(out, "recent") || strings.Contains(out, "old") || !strings.Contains(errs, "first sync") {
		t.Errorf("fleet learnings = %d, want the header, recent only and a first-sync note\n%s%s", code, out, errs)
	}
	var ls []flywheel.FleetLearning
	if code, out, _ := run("--json"); code != 0 || json.Unmarshal([]byte(out), &ls) != nil || len(ls) != 1 || ls[0].Title != "recent" {
		t.Errorf("fleet learnings --json = %d, %d learnings\n%s", code, len(ls), out)
	}
	if code, out, _ := run("--all", "--json"); code != 0 || json.Unmarshal([]byte(out), &ls) != nil || len(ls) != 2 {
		t.Errorf("fleet learnings --all --json = %d, %d learnings\n%s", code, len(ls), out)
	}
	if code, out, errs := run("--done", "REC"); code != 0 || out != "done 1\n" {
		t.Errorf("fleet learnings --done REC = %d %q %s", code, out, errs)
	}
	if code, _, _ := run("--done", "nope"); code != 1 {
		t.Errorf("fleet learnings --done nope = %d, want 1", code)
	}
	for _, args := range [][]string{{"--done", "x", "--done-all"}, {"--all", "--pending"}, {"extra"}} {
		if code, _, errs := run(args...); code != 2 {
			t.Errorf("fleet learnings %v = %d, want 2 (usage)\n%s", args, code, errs)
		}
	}
	if code, out, _ := run("--json"); code != 0 || strings.TrimSpace(out) != "[]" {
		t.Errorf("fleet learnings --json after done = %d %q, want []", code, out)
	}
}

// TestFleetWatchCLI drives learnings --since and --import-seen and watch
// --once against temp registries holding one root with a 1h and a 3h old
// learning: --since 2h queues only the 1h one, --import-seen reports the
// titles it matched, and watch prints each new learning once, then nothing.
func TestFleetWatchCLI(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	root := filepath.Join(t.TempDir(), "a")
	if err := os.MkdirAll(filepath.Join(root, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for _, l := range []struct {
		title string
		age   time.Duration
	}{{"older", 3 * time.Hour}, {"newer", time.Hour}} {
		b, _ := json.Marshal(flywheel.Event{TS: now.Add(-l.age).Format(time.RFC3339Nano), Task: "t1", Kind: "learning", Severity: "P1", Title: l.title, Observed: "o"})
		lines = append(append(lines, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(root, ".flywheel", "events.jsonl"), lines, 0o644); err != nil {
		t.Fatal(err)
	}
	registry := func() string {
		file := filepath.Join(t.TempDir(), "fleet.json")
		if err := flywheel.SaveFleet(file, flywheel.Fleet{Roots: []flywheel.FleetRoot{{Name: "a", Path: root}}}); err != nil {
			t.Fatal(err)
		}
		return file
	}
	run := func(file string, args ...string) (int, string, string) {
		var out, errb strings.Builder
		code := fleetMain(args, &out, &errb, file, now)
		return code, out.String(), errb.String()
	}
	var ls []flywheel.FleetLearning
	if code, out, errs := run(registry(), "learnings", "--since", "2h", "--json"); code != 0 || json.Unmarshal([]byte(out), &ls) != nil ||
		len(ls) != 1 || ls[0].Title != "newer" || !strings.Contains(errs, "newer than 2h0m0s") {
		t.Errorf("learnings --since 2h = %d %s %s; want only newer", code, out, errs)
	}
	seen := filepath.Join(t.TempDir(), "seen.json")
	if err := os.WriteFile(seen, []byte(`[{"title":"newer"},"gone"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	file := registry()
	if code, out, errs := run(file, "learnings", "--import-seen", seen); code != 0 || !strings.HasPrefix(out, "imported: 1 ") {
		t.Errorf("learnings --import-seen = %d %q %s; want 1 matched", code, out, errs)
	}
	if code, out, _ := run(file, "learnings", "--json"); code != 0 || !strings.Contains(out, `"older"`) || strings.Contains(out, `"newer"`) {
		t.Errorf("pending after import = %d %s; want older only", code, out)
	}
	watch := registry()
	if code, out, errs := run(watch, "watch", "--once"); code != 0 ||
		out != "baseline recorded: 1 ledgers, 0 andons, 2 pending learnings; changes from now on are reported\n" {
		t.Errorf("first watch --once = %d %q %s; want only the baseline line", code, out, errs)
	}
	if code, out, _ := run(watch, "watch", "--once"); code != 0 || out != "" {
		t.Errorf("second watch --once = %d %q, want nothing new", code, out)
	}
	code, out, errs := run(watch, "watch", "--once", "--report-all")
	if got := strings.Split(strings.TrimSpace(out), "\n"); code != 0 || len(got) != 2 ||
		!strings.HasPrefix(got[0], now.Format(time.RFC3339)+" a learning: [P1] older") || !strings.Contains(got[1], " a learning: [P1] newer") {
		t.Errorf("watch --report-all = %d %q %s; want the two pending learnings, oldest first", code, out, errs)
	}
	for _, args := range [][]string{{"watch", "--interval", "0s"}, {"learnings", "--since", "0s"}, {"learnings", "--import-seen", seen, "--done-all"}} {
		if code, _, errs := run(watch, args...); code != 2 {
			t.Errorf("fleet %v = %d, want 2 (usage)\n%s", args, code, errs)
		}
	}
}

// TestFleetWatchBaselineNoFlood: a first watch over a ledger that already
// holds an old andon and a learning prints only the baseline line and
// notifies nothing; after one new andon the next watch prints and notifies
// exactly that item; --report-all prints both andons and notifies nothing.
func TestFleetWatchBaselineNoFlood(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	root := filepath.Join(t.TempDir(), "a")
	if err := os.MkdirAll(filepath.Join(root, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	ts := now.Add(-time.Hour).Format(time.RFC3339Nano)
	appendEvents := func(events ...flywheel.Event) {
		t.Helper()
		fh, err := os.OpenFile(filepath.Join(root, ".flywheel", "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer fh.Close()
		for _, e := range events {
			b, _ := json.Marshal(e)
			fh.Write(append(b, '\n'))
		}
	}
	appendEvents(flywheel.Event{TS: ts, Task: "t1", Kind: "learning", Severity: "P1", Title: "old", Observed: "o"},
		flywheel.Event{TS: ts, Task: "t1", Kind: "dispatched", Attempt: "r1"},
		flywheel.Event{TS: ts, Task: "t1", Kind: "finished", Attempt: "r1", Reason: "silent"})
	file := filepath.Join(t.TempDir(), "fleet.json")
	if err := flywheel.SaveFleet(file, flywheel.Fleet{Roots: []flywheel.FleetRoot{{Name: "a", Path: root}}}); err != nil {
		t.Fatal(err)
	}
	var notified []string
	notify := func(_, env []string) error { notified = append(notified, strings.Join(env, ",")); return nil }
	look := func(reportAll bool) string {
		var out, errb strings.Builder
		if err := fleetWatchOnce(&out, &errb, file, "notify-me", reportAll, now, notify); err != nil {
			t.Fatalf("watch: %v (%s)", err, errb.String())
		}
		return out.String()
	}
	if out := look(false); out != "baseline recorded: 1 ledgers, 1 andons, 1 pending learnings; changes from now on are reported\n" || len(notified) != 0 {
		t.Fatalf("first watch = %q, notified %q; want only the baseline line, no notify", out, notified)
	}
	appendEvents(flywheel.Event{TS: ts, Task: "t2", Kind: "dispatched", Attempt: "r1"},
		flywheel.Event{TS: ts, Task: "t2", Kind: "finished", Attempt: "r1", Reason: "error"})
	want := now.Format(time.RFC3339) + " a andon: t2 finished: error"
	if out := look(false); out != want+"\n" || !slices.Equal(notified, []string{"FLYWHEEL_FLEET_ITEM=" + want}) {
		t.Errorf("second watch = %q, notified %q; want exactly the new andon, notified once", out, notified)
	}
	out := look(true)
	if !strings.Contains(out, "a andon: t1 finished: silent\n") || !strings.Contains(out, "a andon: t2 finished: error\n") || len(notified) != 1 {
		t.Errorf("--report-all = %q, notified %d; want both andons, no notify", out, len(notified))
	}
}
