package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPersonaPrompts checks the panel personas (issue #420): every dimension
// is embedded, the default panel is a subset, and each persona prompt is the
// shared review prompt plus a file naming its dimension and its category.
func TestPersonaPrompts(t *testing.T) {
	t.Parallel()
	want := []string{"contract", "correctness", "cross-os", "docs", "errors", "security", "tests"}
	if got := PanelPersonas(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("PanelPersonas() = %v, want %v", got, want)
	}
	for _, d := range DefaultPanel {
		if !personaKnown(d) {
			t.Errorf("default panel member %q is not embedded", d)
		}
	}
	for _, d := range want {
		p, err := personaPrompt(d)
		if err != nil {
			t.Fatalf("personaPrompt(%s) error = %v", d, err)
		}
		if !strings.HasPrefix(p, reviewPrompt) {
			t.Errorf("%s: prompt does not start with review_prompt.md", d)
		}
		persona := strings.Join(strings.Fields(strings.TrimPrefix(p, reviewPrompt)), " ")
		for _, s := range []string{"# Your dimension: " + d, "Report ONLY findings in the " + d + " dimension", "set category to `" + d + "`"} {
			if !strings.Contains(persona, s) {
				t.Errorf("%s: persona lacks %q", d, s)
			}
		}
	}
	if _, err := personaPrompt("style"); err == nil || !strings.Contains(err.Error(), "known: contract") {
		t.Errorf("personaPrompt(style) error = %v, want the known list", err)
	}
}

// TestPanelDimensionEnforced runs the panel against a fake claude (issue
// #420): an answer whose finding is outside the member's dimension is refused,
// retried once, and records nothing; an in-dimension answer records a
// finding with a dimension id and a reviewed event carrying the dimension;
// every member of one panel run shares a round and writes its own prompt,
// which holds its persona.
func TestPanelDimensionEnforced(t *testing.T) {
	// not parallel: reviewAgentRepo sets PATH to a fake claude
	outside := "```json\n{\"findings\":[{\"severity\":\"major\",\"category\":\"docs\",\"file\":\"a.go\",\"line\":1,\"claim\":\"c\",\"scenario\":\"s\"}]}\n```"
	dir := reviewAgentRepo(t, outside)
	// Refused twice, twice: the member crashed (issue #469), no finding kept.
	crashed, err := ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: []PanelMember{{Persona: "tests"}}, Session: "rev-1"})
	if err != nil || strings.Join(crashed.Crashed, ",") != "tests" || crashed.Matrix["tests"] != "crashed" {
		t.Fatalf("out-of-dimension answer: %+v, %v; want tests crashed", crashed, err)
	}
	if f, r := reviewKinds(t, dir); len(f) != 0 || len(r) != 1 || r[0].Verdict != "crashed" ||
		!strings.Contains(r[0].Note, `category "docs" is outside your dimension`) {
		t.Errorf("refused answer recorded findings %+v and reviewed %+v; want one crashed naming the category", f, r)
	}

	inside := strings.ReplaceAll(outside, `"docs"`, `"tests"`)
	dir = reviewAgentRepo(t, inside)
	res, err := ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: []PanelMember{{Persona: "tests"}}, Session: "rev-1"})
	if err != nil {
		t.Fatalf("ReviewPanel() error = %v", err)
	}
	findings, reviewed := reviewKinds(t, dir)
	if len(findings) != 1 || findings[0].Finding != "T1-r1-tests-1" || findings[0].Category != "tests" {
		t.Errorf("findings = %+v, want one T1-r1-tests-1", findings)
	}
	if len(reviewed) != 1 || reviewed[0].Category != "tests" || reviewed[0].Persona != "reviewer" || reviewed[0].Verdict != "correct" {
		t.Errorf("reviewed = %+v, want persona reviewer, category tests, correct", reviewed)
	}
	if res.Matrix["tests"] != "correct" {
		t.Errorf("matrix = %v, want tests correct", res.Matrix)
	}

	dir = reviewAgentRepo(t, "Clean.\n```json\n{\"findings\": []}\n```")
	res, err = ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: []PanelMember{{Persona: "tests"}, {Persona: "errors"}}, Session: "rev-1"})
	if err != nil {
		t.Fatalf("ReviewPanel() clean error = %v", err)
	}
	if res.Round != 1 || len(res.Members) != 2 || res.Matrix["tests"] != "pass" || res.Matrix["errors"] != "pass" {
		t.Errorf("clean panel = round %d, %d members, matrix %v; want round 1, 2, both pass", res.Round, len(res.Members), res.Matrix)
	}
	for _, d := range []string{"tests", "errors"} {
		data, err := os.ReadFile(filepath.Join(dir, ".flywheel", "reviews", "T1.1."+d+".prompt.md"))
		if err != nil || !strings.Contains(string(data), "# Your dimension: "+d) {
			t.Errorf("%s prompt: err %v, want it to hold the persona", d, err)
		}
	}
	if n := nextReviewRound(mustEvents(t, dir), "T1"); n != 2 {
		t.Errorf("nextReviewRound after one panel = %d, want 2", n)
	}
}

