package flywheel

import (
	"strings"
	"testing"
	"time"
)

// rcOf returns a pointer to n for validated event literals.
func rcOf(n int) *int {
	p := new(int)
	*p = n
	return p
}

// recCase is one Reconcile input and its exact expected output. The clock is
// injected per case: Reconcile never reads the real time.
type recCase struct {
	name   string
	events []Event
	leases []Lease
	policy Policy
	now    string
	want   []Action
}

// recCases is the reconcile table: one case per action kind, plus capacity,
// ordering, staleness, quietness and the never-below-zero clamp.
func recCases() []recCase {
	lease := func(task, attempt, expires string) Lease {
		return Lease{Task: task, Attempt: attempt, PID: 1, Host: "h",
			StartedAt: "2026-09-14T00:00:00Z", RenewedAt: "2026-09-14T00:00:00Z",
			ExpiresAt: expires, RunFile: ".flywheel/runs/" + task + "." + attempt + ".jsonl"}
	}
	return []recCase{
		{
			name: "mark-lost",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "m", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "m", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:30Z", Task: "m", Kind: "started"},
			},
			leases: []Lease{lease("m", "r1", "2026-09-14T00:02:00Z")},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want: []Action{{Kind: "MARK_LOST", Task: "m", Attempt: "r1",
				Evidence: "lease expired at 2026-09-14T00:02:00Z"}},
		},
		{
			name: "mark-lost-live-lease",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "m", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "m", Kind: "dispatched", Attempt: "r1"},
			},
			leases: []Lease{lease("m", "r1", "2026-09-14T00:30:00Z")},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "mark-lost-already-lost",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "m", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "m", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:30Z", Task: "m", Kind: "started"},
				{TS: "2026-09-14T00:02:00Z", Task: "m", Kind: "lost", Attempt: "r1", Reason: "lease-expired"},
			},
			leases: []Lease{lease("m", "r1", "2026-09-14T00:02:00Z")},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "request-inspection",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "q", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "q", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:30Z", Task: "q", Kind: "started"},
				{TS: "2026-09-14T00:02:00Z", Task: "q", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(0)},
				{TS: "2026-09-14T00:03:10Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "2", Tree: "t1", RC: rcOf(0)},
				{TS: "2026-09-14T00:04:00Z", Task: "q", Kind: "owns_checked", Attempt: "r1", Tree: "t1"},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want: []Action{{Kind: "REQUEST_INSPECTION", Task: "q",
				Reason: "validated pass awaits inspection"}},
		},
		{
			name: "request-inspection-failed-gate",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "q", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "q", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "q", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(1)},
				{TS: "2026-09-14T00:04:00Z", Task: "q", Kind: "owns_checked", Attempt: "r1", Tree: "t1"},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "inspection-newest-fail-ignores-older-pass",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "q", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "q", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "q", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:05:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(1)},
				{TS: "2026-09-14T00:03:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(0)},
				{TS: "2026-09-14T00:06:00Z", Task: "q", Kind: "owns_checked", Attempt: "r1", Tree: "t1"},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "inspection-newest-pass-ignores-older-fail",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "q", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "q", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "q", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(1)},
				{TS: "2026-09-14T00:05:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(0)},
				{TS: "2026-09-14T00:06:00Z", Task: "q", Kind: "owns_checked", Attempt: "r1", Tree: "t1"},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want: []Action{{Kind: "REQUEST_INSPECTION", Task: "q",
				Reason: "validated pass awaits inspection"}},
		},
		{
			name: "request-inspection-dirty-owns",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "q", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "q", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "q", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(0)},
				{TS: "2026-09-14T00:04:00Z", Task: "q", Kind: "owns_checked", Attempt: "r1", Tree: "t1",
					Outside: []string{"src/out.go"}},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "block",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "a", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "a", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "a", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "a", Kind: "inspected", Verdict: "scrap"},
				{TS: "2026-09-14T00:04:00Z", Task: "x", Kind: "planned", Brief: "b.txt", Needs: []string{"a"}},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{{Kind: "BLOCK", Task: "x", Reason: "needs a"}},
		},
		{
			name: "block-first-rejected-in-brief-order",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "a", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "a", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "a", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "a", Kind: "inspected", Verdict: "scrap"},
				{TS: "2026-09-14T00:00:30Z", Task: "b", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:30Z", Task: "b", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:30Z", Task: "b", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:30Z", Task: "b", Kind: "inspected", Verdict: "scrap"},
				{TS: "2026-09-14T00:04:30Z", Task: "x", Kind: "planned", Brief: "b.txt",
					Needs: []string{"b", "a"}},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{{Kind: "BLOCK", Task: "x", Reason: "needs b"}},
		},
		{
			name: "wait",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "a", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:00:30Z", Task: "a", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:00Z", Task: "w", Kind: "planned", Brief: "b.txt", Needs: []string{"a"}},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{{Kind: "WAIT", Task: "w", Reason: "needs a"}},
		},
		{
			name: "wait-targets-in-brief-order",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "a", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:00:10Z", Task: "a", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:00:30Z", Task: "c", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:00:40Z", Task: "c", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:00Z", Task: "w", Kind: "planned", Brief: "b.txt",
					Needs: []string{"c", "a"}},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{{Kind: "WAIT", Task: "w", Reason: "needs c, a"}},
		},
		{
			name: "dispatch",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "d", Kind: "planned", Brief: "b.txt"},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{{Kind: "DISPATCH", Task: "d", Reason: "ready, needs met, capacity 2 free"}},
		},
		{
			name: "dispatch-needs-met-by-landed",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "a", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "a", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "a", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "a", Kind: "inspected", Verdict: "pass"},
				{TS: "2026-09-14T00:03:30Z", Task: "a", Kind: "landed", Commit: "c"},
				{TS: "2026-09-14T00:04:00Z", Task: "d", Kind: "planned", Brief: "b.txt", Needs: []string{"a"}},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{{Kind: "DISPATCH", Task: "d", Reason: "ready, needs met, capacity 2 free"}},
		},
		{
			name: "capacity-caps-dispatch",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "busy", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "busy", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:30Z", Task: "busy", Kind: "started"},
				{TS: "2026-09-14T00:02:00Z", Task: "d", Kind: "planned", Brief: "b.txt"},
			},
			leases: []Lease{lease("busy", "r1", "2026-09-14T00:30:00Z")},
			policy: Policy{MaxParallel: 1},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "capacity-not-below-zero",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "b1", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:00:30Z", Task: "b1", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:00:40Z", Task: "b1", Kind: "started"},
				{TS: "2026-09-14T00:01:00Z", Task: "b2", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:30Z", Task: "b2", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:40Z", Task: "b2", Kind: "started"},
				{TS: "2026-09-14T00:02:00Z", Task: "d", Kind: "planned", Brief: "b.txt"},
			},
			leases: []Lease{
				lease("b1", "r1", "2026-09-14T00:30:00Z"),
				lease("b2", "r1", "2026-09-14T00:30:00Z"),
			},
			policy: Policy{MaxParallel: 0},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "ordering",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "l", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "l", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:00Z", Task: "d", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:02:00Z", Task: "early", Kind: "planned", Brief: "b.txt", Needs: []string{"l"}},
				{TS: "2026-09-14T00:02:00Z", Task: "early2", Kind: "planned", Brief: "b.txt", Needs: []string{"l"}},
				{TS: "2026-09-14T00:03:00Z", Task: "q", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:04:00Z", Task: "q", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:04:30Z", Task: "q", Kind: "started"},
				{TS: "2026-09-14T00:05:00Z", Task: "q", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:06:00Z", Task: "q", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(0)},
				{TS: "2026-09-14T00:07:00Z", Task: "q", Kind: "owns_checked", Attempt: "r1", Tree: "t1"},
				{TS: "2026-09-14T00:05:30Z", Task: "later", Kind: "planned", Brief: "b.txt", Needs: []string{"l"}},
			},
			leases: []Lease{lease("l", "r1", "2026-09-14T00:02:00Z")},
			policy: Policy{MaxParallel: 4},
			now:    "2026-09-14T00:10:00Z",
			want: []Action{
				{Kind: "MARK_LOST", Task: "l", Attempt: "r1", Evidence: "lease expired at 2026-09-14T00:02:00Z"},
				{Kind: "REQUEST_INSPECTION", Task: "q", Reason: "validated pass awaits inspection"},
				{Kind: "WAIT", Task: "early", Reason: "needs l"},
				{Kind: "WAIT", Task: "early2", Reason: "needs l"},
				{Kind: "WAIT", Task: "later", Reason: "needs l"},
				{Kind: "DISPATCH", Task: "d", Reason: "ready, needs met, capacity 4 free"},
			},
		},
		{
			name: "stale-late-result-not-finished",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "s", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "s", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "s", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "s", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t1", RC: rcOf(0)},
				{TS: "2026-09-14T00:04:00Z", Task: "s", Kind: "owns_checked", Attempt: "r1", Tree: "t1"},
				{TS: "2026-09-14T00:05:00Z", Task: "s", Kind: "dispatched", Attempt: "r2"},
				{TS: "2026-09-14T00:06:00Z", Task: "s", Kind: "finished", Attempt: "r1"},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "quiet-landed-passed-correction",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "p", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "p", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:02:00Z", Task: "p", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:03:00Z", Task: "p", Kind: "inspected", Verdict: "pass"},
				{TS: "2026-09-14T00:04:00Z", Task: "L", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:05:00Z", Task: "L", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:06:00Z", Task: "L", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:07:00Z", Task: "L", Kind: "inspected", Verdict: "pass"},
				{TS: "2026-09-14T00:08:00Z", Task: "L", Kind: "landed", Commit: "c"},
				{TS: "2026-09-14T00:09:00Z", Task: "k", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:09:30Z", Task: "k", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:09:40Z", Task: "k", Kind: "started"},
				{TS: "2026-09-14T00:09:50Z", Task: "k", Kind: "finished", Attempt: "r1"},
				{TS: "2026-09-14T00:09:55Z", Task: "k", Kind: "inspected", Verdict: "rework"},
			},
			leases: []Lease{},
			policy: Policy{MaxParallel: 2},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "per-host-caps-capacity",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "busy", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "busy", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:30Z", Task: "busy", Kind: "started"},
				{TS: "2026-09-14T00:02:00Z", Task: "d", Kind: "planned", Brief: "b.txt"},
			},
			leases: []Lease{lease("busy", "r1", "2026-09-14T00:30:00Z")},
			policy: Policy{MaxParallel: 5, PerHost: 1},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{},
		},
		{
			name: "per-host-zero-allows-dispatch",
			events: []Event{
				{TS: "2026-09-14T00:00:00Z", Task: "busy", Kind: "planned", Brief: "b.txt"},
				{TS: "2026-09-14T00:01:00Z", Task: "busy", Kind: "dispatched", Attempt: "r1"},
				{TS: "2026-09-14T00:01:30Z", Task: "busy", Kind: "started"},
				{TS: "2026-09-14T00:02:00Z", Task: "d", Kind: "planned", Brief: "b.txt"},
			},
			leases: []Lease{lease("busy", "r1", "2026-09-14T00:30:00Z")},
			policy: Policy{MaxParallel: 5, PerHost: 0},
			now:    "2026-09-14T00:10:00Z",
			want:   []Action{{Kind: "DISPATCH", Task: "d", Reason: "ready, needs met, capacity 4 free"}},
		},
	}
}

