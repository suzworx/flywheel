package flywheel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shipGit runs git in dir with LF line endings and a test identity, failing
// the test on error, and returns trimmed stdout.
func shipGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// shipWrite writes body to dir/name, creating parent directories.
func shipWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// shipFixture is a bare origin, a clone of it as the flywheel root with task
// T's worktree on fw/T, and a ledger with T planned (owns src/, the given
// gate), dispatched, finished, validated and, when inspected, inspected pass.
type shipFixture struct {
	origin, dir, wt string
}

func newShipFixture(t *testing.T, gate string, inspected bool) shipFixture {
	t.Helper()
	return newShipFixtureIssue(t, gate, inspected, 0)
}

// newShipFixtureIssue is newShipFixture with T planned from issue (0 none).
func newShipFixtureIssue(t *testing.T, gate string, inspected bool, issue int) shipFixture {
	t.Helper()
	var f shipFixture
	var base string
	f.origin, f.dir, f.wt, base = shipTemplateCopy(t)
	shipWrite(t, f.dir, "brief.txt", "owns: src/\nneeds: none\ngate: "+gate+"\n\n# TASK: T\n")
	rc := 0
	evs := []Event{
		{TS: "2026-01-01T00:00:00Z", Task: "T", Kind: "planned", Brief: "brief.txt", Issue: issue},
		{TS: "2026-01-01T00:01:00Z", Task: "T", Kind: "dispatched", Attempt: "r1", Base: base},
		{TS: "2026-01-01T00:02:00Z", Task: "T", Kind: "finished", Attempt: "r1", Reason: "stop"},
		{TS: "2026-01-01T00:03:00Z", Task: "T", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t", RC: &rc},
	}
	if inspected {
		evs = append(evs, Event{TS: "2026-01-01T00:04:00Z", Task: "T", Kind: "inspected", Attempt: "r1", Verdict: "pass", Session: "lead"})
	}
	if err := AppendEvents(f.dir, evs); err != nil {
		t.Fatal(err)
	}
	return f
}

// advance commits name=body on origin's branch through a scratch clone and
// returns the commit.
func (f shipFixture) advance(t *testing.T, branch, name, body string) string {
	t.Helper()
	c := t.TempDir()
	shipGit(t, c, "clone", "-q", f.origin, ".")
	shipGit(t, c, "checkout", "-q", "-B", branch, "origin/main")
	shipWrite(t, c, name, body)
	shipGit(t, c, "add", "-A")
	shipGit(t, c, "commit", "-q", "-m", "origin "+name)
	shipGit(t, c, "push", "-q", "origin", branch)
	return shipGit(t, c, "rev-parse", "HEAD")
}

// ship runs Ship on the fixture at a fixed instant, returning the progress.
func (f shipFixture) ship(t *testing.T, o ShipOptions) (ShipResult, string, error) {
	t.Helper()
	var b strings.Builder
	o.Progress = &b
	o.Now = func() time.Time { return time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC) }
	if o.Forge == nil {
		o.Forge = &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	}
	switch ff := o.Forge.(type) {
	case *fakeForge:
		ff.t, ff.origin = t, f.origin
	case *sigForge:
		ff.fakeForge.t, ff.fakeForge.origin = t, f.origin
	}
	if o.Sleep == nil {
		o.Sleep = func(time.Duration) {}
	}
	res, err := Ship(f.dir, "T", o)
	return res, b.String(), err
}

