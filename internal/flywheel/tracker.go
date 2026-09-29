package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
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

// PullRequest is one pull request on a forge (issue #457): State is OPEN,
// MERGED or CLOSED, MergeCommit the squash commit once merged.
type PullRequest struct {
	Number      int
	URL         string
	State       string
	MergeCommit string
}

// ChecksState is a pull request's checks, check runs and commit statuses
// together, by name, all reported on Head, the PR's head commit ("" when the
// forge cannot tell). Skipped holds the checks that concluded NEUTRAL or
// SKIPPED: present, neither passed nor failed. All four empty means none were
// reported.
type ChecksState struct {
	Head    string
	Pending []string
	Failed  []string
	Passed  []string
	Skipped []string
}

// Forge is the pull-request half of a tracker that `flywheel ship` drives
// (issue #457). PR returns the open or merged PR whose head is branch;
// Checks the checks on PR n's head commit; MergedPRHeads the head commits of
// the last n pull requests merged into base, newest first; CommitCheckRuns
// the names of the check runs (not commit statuses) reported on commit sha
// (issue #640).
type Forge interface {
	PR(branch string) (PullRequest, bool, error)
	CreatePR(base, head, title, body string) (PullRequest, error)
	Checks(n int) (ChecksState, error)
	MergedPRHeads(base string, n int) ([]string, error)
	CommitCheckRuns(sha string) ([]string, error)
	Merge(n int, title, message string) error
	PRState(n int) (state, mergeCommit string, err error)
	CommentIssue(n int, body string) error
	CloseIssue(n int, comment string) error
}

// gh runs gh with args, --repo Repo appended when repo is set, and returns
// its stdout, or an error naming the command.
func (g GhTracker) gh(repo bool, args ...string) ([]byte, error) {
	if repo && g.Repo != "" {
		args = append(args, "--repo", g.Repo)
	}
	run := g.Run
	if run == nil {
		run = runGhOutput
	}
	out, err := run(args...)
	if err != nil {
		return out, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// ghPR is the JSON gh pr view prints for number,url,state,mergeCommit.
type ghPR struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

func (p ghPR) pullRequest() PullRequest {
	pr := PullRequest{Number: p.Number, URL: p.URL, State: p.State}
	if p.MergeCommit != nil {
		pr.MergeCommit = p.MergeCommit.OID
	}
	return pr
}

// PR runs `gh pr view <branch> --json number,url,state,mergeCommit`; no PR,
// or only a closed one, is (_, false, nil).
func (g GhTracker) PR(branch string) (PullRequest, bool, error) {
	out, err := g.gh(true, "pr", "view", branch, "--json", "number,url,state,mergeCommit")
	if err != nil {
		if strings.Contains(err.Error(), "no pull requests found") {
			return PullRequest{}, false, nil
		}
		return PullRequest{}, false, err
	}
	var v ghPR
	if err := json.Unmarshal(out, &v); err != nil || v.Number == 0 {
		return PullRequest{}, false, fmt.Errorf("gh pr view %s: parse output: %v: %s", branch, err, strings.TrimSpace(string(out)))
	}
	if v.State == "CLOSED" {
		return PullRequest{}, false, nil
	}
	return v.pullRequest(), true, nil
}

// CreatePR runs `gh pr create --base --head --title --body` and reads the
// new PR's number from the URL gh prints.
func (g GhTracker) CreatePR(base, head, title, body string) (PullRequest, error) {
	out, err := g.gh(true, "pr", "create", "--base", base, "--head", head, "--title", title, "--body", body)
	if err != nil {
		return PullRequest{}, err
	}
	url := ""
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); strings.Contains(l, "/pull/") {
			url = l
		}
	}
	n, cerr := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if url == "" || cerr != nil {
		return PullRequest{}, fmt.Errorf("gh pr create: no PR URL in output: %s", strings.TrimSpace(string(out)))
	}
	return PullRequest{Number: n, URL: url, State: "OPEN"}, nil
}

// PRState runs `gh pr view <n> --json number,url,state,mergeCommit`.
func (g GhTracker) PRState(n int) (string, string, error) {
	out, err := g.gh(true, "pr", "view", strconv.Itoa(n), "--json", "number,url,state,mergeCommit")
	if err != nil {
		return "", "", err
	}
	var v ghPR
	if err := json.Unmarshal(out, &v); err != nil || v.State == "" {
		return "", "", fmt.Errorf("gh pr view %d: parse output: %v: %s", n, err, strings.TrimSpace(string(out)))
	}
	pr := v.pullRequest()
	return pr.State, pr.MergeCommit, nil
}

