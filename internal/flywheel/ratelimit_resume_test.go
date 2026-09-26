package flywheel

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resumeLedger is a dir whose task T finished rate-limited on model m with
// the given reset, under limits.rate_limit_max_wait maxWait.
func resumeLedger(t *testing.T, resetAt time.Time, maxWait string) string {
	t.Helper()
	dir := t.TempDir()
	if err := WriteConfig(dir, Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "m"}}, Limits: Limits{RateLimitMaxWait: maxWait}}); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	recoverLedger(t, dir,
		Event{Task: "T", Kind: "planned", Brief: "brief.txt"},
		Event{Task: "T", Kind: "dispatched", Attempt: "r1", Model: "m"},
		Event{Task: "T", Kind: "finished", Attempt: "r1", Model: "m", Reason: "rate-limited", ResetAt: resetAt.Format(time.RFC3339)})
	return dir
}

// TestResumeWaitPausedModel: a --resume on a model paused by a rate limit
// waits until the reset plus a minute, and says so (issue #472).
func TestResumeWaitPausedModel(t *testing.T) {
	t.Parallel()
	dir := resumeLedger(t, recoverNow.Add(2*time.Hour), "5h")
	var buf bytes.Buffer
	wait, err := resumeWait(dir, RunOptions{Task: "T", Resume: true, Progress: &buf}, recoverNow)
	if err != nil {
		t.Fatalf("resumeWait() error = %v", err)
	}
	if want := 2*time.Hour + time.Minute; wait != want {
		t.Errorf("wait = %s, want %s", wait, want)
	}
	if !strings.Contains(buf.String(), "T m is paused by a rate limit until") {
		t.Errorf("progress = %q, want the pause named", buf.String())
	}
}

// TestResumeWaitBeyondMaxWait: a reset beyond limits.rate_limit_max_wait
// refuses the resume with a rate-limit rule naming the rerun (issue #472).
func TestResumeWaitBeyondMaxWait(t *testing.T) {
	t.Parallel()
	dir := resumeLedger(t, recoverNow.Add(3*time.Hour), "1h")
	wait, err := resumeWait(dir, RunOptions{Task: "T", Resume: true}, recoverNow)
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "rate-limit" {
		t.Fatalf("resumeWait() = %s, %v; want a rate-limit RuleRefusal", wait, err)
	}
	if !strings.Contains(rr.Fix, "flywheel run T --resume") {
		t.Errorf("Fix = %q, want the rerun command", rr.Fix)
	}
}

// TestResumeWaitNone: no wait when the model is not paused, when the reset
// has passed, or when the run is not a resume (issue #472).
func TestResumeWaitNone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		reset time.Time
		o     RunOptions
	}{
		{"reset passed", recoverNow.Add(-time.Hour), RunOptions{Task: "T", Resume: true}},
		{"another model", recoverNow.Add(time.Hour), RunOptions{Task: "T", Resume: true, Model: "other"}},
		{"not a resume", recoverNow.Add(time.Hour), RunOptions{Task: "T"}},
	}
	for _, c := range cases {
		dir := resumeLedger(t, c.reset, "5h")
		wait, err := resumeWait(dir, c.o, recoverNow)
		if err != nil || wait != 0 {
			t.Errorf("%s: resumeWait() = %s, %v; want no wait", c.name, wait, err)
		}
	}
}

// deltaLedger is a dir whose task T has a planned brief.txt and finished with
// reason.
func deltaLedger(t *testing.T, reason string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte("owns: a.go\nneeds: none\ngate: go vet ./...\n\n# TASK: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recoverLedger(t, dir,
		Event{Task: "T", Kind: "planned", Brief: "brief.txt"},
		Event{Task: "T", Kind: "dispatched", Attempt: "r1", Model: "m"},
		Event{Task: "T", Kind: "finished", Attempt: "r1", Model: "m", Reason: reason})
	return dir
}

// TestResumeDeltaRateLimited: a bare --resume after a rate-limited finish
// with no delta file resumes with the next unused limit continue delta,
// which carries the brief's header (issue #472).
func TestResumeDeltaRateLimited(t *testing.T) {
	t.Parallel()
	dir := deltaLedger(t, "rate-limited")
	briefs := filepath.Join(dir, ".flywheel", "briefs")
	if err := os.MkdirAll(briefs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(briefs, "T.limit-1.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	got, err := resumeDelta(dir, RunOptions{Task: "T", Resume: true, Progress: &buf})
	if err != nil {
		t.Fatalf("resumeDelta() error = %v", err)
	}
	if want := filepath.Join(".flywheel", "briefs", "T.limit-2.txt"); got.DeltaPath != want {
		t.Fatalf("DeltaPath = %q, want %q", got.DeltaPath, want)
	}
	b, err := os.ReadFile(filepath.Join(dir, got.DeltaPath))
	if err != nil {
		t.Fatalf("read delta: %v", err)
	}
	if !strings.HasPrefix(string(b), "owns: a.go\n") || !strings.Contains(string(b), "# TASK: continue") {
		t.Errorf("delta = %q, want the brief's owns line and the continue task", b)
	}
	if !strings.Contains(buf.String(), "T: no delta given; resuming with") {
		t.Errorf("progress = %q, want the delta named", buf.String())
	}
}

// TestResumeDeltaUnchanged: a default delta file, a given --delta, another
// finish reason or a fresh run leave the options as they are (issue #472).
func TestResumeDeltaUnchanged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, reason string
		o            RunOptions
		deltaFile    bool
	}{
		{"default delta present", "rate-limited", RunOptions{Task: "T", Resume: true}, true},
		{"--delta given", "rate-limited", RunOptions{Task: "T", Resume: true, DeltaPath: "fix.txt"}, false},
		{"failed finish", "failed", RunOptions{Task: "T", Resume: true}, false},
		{"not a resume", "rate-limited", RunOptions{Task: "T"}, false},
	}
	for _, c := range cases {
		dir := deltaLedger(t, c.reason)
		if c.deltaFile {
			briefs := filepath.Join(dir, ".flywheel", "briefs")
			if err := os.MkdirAll(briefs, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(briefs, "T.delta.txt"), []byte("fix\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := resumeDelta(dir, c.o)
		if err != nil || got != c.o {
			t.Errorf("%s: resumeDelta() = %+v, %v; want %+v unchanged", c.name, got, err, c.o)
		}
	}
}

// TestRecoverNextActionWaitReset: wait-reset names the resume, which waits for
// the reset, not flywheel wait (issue #472).
func TestRecoverNextActionWaitReset(t *testing.T) {
	t.Parallel()
	n := nextAction(recoverFacts{Task: "T", Status: "finished", FinishReason: "rate-limited", PausedUntil: "2026-09-25T02:00:00Z"})
	if n.Action != "wait-reset" || n.Command != "flywheel run T --resume" {
		t.Errorf("next = %+v, want wait-reset with flywheel run T --resume", n)
	}
	if !strings.Contains(n.Reason, "waits for the reset") {
		t.Errorf("reason = %q, want it to say the resume waits for the reset", n.Reason)
	}
}