// shipped returns the fixture's shipped events.
func (f shipFixture) shipped(t *testing.T) []Event {
	t.Helper()
	events, err := ReadEvents(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range events {
		if e.Kind == "shipped" {
			out = append(out, e)
		}
	}
	return out
}

// TestShipLocalHappyAndResume: an uncommitted owned change is committed, a
// non-conflicting origin/main commit is merged in, the gates pass with four
// shipped events; a rerun trusts all four records and makes no commit.
func TestShipLocalHappyAndResume(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	shipWrite(t, f.wt, "src/a.go", "package src // task\n")
	up := f.advance(t, "main", "other.txt", "origin\n")
	res, out, err := f.ship(t, ShipOptions{})
	if err != nil {
		t.Fatalf("Ship: %v\n%s", err, out)
	}
	if got := shipSteps(res); got != "preflight=ok commit=ok merge-base=ok gates=ok push=ok pr=ok ci=ok merge=ok landed=ok closed=skip" {
		t.Fatalf("steps = %v\n%s", got, out)
	}
	if evs := f.shipped(t); len(evs) != len(ShipSteps) || evs[3].Step != "gates" || evs[3].Attempt != "r1" || evs[3].Commit != shipGit(t, f.dir, "rev-parse", "fw/T") {
		t.Fatalf("shipped events = %+v", evs)
	}
	shipGit(t, f.dir, "merge-base", "--is-ancestor", up, "fw/T")
	if msg := shipGit(t, f.dir, "log", "-1", "--format=%s", "fw/T^2", "--not", "fw/T^1"); msg != "" && msg != "origin other.txt" {
		t.Errorf("merged parent = %q", msg)
	}
	if subj := shipGit(t, f.dir, "log", "--format=%s", "fw/T"); !strings.Contains(subj, "T ship") {
		t.Errorf("fw/T log lacks the ship commit:\n%s", subj)
	}
	if st := shipGit(t, f.wt, "status", "--porcelain"); st != "" {
		t.Errorf("worktree not clean: %q", st)
	}
	head := shipGit(t, f.dir, "rev-parse", "fw/T")

	res, out, err = f.ship(t, ShipOptions{})
	if err != nil || strings.Count(out, "(done)") != len(ShipSteps) || len(res.Steps) != len(ShipSteps) {
		t.Fatalf("rerun: %v, steps %+v\n%s", err, res.Steps, out)
	}
	if now := shipGit(t, f.dir, "rev-parse", "fw/T"); now != head {
		t.Errorf("rerun moved fw/T %s -> %s", head, now)
	}
	if n := len(f.shipped(t)); n != len(ShipSteps) {
		t.Errorf("rerun appended shipped events: %d", n)
	}
}

// TestShipLocalPreflightRefuses: no inspected pass is a T5 refusal and a
// dirty path outside owns an owns refusal naming it; nothing else runs.
func TestShipLocalPreflightRefuses(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", false)
	shipWrite(t, f.wt, "src/a.go", "package src // task\n")
	head := shipGit(t, f.dir, "rev-parse", "fw/T")
	res, out, err := f.ship(t, ShipOptions{})
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "T5" || !strings.Contains(rr.Fix, "flywheel inspect") || len(res.Steps) != 1 {
		t.Fatalf("Ship = %+v, %v; want a T5 refusal at preflight\n%s", res.Steps, err, out)
	}
	if evs := f.shipped(t); len(evs) != 1 || evs[0].Step != "preflight" || evs[0].Result != "fail" {
		t.Errorf("shipped events = %+v", evs)
	}
	if now := shipGit(t, f.dir, "rev-parse", "fw/T"); now != head {
		t.Error("a refused preflight moved fw/T")
	}

	f = newShipFixture(t, "exit 0", true)
	shipWrite(t, f.wt, "notes.txt", "stray\n")
	res, out, err = f.ship(t, ShipOptions{})
	if !errors.As(err, &rr) || rr.Rule != "owns" || !strings.Contains(err.Error(), "notes.txt") || len(res.Steps) != 1 {
		t.Fatalf("Ship = %+v, %v; want an owns refusal naming notes.txt\n%s", res.Steps, err, out)
	}
	if !strings.Contains(out, "ship T preflight: fail outside owns: notes.txt") {
		t.Errorf("progress = %q", out)
	}
}

// TestShipLocalMergeConflict: a conflicting origin commit fails merge-base
// naming the path; the merge is aborted, the worktree clean, fw/T unchanged.
func TestShipLocalMergeConflict(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	shipWrite(t, f.wt, "src/a.go", "package src // task\n")
	f.advance(t, "main", "src/a.go", "package src // origin\n")
	res, out, err := f.ship(t, ShipOptions{})
	if err == nil || !strings.Contains(err.Error(), "src/a.go") || len(res.Steps) != 3 || res.Steps[2].Result != "fail" {
		t.Fatalf("Ship = %+v, %v; want merge-base to fail naming src/a.go\n%s", res.Steps, err, out)
	}
	if res.Steps[2].Note != "conflict: src/a.go" || res.Steps[2].Commit != res.Steps[1].Commit {
		t.Errorf("merge-base step = %+v, commit step = %+v", res.Steps[2], res.Steps[1])
	}
	if now := shipGit(t, f.dir, "rev-parse", "fw/T"); now != res.Steps[1].Commit {
		t.Errorf("fw/T = %s, want the commit step's %s", now, res.Steps[1].Commit)
	}
	if st := shipGit(t, f.wt, "status", "--porcelain"); st != "" {
		t.Errorf("worktree not clean after the aborted merge: %q", st)
	}
}

// TestShipLocalGateFails: a failing gate on the merged tree fails the gates
// step with ErrShipGates, naming the gate.
func TestShipLocalGateFails(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 1", true)
	shipWrite(t, f.wt, "src/a.go", "package src // task\n")
	res, out, err := f.ship(t, ShipOptions{})
	if !errors.Is(err, ErrShipGates) || len(res.Steps) != 4 || res.Steps[3].Result != "fail" || res.Steps[3].Note != "failing gate(s) 1" {
		t.Fatalf("Ship = %+v, %v; want gates to fail naming gate 1\n%s", res.Steps, err, out)
	}
	if res.Steps[2].Result != "skip" {
		t.Errorf("merge-base with origin unchanged = %+v, want skip", res.Steps[2])
	}
	// A rerun trusts preflight, commit and merge-base and runs the gates again.
	_, out, _ = f.ship(t, ShipOptions{})
	if strings.Count(out, "(done)") != 3 || !strings.Contains(out, "ship T gates: fail failing gate(s) 1") {
		t.Errorf("rerun progress = %q", out)
	}
}

// TestShipLocalIntegrationBranch: integration.branch main2 is what ship
// fetches and merges by default.
func TestShipLocalIntegrationBranch(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	setIntegration(t, f.dir, "main2")
	up := f.advance(t, "main2", "other.txt", "main2\n")
	res, out, err := f.ship(t, ShipOptions{})
	if err != nil || res.Integration != "main2" || res.Steps[2].Result != "ok" || !strings.Contains(res.Steps[2].Note, "origin/main2") {
		t.Fatalf("Ship = %+v, %v; want origin/main2 merged\n%s", res, err, out)
	}
	shipGit(t, f.dir, "merge-base", "--is-ancestor", up, "fw/T")
}

// fakeForge is a scripted Forge: pr is the existing PR (nil none), checks
// the successive Checks answers (the last repeats; none means no checks),
// checkErrs errors Checks returns first, one per call, and mergeState the
// state a Merge leaves the PR in (default MERGED), and onChecks, when set,
// runs on every Checks call with its 1-based number; mergedHeads is what
// MergedPRHeads answers (at most n of them), commitChecks the check runs
// CommitCheckRuns answers per commit and commitStatuses the commit statuses
// also reported on a commit, which CommitCheckRuns leaves out, as GitHub's
// check-runs API does (issue #640). It records every call.
type fakeForge struct {
	pr             *PullRequest
	checks         []ChecksState
	checkErrs      []error
	mergeState     string
	onChecks       func(call int)
	mergedHeads    []string
	commitChecks   map[string][]string
	commitStatuses map[string][]string

	created, merges, checkCalls int
	mergeTitle, mergeMsg        string
	comments                    []string
	closed                      []int
	mergedAsks, commits         []string

	// t, origin and base let Merge make a real squash commit on origin's
	// base branch (land verifies the merge commit, issue #673); shipFixture's
	// ship sets t and origin, CreatePR base (default main).
	t            *testing.T
	origin, base string
}

// squash commits fw/T's tree plus src/merged-<n>.txt onto origin's base
// branch, the way a forge's squash merge lands the PR, and returns the
// commit. Its git runs with GIT_TRACE=0: they are the forge's, not Ship's.
func (f *fakeForge) squash(n int) string {
	t := f.t
	t.Helper()
	base := f.base
	if base == "" {
		base = "main"
	}
	tmp := t.TempDir()
	env := append(os.Environ(), "GIT_TRACE=0", "GIT_INDEX_FILE="+filepath.Join(tmp, "index"),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com")
	run := func(args ...string) string {
		out, err := gitWith(f.origin, env, args...)
		if err != nil {
			t.Fatalf("forge squash: %v", err)
		}
		return out
	}
	blob := filepath.Join(tmp, "blob")
	if err := os.WriteFile(blob, []byte(fmt.Sprintf("merged #%d\n", n)), 0o644); err != nil {
		t.Fatal(err)
	}
	id := run("hash-object", "-w", blob)
	run("read-tree", "refs/heads/fw/T")
	run("update-index", "--add", "--cacheinfo", "100644,"+id+",src/merged-"+fmt.Sprint(n)+".txt")
	tree := run("write-tree")
	c := run("commit-tree", tree, "-p", "refs/heads/"+base, "-m", fmt.Sprintf("T (#%d)", n))
	run("update-ref", "refs/heads/"+base, c)
	return c
}

func (f *fakeForge) MergedPRHeads(base string, n int) ([]string, error) {
	f.mergedAsks = append(f.mergedAsks, fmt.Sprintf("%s/%d", base, n))
	return f.mergedHeads[:min(n, len(f.mergedHeads))], nil
}

func (f *fakeForge) CommitCheckRuns(sha string) ([]string, error) {
	f.commits = append(f.commits, sha)
	return f.commitChecks[sha], nil
}

func (f *fakeForge) PR(branch string) (PullRequest, bool, error) {
	if f.pr == nil {
		return PullRequest{}, false, nil
	}
	return *f.pr, true, nil
}

func (f *fakeForge) CreatePR(base, head, title, body string) (PullRequest, error) {
	f.created++
	f.base = base
	f.pr = &PullRequest{Number: 7, URL: "https://example.test/o/r/pull/7", State: "OPEN"}
	return *f.pr, nil
}

func (f *fakeForge) Checks(n int) (ChecksState, error) {
	f.checkCalls++
	if f.onChecks != nil {
		f.onChecks(f.checkCalls)
	}
	if len(f.checkErrs) > 0 {
		err := f.checkErrs[0]
		f.checkErrs = f.checkErrs[1:]
		return ChecksState{}, err
	}
	if len(f.checks) == 0 {
		return ChecksState{}, nil
	}
	c := f.checks[0]
	if len(f.checks) > 1 {
		f.checks = f.checks[1:]
	}
	return c, nil
}

func (f *fakeForge) Merge(n int, title, message string) error {
	f.merges++
	f.mergeTitle, f.mergeMsg = title, message
	f.pr.State = "MERGED"
	if f.mergeState != "" {
		f.pr.State = f.mergeState
	}
	if f.pr.State == "MERGED" {
		f.pr.MergeCommit = f.squash(n)
	}
	return nil
}

func (f *fakeForge) PRState(n int) (string, string, error) {
	return f.pr.State, f.pr.MergeCommit, nil
}

func (f *fakeForge) CommentIssue(n int, body string) error {
	f.comments = append(f.comments, fmt.Sprintf("%d: %s", n, body))
	return nil
}

func (f *fakeForge) CloseIssue(n int, comment string) error {
	f.closed = append(f.closed, n)
	return nil
}

// shipSteps is res's steps as "step=result", space separated.
func shipSteps(res ShipResult) string {
	var got []string
	for _, s := range res.Steps {
		got = append(got, s.Step+"="+s.Result)
	}
	return strings.Join(got, " ")
}

// landedCommit is the fixture's landed commit, "" when T has not landed.
func (f shipFixture) landedCommit(t *testing.T) string {
	t.Helper()
	events, err := ReadEvents(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Task == "T" && e.Kind == "landed" {
			return e.Commit
		}
	}
	return ""
}

// TestShipRemoteHappyPath (a): push, a new PR, checks pending then passed,
// merged and re-read MERGED, landed with the merge commit, the issue commented
// and closed because the generated body says Fixes #42.
func TestShipRemoteHappyPath(t *testing.T) {
	t.Parallel()
	f := newShipFixtureIssue(t, "exit 0", true, 42)
	ff := &fakeForge{checks: []ChecksState{{Pending: []string{"build"}}, {Passed: []string{"build"}}}}
	var sleeps []time.Duration
	res, out, err := f.ship(t, ShipOptions{Forge: ff, Poll: time.Millisecond, Sleep: func(d time.Duration) { sleeps = append(sleeps, d) }})
	if err != nil || shipSteps(res) != "preflight=ok commit=skip merge-base=skip gates=ok push=ok pr=ok ci=ok merge=ok landed=ok closed=ok" {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if strings.Contains(out, "requeue") {
		t.Errorf("origin/main unchanged, yet a requeue ran:\n%s", out)
	}
	if got, want := shipGit(t, f.origin, "rev-parse", "fw/T"), shipGit(t, f.dir, "rev-parse", "fw/T"); got != want {
		t.Errorf("origin fw/T = %s, want %s", got, want)
	}
	if ff.created != 1 || ff.merges != 1 || ff.mergeTitle != "T (#7)" || ff.checkCalls != 2 || len(sleeps) != 1 {
		t.Errorf("forge = %+v, sleeps %v", ff, sleeps)
	}
	if !strings.Contains(ff.mergeMsg, "Fixes #42") || !strings.Contains(ff.mergeMsg, "exit 0") {
		t.Errorf("merge message = %q", ff.mergeMsg)
	}
	if c := f.landedCommit(t); c == "" || c != ff.pr.MergeCommit || c != shipGit(t, f.origin, "rev-parse", "main") {
		t.Errorf("landed commit = %q, want the merge commit %s on origin main", c, ff.pr.MergeCommit)
	}
	if len(ff.comments) != 1 || !strings.HasPrefix(ff.comments[0], "42: Landed in #7 https://example.test/o/r/pull/7") || len(ff.closed) != 1 || ff.closed[0] != 42 {
		t.Errorf("comments %q, closed %v", ff.comments, ff.closed)
	}
}

// TestShipRemoteReusesOpenPR (b): an open PR for fw/T is reused, not created.
func TestShipRemoteReusesOpenPR(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{pr: &PullRequest{Number: 9, URL: "u9", State: "OPEN"}, checks: []ChecksState{{Passed: []string{"build"}}}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff})
	if err != nil || !strings.Contains(shipSteps(res), "pr=skip ci=ok merge=ok") || ff.created != 0 || ff.mergeTitle != "T (#9)" {
		t.Fatalf("Ship = %s, %v, forge %+v\n%s", shipSteps(res), err, ff, out)
	}
}

// TestShipRemoteEmptyChecksNeverPass (c): a PR with no checks reported is
// never green; ci times out, naming that none were reported, and nothing merges.
func TestShipRemoteEmptyChecksNeverPass(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{}
	res, out, err := f.ship(t, ShipOptions{Forge: ff, Poll: time.Millisecond, CITimeout: 5 * time.Millisecond})
	last := res.Steps[len(res.Steps)-1]
	if !errors.Is(err, ErrShipCI) || last.Step != "ci" || last.Result != "fail" || !strings.Contains(last.Note, "no checks reported") {
		t.Fatalf("Ship = %s, %v, last %+v\n%s", shipSteps(res), err, last, out)
	}
	if ff.merges != 0 || ff.checkCalls != 6 || f.landedCommit(t) != "" {
		t.Errorf("forge = %+v, landed %q", ff, f.landedCommit(t))
	}
}

// TestShipRemoteFailedCheck (d): one failed check fails ci naming it; the
// same check ignored with IgnoreChecks passes.
func TestShipRemoteFailedCheck(t *testing.T) {
	t.Parallel()
	checks := []ChecksState{{Passed: []string{"build"}, Failed: []string{"lint"}}}
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: checks}
	res, out, err := f.ship(t, ShipOptions{Forge: ff})
	last := res.Steps[len(res.Steps)-1]
	if !errors.Is(err, ErrShipCI) || last.Step != "ci" || last.Note != "failed: lint" || ff.merges != 0 {
		t.Fatalf("Ship = %s, %v, last %+v\n%s", shipSteps(res), err, last, out)
	}
	f = newShipFixture(t, "exit 0", true)
	ff = &fakeForge{checks: checks}
	res, out, err = f.ship(t, ShipOptions{Forge: ff, IgnoreChecks: []string{"lint"}})
	if err != nil || !strings.Contains(shipSteps(res), "ci=ok merge=ok") || ff.merges != 1 {
		t.Fatalf("Ship ignoring lint = %s, %v\n%s", shipSteps(res), err, out)
	}
}

// shipMergeBaseEvent is the fixture's latest shipped merge-base event.
func (f shipFixture) shipMergeBaseEvent(t *testing.T) Event {
	t.Helper()
	var last Event
	for _, e := range f.shipped(t) {
		if e.Step == "merge-base" {
			last = e
		}
	}
	return last
}

// TestShipRerunIntegrationMoved (issue #577): a ship stopped at a failed
// check, rerun after origin/main advanced, runs merge-base again and merges
// the new commit instead of trusting the stale record.
func TestShipRerunIntegrationMoved(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	main0 := shipGit(t, f.dir, "rev-parse", "origin/main")
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}, Failed: []string{"lint"}}}}
	if _, out, err := f.ship(t, ShipOptions{Forge: ff}); !errors.Is(err, ErrShipCI) {
		t.Fatalf("first ship: %v, want ErrShipCI\n%s", err, out)
	}
	if e := f.shipMergeBaseEvent(t); e.Result != "skip" || e.Base != main0 {
		t.Fatalf("first merge-base event = %+v, want skip with base %s", e, main0)
	}
	up := f.advance(t, "main", "other.txt", "origin\n")
	res, out, err := f.ship(t, ShipOptions{})
	if err != nil || shipSteps(res) != "preflight=ok commit=skip merge-base=ok gates=ok push=ok pr=ok ci=ok merge=ok landed=ok closed=skip" {
		t.Fatalf("rerun = %s, %v\n%s", shipSteps(res), err, out)
	}
	if strings.Contains(out, "merge-base: skip (done)") || !strings.Contains(out, "ship T merge-base: origin/main moved "+main0[:7]+" -> "+up[:7]+"; re-running from merge-base") {
		t.Errorf("rerun progress = %q", out)
	}
	shipGit(t, f.dir, "merge-base", "--is-ancestor", up, "fw/T")
	if e := f.shipMergeBaseEvent(t); e.Result != "ok" || e.Base != up {
		t.Errorf("rerun merge-base event = %+v, want ok with base %s", e, up)
	}
}

