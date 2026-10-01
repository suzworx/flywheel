package flywheel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	rulesCall   = "api repos/{owner}/{repo}/rules/branches/main"
	classicCall = "api repos/{owner}/{repo}/branches/main/protection"
)

// hostRulesRepo is a repository on main with integration.branch main and,
// when checks is non-nil, ship.required_checks set to it.
func hostRulesRepo(t *testing.T, checks []string) string {
	t.Helper()
	dir := branchRepo(t, "main")
	c := DefaultConfig()
	c.Integration = &IntegrationConfig{Branch: "main"}
	if checks != nil {
		c.Ship = &ShipConfig{RequiredChecks: checks}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", configFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func rulesJSON(strict bool, contexts ...string) string {
	var cs []string
	for _, c := range contexts {
		cs = append(cs, `{"context":"`+c+`"}`)
	}
	s := "false"
	if strict {
		s = "true"
	}
	return `[{"type":"pull_request"},{"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":` + s +
		`,"required_status_checks":[` + strings.Join(cs, ",") + `]}}]`
}

// TestDoctorHostRulesStrict: rules with a PR rule and strict checks give an
// all-on line, no warnings, and no classic-protection call.
func TestDoctorHostRulesStrict(t *testing.T) {
	t.Parallel()
	run, calls := ghScript(map[string]func() ([]byte, error){rulesCall: ghOut(rulesJSON(true, "test", "lint"))})
	line, ws := DoctorHostRules(hostRulesRepo(t, nil), run)
	if line != "host rules: main: pull request on, required checks on, up to date on" || len(ws) != 0 {
		t.Errorf("= %q, %q", line, ws)
	}
	if !reflect.DeepEqual(*calls, []string{rulesCall}) {
		t.Errorf("calls = %q, want only the rules call", *calls)
	}
	r, err := ReadHostRules("main", run)
	if err != nil || !reflect.DeepEqual(r.Checks, []string{"lint", "test"}) {
		t.Errorf("ReadHostRules = %+v, %v; want checks [lint test]", r, err)
	}
}

// TestDoctorHostRulesNotStrict: required checks without strict warn once.
func TestDoctorHostRulesNotStrict(t *testing.T) {
	t.Parallel()
	run, _ := ghScript(map[string]func() ([]byte, error){rulesCall: ghOut(rulesJSON(false, "test"))})
	line, ws := DoctorHostRules(hostRulesRepo(t, nil), run)
	if !strings.HasSuffix(line, "up to date off") || len(ws) != 1 || !strings.HasPrefix(ws[0], "main does not require branches to be up to date before merging") {
		t.Errorf("= %q, %q; want the one up-to-date warning", line, ws)
	}
}

// TestDoctorHostRulesNone: no rules and no classic protection warn about the
// PR rule, then the required checks.
func TestDoctorHostRulesNone(t *testing.T) {
	t.Parallel()
	run, _ := ghScript(map[string]func() ([]byte, error){
		rulesCall:   ghOut(`[]`),
		classicCall: func() ([]byte, error) { return nil, errors.New("exit status 1: gh: Branch not protected (HTTP 404)") },
	})
	line, ws := DoctorHostRules(hostRulesRepo(t, nil), run)
	if line != "host rules: main: pull request off, required checks off, up to date off" || len(ws) != 2 ||
		!strings.HasPrefix(ws[0], "main has no pull-request rule") || !strings.HasPrefix(ws[1], "main has no required status checks") {
		t.Errorf("= %q, %q", line, ws)
	}
}

// TestDoctorHostRulesClassicFallback: classic protection supplies the PR
// rule and strict checks when no ruleset does.
func TestDoctorHostRulesClassicFallback(t *testing.T) {
	t.Parallel()
	run, _ := ghScript(map[string]func() ([]byte, error){
		rulesCall:   ghOut(`[]`),
		classicCall: ghOut(`{"required_status_checks":{"strict":true,"contexts":["ci"]},"required_pull_request_reviews":{}}`),
	})
	line, ws := DoctorHostRules(hostRulesRepo(t, []string{"ci"}), run)
	if line != "host rules: main: pull request on, required checks on, up to date on" || len(ws) != 0 {
		t.Errorf("= %q, %q", line, ws)
	}
}

// TestDoctorHostRulesAPIError: a failing rules call is inconclusive, with
// gh's error text in the one warning.
func TestDoctorHostRulesAPIError(t *testing.T) {
	t.Parallel()
	run, _ := ghScript(map[string]func() ([]byte, error){
		rulesCall: func() ([]byte, error) { return nil, errors.New("exit status 1: HTTP 403: Resource not accessible") },
	})
	line, ws := DoctorHostRules(hostRulesRepo(t, nil), run)
	if line != "host rules: main: inconclusive" || len(ws) != 1 || !strings.Contains(ws[0], "HTTP 403: Resource not accessible") {
		t.Errorf("= %q, %q", line, ws)
	}
}

// TestDoctorHostRulesShipChecks: a ship.required_checks name the rules do
// not require warns, naming it.
func TestDoctorHostRulesShipChecks(t *testing.T) {
	t.Parallel()
	run, _ := ghScript(map[string]func() ([]byte, error){rulesCall: ghOut(rulesJSON(true, "test"))})
	_, ws := DoctorHostRules(hostRulesRepo(t, []string{"test", "e2e"}), run)
	want := "ship.required_checks names e2e, which main's rules do not require; add it to the required status checks"
	if !reflect.DeepEqual(ws, []string{want}) {
		t.Errorf("warnings = %q, want [%q]", ws, want)
	}
}

// TestDoctorHostRulesNoBranch: no integration branch means no line, no
// warnings and no gh call.
func TestDoctorHostRulesNoBranch(t *testing.T) {
	t.Parallel()
	run, calls := ghScript(nil)
	if line, ws := DoctorHostRules(t.TempDir(), run); line != "" || ws != nil || len(*calls) != 0 {
		t.Errorf("= %q, %q, calls %q", line, ws, *calls)
	}
}
