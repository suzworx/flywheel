package flywheel

import (
	"strings"
	"testing"
)

// shippedS1 runs VerifyTasks with Shipped set for task and returns its S1
// items.
func shippedS1(t *testing.T, dir, task string, shipped bool) []VerifyItem {
	t.Helper()
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{task}, Shipped: shipped})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	var out []VerifyItem
	for _, item := range res.Items {
		if item.Rule == "S1" {
			out = append(out, item)
		}
	}
	return out
}

// TestVerifyShippedPlannedOnlyFails checks issue #861: a unit that was only
// planned was never judged, so verify --shipped refuses it.
func TestVerifyShippedPlannedOnlyFails(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	items := shippedS1(t, dir, "T1", true)
	if len(items) != 1 || items[0].Pass || !strings.Contains(items[0].Reason, "never judged") || !strings.Contains(items[0].Reason, "flywheel claim-edit") {
		t.Errorf("planned-only S1 = %+v, want one failing never-judged item", items)
	}
}

func TestVerifyShippedInspectedPassPasses(t *testing.T) {
	t.Parallel()
	dir := buildCleanChain(t)
	items := shippedS1(t, dir, "T1", true)
	if len(items) != 1 || !items[0].Pass {
		t.Errorf("inspected-pass S1 = %+v, want one passing item", items)
	}
}

// TestVerifyShippedLatestVerdictCounts checks that a later non-pass verdict
// overrides an earlier pass.
func TestVerifyShippedLatestVerdictCounts(t *testing.T) {
	t.Parallel()
	dir := buildCleanChain(t)
	if err := AppendEvent(dir, Event{TS: "2099-01-01T00:00:00Z", Task: "T1", Kind: "inspected", Verdict: "rework", Session: "i1", Persona: "inspector", Tree: "t"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	items := shippedS1(t, dir, "T1", true)
	if len(items) != 1 || items[0].Pass || !strings.Contains(items[0].Reason, "verdict is rework") {
		t.Errorf("S1 after a later rework = %+v, want one failing item naming it", items)
	}
}

func TestVerifyShippedLandedPasses(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "landed"}); err != nil {
		t.Fatalf("append landed: %v", err)
	}
	items := shippedS1(t, dir, "T1", true)
	if len(items) != 1 || !items[0].Pass {
		t.Errorf("landed S1 = %+v, want one passing item", items)
	}
}

// TestVerifyShippedOffAddsNoItem checks that verify without --shipped is
// unchanged: no S1 item at all.
func TestVerifyShippedOffAddsNoItem(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if items := shippedS1(t, dir, "T1", false); len(items) != 0 {
		t.Errorf("S1 items without Shipped = %+v, want none", items)
	}
}