// TestShipRerunIntegrationUnchanged (issue #577): the same rerun with
// origin/main not advanced still trusts merge-base; a record without Base (an
// older version's) or a different current commit is cut back before it.
func TestShipRerunIntegrationUnchanged(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}, Failed: []string{"lint"}}}}
	if _, out, err := f.ship(t, ShipOptions{Forge: ff}); !errors.Is(err, ErrShipCI) {
		t.Fatalf("first ship: %v, want ErrShipCI\n%s", err, out)
	}
	ff.checks = []ChecksState{{Passed: []string{"build"}}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff})
	if err != nil || !strings.Contains(out, "ship T merge-base: skip (done)") || strings.Count(out, "(done)") != 6 || strings.Contains(out, "re-running") {
		t.Fatalf("rerun = %s, %v\n%s", shipSteps(res), err, out)
	}

	chain := []Event{{Step: "preflight"}, {Step: "commit"}, {Step: "merge-base", Base: "b1"}, {Step: "gates"}}
	if got := shipTrustBase(chain, "b1"); len(got) != 4 {
		t.Errorf("same base: chain %d, want 4", len(got))
	}
	for _, cur := range []string{"b2", ""} {
		if got := shipTrustBase(chain, cur); len(got) != 2 {
			t.Errorf("current %q: chain %d, want 2", cur, len(got))
		}
	}
	chain[2].Base = ""
	if got := shipTrustBase(chain, "b1"); len(got) != 2 {
		t.Errorf("no recorded base: chain %d, want 2", len(got))
	}
}

