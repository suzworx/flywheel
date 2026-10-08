package flywheel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const landPRURL = "https://github.com/o/r/pull/9"

// landPRSetup makes T1 (owns a.go) and T2 (owns b.go), each dispatched and
// finished, and one squash commit changing the given paths; it returns the
// flywheel dir and the squash commit.
func landPRSetup(t *testing.T, paths ...string) (string, string) {
	t.Helper()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "brief2.txt"), []byte("owns: b.go\nneeds: none\ngate: exit 0\n\n# TASK: two\n"), 0o644); err != nil {
		t.Fatalf("write brief2: %v", err)
	}
	git(t, dir, []string{"add", "brief2.txt"})
	git(t, dir, []string{"commit", "-m", "brief2"})
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:30Z", Task: "T2", Kind: "planned", Brief: "brief2.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	for _, task := range []string{"T1", "T2"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-12T00:01:00Z", Task: task, Kind: "dispatched", Attempt: "r1", Session: "worker-" + task}); err != nil {
			t.Fatalf("append dispatched: %v", err)
		}
		logFinished(t, dir, task, "worker-"+task)
	}
	for _, p := range paths {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("package x // "+p+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		git(t, dir, []string{"add", p})
	}
	git(t, dir, []string{"commit", "-m", "squash"})
	return dir, git(t, dir, []string{"rev-parse", "HEAD"})
}

// fakePR replaces ghPRView with a PR in the given state, merged as sha, with
// one check of the given conclusion and one commit carrying the trailers.
// not parallel: callers replace the package variable ghPRView.
func fakePR(t *testing.T, state, sha, conclusion string, tasks ...string) {
	t.Helper()
	body := "squash\n\n"
	for _, task := range tasks {
		body += "Flywheel-Task: " + task + "\n"
	}
	pr := map[string]any{
		"number": 9, "url": landPRURL, "state": state,
		"mergeCommit": map[string]any{"oid": sha},
		"commits":     []map[string]any{{"oid": sha, "messageHeadline": "squash", "messageBody": body}},
		"statusCheckRollup": []map[string]any{
			{"__typename": "CheckRun", "name": "test", "status": "COMPLETED", "conclusion": conclusion},
			{"__typename": "StatusContext", "context": "ci/legacy", "state": "SUCCESS"},
		},
	}
	out, err := json.Marshal(pr)
	if err != nil {
		t.Fatal(err)
	}
	old := ghPRView
	ghPRView = func(string, int) ([]byte, error) { return out, nil }
	t.Cleanup(func() { ghPRView = old })
}

// outcomes renders a result's units as "task=outcome" in order.
func outcomes(res PRLandResult) string {
	var s []string
	for _, u := range res.Units {
		s = append(s, u.Task+"="+u.Outcome)
	}
	return strings.Join(s, ",")
}

func eventCount(t *testing.T, dir string) int {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	return len(events)
}

// not parallel: replaces the package variable ghPRView.
func TestLandPRLandsEveryUnit(t *testing.T) {
	dir, sha := landPRSetup(t, "a.go", "b.go")
	fakePR(t, "MERGED", sha, "SUCCESS", "T1", "T2", "T1")
	res, err := LandPR(dir, 9, "lead-1", "")
	if err != nil {
		t.Fatalf("LandPR() error = %v", err)
	}
	if got := outcomes(res); got != "T1=landed,T2=landed" {
		t.Fatalf("outcomes = %s (%+v), want both landed", got, res.Units)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"T1", "T2"} {
		attested, inspected, landed := false, false, false
		for _, e := range events {
			if e.Task != task {
				continue
			}
			switch {
			case e.Kind == "validated" && e.Source == "external":
				attested = e.Evidence == landPRURL && e.Commit == sha
			case e.Kind == "inspected":
				inspected = e.Verdict == "pass" && e.Commit == sha && strings.Contains(e.Note, "PR #9")
			case e.Kind == "landed":
				landed = e.Commit == sha
			}
		}
		if !attested || !inspected || !landed {
			t.Errorf("%s: attested %v, inspected %v, landed %v; want all on %s", task, attested, inspected, landed, sha)
		}
	}
	n := eventCount(t, dir)
	res, err = LandPR(dir, 9, "lead-1", "")
	if err != nil || outcomes(res) != "T1=skipped,T2=skipped" {
		t.Fatalf("rerun = %s, %v; want both skipped", outcomes(res), err)
	}
	if got := eventCount(t, dir); got != n {
		t.Errorf("rerun appended %d events, want none", got-n)
	}
}

// not parallel: replaces the package variable ghPRView.
func TestLandPRRefusesPR(t *testing.T) {
	cases := []struct{ name, state, conclusion, want string }{
		{"failing check", "MERGED", "FAILURE", "test (completed/failure)"},
		{"open", "OPEN", "SUCCESS", "not merged"},
		{"no trailers", "MERGED", "SUCCESS", "no Flywheel-Task trailer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, sha := landPRSetup(t, "a.go", "b.go")
			tasks := []string{"T1", "T2"}
			if c.name == "no trailers" {
				tasks = nil
			}
			fakePR(t, c.state, sha, c.conclusion, tasks...)
			n := eventCount(t, dir)
			_, err := LandPR(dir, 9, "lead-1", "")
			var r *RuleRefusal
			if !errors.As(err, &r) || r.Rule != "pr-evidence" || !strings.Contains(r.Fix, c.want) {
				t.Fatalf("LandPR() error = %v, want a pr-evidence refusal naming %q", err, c.want)
			}
			if got := eventCount(t, dir); got != n {
				t.Errorf("a refused PR appended %d events", got-n)
			}
		})
	}
}

// not parallel: replaces the package variable ghPRView.
func TestLandPRUnknownTaskRefusedOtherLands(t *testing.T) {
	dir, sha := landPRSetup(t, "a.go")
	fakePR(t, "MERGED", sha, "SUCCESS", "T9", "T1")
	res, err := LandPR(dir, 9, "lead-1", "")
	if err != nil || outcomes(res) != "T9=refused,T1=landed" {
		t.Fatalf("LandPR() = %s, %v; want T9 refused and T1 landed", outcomes(res), err)
	}
	if !strings.Contains(res.Units[0].Detail, "unknown task") {
		t.Errorf("T9 detail = %q, want unknown task", res.Units[0].Detail)
	}
}

// not parallel: replaces the package variable ghPRView.
func TestLandPRRefusesPathOutsideEveryUnit(t *testing.T) {
	dir, sha := landPRSetup(t, "a.go", "b.go", "c.go")
	fakePR(t, "MERGED", sha, "SUCCESS", "T1", "T2")
	res, err := LandPR(dir, 9, "lead-1", "")
	if err != nil || outcomes(res) != "T1=refused,T2=refused" {
		t.Fatalf("LandPR() = %s, %v; want both refused", outcomes(res), err)
	}
	for _, u := range res.Units {
		if !strings.HasPrefix(u.Detail, "T3") || !strings.Contains(u.Detail, "c.go") {
			t.Errorf("%s detail = %q, want a T3 refusal naming c.go", u.Task, u.Detail)
		}
	}
}
