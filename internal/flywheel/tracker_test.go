package flywheel

import (
	"errors"
	"strings"
	"testing"
)

// TestGhTrackerParsesIssue checks an injected gh returning the issue JSON
// yields the issue, and --repo is passed only when Repo is set (issue #457).
func TestGhTrackerParsesIssue(t *testing.T) {
	t.Parallel()
	var got [][]string
	run := func(args ...string) ([]byte, error) {
		got = append(got, args)
		return []byte(`{"number":7,"title":"Add brief","body":"why\n","url":"https://github.com/o/r/issues/7"}`), nil
	}
	iss, err := GhTracker{Run: run}.Issue(7)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	want := TrackerIssue{Number: 7, Title: "Add brief", Body: "why\n", URL: "https://github.com/o/r/issues/7"}
	if iss != want {
		t.Errorf("Issue() = %+v, want %+v", iss, want)
	}
	if _, err := (GhTracker{Repo: "o/r", Run: run}).Issue(7); err != nil {
		t.Fatalf("Issue() with repo error = %v", err)
	}
	if a := strings.Join(got[0], " "); a != "issue view 7 --json number,title,body,url" {
		t.Errorf("args without repo = %q", a)
	}
	if a := strings.Join(got[1], " "); a != "issue view 7 --json number,title,body,url --repo o/r" {
		t.Errorf("args with repo = %q", a)
	}
}

// TestGhTrackerErrorsNameGh checks a gh failure and an issue with no title are
// errors naming the gh command (issue #457).
func TestGhTrackerErrorsNameGh(t *testing.T) {
	t.Parallel()
	fail := func(args ...string) ([]byte, error) { return nil, errors.New("exit status 1: could not resolve") }
	_, err := GhTracker{Run: fail}.Issue(3)
	if err == nil || !strings.Contains(err.Error(), "gh issue view 3") || !strings.Contains(err.Error(), "could not resolve") {
		t.Errorf("gh failure: err = %v, want one naming gh issue view 3 and gh's output", err)
	}
	noTitle := func(args ...string) ([]byte, error) { return []byte(`{"number":3,"body":"b"}`), nil }
	_, err = GhTracker{Run: noTitle}.Issue(3)
	if err == nil || !strings.Contains(err.Error(), "gh issue view 3") || !strings.Contains(err.Error(), "no title") {
		t.Errorf("missing title: err = %v, want one naming gh and the missing title", err)
	}
	notJSON := func(args ...string) ([]byte, error) { return []byte("nope"), nil }
	if _, err := (GhTracker{Run: notJSON}).Issue(3); err == nil || !strings.Contains(err.Error(), "gh issue view 3") {
		t.Errorf("bad JSON: err = %v, want one naming gh", err)
	}
}

// ghScript is an injected gh that records each argv and answers the first
// reply whose key prefixes the joined argv.
func ghScript(replies map[string]func() ([]byte, error)) (func(args ...string) ([]byte, error), *[]string) {
	var calls []string
	return func(args ...string) ([]byte, error) {
		a := strings.Join(args, " ")
		calls = append(calls, a)
		for k, r := range replies {
			if strings.HasPrefix(a, k) {
				return r()
			}
		}
		return nil, errors.New("unscripted: " + a)
	}, &calls
}

func ghOut(s string) func() ([]byte, error) { return func() ([]byte, error) { return []byte(s), nil } }

