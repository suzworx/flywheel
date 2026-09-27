package flywheel

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// watchItems renders items as "ledger kind: text" joined by " | ".
func watchItems(items []WatchItem) string {
	var s []string
	for _, it := range items {
		s = append(s, it.Ledger+" "+it.Kind+": "+it.Text)
	}
	return strings.Join(s, " | ")
}

// TestFleetWatchReportsEachChangeOnce: from a quiet baseline, a new learning,
// a new andon and a suspension in root a, and a paused model and stale health
// in root b, each give exactly one item, in order; a second call with the
// returned state gives none; a thaw gives one state change; the state
// survives a save and reload.
func TestFleetWatchReportsEachChangeOnce(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339Nano) }
	base := t.TempDir()
	a, b := filepath.Join(base, "a"), filepath.Join(base, "b")
	fleetRaw(t, a, Event{TS: at(time.Hour), Task: "t-cap", Kind: "planned", Brief: "b.txt"})
	fleetRaw(t, b, Event{TS: at(time.Hour), Task: "t-ok", Kind: "planned", Brief: "b.txt"})
	f := Fleet{Roots: []FleetRoot{{Name: "a", Path: a}, {Name: "b", Path: b}}}
	stateFile := WatchStatePath(filepath.Join(t.TempDir(), "fleet.json"))
	prev, err := LoadWatchState(stateFile)
	if err != nil || !prev.First {
		t.Fatalf("missing state = %+v, %v; want First set", prev, err)
	}
	items, next := FleetWatch(f, prev, now)
	if len(items) != 0 {
		t.Fatalf("baseline = %q, want nothing", watchItems(items))
	}
	fleetAppend(t, a,
		learnEvent(now, 5*time.Minute, "new"),
		Event{TS: at(40 * time.Minute), Task: "t-cap", Kind: "dispatched", Attempt: "r1"},
		Event{TS: at(30 * time.Minute), Task: "t-cap", Kind: "finished", Attempt: "r1", Reason: "length"},
		Event{TS: at(2 * time.Minute), Kind: "suspended", Session: "lead", Note: "freeze"})
	fleetAppend(t, b,
		Event{TS: at(time.Minute), Kind: "finished", Task: "T1", Model: "m1", Reason: "rate-limited", ResetAt: now.Add(time.Hour).Format(time.RFC3339)},
		Event{TS: at(30 * time.Minute), Kind: "health", Health: &HealthSnapshot{StaleAfter: "10m"}})
	items, next = FleetWatch(f, next, now)
	want := "a learning: [P2] new (" + LearningKey("new", "saw new")[:12] + ") | a state: running -> SUSPENDED | " +
		"a andon: t-cap finished: length | b state: running -> paused: m1 | b stale: health older than 10m0s"
	if got := watchItems(items); got != want {
		t.Fatalf("items =\n%s\nwant\n%s", got, want)
	}
	if again, _ := FleetWatch(f, next, now); len(again) != 0 {
		t.Errorf("second call = %q, want nothing", watchItems(again))
	}
	if err := SaveWatchState(stateFile, next); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadWatchState(stateFile)
	if err != nil || !reflect.DeepEqual(loaded, next) {
		t.Fatalf("reloaded state = %+v, %v; want %+v", loaded, err, next)
	}
	fleetAppend(t, a, Event{TS: at(0), Kind: "unsuspended", Session: "lead", Note: "thaw"})
	if items, _ := FleetWatch(f, loaded, now); watchItems(items) != "a state: SUSPENDED -> running" {
		t.Errorf("after thaw = %q, want one state change back to running", watchItems(items))
	}
}

// TestFleetWatchNotifyOncePerItem: notify runs the command once per line
// through the shell chooser with FLYWHEEL_FLEET_ITEM set to the line, and a
// failing run is returned without stopping the rest.
func TestFleetWatchNotifyOncePerItem(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	lines := []string{
		WatchLine(WatchItem{Kind: "andon", Ledger: "a", Text: "t1 blocked"}, now),
		WatchLine(WatchItem{Kind: "state", Ledger: "b", Text: "running -> SUSPENDED"}, now),
	}
	if lines[0] != "2026-09-27T10:00:00Z a andon: t1 blocked" {
		t.Errorf("line = %q", lines[0])
	}
	var got []string
	run := func(argv, env []string) error {
		if !slices.Equal(argv, ShellArgv("notify-me")) {
			t.Errorf("argv = %q, want ShellArgv(notify-me)", argv)
		}
		got = append(got, strings.Join(env, ","))
		if len(got) == 1 {
			return errors.New("boom")
		}
		return nil
	}
	errs := NotifyWatch(lines, "notify-me", run)
	if want := []string{"FLYWHEEL_FLEET_ITEM=" + lines[0], "FLYWHEEL_FLEET_ITEM=" + lines[1]}; !slices.Equal(got, want) {
		t.Errorf("runs = %q, want %q", got, want)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "boom") {
		t.Errorf("errs = %v, want the one failure", errs)
	}
}