// TestShipTransientDNS (issue #577): name-resolution failures are retried;
// a permission error is not.
func TestShipTransientDNS(t *testing.T) {
	t.Parallel()
	for msg, want := range map[string]bool{
		"ssh: Could not resolve host: github.com": true,
		"Temporary failure in name resolution":    true,
		"permission denied":                       false,
	} {
		if got := transientErr(errors.New(msg)); got != want {
			t.Errorf("transientErr(%q) = %v, want %v", msg, got, want)
		}
	}
}

// TestShipRemoteMergeNotMerged (e): a merge call that returns nil while the
// PR stays OPEN fails merge naming the state, and nothing lands.
func TestShipRemoteMergeNotMerged(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}, mergeState: "OPEN"}
	res, out, err := f.ship(t, ShipOptions{Forge: ff})
	last := res.Steps[len(res.Steps)-1]
	if err == nil || !strings.Contains(err.Error(), "OPEN") || last.Step != "merge" || last.Result != "fail" || f.landedCommit(t) != "" {
		t.Fatalf("Ship = %s, %v, last %+v\n%s", shipSteps(res), err, last, out)
	}
}

// TestShipRemoteTransientRetry (f): a TLS handshake timeout and a connection
// reset from Checks are retried after 2s and 4s, then ci passes once a
// second poll (one Poll later) sees the same checks (issue #640).
func TestShipRemoteTransientRetry(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}},
		checkErrs: []error{errors.New("net/http: TLS handshake timeout"), errors.New("read tcp: connection reset by peer")}}
	var sleeps []time.Duration
	res, out, err := f.ship(t, ShipOptions{Forge: ff, Poll: time.Second, Sleep: func(d time.Duration) { sleeps = append(sleeps, d) }})
	if err != nil || !strings.Contains(shipSteps(res), "ci=ok merge=ok") {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if len(sleeps) != 3 || sleeps[0] != 2*time.Second || sleeps[1] != 4*time.Second || sleeps[2] != time.Second || ff.checkCalls != 4 {
		t.Errorf("sleeps %v, checks called %d", sleeps, ff.checkCalls)
	}
}

