package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fleetGit runs git in dir for a fleet test, failing t on error.
func fleetGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com", "-C", dir}
	if out, err := exec.Command("git", append(base, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fleetRepo makes a git repository with one commit under a new temp dir.
func fleetRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "main")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fleetGit(t, dir, "init", "-q")
	fleetGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// fleetLedger initialises a flywheel ledger in dir.
func fleetLedger(t *testing.T, dir string) {
	t.Helper()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init %s: %v", dir, err)
	}
}

// TestFleetRegistryRoundTrip: add, save, load, list and remove round-trip;
// the same path twice is refused naming the entry; default names are made
// unique; a path with no .flywheel/ that is not a git repository is refused.
func TestFleetRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "cfg", "fleet.json")
	f, err := LoadFleet(file)
	if err != nil || len(f.Roots) != 0 {
		t.Fatalf("LoadFleet(missing) = %+v, %v; want empty", f, err)
	}
	a := fleetRepo(t)
	b := fleetRepo(t) // same base name "main"
	ra, err := f.AddRoot(a, "")
	if err != nil || ra.Name != "main" {
		t.Fatalf("AddRoot a = %+v, %v; want name main", ra, err)
	}
	rb, err := f.AddRoot(b, "")
	if err != nil || rb.Name != "main-2" {
		t.Fatalf("AddRoot b = %+v, %v; want name main-2", rb, err)
	}
	if _, err := f.AddRoot(a+string(filepath.Separator), "x"); err == nil || !strings.Contains(err.Error(), `"main"`) {
		t.Errorf("AddRoot same path = %v, want a refusal naming main", err)
	}
	plain := t.TempDir()
	if _, err := f.AddRoot(plain, ""); err == nil {
		t.Errorf("AddRoot non-repo %s succeeded, want a refusal", plain)
	}
	fleetLedger(t, plain)
	if _, err := f.AddRoot(plain, "led"); err != nil {
		t.Errorf("AddRoot ledger-only dir: %v", err)
	}
	if err := SaveFleet(file, f); err != nil {
		t.Fatal(err)
	}
	g, err := LoadFleet(file)
	if err != nil || len(g.Roots) != 3 || g.Roots[1] != rb || g.Version != FleetVersion {
		t.Fatalf("LoadFleet = %+v, %v; want the 3 saved roots", g, err)
	}
	if err := g.RemoveRoot("main"); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveRoot("main"); err == nil {
		t.Error("RemoveRoot twice succeeded, want an error")
	}
	if len(g.Roots) != 2 || g.Roots[0].Name != "main-2" {
		t.Errorf("after remove roots = %+v", g.Roots)
	}
}

