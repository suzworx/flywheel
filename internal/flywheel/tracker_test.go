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
