package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// requiredGatesFindings lints brief under the lint config cfg and returns the
// problems and warnings that mention lint.required_gates (issue #751).
func requiredGatesFindings(t *testing.T, cfg, brief string) (probs, warns []string) {
	t.Helper()
	res := lintWith(t, map[string]string{".flywheel/config.json": lintConfigJSON(cfg)}, brief, noGoList)
	for _, p := range res.Problems {
		if strings.Contains(p, "required_gates") {
			probs = append(probs, p)
		}
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "required_gates") {
			warns = append(warns, w)
		}
	}
	return probs, warns
}

// TestRequiredGatesPrefix: a brief owning a path under a prefix needs a gate
// matching each of its patterns; one owning nothing under it needs none
// (issue #751).
func TestRequiredGatesPrefix(t *testing.T) {
	t.Parallel()
	cfg := `{"required_gates":{"scripts/":["check:quality"]}}`
	probs, warns := requiredGatesFindings(t, cfg, "owns: scripts/a.ts\nneeds: none\ngate: npm test\n\n# TASK x\n")
	want := "no gate matches required pattern check:quality for scripts/ (lint.required_gates)"
	if !slices.Equal(probs, []string{want}) || len(warns) != 0 {
		t.Errorf("problems = %v, warnings = %v, want only the problem %q", probs, warns, want)
	}
	if probs, _ := requiredGatesFindings(t, cfg, "owns: scripts/a.ts\nneeds: none\ngate: npm test\ngate: npm run check:quality\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none with the check:quality gate", probs)
	}
	if probs, _ := requiredGatesFindings(t, cfg, "owns: src/a.ts\nneeds: none\ngate: npm test\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none: src/ is under no key", probs)
	}
}

// TestRequiredGatesAllApply: every key prefixing an owns path applies, and
// "" always does, not only the longest prefix (issue #751).
func TestRequiredGatesAllApply(t *testing.T) {
	t.Parallel()
	cfg := `{"required_gates":{"":["lint-all"],"apps/":["apps-check"],"apps/api/":["api-check"]}}`
	all := []string{"lint-all", "apps-check", "api-check"}
	head := "owns: apps/api/x.ts\nneeds: none\n"
	if probs, _ := requiredGatesFindings(t, cfg, head+"gate: echo lint-all\ngate: echo apps-check\ngate: echo api-check\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none with all three gates", probs)
	}
	for i, miss := range all {
		brief := head
		for j, g := range all {
			if j != i {
				brief += "gate: echo " + g + "\n"
			}
		}
		probs, _ := requiredGatesFindings(t, cfg, brief+"\n# TASK x\n")
		if len(probs) != 1 || !strings.Contains(probs[0], "required pattern "+miss) {
			t.Errorf("missing %s: problems = %v, want one naming it", miss, probs)
		}
	}
}

// TestRequiredGatesInvalidPattern: an invalid pattern is a lint problem
// naming lint.required_gates, from the config load or, given the config
// directly, from requiredGatesMissing (issue #751).
func TestRequiredGatesInvalidPattern(t *testing.T) {
	t.Parallel()
	probs, warns := requiredGatesFindings(t, `{"required_gates":{"scripts/":["("]}}`, "owns: scripts/a.ts\nneeds: none\ngate: echo x\n\n# TASK x\n")
	if len(probs) != 1 || len(warns) != 0 || !strings.Contains(probs[0], `lint.required_gates["scripts/"][0]: "(" is not a valid regular expression`) {
		t.Errorf("problems = %v, warnings = %v, want one problem naming lint.required_gates[\"scripts/\"][0]", probs, warns)
	}
	lc := &LintConfig{RequiredGates: map[string][]string{"scripts/": {"("}}}
	if _, err := requiredGatesMissing(lc, nil, nil); err == nil || !strings.HasPrefix(err.Error(), `config lint.required_gates["scripts/"] "(" is not a valid regular expression`) {
		t.Errorf("requiredGatesMissing() error = %v, want one naming lint.required_gates[\"scripts/\"]", err)
	}
	if r := requiredGatesRefusal(lc, nil, nil); r != nil {
		t.Errorf("requiredGatesRefusal() = %+v, want nil for an invalid pattern", r)
	}
}

// TestRequiredGatesMissingUnit: nil and empty config check nothing; misses
// are sorted by prefix then pattern and de-duplicated (issue #751).
func TestRequiredGatesMissingUnit(t *testing.T) {
	t.Parallel()
	for _, lc := range []*LintConfig{nil, {}} {
		if got, err := requiredGatesMissing(lc, nil, []string{"a.go"}); got != nil || err != nil {
			t.Errorf("requiredGatesMissing(%v) = %v, %v, want nil, nil", lc, got, err)
		}
	}
	lc := &LintConfig{RequiredGates: map[string][]string{"b/": {"zz", "aa", "zz"}, "": {"mm"}, "c/": {"cc"}}}
	got, err := requiredGatesMissing(lc, []string{"echo none"}, []string{filepath.Join("b", "x.go"), "b/y.go"})
	want := []requiredGateWant{{"", "mm"}, {"b/", "aa"}, {"b/", "zz"}}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("requiredGatesMissing() = %v, %v, want %v", got, err, want)
	}
	if s := want[0].String(); s != "no gate matches required pattern mm" {
		t.Errorf("String() = %q", s)
	}
}

// requiredGatesLint requires a quality-marker gate on every brief (issue #751).
var requiredGatesLint = LintConfig{RequiredGates: map[string][]string{"": {"quality-marker"}}}

// TestRequiredGatesRunRefuses: run refuses a brief missing a required gate
// with rule required-gates, appending one dispatch_refused (issue #751).
func TestRequiredGatesRunRefuses(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go\nneeds: none\ngate: echo x\n\n# TASK x\n")
	cfg := simConfig(fixturePath("clean.jsonl", t))
	lc := requiredGatesLint
	cfg.Lint = &lc
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, RunOptions{Task: "T1"}); !errors.As(err, &rf) || rf.Rule != "required-gates" || !strings.Contains(rf.Fix, "no gate matches required pattern quality-marker") {
		t.Fatalf("Run() error = %v, want a required-gates refusal naming quality-marker", err)
	}
	wantRefusedAppended(t, dir, before, "T1", "required-gates")
}

// TestRequiredGatesValidateRefuses: validate refuses a brief missing a
// required gate before any gate runs, and runs the gates of one that has it
// (issue #751).
func TestRequiredGatesValidateRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		gate   string
		refuse bool
	}{{"echo x > marker.txt", true}, {"echo quality-marker > marker.txt", false}} {
		dir, err := initTask(t, []string{tc.gate})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		cfg := simConfig(fixturePath("clean.jsonl", t))
		lc := requiredGatesLint
		cfg.Lint = &lc
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		var rf *RuleRefusal
		_, statErr := os.Stat(filepath.Join(dir, "marker.txt"))
		if tc.refuse {
			if !errors.As(err, &rf) || rf.Rule != "required-gates" || !strings.Contains(rf.Fix, "quality-marker") {
				t.Errorf("%s: ValidateTask() error = %v, want a required-gates RuleRefusal", tc.gate, err)
			}
			if statErr == nil {
				t.Errorf("%s: gate ran despite the refusal", tc.gate)
			}
			continue
		}
		if err != nil || len(res.Gates) != 1 || statErr != nil {
			t.Errorf("%s: ValidateTask() = %+v, %v (marker %v), want the gate run", tc.gate, res.Gates, err, statErr)
		}
	}
}