// fleetAppend appends raw events to dir's events.jsonl.
func fleetAppend(t *testing.T, dir string, events ...Event) {
	t.Helper()
	fh, err := os.OpenFile(filepath.Join(dir, ".flywheel", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, e := range events {
		b, _ := json.Marshal(e)
		if _, err := fh.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFleetDiscovery: discovery finds the root, a sibling git worktree, a
// .flywheel/worktrees unit and a .claude/worktrees ledger; skips a worktree
// without a ledger; keeps one entry for a path reached both as a git
// worktree and under .flywheel/worktrees; and reports a missing root.
func TestFleetDiscovery(t *testing.T) {
	t.Parallel()
	root := fleetRepo(t)
	fleetLedger(t, root)
	sib := filepath.Join(filepath.Dir(root), "sib")
	fleetGit(t, root, "worktree", "add", "-q", "--detach", sib)
	fleetLedger(t, sib)
	fleetGit(t, root, "worktree", "add", "-q", "--detach", filepath.Join(filepath.Dir(root), "bare"))
	unit := filepath.Join(root, ".flywheel", "worktrees", "u1")
	fleetGit(t, root, "worktree", "add", "-q", "--detach", unit) // reached twice
	fleetLedger(t, unit)
	claude := filepath.Join(root, ".claude", "worktrees", "c1")
	fleetLedger(t, claude)
	if err := os.MkdirAll(filepath.Join(root, ".claude", "worktrees", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	f := Fleet{Roots: []FleetRoot{{Name: "main", Path: root}, {Name: "gone", Path: gone}}}
	got := FleetLedgers(f)
	var kinds []string
	for _, l := range got {
		kinds = append(kinds, l.Name+"="+l.Kind+"|"+l.Error)
	}
	want := []string{"main=root|", "main/c1=claude-worktree|", "main/u1=git-worktree|", "main/sib=git-worktree|", "gone=root|root missing"}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Errorf("FleetLedgers =\n %v\nwant\n %v", kinds, want)
	}
}

// TestFleetStatus: a suspended ledger shows SUSPENDED, a paused model shows
// "paused: m1" with its andon counted, the latest event and health ages are
// set, and a ledger that fails to read carries an error without failing the
// others.
func TestFleetStatus(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	base := t.TempDir()
	mk := func(name string) string {
		d := filepath.Join(base, name)
		fleetLedger(t, d)
		return d
	}
	sus, paused, bad := mk("sus"), mk("paused"), mk("bad")
	if err := Suspend(sus, "lead", "freeze", time.Time{}); err != nil {
		t.Fatal(err)
	}
	ts := now.Add(-time.Minute).Format(time.RFC3339Nano)
	fleetAppend(t, paused, Event{TS: ts, Kind: "finished", Task: "T1", Model: "m1", Reason: "rate-limited",
		ResetAt: now.Add(time.Hour).Format(time.RFC3339)})
	if err := os.WriteFile(filepath.Join(bad, ".flywheel", shardDirName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := Fleet{Roots: []FleetRoot{{Name: "sus", Path: sus}, {Name: "paused", Path: paused}, {Name: "bad", Path: bad}}}
	rows := FleetStatus(f, now)
	if len(rows) != 3 {
		t.Fatalf("FleetStatus = %d rows, want 3: %+v", len(rows), rows)
	}
	if r := rows[0]; r.State() != "SUSPENDED" || r.Error != "" || r.LastAge == nil {
		t.Errorf("sus row = %+v state %q, want SUSPENDED with a last age", r, r.State())
	}
	r := rows[1]
	rep, err := Status(paused, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.State() != "paused: m1" || r.Andon != rep.Andon || r.Andon == 0 || r.LastAge == nil || *r.LastAge != 60 {
		t.Errorf("paused row = %+v state %q, want paused: m1 with andon %d", r, r.State(), rep.Andon)
	}
	if r.HealthAge != nil {
		t.Errorf("paused row health age = %d, want none recorded", *r.HealthAge)
	}
	if rows[2].Error == "" || rows[2].Name != "bad" {
		t.Errorf("bad row = %+v, want a read error", rows[2])
	}
}

// fleetRaw makes a bare ledger in dir (no Init, no git, no config) holding
// events.
func fleetRaw(t *testing.T, dir string, events ...Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fleetAppend(t, dir, events...)
}

// TestFleetSummaryCounts: ledgerSummary on a bare ledger outside any git
// repository counts the stages, the event-derived andon (suspension, paused
// model, stale health, two unclean finishes), the freeze, the paused model
// and the health and last-event ages.
func TestFleetSummaryCounts(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339Nano) }
	dir := filepath.Join(t.TempDir(), "plain")
	var events []Event
	for _, task := range []string{"t-run", "t-cap", "t-ok", "t-rl", "t-pass", "t-plan"} {
		events = append(events, Event{TS: at(time.Hour), Task: task, Kind: "planned", Brief: "b.txt"})
		if task != "t-plan" {
			events = append(events, Event{TS: at(time.Hour), Task: task, Kind: "dispatched", Attempt: "r1"},
				Event{TS: at(time.Hour), Task: task, Kind: "started"})
		}
	}
	events = append(events,
		Event{TS: at(50 * time.Minute), Task: "t-cap", Kind: "finished", Attempt: "r1", Reason: "length"},
		Event{TS: at(50 * time.Minute), Task: "t-ok", Kind: "finished", Attempt: "r1", Reason: "stop"},
		Event{TS: at(50 * time.Minute), Task: "t-rl", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "rate-limited",
			ResetAt: now.Add(time.Hour).Format(time.RFC3339)},
		Event{TS: at(50 * time.Minute), Task: "t-pass", Kind: "finished", Attempt: "r1", Reason: "stop"},
		Event{TS: at(45 * time.Minute), Task: "t-pass", Kind: "inspected", Attempt: "r1", Verdict: "pass"},
		Event{TS: at(30 * time.Minute), Kind: "health", Health: &HealthSnapshot{StaleAfter: "10m"}},
		Event{TS: at(2 * time.Minute), Kind: "suspended", Session: "lead", Note: "freeze"},
	)
	fleetRaw(t, dir, events...)
	if exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil {
		t.Skipf("%s is inside a git repository; the no-git premise does not hold", dir)
	}
	row, err := ledgerSummary(dir, now)
	if err != nil {
		t.Fatalf("ledgerSummary: %v", err)
	}
	want := StatusTasks{Total: 6, Planned: 1, Running: 1, Finished: 3, Passed: 1}
	if row.Tasks != want {
		t.Errorf("tasks = %+v, want %+v", row.Tasks, want)
	}
	if row.Andon != 5 {
		t.Errorf("andon = %d, want 5 (suspended, model/m1, stale health, t-cap, t-rl)", row.Andon)
	}
	if !row.Suspended || strings.Join(row.Paused, ",") != "m1" {
		t.Errorf("suspended/paused = %v/%v, want true/[m1]", row.Suspended, row.Paused)
	}
	if row.HealthAge == nil || *row.HealthAge != 1800 || row.LastAge == nil || *row.LastAge != 120 {
		t.Errorf("health/last age = %v/%v, want 1800/120", row.HealthAge, row.LastAge)
	}
}

// fleetIdleFixture makes two roots, each a bare ledger with three ledgers
// under .flywheel/worktrees: one active (1h) and two idle (100h, 200h).
// Root b's own latest event is 300h old. It returns the fleet.
func fleetIdleFixture(t *testing.T, now time.Time) Fleet {
	t.Helper()
	var f Fleet
	for _, name := range []string{"a", "b"} {
		root := filepath.Join(t.TempDir(), name)
		rootAge := time.Hour
		if name == "b" {
			rootAge = 300 * time.Hour
		}
		fleetRaw(t, root, Event{TS: now.Add(-rootAge).Format(time.RFC3339Nano), Kind: "staffed", Persona: "lead", Session: "s"})
		for unit, age := range map[string]time.Duration{"u-active": time.Hour, "u-idle1": 100 * time.Hour, "u-idle2": 200 * time.Hour} {
			fleetRaw(t, filepath.Join(root, ".flywheel", "worktrees", unit),
				Event{TS: now.Add(-age).Format(time.RFC3339Nano), Kind: "staffed", Persona: "lead", Session: "s"})
		}
		f.Roots = append(f.Roots, FleetRoot{Name: name, Path: root})
	}
	return f
}

// TestFleetIdleFold: FoldIdle keeps both roots (b's own ledger is idle) and
// the active worktree ledgers, and folds each root's idle ones into one row
// with the oldest age; the unfolded rows list every ledger.
func TestFleetIdleFold(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	all := FleetStatus(fleetIdleFixture(t, now), now)
	names := func(rows []FleetRow) string {
		var s []string
		for _, r := range rows {
			s = append(s, r.Name+"="+r.Kind)
		}
		return strings.Join(s, " ")
	}
	wantAll := "a=root a/u-active=flywheel-worktree a/u-idle1=flywheel-worktree a/u-idle2=flywheel-worktree " +
		"b=root b/u-active=flywheel-worktree b/u-idle1=flywheel-worktree b/u-idle2=flywheel-worktree"
	if got := names(all); got != wantAll {
		t.Fatalf("FleetStatus =\n %s\nwant\n %s", got, wantAll)
	}
	folded := FoldIdle(all, DefaultIdleAfter)
	want := "a=root a/u-active=flywheel-worktree +2 idle worktree ledgers=idle " +
		"b=root b/u-active=flywheel-worktree +2 idle worktree ledgers=idle"
	if got := names(folded); got != want {
		t.Fatalf("FoldIdle =\n %s\nwant\n %s", got, want)
	}
	if r := folded[2]; r.Root != "a" || r.Idle != 2 || r.LastAge == nil || *r.LastAge != 200*3600 {
		t.Errorf("fold row = %+v, want root a, 2 idle, oldest 200h", r)
	}
	if got := names(FoldIdle(all, 150*time.Hour)); !strings.Contains(got, "u-idle1") || strings.Contains(got, "u-idle2") {
		t.Errorf("FoldIdle(150h) = %s, want u-idle1 listed and u-idle2 folded", got)
	}
}

// TestFleetStatusBudget: 150 worktree ledgers of 1,000 events each are
// summarised within a 60s hang guard, every row present, without error and
// in discovery order.
func TestFleetStatusBudget(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	ts := now.Add(-time.Hour).Format(time.RFC3339Nano)
	var log []byte
	for i := range 250 {
		task := fmt.Sprintf("t%03d", i)
		for _, e := range []Event{
			{TS: ts, Task: task, Kind: "planned", Brief: "b.txt"},
			{TS: ts, Task: task, Kind: "dispatched", Attempt: "r1"},
			{TS: ts, Task: task, Kind: "started"},
			{TS: ts, Task: task, Kind: "finished", Attempt: "r1", Reason: "stop"},
		} {
			b, _ := json.Marshal(e)
			log = append(append(log, b...), '\n')
		}
	}
	root := filepath.Join(t.TempDir(), "big")
	fleetRaw(t, root)
	for i := range 150 {
		unit := filepath.Join(root, ".flywheel", "worktrees", fmt.Sprintf("u%03d", 149-i))
		fleetRaw(t, unit)
		if err := os.WriteFile(filepath.Join(unit, ".flywheel", "events.jsonl"), log, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := Fleet{Roots: []FleetRoot{{Name: "big", Path: root}}}
	done := make(chan []FleetRow, 1)
	go func() { done <- FleetStatus(f, now) }()
	var rows []FleetRow
	select {
	case rows = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("FleetStatus over 151 ledgers did not finish in 60s")
	}
	ledgers := FleetLedgers(f)
	if len(rows) != 151 || len(ledgers) != 151 {
		t.Fatalf("FleetStatus = %d rows over %d ledgers, want 151", len(rows), len(ledgers))
	}
	for i, r := range rows {
		if r.FleetLedger != ledgers[i] {
			t.Fatalf("row %d = %s, want %s (discovery order)", i, r.Name, ledgers[i].Name)
		}
		if i > 0 && (r.Error != "" || r.Tasks.Finished != 250) {
			t.Errorf("row %s = error %q, %d finished; want 250", r.Name, r.Error, r.Tasks.Finished)
		}
	}
}