// TestVerdictMatrix checks the panel's verdict per dimension (issue #420):
// missing with no review of that dimension on the tree (another tree, a hand
// verdict and a general review do not count), the latest review's verdict
// otherwise, and correct turning pass once its blocking findings are
// dismissed; a clean review of one dimension never closes another's finding.
func TestVerdictMatrix(t *testing.T) {
	t.Parallel()
	panel := []string{"correctness", "tests", "docs"}
	blocker := ReviewFinding{Severity: "blocker", Category: "correctness", File: "a.go", Claim: "wrong", Scenario: "s"}
	var evs []Event
	evs = append(evs, panelEvents("t0", "tests")...)
	evs = append(evs, Event{Task: "T1", Kind: "reviewed", Verdict: "pass", Persona: "reviewer", Tree: "t"}) // by hand
	general, _, _ := reviewEvents("T1", "r1", 1, "rev-1", "m", "claude", "t", nil)
	evs = append(evs, general...)
	evs = append(evs, panelEvents("t", "correctness", blocker)...)
	evs = append(evs, panelEvents("t", "tests")...)
	m := VerdictMatrix(evs, "T1", "t", panel)
	if m["correctness"] != "correct" || m["tests"] != "pass" || m["docs"] != "missing" {
		t.Errorf("matrix = %v, want correctness correct, tests pass, docs missing", m)
	}
	if ids := openBlockingIDs(evs, "T1"); len(ids) != 1 || ids[0] != "T1-r1-correctness-1" {
		t.Errorf("open blocking after a clean tests review = %v, want the correctness blocker", ids)
	}
	if got := panelIncomplete(m, panel); strings.Join(got, ",") != "correctness=correct,docs=missing" {
		t.Errorf("panelIncomplete = %v", got)
	}
	if m := VerdictMatrix(evs, "T1", "t0", panel); m["tests"] != "pass" || m["correctness"] != "missing" {
		t.Errorf("matrix on t0 = %v, want tests pass, correctness missing", m)
	}
	dismissed := append(append([]Event(nil), evs...), Event{Task: "T1", Kind: "finding_response", Session: "lead-1",
		Finding: "T1-r1-correctness-1", Verdict: "disputed", Note: "dismissed: by design"})
	if m := VerdictMatrix(dismissed, "T1", "t", panel); m["correctness"] != "pass" {
		t.Errorf("after dismissal matrix = %v, want correctness pass", m)
	}
	// A later clean correctness round closes the finding and passes.
	fixed := append(append([]Event(nil), evs...), panelEvents("t", "correctness")...)
	if m := VerdictMatrix(fixed, "T1", "t", panel); m["correctness"] != "pass" {
		t.Errorf("after a clean round matrix = %v, want correctness pass", m)
	}
	if ids := openBlockingIDs(fixed, "T1"); len(ids) != 0 {
		t.Errorf("open blocking after a clean correctness round = %v, want none", ids)
	}
	if !panelReviewed(evs, "T1") || panelReviewed(general, "T1") {
		t.Error("panelReviewed: want true for panel events, false for a general review")
	}
}

// TestReviewThreadPanel checks the generated thread of a panel review (issue
// #420): one round for the whole panel, a line per member, the findings
// grouped under their dimension, and the verdict matrix.
func TestReviewThreadPanel(t *testing.T) {
	t.Parallel()
	blocker := ReviewFinding{Severity: "major", Category: "tests", File: "a.go", Claim: "no test fails", Scenario: "s"}
	var evs []Event
	evs = append(evs, panelEvents("tree1234567", "correctness")...)
	evs = append(evs, panelEvents("tree1234567", "tests", blocker)...)
	got := string(RenderReviewThread(evs, "T1"))
	for _, want := range []string{
		"## Round 1 — review panel, tree tree123",
		"- correctness: pass,",
		"- tests: correct,",
		"### tests\n\n- **T1-r1-tests-1** [major/tests]",
		"## Panel verdict matrix — tree tree123\n\n- correctness: pass\n- tests: correct\n",
		"## Open blocking findings\n\nT1-r1-tests-1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("thread lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## Round 2") {
		t.Errorf("one panel run rendered as two rounds:\n%s", got)
	}
}

