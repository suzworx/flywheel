package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestFleetTableEvents (issue #608): the table has an EVENTS column after
// KIND; a root row prints its plain count, a forked worktree row "+N" (its
// own events after the fork, "+0" for an exact copy), and the idle fold row
// leaves it empty. On the unfixed code the header has no EVENTS column.
func TestFleetTableEvents(t *testing.T) {
	t.Parallel()
	row := func(name, kind string, events, inherited int) flywheel.FleetRow {
		return flywheel.FleetRow{FleetLedger: flywheel.FleetLedger{Root: "r", Name: name, Kind: kind}, Events: events, Inherited: inherited}
	}
	age := 7200
	idle := flywheel.FleetRow{FleetLedger: flywheel.FleetLedger{Root: "r", Name: "+1 idle worktree ledgers", Kind: flywheel.FleetKindIdle}, Idle: 1, LastAge: &age}
	var buf bytes.Buffer
	writeFleetTable(&buf, []flywheel.FleetRow{
		row("r", flywheel.FleetKindRoot, 412, 0),
		row("r/u", flywheel.FleetKindGitWorktree, 3, 412),
		row("r/copy", flywheel.FleetKindGitWorktree, 0, 412),
		idle,
	})
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("table = %d lines, want 5:\n%s", len(lines), buf.String())
	}
	want := [][]string{
		{"NAME", "KIND", "EVENTS", "RUNNING"},
		{"r", "root", "412", "0"},
		{"r/u", "git-worktree", "+3", "0"},
		{"r/copy", "git-worktree", "+0", "0"},
	}
	for i, w := range want {
		if got := strings.Fields(lines[i]); len(got) < len(w) || strings.Join(got[:len(w)], " ") != strings.Join(w, " ") {
			t.Errorf("line %d = %q, want it to start %v", i, lines[i], w)
		}
	}
	if got := strings.Fields(lines[4]); strings.Join(got, " ") != "+1 idle worktree ledgers (oldest 2h) idle" {
		t.Errorf("idle line = %q, want the fold row with no cell after KIND", lines[4])
	}
}
