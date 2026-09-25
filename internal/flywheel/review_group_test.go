package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGroupMembers checks both group forms (issue #420): a goal id is every
// task planned with it, in order; tasks:a,b lists tasks in the ledger.
func TestGroupMembers(t *testing.T) {
	evs := []Event{
		{Task: "A", Kind: "planned", GoalID: "g1"},
		{Task: "B", Kind: "planned", GoalID: "g2"},
		{Task: "C", Kind: "planned", GoalID: "g1"},
		{Task: "A", Kind: "amended", GoalID: "g1"},
		{Task: "A", Kind: "planned", GoalID: "g1"},
	}
	got, err := GroupMembers(evs, "g1")
	if err != nil || strings.Join(got, ",") != "A,C" {
		t.Fatalf("GroupMembers(g1) = %v, %v; want [A C]", got, err)
	}
	got, err = GroupMembers(evs, "tasks:B, A,B")
	if err != nil || strings.Join(got, ",") != "B,A" {
		t.Fatalf("GroupMembers(tasks:B,A,B) = %v, %v; want [B A]", got, err)
	}
	for _, bad := range []string{"g9", "tasks:", "tasks:A,Z", "a b"} {
		if _, err := GroupMembers(evs, bad); err == nil {
			t.Errorf("GroupMembers(%q): want an error", bad)
		}
	}
	if GroupTask("tasks:a,b") != "group:tasks-a-b" || !groupTaskOK(GroupTask("g1")) || groupTaskOK("group:") {
		t.Errorf("GroupTask/groupTaskOK: got %q", GroupTask("tasks:a,b"))
	}
}

// TestGroupGates checks review.group_gates (";;" or newline separated, empty
// by default) and that each group gate runs in the tree and becomes a valid
// validated event of the group, gate g<n>.
func TestGroupGates(t *testing.T) {
	var c Config
	if v, err := c.Get("review.group_gates"); err != nil || v != "" {
		t.Fatalf("default review.group_gates = %q, %v; want empty", v, err)
	}
	if err := c.Set("review.group_gates", "go vet ./... ;; go test ./...\nmake lint;;"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(c.ReviewGroupGates(), "|"); got != "go vet ./...|go test ./...|make lint" {
		t.Fatalf("group gates = %q", got)
	}
	if v, _ := c.Get("review.group_gates"); v != "go vet ./...;;go test ./...;;make lint" {
		t.Errorf("config get review.group_gates = %q", v)
	}
	if err := c.Set("review.group_gates", ""); err != nil || len(c.ReviewGroupGates()) != 0 {
		t.Errorf("empty set: %v, %v", c.ReviewGroupGates(), err)
	}

	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gates := runGroupGates(wt, []string{"exit 0", "exit 3"})
	if len(gates) != 2 || gates[0].Gate != "g1" || gates[0].RC != 0 || gates[1].Gate != "g2" || gates[1].RC != 3 {
		t.Fatalf("runGroupGates = %+v", gates)
	}
	evs := groupGateEvents(GroupTask("g1"), "abc1234", "", "lead", gates)
	for i, e := range evs {
		if err := Validate(e); err != nil {
			t.Errorf("event %d invalid: %v", i, err)
		}
		if e.Task != "group:g1" || e.Kind != "validated" || e.Gate != gates[i].Gate || e.RC == nil || *e.RC != gates[i].RC {
			t.Errorf("event %d = %+v", i, e)
		}
	}
}

// TestRouteGroupFinding checks routing (issue #420): a finding goes to the
// first member whose effective owns contain its file, else to the group task;
// recording a review puts a blocker per conflicting path on the member whose
// merge conflicted, closes with group_reviewed and writes the group thread.
func TestRouteGroupFinding(t *testing.T) {
	gt := GroupTask("g1")
	members := []string{"A", "B"}
	owns := map[string][]string{"A": {"internal/", "!internal/b.go"}, "B": {"internal/b.go", "docs/"}}
	for file, want := range map[string]string{
		"internal/a.go": "A", "internal/b.go": "B", "docs/x.md": "B", "README.md": gt, ".flywheel/events.jsonl": gt,
	} {
		if got := routeGroupFinding(file, members, owns, gt); got != want {
			t.Errorf("routeGroupFinding(%s) = %s, want %s", file, got, want)
		}
	}

	dir := t.TempDir()
	r := GroupResult{Group: "g1", Task: gt, Round: 1, Members: members, Owns: owns, Tree: "abc1234",
		Conflicts: map[string][]string{"B": {"internal/a.go"}}, Gates: []GroupGate{{Gate: "g1", Command: "exit 0"}}}
	findings := []ReviewFinding{
		{Severity: "minor", File: "docs/x.md", Claim: "dup para", Scenario: "s"},
		{Severity: "major", File: "README.md", Claim: "flag drift", Scenario: "s"},
	}
	res, err := recordGroupReview(dir, r, "", Worker{Adapter: "claude", Model: "m"}, "rev", findings, nil)
	if err != nil {
		t.Fatalf("recordGroupReview: %v", err)
	}
	if res.Verdict != "correct" || len(res.Findings) != 3 {
		t.Fatalf("result = %s with %d findings", res.Verdict, len(res.Findings))
	}
	want := []string{"B blocker internal/a.go", "B minor docs/x.md", gt + " major README.md"}
	for i, e := range res.Findings {
		if got := e.Task + " " + e.Severity + " " + e.Path; got != want[i] || e.Category != IntegrationPersona || e.Reason != gt || e.Persona != "reviewer:integration" {
			t.Errorf("finding %d = %s (%+v), want %s", i, got, e, want[i])
		}
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Kind != "group_reviewed" || last.Task != gt || last.Verdict != "correct" || !strings.Contains(last.Note, "conflicts B: internal/a.go") {
		t.Errorf("closing event = %+v", last)
	}
	if n := len(openIntegrationFindings(evs, "B")); n != 1 {
		t.Errorf("open blocking integration findings on B = %d, want 1 (the conflict)", n)
	}
	thread, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(GroupThreadPath("g1"))))
	if err != nil || !strings.Contains(string(thread), "## Round 1 — correct") || !strings.Contains(string(thread), gt+"-r1-1 (B)") {
		t.Errorf("group thread = %s (%v)", thread, err)
	}
}

// TestIntegrationPersona checks the integration persona (issue #420): it is
// embedded and built like a panel persona, carries the one cross-unit rule,
// is never a panel dimension, and its persona name is a valid event persona.
func TestIntegrationPersona(t *testing.T) {
	p, err := personaPrompt(IntegrationPersona)
	if err != nil || !strings.HasPrefix(p, reviewPrompt) {
		t.Fatalf("personaPrompt(integration) = %v", err)
	}
	persona := strings.Join(strings.Fields(strings.TrimPrefix(p, reviewPrompt)), " ")
	for _, s := range []string{"# Your dimension: integration", "merge seams", "duplicated logic", "conflicting assumptions",
		"contract drift", "Report ONLY defects that involve more than one unit's change, or the merge itself",
		"set category to `integration`"} {
		if !strings.Contains(persona, s) {
			t.Errorf("integration persona lacks %q", s)
		}
	}
	for _, d := range PanelPersonas() {
		if d == IntegrationPersona {
			t.Error("integration is listed as a panel dimension")
		}
	}
	c := Config{Review: &ReviewConfig{Panel: []PanelMember{{Persona: IntegrationPersona}}}}
	if err := c.Validate(); err == nil {
		t.Error("review.panel accepts the integration persona")
	}
	if err := Validate(Event{Task: "A", Kind: "review_finding", Persona: "reviewer:integration", Session: "s", Title: "x", Path: "a", Severity: "nit"}); err != nil {
		t.Errorf("persona reviewer:integration refused: %v", err)
	}
	prompt := buildGroupPrompt("P", GroupResult{Group: "g1", Members: []string{"A", "B"}, Owns: map[string][]string{"A": {"a.go"}},
		Missing: []string{"C"}, Conflicts: map[string][]string{"B": {"a.go"}}}, "DIFF")
	for _, s := range []string{"- A: a.go", "- B: -", "C has no branch", "merging B conflicted in a.go", "DIFF"} {
		if !strings.Contains(prompt, s) {
			t.Errorf("group prompt lacks %q:\n%s", s, prompt)
		}
	}
}

// commitOn commits file=content on a new branch forked from main, then
// returns to main, and returns the commit.
func commitOn(t *testing.T, dir, branch, file, content string) string {
	t.Helper()
	git(t, dir, []string{"checkout", "-q", "-b", branch, "main"})
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", file})
	git(t, dir, []string{"commit", "-q", "-m", branch})
	sha := git(t, dir, []string{"rev-parse", "HEAD"})
	git(t, dir, []string{"checkout", "-q", "main"})
	return sha
}

// TestIntegrationTree merges task branches and a finished commit onto main in
// member order: different files merge cleanly, a second change to the same
// line is a recorded conflict, and a member with no work is skipped.
func TestIntegrationTree(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	git(t, dir, []string{"branch", "-M", "main"})
	commitOn(t, dir, "fw/A", "b.txt", "b\n")
	commitOn(t, dir, "fw/C", "a.go", "package c\n")
	commitOn(t, dir, "fw/D", "a.go", "package d\n")
	sha := commitOn(t, dir, "tmp-e", "e.txt", "e\n")
	git(t, dir, []string{"branch", "-q", "-D", "tmp-e"})
	evs := []Event{{Task: "E", Kind: "finished", Commit: sha}}

	wt, cleanup, conflicts, err := IntegrationTree(dir, evs, []string{"A", "C", "D", "E", "F"}, "")
	if err != nil {
		t.Fatalf("IntegrationTree: %v", err)
	}
	defer cleanup()
	for file, want := range map[string]string{"b.txt": "b\n", "a.go": "package c\n", "e.txt": "e\n"} {
		if data, err := os.ReadFile(filepath.Join(wt, file)); err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", file, data, err, want)
		}
	}
	if len(conflicts) != 1 || strings.Join(conflicts["D"], ",") != "a.go" {
		t.Errorf("conflicts = %v; want D: [a.go]", conflicts)
	}
	if st := git(t, wt, []string{"status", "--porcelain"}); st != "" {
		t.Errorf("integration tree not clean after an aborted merge: %q", st)
	}
	cleanup()
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s (%v)", wt, err)
	}
	if list := git(t, dir, []string{"worktree", "list"}); strings.Contains(list, "flywheel-group-") {
		t.Errorf("cleanup left the worktree registered:\n%s", list)
	}
}
