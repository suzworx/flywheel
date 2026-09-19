package flywheel

import (
	"strings"
	"testing"
)

func TestGateEmptyIsClear(t *testing.T) {
	result := Gate([]Event{})
	if !result.OK() {
		t.Errorf("empty ledger: OK() = false, want true")
	}
	if result.Blockers == nil {
		t.Errorf("empty ledger: Blockers = nil, want non-nil slice")
	}
	if len(result.Blockers) != 0 {
		t.Errorf("empty ledger: len(Blockers) = %d, want 0", len(result.Blockers))
	}
}

func TestGateFinishedUninspectedBlocks(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-18T10:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:00:02Z", Task: "T1", Kind: "finished", Attempt: "r1"},
	}
	result := Gate(events)
	if result.OK() {
		t.Errorf("finished uninspected: OK() = true, want false")
	}
	if len(result.Blockers) != 1 {
		t.Errorf("finished uninspected: len(Blockers) = %d, want 1", len(result.Blockers))
	}
	if len(result.Blockers) > 0 {
		b := result.Blockers[0]
		if b.Kind != "uninspected" {
			t.Errorf("finished uninspected: kind = %q, want uninspected", b.Kind)
		}
		if b.Task != "T1" {
			t.Errorf("finished uninspected: task = %q, want T1", b.Task)
		}
		if b.Detail != "finished r1, not yet inspected" {
			t.Errorf("finished uninspected: detail = %q, want 'finished r1, not yet inspected'", b.Detail)
		}
	}
}

func TestGateInspectedPassIsClear(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-18T10:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:00:02Z", Task: "T1", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-18T10:00:03Z", Task: "T1", Kind: "inspected", Verdict: "pass"},
	}
	result := Gate(events)
	if !result.OK() {
		t.Errorf("inspected pass: OK() = false, want true")
	}
	if len(result.Blockers) != 0 {
		t.Errorf("inspected pass: len(Blockers) = %d, want 0", len(result.Blockers))
	}
}

func TestGateUntriagedSignalBlocks(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-18T10:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:00:02Z", Task: "T1", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-18T10:00:03Z", Task: "T1", Kind: "signal", Attempt: "r1", Signal: "no-plan"},
	}
	result := Gate(events)
	if result.OK() {
		t.Errorf("untriaged signal: OK() = true, want false")
	}
	if len(result.Blockers) != 2 {
		t.Errorf("untriaged signal: len(Blockers) = %d, want 2 (uninspected + signal)", len(result.Blockers))
	}
	if len(result.Blockers) > 0 {
		hasUntriaged := false
		for _, b := range result.Blockers {
			if b.Kind == "untriaged" && b.Detail == "r1 no-plan" {
				hasUntriaged = true
			}
		}
		if !hasUntriaged {
			t.Errorf("untriaged signal: expected blocker with kind=untriaged and detail='r1 no-plan'")
		}
	}
}

func TestGateTriagedSignalIsClear(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-18T10:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:00:02Z", Task: "T1", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-18T10:00:03Z", Task: "T1", Kind: "signal", Attempt: "r1", Signal: "no-plan"},
		{TS: "2026-09-18T10:00:04Z", Task: "T1", Kind: "inspected", Verdict: "pass"},
		{TS: "2026-09-18T10:00:05Z", Task: "T1", Kind: "learning", Severity: "P2", Title: "t", Observed: "o", Evidence: "e", Ask: "a", Signals: []string{"no-plan"}},
	}
	result := Gate(events)
	if !result.OK() {
		t.Errorf("triaged signal: OK() = false, want true")
	}
	if len(result.Blockers) != 0 {
		t.Errorf("triaged signal: len(Blockers) = %d, want 0", len(result.Blockers))
	}
}

// TestGateUninspectedNamesCurrentAttempt checks a late stale finish of an older
// attempt does not replace the current attempt in the blocker (#291 review).
func TestGateUninspectedNamesCurrentAttempt(t *testing.T) {
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "dispatched", Attempt: "r2"},
		{TS: "2026-09-18T10:03:00Z", Task: "T1", Kind: "finished", Attempt: "r2"},
		{TS: "2026-09-18T10:04:00Z", Task: "T1", Kind: "finished", Attempt: "r1"},
	}
	g := Gate(events)
	if len(g.Blockers) != 1 || !strings.Contains(g.Blockers[0].Detail, "r2") {
		t.Fatalf("blockers = %+v, want one naming the current attempt r2", g.Blockers)
	}
}
