package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateOtherLedgerTaskWorktree checks a sibling that is another
// ledger's `run --worktree` task worktree, <L>/.flywheel/worktrees/<OTHER>,
// is attributed to OTHER from L's ledger while OTHER is dispatched and
// unlanded there, though the unit's own ledger A is a different checkout
// (issue #918). Once OTHER lands in L, or L has no event log, the path is
// outside again.
func TestValidateOtherLedgerTaskWorktree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		landed     bool
		noEvents   bool
		wantOwnsOK bool
	}{
		{"in flight", false, false, true},
		{"landed", true, false, false},
		{"no events file", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledger, err := initTask(t, []string{"exit 0"})
			if err != nil {
				t.Fatalf("initTask() error = %v", err)
			}
			otherTaskPlanned(t, ledger, "OTHER", "b.go")
			if err := AppendEvent(ledger, Event{TS: "2026-09-12T00:40:00Z", Task: "OTHER", Kind: "dispatched", Attempt: "r1"}); err != nil {
				t.Fatalf("AppendEvent() dispatched OTHER error = %v", err)
			}
			wt := filepath.Join(ledger, ".flywheel", "worktrees", "OTHER")
			git(t, ledger, []string{"worktree", "add", "-b", "fw/OTHER", wt, "HEAD"})
			// The unit's own ledger A: a second worktree of the same repository,
			// outside L's .flywheel, with a ledger of its own.
			a := filepath.Join(t.TempDir(), "A")
			git(t, ledger, []string{"worktree", "add", "--detach", a, "HEAD"})
			if _, err := Init(a, false); err != nil {
				t.Fatalf("Init(A) error = %v", err)
			}
			if err := AppendEvent(a, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
				t.Fatalf("AppendEvent() planned T1 in A error = %v", err)
			}
			dispatchedWithWorktree(t, a, wt)
			if tc.landed {
				if err := AppendEvent(ledger, Event{TS: "2026-09-12T01:30:00Z", Task: "OTHER", Kind: "landed"}); err != nil {
					t.Fatalf("AppendEvent() landed OTHER error = %v", err)
				}
			}
			if tc.noEvents {
				if err := os.Remove(filepath.Join(ledger, ".flywheel", "events.jsonl")); err != nil {
					t.Fatalf("remove L's events: %v", err)
				}
			}
			if err := os.WriteFile(filepath.Join(wt, "b.go"), []byte("package b\n"), 0o644); err != nil {
				t.Fatalf("write b.go: %v", err)
			}
			res, err := ValidateTask(a, "T1", ValidateOptions{Dir: a})
			if err != nil {
				t.Fatalf("ValidateTask() error = %v", err)
			}
			if res.OwnsOK != tc.wantOwnsOK {
				t.Errorf("OwnsOK = %v, want %v (outside %v, attributed %v)", res.OwnsOK, tc.wantOwnsOK, res.Outside, res.Attributed)
			}
			if tc.wantOwnsOK {
				want := wt + ": b.go -> OTHER (another ledger " + filepath.ToSlash(ledger) + ")"
				if len(res.Attributed) != 1 || res.Attributed[0] != want {
					t.Errorf("attributed = %v, want [%s]", res.Attributed, want)
				}
				return
			}
			want := wt + ": b.go"
			if len(res.Outside) != 1 || res.Outside[0] != want {
				t.Errorf("outside = %v, want [%s]", res.Outside, want)
			}
			if len(res.Attributed) != 0 {
				t.Errorf("attributed = %v, want nothing", res.Attributed)
			}
		})
	}
}