// TestShipRemoteNoMerge (g): NoMerge stops after ci with Ship ok.
func TestShipRemoteNoMerge(t *testing.T) {
	t.Parallel()
	f := newShipFixtureIssue(t, "exit 0", true, 42)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff, NoMerge: true})
	if err != nil || !strings.HasSuffix(shipSteps(res), "push=ok pr=ok ci=ok") || ff.merges != 0 || f.landedCommit(t) != "" || len(ff.comments) != 0 {
		t.Fatalf("Ship = %s, %v, forge %+v\n%s", shipSteps(res), err, ff, out)
	}
}

// TestShipRemoteBodyWithoutFixes (h): a body without Fixes #42 comments that
// part landed and leaves the issue open.
func TestShipRemoteBodyWithoutFixes(t *testing.T) {
	t.Parallel()
	f := newShipFixtureIssue(t, "exit 0", true, 42)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff, Body: "part one of #42"})
	if err != nil || !strings.HasSuffix(shipSteps(res), "closed=ok") {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if len(ff.comments) != 1 || ff.comments[0] != "42: Part of this issue landed in #7" || len(ff.closed) != 0 {
		t.Errorf("comments %q, closed %v", ff.comments, ff.closed)
	}
}

// TestShipRemoteResumeAfterMerged (i): a ship stopped after ci whose PR was
// merged since resumes at merge, which skips without a merge call, then lands
// and closes; a third run trusts every step.
func TestShipRemoteResumeAfterMerged(t *testing.T) {
	t.Parallel()
	f := newShipFixtureIssue(t, "exit 0", true, 42)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	if _, out, err := f.ship(t, ShipOptions{Forge: ff, NoMerge: true}); err != nil {
		t.Fatalf("first ship: %v\n%s", err, out)
	}
	// The squash is a real commit on origin main (land verifies it, issue
	// #673), so the resume sees origin/main moved and re-runs merge-base
	// through push; pr, ci and merge still skip on the merged PR.
	ff.pr.State, ff.pr.MergeCommit = "MERGED", ff.squash(ff.pr.Number)
	res, out, err := f.ship(t, ShipOptions{Forge: ff})
	if err != nil || !strings.HasSuffix(shipSteps(res), "pr=skip ci=skip merge=skip landed=ok closed=ok") || !strings.HasSuffix(shipSteps(res), "merge=skip landed=ok closed=ok") || ff.merges != 0 {
		t.Fatalf("resume = %s, %v, merges %d\n%s", shipSteps(res), err, ff.merges, out)
	}
	if c := f.landedCommit(t); c != ff.pr.MergeCommit || len(ff.closed) != 1 {
		t.Errorf("landed %q, closed %v", c, ff.closed)
	}
	if _, out, err = f.ship(t, ShipOptions{Forge: ff}); err != nil || strings.Count(out, "(done)") != len(ShipSteps) || ff.merges != 0 || len(ff.closed) != 1 {
		t.Errorf("third run: %v, merges %d, closed %v\n%s", err, ff.merges, ff.closed, out)
	}
}