// crashingMember is a fake panel member run: dimension d fails (a run
// failure) its first fails[d] calls, then records a clean review on tree t.
func crashingMember(fails map[string]int, calls map[string]int) func(string, string, ReviewAgentOptions) (ReviewAgentResult, error) {
	return func(dir, task string, o ReviewAgentOptions) (ReviewAgentResult, error) {
		calls[o.Dimension]++
		if calls[o.Dimension] <= fails[o.Dimension] {
			return ReviewAgentResult{}, &reviewRunError{"review agent claude failed (exit status 1, stream <nil>): API Error: overloaded; transcript x.jsonl"}
		}
		evs, verdict, _ := dimensionReviewEvents(task, "r1", o.Round, o.Session, "m", "claude", "t", o.Dimension, nil)
		return ReviewAgentResult{Round: o.Round, Verdict: verdict, Tree: "t"}, AppendEvents(dir, evs)
	}
}

// TestReviewPanelCrash checks a crashed member (issue #469): run once more,
// then recorded crashed while the other members still run; the matrix never
// passes it. A member that fails once and then succeeds records normally; an
// error that is not a run failure still stops the panel.
func TestReviewPanelCrash(t *testing.T) {
	t.Parallel()
	members := []PanelMember{{Persona: "correctness"}, {Persona: "tests"}, {Persona: "docs"}}
	dims := []string{"correctness", "tests", "docs"}
	dir := loopRepo(t)
	calls := map[string]int{}
	res, err := ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: members, Session: "rev-1", review: crashingMember(map[string]int{"tests": 2}, calls)})
	if err != nil || strings.Join(res.Crashed, ",") != "tests" || len(res.Members) != 2 || res.Tree != "t" {
		t.Fatalf("ReviewPanel = %+v, %v; want tests crashed, two members on t", res, err)
	}
	if calls["correctness"] != 1 || calls["tests"] != 2 || calls["docs"] != 1 {
		t.Errorf("calls = %v, want tests retried once and the others run once", calls)
	}
	var crashed []Event
	for _, e := range mustEvents(t, dir) {
		if e.Kind == "reviewed" && e.Verdict == "crashed" {
			crashed = append(crashed, e)
		}
	}
	if len(crashed) != 1 || crashed[0].Category != "tests" || crashed[0].Persona != "reviewer" || crashed[0].Session != "rev-1" ||
		crashed[0].Tree != "t" || !strings.Contains(crashed[0].Note, "API Error: overloaded") {
		t.Errorf("crashed events = %+v, want one for tests on t noting the cause", crashed)
	}
	if res.Matrix["tests"] != "crashed" || res.Matrix["correctness"] != "pass" || res.Matrix["docs"] != "pass" {
		t.Errorf("matrix = %v, want tests crashed, the others pass", res.Matrix)
	}
	if got := panelIncomplete(res.Matrix, dims); strings.Join(got, ",") != "tests=crashed" {
		t.Errorf("panelIncomplete = %v, want tests=crashed", got)
	}
	if n := nextReviewRound(mustEvents(t, dir), "T1"); n != 2 {
		t.Errorf("nextReviewRound after a panel with a crash = %d, want 2", n)
	}

	dir = loopRepo(t)
	calls = map[string]int{}
	res, err = ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: members, Session: "rev-1", review: crashingMember(map[string]int{"tests": 1}, calls)})
	if err != nil || len(res.Crashed) != 0 || len(res.Members) != 3 || calls["tests"] != 2 || res.Matrix["tests"] != "pass" {
		t.Errorf("fail once then succeed = %+v, %v, calls %v; want recorded normally", res, err, calls)
	}
	for _, e := range mustEvents(t, dir) {
		if e.Verdict == "crashed" {
			t.Errorf("a retried success recorded %+v", e)
		}
	}

	dir = loopRepo(t)
	fail := func(string, string, ReviewAgentOptions) (ReviewAgentResult, error) {
		return ReviewAgentResult{}, &RuleRefusal{Rule: "T4", Fix: "worker session"}
	}
	if _, err := ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: members, Session: "rev-1", review: fail}); err == nil || !IsRuleRefusal(err) {
		t.Errorf("rule refusal: err = %v, want it returned", err)
	}
}
