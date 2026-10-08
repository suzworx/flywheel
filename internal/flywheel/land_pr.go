package flywheel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// PRView is the part of `gh pr view --json` LandPR reads (issue #831).
type PRView struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	State       string `json:"state"`
	MergeCommit *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
	Commits []struct {
		OID             string `json:"oid"`
		MessageHeadline string `json:"messageHeadline"`
		MessageBody     string `json:"messageBody"`
	} `json:"commits"`
	StatusCheckRollup []PRCheck `json:"statusCheckRollup"`
}

// PRCheck is one statusCheckRollup entry: a CheckRun (name, status,
// conclusion) or a StatusContext (context, state).
type PRCheck struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

// PRUnitOutcome is what LandPR did with one unit: "landed", "skipped"
// (already landed on the merge commit), "refused" or "error".
type PRUnitOutcome struct {
	Task    string
	Outcome string
	Detail  string
}

// PRLandResult is the PR LandPR read and each unit's outcome.
type PRLandResult struct {
	Number   int
	URL      string
	MergeSHA string
	Units    []PRUnitOutcome
}

// prViewFields are the fields LandPR asks gh for.
const prViewFields = "number,url,state,mergeCommit,commits,statusCheckRollup"

// ghPRView runs `gh pr view <n> --json ...` in dir; tests replace it.
var ghPRView = func(dir string, n int) ([]byte, error) {
	return runGhOutputIn(dir, "pr", "view", strconv.Itoa(n), "--json", prViewFields)
}