// TestShipRemoteScrubsSessionLines (j): the squash message keeps the body but
// drops a claude.ai/code/session link and a Claude-Session trailer.
func TestShipRemoteScrubsSessionLines(t *testing.T) {
	t.Parallel()
	f := newShipFixtureIssue(t, "exit 0", true, 42)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	body := "Summary\nsee https://claude.ai/code/session_abc\nClaude-Session: xyz\n\nFixes #42\n"
	if res, out, err := f.ship(t, ShipOptions{Forge: ff, Body: body}); err != nil {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if strings.Contains(ff.mergeMsg, "claude.ai/code/session") || strings.Contains(ff.mergeMsg, "Claude-Session") ||
		!strings.Contains(ff.mergeMsg, "Summary") || !strings.Contains(ff.mergeMsg, "Fixes #42") {
		t.Errorf("merge message = %q", ff.mergeMsg)
	}
	if len(ff.closed) != 1 {
		t.Errorf("closed = %v; the body says Fixes #42", ff.closed)
	}
}

// shipRan counts the steps named step that res ran rather than trusted.
func shipRan(res ShipResult, step string) int {
	n := 0
	for _, s := range res.Steps {
		if s.Step == step && !s.Done {
			n++
		}
	}
	return n
}

// TestShipStaleRequeue (issue #591, a): origin/main advances while CI is
// polled the first time. merge refuses the stale head, records a failed merge
// event naming the move, and the default requeue re-runs merge-base (merging
// the new commit), gates, push and ci, then merges once. On the unfixed code
// Merge runs on the stale head: merge-base runs once and no failed merge is
// recorded.
func TestShipStaleRequeue(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	var up string
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}, onChecks: func(call int) {
		if call == 1 {
			up = f.advance(t, "main", "other.txt", "origin\n")
		}
	}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff})
	if err != nil || !strings.HasSuffix(shipSteps(res), "ci=ok merge=ok landed=ok closed=skip") {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if n := shipRan(res, "merge-base"); n != 2 || ff.merges != 1 {
		t.Fatalf("merge-base ran %d times, Merge called %d times; want 2 and 1\n%s", n, ff.merges, out)
	}
	shipGit(t, f.dir, "merge-base", "--is-ancestor", up, "fw/T")
	if !strings.Contains(out, "ship T merge: origin/main moved; requeue 1/2 from merge-base") {
		t.Errorf("progress lacks the requeue line:\n%s", out)
	}
	stale := false
	for _, e := range f.shipped(t) {
		if e.Step == "merge" && e.Result == "fail" && strings.Contains(e.Note, "origin/main moved to "+up[:7]+" after CI ran on ") {
			stale = true
		}
	}
	if !stale {
		t.Errorf("no failed merge event naming the move: %+v", f.shipped(t))
	}
}