// Checks runs `gh pr view <n> --json headRefOid,statusCheckRollup`, which
// carries the head commit and, in one read, that commit's check runs
// (CheckRun: status, conclusion) and its commit statuses (StatusContext:
// state), and sorts them by name. gh pr checks is not
// used: it exits non-zero while a check is pending or failed, which a Run that
// returns only stdout on success cannot tell from gh failing. A check run not
// completed, or a status PENDING or EXPECTED, is pending; SUCCESS passes;
// NEUTRAL and SKIPPED neither pass nor fail (Skipped); anything else (FAILURE, ERROR,
// CANCELLED, TIMED_OUT, ...) fails.
func (g GhTracker) Checks(n int) (ChecksState, error) {
	out, err := g.gh(true, "pr", "view", strconv.Itoa(n), "--json", "headRefOid,statusCheckRollup")
	if err != nil {
		return ChecksState{}, err
	}
	var v struct {
		Head   string `json:"headRefOid"`
		Rollup []struct {
			Type       string `json:"__typename"`
			Name       string `json:"name"`
			Context    string `json:"context"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return ChecksState{}, fmt.Errorf("gh pr view %d: parse output: %v: %s", n, err, strings.TrimSpace(string(out)))
	}
	cs := ChecksState{Head: v.Head}
	for _, c := range v.Rollup {
		name, outcome := c.Name, c.Conclusion
		if c.Type == "StatusContext" || c.Context != "" {
			name, outcome = c.Context, c.State
			if outcome == "PENDING" || outcome == "EXPECTED" {
				outcome = ""
			}
		} else if c.Status != "COMPLETED" {
			outcome = ""
		}
		switch outcome {
		case "":
			cs.Pending = append(cs.Pending, name)
		case "SUCCESS":
			cs.Passed = append(cs.Passed, name)
		case "NEUTRAL", "SKIPPED":
			cs.Skipped = append(cs.Skipped, name)
		default:
			cs.Failed = append(cs.Failed, name)
		}
	}
	return cs, nil
}

// MergedPRHeads runs `gh pr list --base <base> --state merged --limit <n>
// --json headRefOid` and returns the head commits gh lists, newest first.
func (g GhTracker) MergedPRHeads(base string, n int) ([]string, error) {
	out, err := g.gh(true, "pr", "list", "--base", base, "--state", "merged", "--limit", strconv.Itoa(n), "--json", "headRefOid")
	if err != nil {
		return nil, err
	}
	var v []struct {
		Head string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("gh pr list --base %s: parse output: %v: %s", base, err, strings.TrimSpace(string(out)))
	}
	var heads []string
	for _, p := range v {
		if p.Head != "" {
			heads = append(heads, p.Head)
		}
	}
	return heads, nil
}

// CommitCheckRuns runs `gh api --paginate repos/{owner}/{repo}/commits/<sha>/check-runs`
// (Repo substituted when set) and returns the names of the check runs
// reported on sha, sorted, each once. Commit statuses (the /status API) are
// left out: they come from external bots that come and go (issue #640 c3).
func (g GhTracker) CommitCheckRuns(sha string) ([]string, error) {
	repo := "{owner}/{repo}"
	if g.Repo != "" {
		repo = g.Repo
	}
	out, err := g.gh(false, "api", "--paginate", "repos/"+repo+"/commits/"+sha+"/check-runs?per_page=100", "--jq", ".check_runs[].name")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// policyRefusal reports whether a gh pr merge error is the host refusing the
// high-level merge on a ruleset or branch policy, which the REST endpoint may
// still accept.
func policyRefusal(err error) bool {
	s := strings.ToLower(err.Error())
	for _, m := range []string{"base branch policy", "ruleset", "rule violation", "not allowed", "prohibited", "--admin"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// Merge squash-merges PR n with `gh pr merge --squash --subject --body`; on a
// policy refusal it falls back to `gh api -X PUT repos/{owner}/{repo}/pulls/<n>/merge`
// (Repo substituted when set). The caller re-reads the state either way.
func (g GhTracker) Merge(n int, title, message string) error {
	_, err := g.gh(true, "pr", "merge", strconv.Itoa(n), "--squash", "--subject", title, "--body", message)
	if err == nil || !policyRefusal(err) {
		return err
	}
	repo := "{owner}/{repo}"
	if g.Repo != "" {
		repo = g.Repo
	}
	if _, rerr := g.gh(false, "api", "-X", "PUT", "repos/"+repo+"/pulls/"+strconv.Itoa(n)+"/merge",
		"-f", "merge_method=squash", "-f", "commit_title="+title, "-f", "commit_message="+message); rerr != nil {
		return fmt.Errorf("%v; the REST fallback failed too: %w", err, rerr)
	}
	return nil
}

// CommentIssue runs `gh issue comment <n> --body <body>`.
func (g GhTracker) CommentIssue(n int, body string) error {
	_, err := g.gh(true, "issue", "comment", strconv.Itoa(n), "--body", body)
	return err
}

// CloseIssue runs `gh issue close <n>`, with --comment when one is given.
func (g GhTracker) CloseIssue(n int, comment string) error {
	args := []string{"issue", "close", strconv.Itoa(n)}
	if comment != "" {
		args = append(args, "--comment", comment)
	}
	_, err := g.gh(true, args...)
	return err
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
