package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TrackerIssue is one issue read from a tracker (issue #457).
type TrackerIssue struct {
	Number int
	Title  string
	Body   string
	URL    string
}

// Tracker reads issues from an issue tracker; flywheel adds no Go dependency
// for it, so the first implementation shells out to gh (issue #457).
type Tracker interface {
	Issue(n int) (TrackerIssue, error)
}

// GhTracker is a Tracker backed by the gh CLI. Repo is OWNER/REPO, passed as
// --repo when set; empty uses gh's own repository resolution. Run runs gh with
// the given arguments and returns its stdout; nil runs the real gh.
type GhTracker struct {
	Repo string
	Run  func(args ...string) ([]byte, error)
}

// Issue runs `gh issue view <n> --json number,title,body,url [--repo Repo]`
// and returns the parsed issue, or an error naming the command and gh's output
// when gh fails, its output is not the JSON asked for, or the issue has no title.
func (g GhTracker) Issue(n int) (TrackerIssue, error) {
	args := []string{"issue", "view", strconv.Itoa(n), "--json", "number,title,body,url"}
	if g.Repo != "" {
		args = append(args, "--repo", g.Repo)
	}
	run := g.Run
	if run == nil {
		run = runGhOutput
	}
	cmd := "gh " + strings.Join(args, " ")
	out, err := run(args...)
	if err != nil {
		return TrackerIssue{}, fmt.Errorf("%s: %w", cmd, err)
	}
	var v struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return TrackerIssue{}, fmt.Errorf("%s: parse output: %v: %s", cmd, err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(v.Title) == "" {
		return TrackerIssue{}, fmt.Errorf("%s: issue has no title: %s", cmd, strings.TrimSpace(string(out)))
	}
	return TrackerIssue{Number: v.Number, Title: v.Title, Body: v.Body, URL: v.URL}, nil
}

// runGhOutput runs the real gh binary and returns its stdout, with its stderr
// in the error when it fails.
func runGhOutput(args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	c := exec.Command("gh", args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