// TestShipStaleRequeueExhausted (issue #591, b): origin/main advances on every
// CI poll; with Requeue 1 the second stale merge returns ErrShipStale and
// Merge is never called. On the unfixed code the first merge succeeds.
func TestShipStaleRequeueExhausted(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}, onChecks: func(call int) {
		f.advance(t, "main", fmt.Sprintf("other%d.txt", call), "origin\n")
	}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff, Requeue: 1})
	if !errors.Is(err, ErrShipStale) || ff.merges != 0 || f.landedCommit(t) != "" {
		t.Fatalf("Ship = %s, %v, merges %d; want ErrShipStale and no merge\n%s", shipSteps(res), err, ff.merges, out)
	}
	if shipRan(res, "merge-base") != 2 || shipRan(res, "merge") != 2 || !strings.Contains(out, "requeue 1/1") || strings.Contains(out, "requeue 2/") {
		t.Errorf("steps %s\n%s", shipSteps(res), out)
	}
	if last := res.Steps[len(res.Steps)-1]; last.Step != "merge" || last.Result != "fail" {
		t.Errorf("last step = %+v, want merge fail", last)
	}
}

// TestShipStaleNever (issue #591, c): Requeue -1 never requeues; the first
// stale merge returns ErrShipStale and nothing merges. On the unfixed code
// Merge is called and Ship is ok.
func TestShipStaleNever(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}, onChecks: func(call int) {
		if call == 1 {
			f.advance(t, "main", "other.txt", "origin\n")
		}
	}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff, Requeue: -1})
	if !errors.Is(err, ErrShipStale) || ff.merges != 0 || strings.Contains(out, "requeue") || shipRan(res, "merge-base") != 1 {
		t.Fatalf("Ship = %s, %v, merges %d\n%s", shipSteps(res), err, ff.merges, out)
	}
}

