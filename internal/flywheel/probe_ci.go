package flywheel

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// HeadCommit returns dir's HEAD commit id, or "" when it cannot be resolved.
func HeadCommit(dir string) string { return headCommit(dir) }

// GhIn returns a gh runner that runs in dir, so gh resolves {owner}/{repo}
// from that checkout.
func GhIn(dir string) func(args ...string) ([]byte, error) { return runGhIn(dir) }

// CICommitStatus reads sha's check runs through gh (issue #809). known is
// true only when there is at least one check run and every run has status
// completed; green is true when known and every conclusion is success,
// skipped or neutral. A gh failure returns err.
func CICommitStatus(sha string, gh func(args ...string) ([]byte, error)) (green bool, known bool, err error) {
	path := "repos/{owner}/{repo}/commits/" + sha + "/check-runs?per_page=100"
	out, err := gh("api", path)
	if err != nil {
		return false, false, fmt.Errorf("gh api %s: %w", path, err)
	}
	var resp struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return false, false, fmt.Errorf("gh api %s: parse check runs: %w", path, err)
	}
	if len(resp.CheckRuns) == 0 {
		return false, false, nil
	}
	green = true
	for _, r := range resp.CheckRuns {
		if r.Status != "completed" {
			return false, false, nil
		}
		switch r.Conclusion {
		case "success", "skipped", "neutral":
		default:
			green = false
		}
	}
	return green, true, nil
}

// HostDependentGateFindings returns lint --probe's host-dependent-gate
// findings (issue #809): a full-suite gate that fails on the base tree while
// CI is green on commit measures the host, not the code, so each such gate is
// a problem. Only full-suite gates count: red-first (#648) wants a targeted
// gate to fail on base. CI is asked once, and only when such a gate failed;
// CI unknown (no runs, still running) or a gh error warns instead, and CI red
// adds nothing. CI vouches for the commit, not the working tree: when the
// tree has uncommitted changes outside .flywheel/ and flywheel.md (or git
// status fails), each such gate warns and CI is not asked. Nothing when !on
// or commit is "".
func HostDependentGateFindings(dir string, lc *LintConfig, owns, gates []string, probes []GateProbe, commit string, on bool, gh func(args ...string) ([]byte, error)) (problems, warnings []string) {
	return hostDependentGateFindings(dir, lc, owns, gates, probes, commit, on, gh, dirtyPaths)
}

// hostDependentGateFindings is HostDependentGateFindings with the dirty-tree
// check injected, so tests need no git repo.
func hostDependentGateFindings(dir string, lc *LintConfig, owns, gates []string, probes []GateProbe, commit string, on bool, gh func(args ...string) ([]byte, error), dirty func(string) ([]string, error)) (problems, warnings []string) {
	if !on || commit == "" {
		return nil, nil
	}
	wants, err := fullSuiteWants(dir, lc, owns)
	if err != nil || len(wants) == 0 {
		return nil, nil
	}
	var failed []int
	for _, p := range probes {
		if p.RC == 0 || p.CannotStart || p.N < 1 || p.N > len(gates) {
			continue
		}
		g := gates[p.N-1]
		if slices.ContainsFunc(wants, func(w fullSuiteWant) bool { return regexp.MustCompile(w.Pattern).MatchString(g) }) {
			failed = append(failed, p.N)
		}
	}
	if len(failed) == 0 {
		return nil, nil
	}
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	paths, err := dirty(dir)
	if err != nil || len(paths) > 0 {
		for _, n := range failed {
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("gate %d fails on the base tree and the tree could not be compared with %s: %s; "+
					"rule host-dependent-gate not checked (#809)", n, short, strings.TrimSpace(err.Error())))
				continue
			}
			warnings = append(warnings, fmt.Sprintf("gate %d fails on the base tree, but the tree differs from %s (%d uncommitted path(s), e.g. %s), "+
				"so CI on that commit cannot vouch for it; rule host-dependent-gate not checked (#809)", n, short, len(paths), paths[0]))
		}
		return nil, warnings
	}
	green, known, err := CICommitStatus(commit, gh)
	reason := ""
	switch {
	case err != nil:
		reason = err.Error()
	case !known:
		reason = "no completed check runs on " + short + " (none yet, or still running)"
	case !green:
		return nil, nil
	}
	for _, n := range failed {
		if reason != "" {
			warnings = append(warnings, fmt.Sprintf("gate %d fails on the base tree and CI could not confirm the base %s: %s; "+
				"rule host-dependent-gate not checked (#809)", n, short, strings.TrimSpace(reason)))
			continue
		}
		problems = append(problems, fmt.Sprintf("gate %d fails on the base tree but CI is green on %s: the gate depends on local state "+
			"(env, database, caches); make it hermetic or run it against a fresh stack "+
			"(rule host-dependent-gate; lint.probe_ci false turns it off, #809)", n, short))
	}
	return problems, warnings
}
