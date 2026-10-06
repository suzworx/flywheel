package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cleanAnswer is a reviewer answer with no findings.
const cleanAnswer = "Nothing wrong.\n```json\n{\"findings\": []}\n```"

// committedReviewRepo is reviewAgentRepo with the change to a.go committed:
// it returns the dir and the commit before the change.
func committedReviewRepo(t *testing.T, answer string) (string, string) {
	t.Helper()
	dir := reviewAgentRepo(t, answer)
	before := git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"add", "a.go"})
	git(t, dir, []string{"commit", "-m", "change"})
	return dir, before
}

// dispatchAt records T1's r1 dispatch with base.
func dispatchAt(t *testing.T, dir, base string) {
	t.Helper()
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:30:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1",
		Worker: "claude", Adapter: "claude", Model: "claude-sonnet-5", Session: "w1", Base: base}); err != nil {
		t.Fatalf("AppendEvent(dispatched) error = %v", err)
	}
}

// TestReviewEmptyDiff is issue #789: a dispatched unit with no change since
// its base is refused review-empty before any prompt, run or event.
func TestReviewEmptyDiff(t *testing.T) {
	// not parallel: fakeClaudeAnswer sets PATH. Its garbage answer would fail
	// the review with a transcript error, never a review-empty refusal.
	dir, _ := committedReviewRepo(t, "Looks fine to me.")
	dispatchAt(t, dir, git(t, dir, []string{"rev-parse", "HEAD"}))
	n := len(mustEvents(t, dir))
	_, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
	if err == nil || refusalRule(t, err) != "review-empty" {
		t.Fatalf("ReviewAgent() error = %v, want a review-empty refusal", err)
	}
	if !strings.Contains(err.Error(), "flywheel review T1 --agent --base <ref>") || !strings.Contains(err.Error(), "--workdir") {
		t.Errorf("refusal %q does not name the remedy", err)
	}
	reviews := filepath.Join(dir, ".flywheel", "reviews")
	for _, f := range []string{"T1.1.prompt.md", "T1.1.jsonl"} {
		if _, err := os.Stat(filepath.Join(reviews, f)); err == nil {
			t.Errorf("%s exists: the reviewer was prepared or run on an empty diff", f)
		}
	}
	if got := len(mustEvents(t, dir)); got != n {
		t.Errorf("events: %d after the refusal, want %d", got, n)
	}
}

// TestReviewBase is issue #789: --base names the range of a committed unit
// whose dispatch base is past its change; the reviewed event records it. A
// ref that does not resolve is inconclusive and records nothing.
func TestReviewBase(t *testing.T) {
	// not parallel: fakeClaudeAnswer sets PATH
	dir, before := committedReviewRepo(t, cleanAnswer)
	dispatchAt(t, dir, git(t, dir, []string{"rev-parse", "HEAD"}))
	n := len(mustEvents(t, dir))
	var inc *InconclusiveError
	if _, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1", Base: "no-such-ref"}); !errors.As(err, &inc) {
		t.Errorf("Base no-such-ref: err = %v, want an InconclusiveError", err)
	}
	if got := len(mustEvents(t, dir)); got != n {
		t.Errorf("events: %d after an unresolved base, want %d", got, n)
	}
	res, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1", Base: before[:7]})
	if err != nil {
		t.Fatalf("ReviewAgent(Base) error = %v", err)
	}
	if prompt, err := os.ReadFile(res.Prompt); err != nil || !strings.Contains(string(prompt), "func Changed()") {
		t.Errorf("prompt %s lacks the committed diff (err %v)", res.Prompt, err)
	}
	_, reviewed := reviewKinds(t, dir)
	if len(reviewed) != 1 || reviewed[0].Base != before {
		t.Errorf("reviewed = %+v, want one with Base %s", reviewed, before)
	}
}

// TestReviewLeadBuiltBase is issue #789: a committed lead-built unit is
// reviewed from the HEAD its planned event recorded, not as an empty diff.
func TestReviewLeadBuiltBase(t *testing.T) {
	// not parallel: fakeClaudeAnswer sets PATH
	dir := reviewAgentRepo(t, cleanAnswer)
	before := git(t, dir, []string{"rev-parse", "HEAD"})
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:10:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt", Base: before}); err != nil {
		t.Fatalf("AppendEvent(planned) error = %v", err)
	}
	git(t, dir, []string{"add", "a.go"})
	git(t, dir, []string{"commit", "-m", "change"})
	res, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
	if err != nil {
		t.Fatalf("ReviewAgent() error = %v", err)
	}
	if prompt, err := os.ReadFile(res.Prompt); err != nil || !strings.Contains(string(prompt), "func Changed()") {
		t.Errorf("prompt %s lacks the committed change (err %v)", res.Prompt, err)
	}
	if res.Base != before {
		t.Errorf("Base = %q, want the planned base %s", res.Base, before)
	}
}

// TestReviewPanelEmptyDiff is issue #789: a panel over an empty diff is
// refused once, before any member runs or anything is recorded.
func TestReviewPanelEmptyDiff(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	n := len(mustEvents(t, dir))
	calls := 0
	member := func(string, string, ReviewAgentOptions) (ReviewAgentResult, error) {
		calls++
		return ReviewAgentResult{}, nil
	}
	_, err = ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: []PanelMember{{Persona: "correctness"}, {Persona: "tests"}}, Session: "rev-1", review: member})
	if err == nil || refusalRule(t, err) != "review-empty" {
		t.Fatalf("ReviewPanel() error = %v, want a review-empty refusal", err)
	}
	if calls != 0 {
		t.Errorf("%d member(s) ran on an empty diff", calls)
	}
	if got := len(mustEvents(t, dir)); got != n {
		t.Errorf("events: %d after the refusal, want %d", got, n)
	}
}