// canonActions renders one action per line so lists compare by value.
func canonActions(acts []Action) string {
	var lines []string
	for _, a := range acts {
		lines = append(lines, a.Kind+"|"+a.Task+"|"+a.Attempt+"|"+a.Reason+"|"+a.Evidence)
	}
	return strings.Join(lines, "\n")
}

// runRecCase derives the state, reconciles at the injected clock and compares
// the canonical action lines, so a mismatch prints both sides.
func runRecCase(t *testing.T, c recCase) {
	now, err := time.Parse(time.RFC3339, c.now)
	if err != nil {
		t.Fatalf("%s: parse now %q: %v", c.name, c.now, err)
	}
	st := Derive(c.events)
	got := Reconcile(st, c.events, Observed{Leases: c.leases}, c.policy, now)
	if canonActions(got) != canonActions(c.want) {
		t.Errorf("%s: actions:\n%s\nwant:\n%s", c.name, canonActions(got), canonActions(c.want))
	}
}

// TestReconcileTable runs every case: one per action kind, capacity caps
// dispatch, stable ordering, stale late results, quiet tasks and the
// never-below-zero clamp.
func TestReconcileTable(t *testing.T) {
	for _, c := range recCases() {
		runRecCase(t, c)
	}
}

// recCaseByName returns the table case with the given name.
func recCaseByName(t *testing.T, name string) recCase {
	for _, c := range recCases() {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no reconcile case named %q", name)
	return recCase{}
}

// TestReconcileIdempotent reconciles the same inputs twice: identical output,
// so a repeated tick never invents work and two operators agree.
func TestReconcileIdempotent(t *testing.T) {
	c := recCaseByName(t, "ordering")
	now, err := time.Parse(time.RFC3339, c.now)
	if err != nil {
		t.Fatalf("parse now %q: %v", c.now, err)
	}
	st := Derive(c.events)
	obs := Observed{Leases: c.leases}
	first := Reconcile(st, c.events, obs, c.policy, now)
	second := Reconcile(st, c.events, obs, c.policy, now)
	if canonActions(first) != canonActions(second) {
		t.Errorf("idempotent: first:\n%s\nsecond:\n%s", canonActions(first), canonActions(second))
	}
}

// TestReconcilePerHostCapsCapacity checks that per_host caps the dispatch capacity.
func TestReconcilePerHostCapsCapacity(t *testing.T) {
	c := recCaseByName(t, "per-host-caps-capacity")
	now, err := time.Parse(time.RFC3339, c.now)
	if err != nil {
		t.Fatalf("parse now %q: %v", c.now, err)
	}
	st := Derive(c.events)
	obs := Observed{Leases: c.leases}
	acts := Reconcile(st, c.events, obs, c.policy, now)
	if len(acts) != 0 {
		t.Errorf("per-host-caps-capacity: got %d actions, want 0 (dispatch should be refused by per_host cap)", len(acts))
	}
}

// TestPolicyFromConfig pins the max_parallel mapping: 0 means 1, and checks PerHost is copied.
func TestPolicyFromConfig(t *testing.T) {
	if p := PolicyFromConfig(Config{Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "opencode", Model: "m", MaxParallel: 0}}}); p.MaxParallel != 1 {
		t.Errorf("max_parallel 0 = %d, want 1", p.MaxParallel)
	}
	if p := PolicyFromConfig(Config{Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "opencode", Model: "m", MaxParallel: 3}}}); p.MaxParallel != 3 {
		t.Errorf("max_parallel 3 = %d, want 3", p.MaxParallel)
	}
	if p := PolicyFromConfig(Config{Version: 1, Limits: Limits{PerHost: 5},
		Workers: []Worker{{Name: "default", Adapter: "opencode", Model: "m", MaxParallel: 3}}}); p.PerHost != 5 {
		t.Errorf("PerHost = %d, want 5", p.PerHost)
	}
}
