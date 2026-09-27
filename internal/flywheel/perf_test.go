package flywheel

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// perfLedger is a synthetic ledger of units units, 35 events each, in the mix
// a working factory records (issue #630): planned and amended, five attempts
// of dispatched, started, a signal, finished and two gate readings, then
// inspected and landed for most units, and a learning.
func perfLedger(units int) []Event {
	// Each event gets its own sub-second part, as AppendEvent's
	// nanosecond timestamps give a real ledger.
	at, k := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), 0
	ts := func(n int) string {
		k++
		return at.Add(time.Duration(n)*time.Minute + time.Duration(k%50000)*time.Microsecond).Format(time.RFC3339Nano)
	}
	var out []Event
	for u := 0; u < units; u++ {
		task, n := fmt.Sprintf("P%03d", u), u*40
		out = append(out, Event{TS: ts(n), Task: task, Kind: "planned", Brief: ".flywheel/briefs/" + task + ".txt"},
			Event{TS: ts(n + 1), Task: task, Kind: "amended", Note: "owns widened"})
		for a := 1; a <= 5; a++ {
			att, m := fmt.Sprintf("r%d", a), n+a*6
			out = append(out,
				Event{TS: ts(m), Task: task, Kind: "dispatched", Attempt: att, Model: "claude-opus-5-5", Adapter: "claude", Worker: "w1", Session: "s"},
				Event{TS: ts(m + 1), Task: task, Kind: "started", Attempt: att, Session: "s"},
				Event{TS: ts(m + 2), Task: task, Kind: "signal", Attempt: att, Signal: "no-plan", Session: "s"},
				Event{TS: ts(m + 3), Task: task, Kind: "finished", Attempt: att, RC: intPtr(0), Reason: "stop", Cost: 0.5, Session: "s"},
				Event{TS: ts(m + 4), Task: task, Kind: "validated", Attempt: att, Gate: "build", RC: intPtr(a % 2)},
				Event{TS: ts(m + 4), Task: task, Kind: "validated", Attempt: att, Gate: "test", RC: intPtr(0)})
		}
		out = append(out, Event{TS: ts(n + 37), Task: task, Kind: "learning", Severity: "P2", Title: "t", Observed: "o", Evidence: "e", Ask: "a"})
		if u%4 != 0 {
			out = append(out, Event{TS: ts(n + 38), Task: task, Kind: "inspected", Verdict: "pass", Session: "lead"},
				Event{TS: ts(n + 39), Task: task, Kind: "landed", Commit: "abc1234"})
		}
	}
	return out
}

// oldDerivationOrder is derivationOrder as it was before issue #630: the
// canonical JSON built for every event, the reference the new one must match.
func oldDerivationOrder(events []Event) []Event {
	type key struct {
		e     Event
		t     time.Time
		valid bool
		kind  int
		canon string
	}
	keys := make([]key, len(events))
	for i, e := range events {
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		keys[i] = key{e, t, err == nil, kindRank[e.Kind], canonical(e)}
	}
	slices.SortStableFunc(keys, func(a, b key) int {
		switch {
		case a.valid != b.valid && a.valid:
			return -1
		case a.valid != b.valid:
			return 1
		case a.valid && !a.t.Equal(b.t):
			return a.t.Compare(b.t)
		}
		if c := strings.Compare(a.e.Task, b.e.Task); c != 0 {
			return c
		}
		if c := a.kind - b.kind; c != 0 {
			return c
		}
		return strings.Compare(a.canon, b.canon)
	})
	out := make([]Event, len(keys))
	for i, k := range keys {
		out[i] = k.e
	}
	return out
}

// TestDerivationOrderStableAgainstOld checks the lazy tiebreak (issue #630):
// on 5,000 shuffled events with ties on time, task and kind, and unparseable
// times, the order is the old algorithm's, event for event.
func TestDerivationOrderStableAgainstOld(t *testing.T) {
	t.Parallel()
	events := perfLedger(140)[:4900]
	for i := 0; i < 100; i++ {
		// Same time, task and kind: only the canonical JSON orders them.
		events = append(events, Event{TS: "2026-09-02T00:00:00Z", Task: "TIE", Kind: "validated", Gate: fmt.Sprintf("g%d", i%7), RC: intPtr(i % 3)})
	}
	events[10].TS, events[20].TS = "not a time", "also not"
	rand.New(rand.NewSource(630)).Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
	got, want := derivationOrder(events), oldDerivationOrder(events)
	for i := range want {
		if canonical(got[i]) != canonical(want[i]) {
			t.Fatalf("event %d = %s, want %s", i, canonical(got[i]), canonical(want[i]))
		}
	}
}

// TestPerfDeriveBudget is a hang guard (issue #630): Derive over 200 units and
// 7,000 events stays under a second.
// not parallel: a wall-clock budget; parallel tests would share its CPU.
func TestPerfDeriveBudget(t *testing.T) {
	events := perfLedger(200)
	start := time.Now()
	st := Derive(events)
	if d, limit := time.Since(start), budget(time.Second); d > limit || len(st.Tasks) != 200 {
		t.Errorf("Derive of %d events: %s and %d tasks, want under %s (race %v) and 200", len(events), d, len(st.Tasks), limit, raceEnabled)
	}
}

// TestPerfTUIFetchUnits is a hang guard (issue #630): one units fetch of the
// factory view over that ledger stays under 5s.
// not parallel: a wall-clock budget; parallel tests would share its CPU.
func TestPerfTUIFetchUnits(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	var b strings.Builder
	for _, e := range perfLedger(200) {
		line, err := marshalEvent(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(append(line, '\n'))
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	fetch := TUIFetcher(dir, func() time.Time { return time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC) })
	start := time.Now()
	d, err := fetch(NewTUI())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if took, limit := time.Since(start), budget(5*time.Second); took > limit || len(d.Floor.Units) != 200 || len(d.Why) != 200 {
		t.Errorf("units fetch: %s, %d units, %d whys; want under %s (race %v) and 200 of each", took, len(d.Floor.Units), len(d.Why), limit, raceEnabled)
	}
}
