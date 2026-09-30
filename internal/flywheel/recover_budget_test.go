package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// budgetLedger writes a synthetic ledger of units units (issue #628) in one
// AppendEvents: 60% landed, 25% finished with stop, 10% passed, 5% in
// flight, each settled unit with 33 gate readings. Landed units record plain
// workdirs nobody else uses, so a world read for one shows as an extra tree
// hash. It returns the dir, the event count and the distinct workdirs of the
// finished-with-stop units that have a reading after their finish.
func budgetLedger(t *testing.T, units int) (dir string, events int, readWorkdirs int) {
	t.Helper()
	dir = newRepo(t)
	extra := []string{newRepo(t), newRepo(t)} // two worktrees some finished units share
	var landedWDs []string
	for i := range 3 {
		wd := filepath.Join(t.TempDir(), fmt.Sprintf("landed-wd-%d", i))
		if err := os.MkdirAll(wd, 0o755); err != nil {
			t.Fatal(err)
		}
		landedWDs = append(landedWDs, wd)
	}
	rc0 := 0
	var evs []Event
	add := func(e Event) {
		e.TS = recoverNow.Add(-72*time.Hour + time.Duration(len(evs))*time.Second).Format(time.RFC3339)
		evs = append(evs, e)
	}
	readings := func(id string) {
		for r := range 11 {
			for g := 1; g <= 3; g++ {
				add(Event{Task: id, Kind: "validated", Attempt: "r1", Gate: fmt.Sprint(g), Tree: fmt.Sprintf("tree-%s-%d", id, r), RC: &rc0})
			}
		}
	}
	read := map[string]bool{}
	nLanded, nFinished, nPassed := units*60/100, units*25/100, units*10/100
	for i := range units {
		id, wd := fmt.Sprintf("U%03d", i), ""
		switch {
		case i < nLanded:
			wd = landedWDs[i%len(landedWDs)]
		case i < nLanded+nFinished && i%10 == 0:
			wd = extra[(i/10)%len(extra)]
		}
		add(Event{Task: id, Kind: "planned", Brief: "brief.txt"})
		add(Event{Task: id, Kind: "dispatched", Attempt: "r1", Model: "m", Workdir: wd})
		add(Event{Task: id, Kind: "started", Attempt: "r1"})
		if i >= nLanded+nFinished+nPassed {
			continue // in flight
		}
		readings(id)
		add(Event{Task: id, Kind: "finished", Attempt: "r1", Model: "m", Reason: "stop"})
		if i >= nLanded && i < nLanded+nFinished && i%5 == 4 {
			continue // finished, no reading since: re-validate without a tree hash
		}
		add(Event{Task: id, Kind: "owns_checked", Attempt: "r1", Tree: "stale-" + id})
		switch {
		case i < nLanded:
			add(Event{Task: id, Kind: "inspected", Attempt: "r1", Verdict: "pass"})
			add(Event{Task: id, Kind: "landed", Commit: "abcdef1"})
		case i < nLanded+nFinished:
			if wd == "" {
				wd = dir
			}
			read[filepath.Clean(wd)] = true
		default:
			add(Event{Task: id, Kind: "inspected", Attempt: "r1", Verdict: "pass"})
		}
	}
	if err := AppendEvents(dir, evs); err != nil {
		t.Fatal(err)
	}
	return dir, len(evs), len(read)
}

// budgetRecover runs Recover with a counting tree hash under the one hang
// guard: 60 seconds, however loaded the host.
func budgetRecover(t *testing.T, dir string) (RecoverReport, int32, time.Duration) {
	t.Helper()
	var n atomic.Int32
	type result struct {
		rep RecoverReport
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		rep, err := Recover(dir, recoverNow, RecoverOptions{treeHash: countingTreeHash(&n)})
		done <- result{rep, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Recover: %v", r.err)
		}
		return r.rep, n.Load(), time.Since(start)
	case <-time.After(60 * time.Second):
		t.Fatalf("budget: Recover did not return within 60s")
	}
	return RecoverReport{}, 0, 0
}

// TestRecoverBudget checks recover over 200 units and 7,000+ events (issue
// #628) by counted reads, never a wall-clock window: tree hashes at most one
// per distinct workdir of a finished unit with a reading, no world read for a
// landed unit, and the same report twice.
func TestRecoverBudget(t *testing.T) {
	t.Parallel()
	dir, nev, bound := budgetLedger(t, 200)
	if nev < 7000 {
		t.Fatalf("synthetic ledger has %d events, want at least 7000", nev)
	}
	rep, n, took := budgetRecover(t, dir)
	t.Logf("budget: recover over 200 units / %d events took %s (%d tree hashes, bound %d)", nev, took.Round(time.Millisecond), n, bound)
	if n > int32(bound) {
		t.Errorf("tree hashes = %d, want at most %d (one per distinct workdir with a reading)", n, bound)
	}
	if len(rep.Tasks) != 200 {
		t.Fatalf("tasks = %d, want 200", len(rep.Tasks))
	}
	landed, worktrees := 0, 0
	for _, tk := range rep.Tasks {
		if tk.Status != "landed" {
			if tk.Worktree != "" {
				worktrees++
			}
			continue
		}
		landed++
		if tk.Worktree != "" || tk.Head != "" || tk.RunFile != "" || tk.Uncommitted != nil || tk.Next != (Next{Action: "none", Reason: "landed"}) {
			t.Errorf("%s: landed row read the world: %+v", tk.Task, tk)
		}
	}
	if landed != 120 || worktrees == 0 {
		t.Errorf("landed rows = %d (want 120), non-landed rows with a worktree = %d (want > 0)", landed, worktrees)
	}
	again, n2, _ := budgetRecover(t, dir)
	if !reflect.DeepEqual(rep, again) {
		t.Errorf("second recover differs from the first:\n%+v\nvs\n%+v", rep.Tasks, again.Tasks)
	}
	if n2 > int32(bound) {
		t.Errorf("second pass tree hashes = %d, want at most %d", n2, bound)
	}
}

// TestRecoverBudgetScales checks the world reads do not grow with the unit
// count (issue #628): 100 and 200 units over the same workdirs hash the same
// number of trees, one per workdir, not per unit.
func TestRecoverBudgetScales(t *testing.T) {
	t.Parallel()
	var hashes [2]int32
	for i, units := range []int{100, 200} {
		dir, nev, bound := budgetLedger(t, units)
		rep, n, took := budgetRecover(t, dir)
		t.Logf("budget: recover over %d units / %d events took %s (%d tree hashes)", units, nev, took.Round(time.Millisecond), n)
		if len(rep.Tasks) != units || n > int32(bound) {
			t.Errorf("%d units: tasks = %d, tree hashes = %d (bound %d)", units, len(rep.Tasks), n, bound)
		}
		hashes[i] = n
	}
	if hashes[0] != hashes[1] {
		t.Errorf("tree hashes 100 units = %d, 200 units = %d; want equal (per workdir, not per unit)", hashes[0], hashes[1])
	}
}