// runGhOutputIn is runGhOutput with the command's working directory set to
// dir, so gh resolves the repository of the flywheel root.
func runGhOutputIn(dir string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	c := exec.Command("gh", args...)
	c.Dir = dir
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// checkOK reports whether a rollup entry succeeded, and its name.
func checkOK(c PRCheck) (string, bool) {
	if c.Typename == "StatusContext" || c.Context != "" && c.Name == "" {
		return c.Context, c.State == "SUCCESS"
	}
	if c.Status != "COMPLETED" {
		return c.Name, false
	}
	switch c.Conclusion {
	case "SUCCESS", "SKIPPED", "NEUTRAL":
		return c.Name, true
	}
	return c.Name, false
}

// failingChecks names every rollup entry that did not succeed.
func failingChecks(rollup []PRCheck) []string {
	var bad []string
	for _, c := range rollup {
		name, ok := checkOK(c)
		if ok {
			continue
		}
		state := c.State
		if c.Typename != "StatusContext" && c.Context == "" {
			state = strings.ToLower(c.Status)
			if c.Conclusion != "" {
				state += "/" + strings.ToLower(c.Conclusion)
			}
		}
		bad = append(bad, fmt.Sprintf("%s (%s)", name, strings.ToLower(state)))
	}
	return bad
}

var prTrailer = regexp.MustCompile(`^Flywheel-Task:\s*([A-Za-z0-9._-]+)\s*$`)

// prUnits returns the Flywheel-Task trailer values of the PR's commits,
// de-duplicated, in first appearance order.
func prUnits(v PRView) []string {
	seen := map[string]bool{}
	var units []string
	for _, c := range v.Commits {
		for _, line := range strings.Split(c.MessageBody, "\n") {
			m := prTrailer.FindStringSubmatch(strings.TrimSpace(line))
			if m != nil && !seen[m[1]] {
				seen[m[1]] = true
				units = append(units, m[1])
			}
		}
	}
	return units
}

// LandPR lands every unit a merged, CI-green PR names in its Flywheel-Task
// trailers on the PR's merge commit (issue #831): for each unit it attests
// the merge commit with the PR url as evidence (scoped to the PR's units,
// since a squash holds all their paths), inspects that commit as a pass and
// lands it. Only a missing session, an unreadable PR, a PR that is not merged
// and green, a PR naming no unit or a merge commit that does not resolve
// locally return an error; each unit's outcome is in the result.
func LandPR(dir string, n int, session, note string) (PRLandResult, error) {
	if dir == "" {
		dir = "."
	}
	res := PRLandResult{Number: n}
	if session == "" {
		return res, &RuleRefusal{Rule: "T4", Fix: fmt.Sprintf("landing PR #%d requires the lead's --session", n)}
	}
	out, err := ghPRView(dir, n)
	if err != nil {
		return res, fmt.Errorf("gh pr view %d: %w", n, err)
	}
	var v PRView
	if err := json.Unmarshal(out, &v); err != nil {
		return res, fmt.Errorf("gh pr view %d: decode: %w", n, err)
	}
	res.URL = v.URL
	if v.MergeCommit != nil {
		res.MergeSHA = v.MergeCommit.OID
	}
	switch {
	case v.State != "MERGED":
		return res, &RuleRefusal{Rule: "pr-evidence", Fix: fmt.Sprintf("PR #%d is %s, not merged; land it after it merges", n, strings.ToLower(v.State))}
	case res.MergeSHA == "":
		return res, &RuleRefusal{Rule: "pr-evidence", Fix: fmt.Sprintf("PR #%d has no merge commit", n)}
	case len(v.StatusCheckRollup) == 0:
		return res, &RuleRefusal{Rule: "pr-evidence", Fix: fmt.Sprintf("PR #%d has no CI checks; with no checks there is no evidence to attest", n)}
	}
	if bad := failingChecks(v.StatusCheckRollup); len(bad) > 0 {
		return res, &RuleRefusal{Rule: "pr-evidence", Fix: fmt.Sprintf("PR #%d has checks that did not succeed: %s; land only a PR whose every check passed", n, strings.Join(bad, ", "))}
	}
	units := prUnits(v)
	if len(units) == 0 {
		return res, &RuleRefusal{Rule: "pr-evidence", Fix: fmt.Sprintf("PR #%d has no Flywheel-Task trailer in its commits; land its units one by one with flywheel attest, inspect --commit and land --commit", n)}
	}
	if landedTree(dir, res.MergeSHA) == "" {
		return res, &InconclusiveError{Fix: fmt.Sprintf("merge commit %s of PR #%d does not resolve in this repository; fetch the integration branch, then land again", short7(res.MergeSHA), n)}
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return res, err
	}
	inspectNote := fmt.Sprintf("landed from PR #%d", n)
	if note != "" {
		inspectNote += "; " + note
	}
	for _, task := range units {
		res.Units = append(res.Units, landPRUnit(dir, events, task, res.MergeSHA, v.URL, session, inspectNote, note, units))
	}
	return res, nil
}

// landPRUnit attests, inspects and lands one unit of a PR on its merge
// commit; any step's error stops that unit only.
func landPRUnit(dir string, events []Event, task, sha, url, session, inspectNote, note string, units []string) PRUnitOutcome {
	planned := false
	for _, e := range events {
		if e.Task == task && e.Kind == "planned" {
			planned = true
			break
		}
	}
	if !planned {
		return PRUnitOutcome{Task: task, Outcome: "refused", Detail: "unknown task: no planned event in the log"}
	}
	if landed, _, _ := landedCommit(events, task); landed == sha {
		return PRUnitOutcome{Task: task, Outcome: "skipped", Detail: "already landed " + short7(sha)}
	} else if landed != "" {
		return PRUnitOutcome{Task: task, Outcome: "refused", Detail: fmt.Sprintf("already landed on %s, not the merge commit %s", short7(landed), short7(sha))}
	}
	fail := func(err error) PRUnitOutcome {
		if errors.Is(err, ErrAlreadyLanded) {
			return PRUnitOutcome{Task: task, Outcome: "skipped", Detail: "already landed " + short7(sha)}
		}
		if IsRuleRefusal(err) {
			return PRUnitOutcome{Task: task, Outcome: "refused", Detail: err.Error()}
		}
		return PRUnitOutcome{Task: task, Outcome: "error", Detail: err.Error()}
	}
	if _, err := AttestScoped(dir, task, sha, url, session, units); err != nil {
		return fail(err)
	}
	if err := InspectTask(dir, task, InspectOptions{Dir: dir, Verdict: "pass", Commit: sha, Session: session, Note: inspectNote}); err != nil {
		return fail(err)
	}
	if err := LandTaskWithException(dir, task, sha, note, false, "", "", "", ""); err != nil {
		return fail(err)
	}
	return PRUnitOutcome{Task: task, Outcome: "landed"}
}