// TestGhForgePRs: PR, CreatePR and PRState build the gh argv (with --repo)
// and parse gh's output; no PR found or a closed one is not found.
func TestGhForgePRs(t *testing.T) {
	t.Parallel()
	run, calls := ghScript(map[string]func() ([]byte, error){
		"pr view fw/A":   ghOut(`{"number":5,"url":"https://github.com/o/r/pull/5","state":"MERGED","mergeCommit":{"oid":"abc1234"}}`),
		"pr view fw/C":   ghOut(`{"number":6,"url":"u6","state":"CLOSED","mergeCommit":null}`),
		"pr view fw/N":   func() ([]byte, error) { return nil, errors.New(`no pull requests found for branch "fw/N"`) },
		"pr create":      ghOut("Creating pull request\nhttps://github.com/o/r/pull/12\n"),
		"pr view 12 ":    ghOut(`{"number":12,"url":"u","state":"OPEN","mergeCommit":null}`),
		"issue comment ": ghOut(""),
		"issue close ":   ghOut(""),
	})
	g := GhTracker{Repo: "o/r", Run: run}
	if pr, ok, err := g.PR("fw/A"); err != nil || !ok || pr != (PullRequest{Number: 5, URL: "https://github.com/o/r/pull/5", State: "MERGED", MergeCommit: "abc1234"}) {
		t.Errorf("PR(fw/A) = %+v, %v, %v", pr, ok, err)
	}
	for _, b := range []string{"fw/C", "fw/N"} {
		if _, ok, err := g.PR(b); ok || err != nil {
			t.Errorf("PR(%s) = %v, %v; want not found", b, ok, err)
		}
	}
	if pr, err := g.CreatePR("main", "fw/A", "Ti", "Bo"); err != nil || pr.Number != 12 || pr.URL != "https://github.com/o/r/pull/12" {
		t.Errorf("CreatePR = %+v, %v", pr, err)
	}
	if st, mc, err := g.PRState(12); err != nil || st != "OPEN" || mc != "" {
		t.Errorf("PRState = %q, %q, %v", st, mc, err)
	}
	if err := g.CommentIssue(3, "hi"); err != nil {
		t.Error(err)
	}
	if err := g.CloseIssue(3, ""); err != nil {
		t.Error(err)
	}
	want := []string{
		"pr view fw/A --json number,url,state,mergeCommit --repo o/r",
		"pr view fw/C --json number,url,state,mergeCommit --repo o/r",
		"pr view fw/N --json number,url,state,mergeCommit --repo o/r",
		"pr create --base main --head fw/A --title Ti --body Bo --repo o/r",
		"pr view 12 --json number,url,state,mergeCommit --repo o/r",
		"issue comment 3 --body hi --repo o/r",
		"issue close 3 --repo o/r",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("argv =\n%s\nwant\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}

// TestGhForgeChecks: check runs and commit statuses from the rollup sort into
// pending, failed and passed; an empty rollup is all empty.
func TestGhForgeChecks(t *testing.T) {
	t.Parallel()
	run, calls := ghScript(map[string]func() ([]byte, error){
		"pr view 4 ": ghOut(`{"statusCheckRollup":[
			{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"},
			{"__typename":"CheckRun","name":"test","status":"IN_PROGRESS","conclusion":""},
			{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"CANCELLED"},
			{"__typename":"CheckRun","name":"opt","status":"COMPLETED","conclusion":"SKIPPED"},
			{"__typename":"StatusContext","context":"ci/ext","state":"FAILURE"},
			{"__typename":"StatusContext","context":"ci/wait","state":"PENDING"},
			{"__typename":"StatusContext","context":"ci/ok","state":"SUCCESS"}]}`),
		"pr view 5 ": ghOut(`{"statusCheckRollup":[]}`),
	})
	g := GhTracker{Run: run}
	cs, err := g.Checks(4)
	want := ChecksState{Pending: []string{"test", "ci/wait"}, Failed: []string{"lint", "ci/ext"}, Passed: []string{"build", "ci/ok"}}
	if err != nil || strings.Join(cs.Pending, ",") != strings.Join(want.Pending, ",") || strings.Join(cs.Failed, ",") != strings.Join(want.Failed, ",") || strings.Join(cs.Passed, ",") != strings.Join(want.Passed, ",") {
		t.Errorf("Checks(4) = %+v, %v; want %+v", cs, err, want)
	}
	if cs, err := g.Checks(5); err != nil || len(cs.Pending)+len(cs.Failed)+len(cs.Passed) != 0 {
		t.Errorf("Checks(5) = %+v, %v; want none", cs, err)
	}
	if (*calls)[0] != "pr view 4 --json statusCheckRollup" {
		t.Errorf("argv = %q", (*calls)[0])
	}
}

// TestGhForgeMergeFallback: a policy refusal from gh pr merge falls back to
// the REST merge endpoint; any other error does not.
func TestGhForgeMergeFallback(t *testing.T) {
	t.Parallel()
	run, calls := ghScript(map[string]func() ([]byte, error){
		"pr merge 8 ": func() ([]byte, error) {
			return nil, errors.New("GraphQL: Repository rule violations found: 2 of 2 required status checks are expected")
		},
		"pr merge 9 ": func() ([]byte, error) { return nil, errors.New("Pull request is not mergeable: conflicts") },
		"api -X PUT":  ghOut(`{"merged":true}`),
	})
	g := GhTracker{Repo: "o/r", Run: run}
	if err := g.Merge(8, "Ti (#8)", "Bo"); err != nil {
		t.Errorf("Merge(8) = %v, want the REST fallback to succeed", err)
	}
	if err := g.Merge(9, "Ti (#9)", "Bo"); err == nil {
		t.Error("Merge(9) = nil, want the conflict error")
	}
	want := []string{
		"pr merge 8 --squash --subject Ti (#8) --body Bo --repo o/r",
		"api -X PUT repos/o/r/pulls/8/merge -f merge_method=squash -f commit_title=Ti (#8) -f commit_message=Bo",
		"pr merge 9 --squash --subject Ti (#9) --body Bo --repo o/r",
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("argv =\n%s\nwant\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
}
