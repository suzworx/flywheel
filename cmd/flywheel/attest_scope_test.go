package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestAttestScopeParse covers attestScope (issue #908): empty is nil, a
// tasks: list resolves against the ledger and always covers the attested
// task, and a missing prefix, an empty list or an unknown task is an error.
func TestAttestScopeParse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, task := range []string{"A", "B"} {
		if err := flywheel.AppendEvent(dir, flywheel.Event{TS: "2026-10-29T00:00:00Z", Task: task, Kind: "planned"}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := attestScope(dir, "A", ""); err != nil || got != nil {
		t.Fatalf(`attestScope("") = %v, %v; want nil, nil`, got, err)
	}
	got, err := attestScope(dir, "A", "tasks:A,B")
	if err != nil || !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("attestScope(tasks:A,B) = %v, %v; want [A B]", got, err)
	}
	got, err = attestScope(dir, "A", "tasks:B")
	if err != nil || !slices.Contains(got, "A") || !slices.Contains(got, "B") {
		t.Fatalf("attestScope(tasks:B) for A = %v, %v; want A and B", got, err)
	}
	if _, err := attestScope(dir, "A", "tasks:A,Z"); err == nil || !strings.Contains(err.Error(), "Z") {
		t.Fatalf("attestScope(tasks:A,Z) err = %v; want an error naming Z", err)
	}
	for _, bad := range []string{"A,B", "tasks:"} {
		if _, err := attestScope(dir, "A", bad); err == nil {
			t.Fatalf("attestScope(%q) = nil error; want an error", bad)
		}
	}
}

// TestAttestScopeFlagBinds checks --scope is bound into attestOptions.
func TestAttestScopeFlagBinds(t *testing.T) {
	t.Parallel()
	fs, o := attestFlags()
	if err := fs.Parse([]string{"--scope", "tasks:A,B"}); err != nil {
		t.Fatal(err)
	}
	if o.scope != "tasks:A,B" {
		t.Fatalf("o.scope = %q; want tasks:A,B", o.scope)
	}
}
