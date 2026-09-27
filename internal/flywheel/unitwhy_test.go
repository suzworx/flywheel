package flywheel

import (
	"testing"
	"time"
)

var whyT0 = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

// wev is a hand-built event of task at whyT0 plus min minutes.
func wev(min float64, task, kind string, mods ...func(*Event)) Event {
	e := Event{TS: whyT0.Add(time.Duration(min * float64(time.Minute))).Format(time.RFC3339Nano), Task: task, Kind: kind}
	for _, m := range mods {
		m(&e)
	}
	return e
}

func wAtt(e *Event) { e.Attempt = "2" }

func wRC(n int) *int { return &n }

// wRun is k1 planned, then attempt 2 dispatched and started at +10m.
func wRun(more ...Event) []Event {
	evs := []Event{wev(0, "k1", "planned"), wev(10, "k1", "dispatched", wAtt), wev(10, "k1", "started", wAtt)}
	return append(evs, more...)
}

func wFin(min float64, reason string, mods ...func(*Event)) Event {
	return wev(min, "k1", "finished", append([]func(*Event){wAtt, func(e *Event) { e.Reason, e.Model = reason, "m1" }}, mods...)...)
}

func TestUnitWhyStates(t *testing.T) {
	t.Parallel()
	gate := func(min float64, id, cmd string, rc int) Event {
		return wev(min, "k1", "validated", wAtt, func(e *Event) { e.Gate, e.Command, e.RC, e.Tree = id, cmd, wRC(rc), "t1" })
	}
	cases := []struct {
		name   string
		events []Event
		now    float64
		ctx    WhyContext
		want   string
	}{
		{"blocked", []Event{wev(0, "k1", "planned"), wev(1, "k2", "planned", func(e *Event) { e.Needs = []string{"k1"} })}, 5, WhyContext{},
			"blocked: needs k1, which has not landed."},
		{"queued", []Event{wev(0, "k1", "planned"), wev(1, "k1", "landed"), wev(2, "k2", "planned", func(e *Event) { e.Needs = []string{"k1"} })}, 5, WhyContext{},
			"queued: planned and its needs are met; next: flywheel run k2."},
		{"dispatched", wRun()[:2], 13, WhyContext{},
			"queued: attempt 2 dispatched 3m ago, waiting for the worker to start."},
		{"building", wRun(), 33, WhyContext{Steps: 14},
			"building: attempt 2, 14 steps, running for 23m."},
		{"stalled", wRun(), 22, WhyContext{StallTimeout: 10 * time.Minute},
			"stalled: attempt 2 has shown no progress for 12m (stall timeout 10m); the run stops at the timeout and flywheel recover --apply marks it lost."},
		{"rate-limited", wRun(wFin(30, "rate-limited", func(e *Event) { e.ResetAt = whyT0.Add(90 * time.Minute).Format(time.RFC3339) })), 40, WhyContext{},
			"rate-limited: attempt 2 hit m1's limit, which resets at 11:30 UTC; next: flywheel run k1 --resume."},
		{"capped", wRun(wFin(30, "length", func(e *Event) { e.PeakReasoning = 50000 })), 40, WhyContext{},
			"capped: attempt 2 hit the output cap (peak reasoning 50k); correct the brief or dispatch again."},
		{"suspended", wRun(wev(20, "", "suspended", func(e *Event) { e.Session = "lead" }), wFin(21, "suspended")), 40, WhyContext{},
			"suspended: frozen by suspend at 10:20 UTC; it resumes on flywheel resume."},
		{"suspended until", wRun(wev(20, "", "suspended", func(e *Event) { e.Until = whyT0.Add(2 * time.Hour).Format(time.RFC3339) }), wFin(21, "suspended")), 40, WhyContext{},
			"suspended: frozen by suspend at 10:20 UTC; it thaws at 12:00 UTC."},
		{"awaiting validation", wRun(wFin(30, "stop")), 40, WhyContext{},
			"finished, awaiting validation; next: flywheel validate k1."},
		{"validation failed", wRun(wFin(30, "stop"), gate(31, "g1", "go build ./...", 0), gate(31, "g2", "go test ./...", 1)), 40, WhyContext{},
			"validation failed: gates g2; first failing `go test ./...`; correct the unit."},
		{"needs correction", wRun(wFin(30, "stop"),
			wev(35, "k1", "review_finding", func(e *Event) { e.Finding, e.Severity, e.Title = "k1-r1-1", "blocker", "x" }),
			wev(35, "k1", "reviewed", func(e *Event) { e.Verdict, e.Persona, e.Adapter = "correct", "reviewer", "claude" })), 40, WhyContext{},
			"needs correction: 1 open blocking finding(s) (k1-r1-1); next: flywheel review k1 --agent --fix."},
		{"passed", wRun(wFin(30, "stop"), wev(40, "k1", "inspected", func(e *Event) { e.Verdict = "pass" })), 45, WhyContext{},
			"passed, awaiting landing; next: flywheel land k1."},
		{"landed", wRun(wFin(30, "stop"), wev(50, "k1", "shipped", func(e *Event) { e.Step, e.Result, e.Note = "pr", "ok", "opened #42 https://x/pull/42" }),
			wev(55, "k1", "landed", func(e *Event) { e.Commit = "abcdef1234" })), 60, WhyContext{},
			"landed as abcdef1 (PR #42); nothing left to do."},
		{"withdrawn", []Event{wev(0, "k1", "planned"), wev(5, "k1", "withdrawn", func(e *Event) { e.Note = "superseded by k9" })}, 10, WhyContext{},
			"withdrawn: superseded by k9; a new flywheel plan revives it."},
	}
	for _, c := range cases {
		task := "k1"
		if c.name == "blocked" || c.name == "queued" {
			task = "k2"
		}
		got := UnitWhy(c.events, task, whyT0.Add(time.Duration(c.now*float64(time.Minute))), c.ctx)
		if got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func TestUnitTimelineGapsAndSummary(t *testing.T) {
	t.Parallel()
	att := func(a string) func(*Event) { return func(e *Event) { e.Attempt = a } }
	events := []Event{
		wev(0, "k1", "planned"),
		wev(10, "k1", "dispatched", att("2")), wev(10, "k1", "started", att("2")),
		wev(30, "k1", "finished", att("2"), func(e *Event) {
			e.Reason, e.ResetAt = "rate-limited", whyT0.Add(90*time.Minute).Format(time.RFC3339)
		}),
		wev(90, "k1", "dispatched", att("3")), wev(90, "k1", "started", att("3")),
		wev(95, "k9", "planned"), // another unit's event is not on k1's timeline
		wev(100, "k1", "finished", att("3"), func(e *Event) { e.Reason = "stop" }),
		wev(101, "", "suspended", func(e *Event) { e.Session = "lead" }),
		wev(160, "", "unsuspended"),
		wev(170, "k1", "validated", att("3"), func(e *Event) { e.Gate, e.RC = "g1", wRC(0) }),
	}
	rows := UnitTimeline(events, "k1")
	want := []struct{ kind, detail string }{
		{"planned", ""}, {"gap", "idle"}, {"dispatched", ""}, {"started", ""}, {"gap", "attempt 2 running"}, {"finished", ""},
		{"gap", "waiting for the rate-limit reset at 11:30 UTC"}, {"dispatched", ""}, {"started", ""}, {"gap", "attempt 3 running"}, {"finished", ""},
		{"gap", "frozen by suspend"}, {"validated", ""},
		{"summary", "total 2h50m · touch 30m · flow efficiency 18%"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].Kind != w.kind || (w.detail != "" && rows[i].Detail != w.detail) {
			t.Errorf("row %d: got %s %q, want %s %q", i, rows[i].Kind, rows[i].Detail, w.kind, w.detail)
		}
		if i > 0 && rows[i].TS.Before(rows[i-1].TS) {
			t.Errorf("row %d at %s is before row %d", i, rows[i].TS, i-1)
		}
	}
	if g := rows[6]; g.Dur != time.Hour {
		t.Errorf("rate-limit gap: %s, want 1h", g.Dur)
	}
	sum := rows[len(rows)-1]
	if sum.Dur != 170*time.Minute || sum.Touch != 30*time.Minute || sum.Flow < 0.17 || sum.Flow > 0.18 {
		t.Errorf("summary: total %s touch %s flow %.3f", sum.Dur, sum.Touch, sum.Flow)
	}
}
