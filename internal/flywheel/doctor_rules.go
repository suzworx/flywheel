package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// HostRules is what the host enforces on the integration branch (issue #686):
// a pull-request rule, required status checks, and strict (branches must be
// up to date before merging). Checks is the required check contexts, sorted,
// each once.
type HostRules struct {
	PullRequest, RequiredChecks, Strict bool
	Checks                              []string
}

// runGhIn is runGhOutput with the command run in dir, so gh resolves
// {owner}/{repo} from the checkout doctor --dir names (issue #686).
func runGhIn(dir string) func(args ...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
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
}

// ReadHostRules reads branch's rules through run (issue #686): the rulesets
// that apply to it, then, only when those leave the pull-request rule or the
// required checks off, its classic branch protection. A setting is on when
// either source has it. "Branch not protected" or HTTP 404 from the classic
// endpoint means no classic protection; any other error is returned.
func ReadHostRules(branch string, run func(args ...string) ([]byte, error)) (HostRules, error) {
	var r HostRules
	checks := map[string]bool{}
	rulesPath := "repos/{owner}/{repo}/rules/branches/" + branch
	out, err := run("api", rulesPath)
	if err != nil {
		return HostRules{}, fmt.Errorf("gh api %s: %w", rulesPath, err)
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Strict bool `json:"strict_required_status_checks_policy"`
			Checks []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(out, &rules); err != nil {
		return HostRules{}, fmt.Errorf("gh api %s: %w", rulesPath, err)
	}
	for _, rule := range rules {
		switch rule.Type {
		case "pull_request":
			r.PullRequest = true
		case "required_status_checks":
			r.RequiredChecks = true
			r.Strict = r.Strict || rule.Parameters.Strict
			for _, c := range rule.Parameters.Checks {
				checks[c.Context] = true
			}
		}
	}
	if !r.PullRequest || !r.RequiredChecks {
		protPath := "repos/{owner}/{repo}/branches/" + branch + "/protection"
		out, err := run("api", protPath)
		switch {
		case err != nil && (strings.Contains(err.Error(), "Branch not protected") || strings.Contains(err.Error(), "HTTP 404")):
		case err != nil:
			return HostRules{}, fmt.Errorf("gh api %s: %w", protPath, err)
		default:
			var prot struct {
				Reviews *json.RawMessage `json:"required_pull_request_reviews"`
				Status  *struct {
					Strict   bool     `json:"strict"`
					Contexts []string `json:"contexts"`
					Checks   []struct {
						Context string `json:"context"`
					} `json:"checks"`
				} `json:"required_status_checks"`
			}
			if err := json.Unmarshal(out, &prot); err != nil {
				return HostRules{}, fmt.Errorf("gh api %s: %w", protPath, err)
			}
			if prot.Reviews != nil {
				r.PullRequest = true
			}
			if s := prot.Status; s != nil {
				r.RequiredChecks = true
				r.Strict = r.Strict || s.Strict
				for _, c := range s.Contexts {
					checks[c] = true
				}
				for _, c := range s.Checks {
					checks[c.Context] = true
				}
			}
		}
	}
	for c := range checks {
		r.Checks = append(r.Checks, c)
	}
	sort.Strings(r.Checks)
	return r, nil
}

// DoctorHostRules is doctor's host-rules line and warnings for dir's
// integration branch (issue #686): warnings only, never a refusal. A nil run
// is the real gh in dir; no integration branch returns ("", nil) with no call.
func DoctorHostRules(dir string, run func(args ...string) ([]byte, error)) (line string, warnings []string) {
	b, _ := IntegrationBranch(dir)
	if b == "" {
		return "", nil
	}
	if run == nil {
		run = runGhIn(dir)
	}
	r, err := ReadHostRules(b, run)
	var required []string
	if c, _, cerr := LoadConfig(dir); cerr == nil {
		required = c.ShipRequiredChecks()
	}
	return hostRulesReport(b, r, err, required)
}

// hostRulesReport is the host-rules line and warnings for branch b's rules r
// as read with error err, required being ship.required_checks (issue #686):
// doctor and ship's preflight print exactly this text.
func hostRulesReport(b string, r HostRules, err error, required []string) (line string, warnings []string) {
	if err != nil {
		return fmt.Sprintf("host rules: %s: inconclusive", b),
			[]string{fmt.Sprintf("host rules for %s are inconclusive (%v); check the branch's ruleset on the host by hand (#686)", b, err)}
	}
	onOff := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}
	line = fmt.Sprintf("host rules: %s: pull request %s, required checks %s, up to date %s", b, onOff(r.PullRequest), onOff(r.RequiredChecks), onOff(r.Strict))
	if !r.PullRequest {
		warnings = append(warnings, fmt.Sprintf(`%s has no pull-request rule: anyone can push to it without a PR; add a ruleset rule "Require a pull request before merging"`, b))
	}
	if !r.RequiredChecks {
		warnings = append(warnings, fmt.Sprintf(`%s has no required status checks: a PR merges with red or missing CI; add "Require status checks to pass" with the CI checks`, b))
	} else if !r.Strict {
		warnings = append(warnings, fmt.Sprintf(`%s does not require branches to be up to date before merging: two PRs each green on an old base can merge together and break %s; enable "Require branches to be up to date before merging" (strict)`, b, b))
	}
	for _, name := range required {
		if i := sort.SearchStrings(r.Checks, name); i == len(r.Checks) || r.Checks[i] != name {
			warnings = append(warnings, fmt.Sprintf("ship.required_checks names %s, which %s's rules do not require; add it to the required status checks", name, b))
		}
	}
	return line, warnings
}
