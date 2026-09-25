package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// attestSetup makes a task T1 (owns a.go, the given gates) dispatched to
// worker session "worker-1", and a commit that changes the given paths; it
// returns the flywheel dir and that commit.
func attestSetup(t *testing.T, gates []string, paths ...string) (string, string) {
	t.Helper()
	dir, err := initTask(t, gates)
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "worker-1"}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}
	for _, p := range paths {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("package x // "+p+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		git(t, dir, []string{"add", p})
	}
	git(t, dir, []string{"commit", "-m", "unit"})
	return dir, git(t, dir, []string{"rev-parse", "HEAD"})
}

func TestAttestAppendsExternalReadings(t *testing.T) {
	t.Parallel()
	dir, commit := attestSetup(t, []string{"go build ./...", "go test ./..."}, "a.go")
	tree, err := Attest(dir, "T1", commit, "https://ci.example/run/7", "lead-1")
	if err != nil {
		t.Fatalf("Attest() error = %v", err)
	}
	if want := landedTree(dir, commit); tree != want || tree == "" {
		t.Errorf("Attest() tree = %q, want %q", tree, want)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	validated, owns := 0, 0
	for _, e := range events {
		if e.Source != "external" {
			continue
		}
		if e.Tree != tree || e.Commit != commit || e.Session != "lead-1" || e.Evidence != "https://ci.example/run/7" || e.Persona != "supervisor" {
			t.Errorf("external event = %+v, want tree, commit, session, evidence and persona supervisor", e)
		}
		switch e.Kind {
		case "validated":
			validated++
			if e.RC == nil || *e.RC != 0 || e.Attempt != "r1" || e.Command == "" {
				t.Errorf("validated = %+v, want rc 0, attempt r1 and the gate command", e)
			}
		case "owns_checked":
			owns++
			if len(e.Outside) != 0 {
				t.Errorf("owns_checked outside = %v, want none", e.Outside)
			}
		}
	}
	if validated != 2 || owns != 1 {
		t.Errorf("external readings: %d validated, %d owns_checked; want 2 and 1", validated, owns)
	}
	if !allReadings(events, BriefHeader{Gates: []string{"a", "b"}}, "T1", tree, latestFinished(events, "T1")) {
		t.Error("the attested tree has no complete reading")
	}
}

func TestAttestRefusesWorkerSession(t *testing.T) {
	t.Parallel()
	dir, commit := attestSetup(t, []string{"true"}, "a.go")
	_, err := Attest(dir, "T1", commit, "https://ci.example/run/7", "worker-1")
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T4" {
		t.Fatalf("Attest() error = %v, want a T4 refusal", err)
	}
	if _, err := Attest(dir, "T1", commit, "https://ci.example/run/7", ""); !errors.As(err, &r) || r.Rule != "T4" {
		t.Fatalf("Attest() without a session error = %v, want a T4 refusal", err)
	}
}

func TestAttestRefusesPathsOutsideOwns(t *testing.T) {
	t.Parallel()
	dir, commit := attestSetup(t, []string{"true"}, "a.go", "b.go")
	_, err := Attest(dir, "T1", commit, "https://ci.example/run/7", "lead-1")
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T3" || !strings.Contains(r.Fix, "b.go") {
		t.Fatalf("Attest() error = %v, want a T3 refusal naming b.go", err)
	}
	assertNoExternal(t, dir)
}

func TestAttestRefusesUnknownCommit(t *testing.T) {
	t.Parallel()
	dir, _ := attestSetup(t, []string{"true"}, "a.go")
	if _, err := Attest(dir, "T1", "deadbeefdeadbeef", "https://ci.example/run/7", "lead-1"); err == nil || !strings.Contains(err.Error(), "not in the repository") {
		t.Fatalf("Attest() error = %v, want an unknown-commit error", err)
	}
	assertNoExternal(t, dir)
}

func TestAttestRefusesEmptyEvidence(t *testing.T) {
	t.Parallel()
	dir, commit := attestSetup(t, []string{"true"}, "a.go")
	if _, err := Attest(dir, "T1", commit, "", "lead-1"); err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("Attest() error = %v, want an evidence error", err)
	}
	assertNoExternal(t, dir)
}

func TestAttestRefusesUndispatchedTask(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	commit := git(t, dir, []string{"rev-parse", "HEAD"})
	_, err = Attest(dir, "T1", commit, "https://ci.example/run/7", "lead-1")
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T5" {
		t.Fatalf("Attest() error = %v, want a T5 refusal", err)
	}
}

// assertNoExternal fails when the log holds any external reading.
func assertNoExternal(t *testing.T, dir string) {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, e := range events {
		if e.Source == "external" {
			t.Errorf("a refused attestation appended %+v", e)
		}
	}
}
