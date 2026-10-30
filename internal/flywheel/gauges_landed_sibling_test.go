package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidateLandedTaskWorktree checks a changed path in a `run --worktree`
// task worktree <L>/.flywheel/worktrees/<B> whose task B has landed is
// attributed to B when its content matches B's landed tree, in the unit's own
// ledger and in another ledger (issue #937). An edit after landing, or a
// landed event without a tree, stays outside.
func TestValidateLandedTaskWorktree(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		otherLedger bool
		emptyTree   bool
		editAfter   bool
		wantOwnsOK  bool
	}{
		{"matches landed tree", false, false, false, true},
		{"edited after landing", false, false, true, false},
		{"landed without tree", false, true, false, false},
		{"another ledger matches landed tree", true, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ledger, err := initTask(t, []string{"exit 0"})
			if err != nil {
				t.Fatalf("initTask() error = %v", err)
			}
			otherTaskPlanned(t, ledger, "B", "b.go")
			if err := AppendEvent(ledger, Event{TS: "2026-09-12T00:40:00Z", Task: "B", Kind: "dispatched", Attempt: "r1"}); err != nil {
				t.Fatalf("AppendEvent() dispatched B error = %v", err)
			}
			wt := filepath.Join(ledger, ".flywheel", "worktrees", "B")
			git(t, ledger, []string{"worktree", "add", "-b", "fw/B", wt, "HEAD"})
			unit := ledger
			if tc.otherLedger {
				// The unit's own ledger A: a second worktree of the same
				// repository, outside L's .flywheel, with a ledger of its own.
				unit = filepath.Join(t.TempDir(), "A")
				git(t, ledger, []string{"worktree", "add", "--detach", unit, "HEAD"})
				if _, err := Init(unit, false); err != nil {
					t.Fatalf("Init(A) error = %v", err)
				}
				if err := AppendEvent(unit, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
					t.Fatalf("AppendEvent() planned T1 in A error = %v", err)
				}
			}
			dispatchedWithWorktree(t, unit, wt)
			// B's work in its worktree, left uncommitted there so it stays
			// changed against the worktree's HEAD.
			if err := os.WriteFile(filepath.Join(wt, "b.go"), []byte("package b\n"), 0o644); err != nil {
				t.Fatalf("write b.go: %v", err)
			}
			// B's landed commit, built in a scratch worktree with the same
			// content, so its tree holds the blob the task worktree has.
			land := filepath.Join(t.TempDir(), "land")
			git(t, ledger, []string{"worktree", "add", "--detach", land, "HEAD"})
			if err := os.WriteFile(filepath.Join(land, "b.go"), []byte("package b\n"), 0o644); err != nil {
				t.Fatalf("write landed b.go: %v", err)
			}
			git(t, land, []string{"add", "b.go"})
			git(t, land, []string{"commit", "-m", "B"})
			tree, err := landedTree(ledger, git(t, land, []string{"rev-parse", "HEAD"}))
			if err != nil {
				t.Fatalf("landedTree() error = %v", err)
			}
			if tc.emptyTree {
				tree = ""
			}
			if err := AppendEvent(ledger, Event{TS: "2026-09-12T01:30:00Z", Task: "B", Kind: "landed", Tree: tree}); err != nil {
				t.Fatalf("AppendEvent() landed B error = %v", err)
			}
			if tc.editAfter {
				if err := os.WriteFile(filepath.Join(wt, "b.go"), []byte("package b // later\n"), 0o644); err != nil {
					t.Fatalf("edit b.go: %v", err)
				}
			}
			res, err := ValidateTask(unit, "T1", ValidateOptions{Dir: unit})
			if err != nil {
				t.Fatalf("ValidateTask() error = %v", err)
			}
			if res.OwnsOK != tc.wantOwnsOK {
				t.Errorf("OwnsOK = %v, want %v (outside %v, attributed %v)", res.OwnsOK, tc.wantOwnsOK, res.Outside, res.Attributed)
			}
			if tc.wantOwnsOK {
				want := wt + ": b.go -> B (landed)"
				if tc.otherLedger {
					want = wt + ": b.go -> B (landed, another ledger " + filepath.ToSlash(ledger) + ")"
				}
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
