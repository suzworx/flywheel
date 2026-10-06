package flywheel

import (
	"encoding/json"
	"strings"
	"testing"
)

// reviewOwedBase is the planned base of the lead-built unit in these tests.
const reviewOwedBase = "0123456789abcdef0123456789abcdef01234567"

// reviewOwedEvents is a lead-built unit T planned on reviewOwedBase and
// passed by inspect with an exception over the cap.
func reviewOwedEvents() []Event {
	return []Event{
		{Task: "T", Kind: "planned", Brief: "brief.txt", Base: reviewOwedBase},
		{Task: "T", Kind: "validated", Gate: "go test ./...", Tree: "t1"},
		{Task: "T", Kind: "inspected", Verdict: "pass", Session: "lead", LeadBuilt: true, ChangedLines: 400, Exception: "workers rate-limited"},
	}
}

// agentReview is an agent reviewer's reviewed event for T.
var agentReview = Event{Task: "T", Kind: "reviewed", Verdict: "correct", Persona: "reviewer", Adapter: "claude", Session: "rev"}

// TestReviewOwed checks reviewOwed on each kind of unit.
func TestReviewOwed(t *testing.T) {
	t.Parallel()
	owed := reviewOwedEvents()
	noException := reviewOwedEvents()
	noException[2].Exception = ""
	worker := []Event{
		{Task: "T", Kind: "planned", Base: reviewOwedBase},
		{Task: "T", Kind: "dispatched", Attempt: "r1"},
		{Task: "T", Kind: "inspected", Verdict: "pass"},
	}
	noBase := reviewOwedEvents()
	noBase[0].Base = ""
	hand := Event{Task: "T", Kind: "reviewed", Verdict: "pass", Persona: "reviewer", Session: "lead"}
	// A panel member whose runs failed records crashed, with a category and
	// no adapter (crashedReview): it does not pay the debt.
	crashed := Event{Task: "T", Kind: "reviewed", Verdict: "crashed", Persona: "reviewer:security", Category: "security", Session: "rev"}
	want := "flywheel review T --agent --base " + reviewOwedBase + " --session <reviewer>"
	for _, c := range []struct {
		name   string
		events []Event
		want   string
		ok     bool
	}{
		{"exception", owed, want, true},
		{"no exception", noException, "", false},
		{"worker-built", worker, "", false},
		{"agent review", append(reviewOwedEvents(), agentReview), "", false},
		{"hand review", append(reviewOwedEvents(), hand), want, true},
		{"crashed panel member", append(reviewOwedEvents(), crashed), want, true},
		{"no base", noBase, "flywheel review T --agent --base <the commit before the unit> --session <reviewer>", true},
		{"no pass", owed[:2], "", false},
	} {
		if got, ok := reviewOwed(c.events, "T"); got != c.want || ok != c.ok {
			t.Errorf("%s: reviewOwed = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestRecoverReviewOwed checks recover's review_owed field and line, and
// that an agent review clears them.
func TestRecoverReviewOwed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recoverLedger(t, dir, reviewOwedEvents()...)
	rep, err := Recover(dir, recoverNow, RecoverOptions{})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(rep.Tasks) != 1 || !strings.HasPrefix(rep.Tasks[0].ReviewOwed, "flywheel review T --agent --base "+reviewOwedBase) {
		t.Fatalf("Tasks = %+v, want T owing a review", rep.Tasks)
	}
	if text := rep.Text(); !strings.Contains(text, "review owed: flywheel review") {
		t.Errorf("Text() lacks the review owed line:\n%s", text)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"review_owed"`) {
		t.Errorf("JSON lacks review_owed: %s", b)
	}
	recoverLedger(t, dir, agentReview)
	rep, err = Recover(dir, recoverNow, RecoverOptions{})
	if err != nil {
		t.Fatalf("Recover after review: %v", err)
	}
	if got := rep.Tasks[0].ReviewOwed; got != "" {
		t.Errorf("ReviewOwed after agent review = %q, want empty", got)
	}
	if text := rep.Text(); strings.Contains(text, "review owed") {
		t.Errorf("Text() still owes a review:\n%s", text)
	}
}

// TestNextReviewOwed checks NextActions lists a REVIEW_OWED action until an
// agent review is recorded.
func TestNextReviewOwed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recoverLedger(t, dir, reviewOwedEvents()...)
	owing := func() []Action {
		t.Helper()
		actions, err := NextActions(dir, recoverNow)
		if err != nil {
			t.Fatalf("NextActions: %v", err)
		}
		var out []Action
		for _, a := range actions {
			if a.Kind == "REVIEW_OWED" {
				out = append(out, a)
			}
		}
		return out
	}
	got := owing()
	if len(got) != 1 || got[0].Task != "T" || !strings.HasPrefix(got[0].Reason, "flywheel review T --agent --base ") {
		t.Fatalf("REVIEW_OWED actions = %+v, want one for T", got)
	}
	recoverLedger(t, dir, agentReview)
	if got := owing(); len(got) != 0 {
		t.Errorf("REVIEW_OWED actions after agent review = %+v, want none", got)
	}
}
