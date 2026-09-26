package flywheel

import (
	"errors"
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
	f := shipFixture{origin: t.TempDir(), dir: t.TempDir()}
	shipGit(t, f.origin, "init", "-q", "--bare")
	shipGit(t, f.dir, "init", "-q")
	shipGit(t, f.dir, "config", "core.autocrlf", "false")
	shipWrite(t, f.dir, ".gitignore", ".flywheel/\nflywheel.md\n")
	shipWrite(t, f.dir, "src/a.go", "package src // v0\n")
	shipGit(t, f.dir, "add", "-A")
	shipGit(t, f.dir, "commit", "-q", "-m", "init")
	shipGit(t, f.dir, "branch", "-M", "main")
	shipGit(t, f.dir, "remote", "add", "origin", f.origin)
	shipGit(t, f.dir, "push", "-q", "origin", "main")
	shipGit(t, f.dir, "fetch", "-q", "origin")
	base := shipGit(t, f.dir, "rev-parse", "HEAD")
	f.wt = filepath.Join(f.dir, ".flywheel", "worktrees", "T")
	shipGit(t, f.dir, "worktree", "add", "-q", "-b", "fw/T", f.wt, "main")
	shipWrite(t, f.dir, "brief.txt", "owns: src/\nneeds: none\ngate: "+gate+"\n\n# TASK: T\n")
	rc := 0
	evs := []Event{
		{TS: "2026-01-01T00:00:00Z", Task: "T", Kind: "planned", Brief: "brief.txt"},
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
	var got []string
	for _, s := range res.Steps {
		got = append(got, s.Step+"="+s.Result)
	}
	if strings.Join(got, " ") != "preflight=ok commit=ok merge-base=ok gates=ok" {
		t.Fatalf("steps = %v\n%s", got, out)
	}
	if evs := f.shipped(t); len(evs) != 4 || evs[3].Step != "gates" || evs[3].Attempt != "r1" || evs[3].Commit != shipGit(t, f.dir, "rev-parse", "fw/T") {
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
	if err != nil || strings.Count(out, "(done)") != 4 || len(res.Steps) != 4 {
		t.Fatalf("rerun: %v, steps %+v\n%s", err, res.Steps, out)
	}
	if now := shipGit(t, f.dir, "rev-parse", "fw/T"); now != head {
		t.Errorf("rerun moved fw/T %s -> %s", head, now)
	}
	if n := len(f.shipped(t)); n != 4 {
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