// shipGitBudget is the most git processes one remote ship end to end may
// start, git's own children (upload-pack, receive-pack, ...) included, as
// measured with the per-run cache in shipRun on git 2.52 (issue #619): 46,
// where a no-merge ship (push through ci) starts 36; 57 since landed fetches
// the merge commit and land verifies it (issue #673). Each git process costs
// 0.1-3s on a loaded Windows host, so the count, not the wall clock, is what
// a regression shows up in.
const shipGitBudget = 57

// TestShipGitProcessBudget ships the happy path of TestShipRemoteHappyPath
// while GIT_TRACE logs one "trace: built-in:" line per git process to a
// file, and fails when Ship started more than shipGitBudget of them.
// not parallel: GIT_TRACE is process-wide (t.Setenv, restored at cleanup), so
// every git a parallel test started would be counted too.
func TestShipGitProcessBudget(t *testing.T) {
	f := newShipFixtureIssue(t, "exit 0", true, 42)
	trace := filepath.Join(t.TempDir(), "git-trace.txt")
	t.Setenv("GIT_TRACE", trace)
	ff := &fakeForge{checks: []ChecksState{{Pending: []string{"build"}}, {Passed: []string{"build"}}}}
	res, out, err := f.ship(t, ShipOptions{Forge: ff, Poll: time.Millisecond})
	if err != nil || shipSteps(res) != "preflight=ok commit=skip merge-base=skip gates=ok push=ok pr=ok ci=ok merge=ok landed=ok closed=ok" {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	b, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(string(b), "trace: built-in: git ")
	t.Logf("git processes: %d", n)
	if n == 0 || n > shipGitBudget {
		t.Errorf("Ship started %d git processes, want 1..%d; a new uncached query or a lost forget() shows here:\n%s", n, shipGitBudget, b)
	}
}
