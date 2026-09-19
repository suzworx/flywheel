package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSiblingClaimsExcuseClaimedEdit validates that a lead_edit claim in a
// sibling worktree excuses the claimed path: the claim covers it while the
// sibling has a dispatched, unlanded unit, so it is attributed "lead <session>"
// and Outside is empty (issue #339).
func TestSiblingClaimsExcuseClaimedEdit(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := siblingInFlightTask(t, "B", "theirs.go")
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "lead.go"), []byte("package lead\n"), 0o644); err != nil {
		t.Fatalf("write lead.go: %v", err)
	}
	if err := AppendEvent(wt, Event{Kind: "lead_edit", Session: "lead-1", Owns: []string{"lead.go"}, Baseline: claimBaselineOf(t, wt, "lead.go")}); err != nil {
		t.Fatalf("AppendEvent() lead_edit error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK/OK = %v/%v, want true/true; outside = %v", res.OwnsOK, res.OK(), res.Outside)
	}
	if len(res.Outside) != 0 {
		t.Errorf("outside = %v, want nothing", res.Outside)
	}
	want := wt + ": lead.go -> lead lead-1"
	found := false
	for _, attr := range res.Attributed {
		if attr == want {
			found = true
		}
	}
	if !found {
		t.Errorf("attributed = %v, want it to contain %q", res.Attributed, want)
	}
}

// TestSiblingClaimsContentChangedAfterClaim validates that when the claimed
// path's content changes after the claim, the claim no longer covers it and the
// path is reported in Outside (issue #339).
func TestSiblingClaimsContentChangedAfterClaim(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := siblingInFlightTask(t, "B", "theirs.go")
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "lead.go"), []byte("package lead\n"), 0o644); err != nil {
		t.Fatalf("write lead.go: %v", err)
	}
	if err := AppendEvent(wt, Event{Kind: "lead_edit", Session: "lead-1", Owns: []string{"lead.go"}, Baseline: claimBaselineOf(t, wt, "lead.go")}); err != nil {
		t.Fatalf("AppendEvent() lead_edit error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "lead.go"), []byte("package changed\n"), 0o644); err != nil {
		t.Fatalf("rewrite lead.go: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (content changed after claim)", res.OwnsOK)
	}
	want := wt + ": lead.go"
	found := false
	for _, o := range res.Outside {
		if o == want {
			found = true
		}
	}
	if !found {
		t.Errorf("outside = %v, want it to contain %q", res.Outside, want)
	}
}

// TestSiblingClaimsNoInFlightUnit validates that when the claimed unit lands
// (no longer in-flight) before validation, the lead_edit claim no longer
// counts and the claimed path is reported in Outside (issue #339).
func TestSiblingClaimsNoInFlightUnit(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := siblingInFlightTask(t, "B", "theirs.go")
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "lead.go"), []byte("package lead\n"), 0o644); err != nil {
		t.Fatalf("write lead.go: %v", err)
	}
	if err := AppendEvent(wt, Event{Kind: "lead_edit", Session: "lead-1", Owns: []string{"lead.go"}, Baseline: claimBaselineOf(t, wt, "lead.go")}); err != nil {
		t.Fatalf("AppendEvent() lead_edit error = %v", err)
	}
	if err := AppendEvent(wt, Event{Task: "B", Kind: "landed", Commit: "abc"}); err != nil {
		t.Fatalf("AppendEvent() landed error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (unit B landed, claim no longer counts)", res.OwnsOK)
	}
	want := wt + ": lead.go"
	found := false
	for _, o := range res.Outside {
		if o == want {
			found = true
		}
	}
	if !found {
		t.Errorf("outside = %v, want it to contain %q", res.Outside, want)
	}
}

// TestSiblingClaimsWorkerSessionIgnored validates that a lead_edit claim
// whose session is a worker session of the sibling's unit does not excuse the
// path: the path is reported in Outside (issue #339).
func TestSiblingClaimsWorkerSessionIgnored(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := siblingInFlightTask(t, "B", "theirs.go")
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "lead.go"), []byte("package lead\n"), 0o644); err != nil {
		t.Fatalf("write lead.go: %v", err)
	}
	if err := AppendEvent(wt, Event{TS: "2026-09-12T01:10:00Z", Task: "B", Kind: "started", Session: "w-1"}); err != nil {
		t.Fatalf("AppendEvent() started error = %v", err)
	}
	if err := AppendEvent(wt, Event{Kind: "lead_edit", Session: "w-1", Owns: []string{"lead.go"}, Baseline: claimBaselineOf(t, wt, "lead.go")}); err != nil {
		t.Fatalf("AppendEvent() lead_edit error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (claim session is B's worker session)", res.OwnsOK)
	}
	want := wt + ": lead.go"
	found := false
	for _, o := range res.Outside {
		if o == want {
			found = true
		}
	}
	if !found {
		t.Errorf("outside = %v, want it to contain %q", res.Outside, want)
	}
}

// TestSiblingClaimsUnclaimedStillOutside validates that without a lead_edit
// claim for a path, the path is still reported in Outside: the old behaviour
// is unchanged when no claim covers it (issue #339).
func TestSiblingClaimsUnclaimedStillOutside(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := siblingInFlightTask(t, "B", "theirs.go")
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "lead.go"), []byte("package lead\n"), 0o644); err != nil {
		t.Fatalf("write lead.go: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (no lead_edit covers lead.go)", res.OwnsOK)
	}
	want := wt + ": lead.go"
	found := false
	for _, o := range res.Outside {
		if o == want {
			found = true
		}
	}
	if !found {
		t.Errorf("outside = %v, want it to contain %q", res.Outside, want)
	}
}

// TestSiblingClaimsBriefOwnerStillWins validates that the brief owner is
// tried first: when both the brief owner and a lead_edit claim cover the same
// path, it is attributed to the brief owner, not to lead (issue #339).
func TestSiblingClaimsBriefOwnerStillWins(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := siblingInFlightTask(t, "B", "theirs.go")
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "theirs.go"), []byte("package theirs\n"), 0o644); err != nil {
		t.Fatalf("write theirs.go: %v", err)
	}
	if err := AppendEvent(wt, Event{Kind: "lead_edit", Session: "lead-1", Owns: []string{"theirs.go"}, Baseline: claimBaselineOf(t, wt, "theirs.go")}); err != nil {
		t.Fatalf("AppendEvent() lead_edit error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK/OK = %v/%v, want true/true; outside = %v", res.OwnsOK, res.OK(), res.Outside)
	}
	want := wt + ": theirs.go -> B"
	found := false
	for _, attr := range res.Attributed {
		if attr == want {
			found = true
		}
	}
	if !found {
		t.Errorf("attributed = %v, want it to contain %q (brief owner B wins over lead)", res.Attributed, want)
	}
}
