package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// requiredLiveGatesProblems lints brief under the lint config cfg and returns
// the problems that mention lint.required_live_gates (issue #845).
func requiredLiveGatesProblems(t *testing.T, cfg, brief string) []string {
	t.Helper()
	res := lintWith(t, map[string]string{".flywheel/config.json": lintConfigJSON(cfg)}, brief, noGoList)
	var probs []string
	for _, p := range res.Problems {
		if strings.Contains(p, "required_live_gates") {
			probs = append(probs, p)
		}
	}
	return probs
}

// TestRequiredLiveGatesLint: a brief owning a path under a prefix needs a
// live-gate matching each pattern; a gate: line never counts, a prefix no owns
// path is under does not apply, and "" always does (issue #845).
func TestRequiredLiveGatesLint(t *testing.T) {
	t.Parallel()
	cfg := `{"required_live_gates":{"fw/":["hil-check"]}}`
	want := "no live-gate matches required pattern hil-check for fw/ (lint.required_live_gates)"
	if probs := requiredLiveGatesProblems(t, cfg, "owns: fw/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n"); !slices.Equal(probs, []string{want}) {
		t.Errorf("no live-gate: problems = %v, want %q", probs, want)
	}
	if probs := requiredLiveGatesProblems(t, cfg, "owns: fw/a.c\nneeds: none\ngate: echo hil-check\n\n# TASK x\n"); !slices.Equal(probs, []string{want}) {
		t.Errorf("gate: match only: problems = %v, want %q", probs, want)
	}
	if probs := requiredLiveGatesProblems(t, cfg, "owns: fw/a.c\nneeds: none\ngate: echo x\nlive-gate: echo hil-check\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("matching live-gate: problems = %v, want none", probs)
	}
	if probs := requiredLiveGatesProblems(t, cfg, "owns: src/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("src/ under no key: problems = %v, want none", probs)
	}
	all := `{"required_live_gates":{"":["hil-check"]}}`
	if probs := requiredLiveGatesProblems(t, all, "owns: src/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n"); !slices.Equal(probs, []string{"no live-gate matches required pattern hil-check (lint.required_live_gates)"}) {
		t.Errorf(`"" key: problems = %v, want one miss`, probs)
	}
	// A live-gate never satisfies a required_gates pattern (issue #751 kept).
	probs, _ := requiredGatesFindings(t, `{"required_gates":{"":["hil-check"]}}`, "owns: a.c\nneeds: none\ngate: echo x\nlive-gate: echo hil-check\n\n# TASK x\n")
	if len(probs) != 1 {
		t.Errorf("required_gates met by a live-gate: problems = %v, want one miss", probs)
	}
}

// TestRequiredLiveGatesConfigInvalid: an invalid pattern fails config load
// naming lint.required_live_gates[key][i], and lint reports it (issue #845).
func TestRequiredLiveGatesConfigInvalid(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "m"}}, Lint: &LintConfig{RequiredLiveGates: map[string][]string{"fw/": {"ok", "("}}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `lint.required_live_gates["fw/"][1]: "(" is not a valid regular expression`) {
		t.Errorf("Validate() error = %v, want one naming lint.required_live_gates[\"fw/\"][1]", err)
	}
	probs := requiredLiveGatesProblems(t, `{"required_live_gates":{"fw/":["("]}}`, "owns: fw/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n")
	if len(probs) != 1 || !strings.Contains(probs[0], `lint.required_live_gates["fw/"][0]`) {
		t.Errorf("problems = %v, want one naming lint.required_live_gates[\"fw/\"][0]", probs)
	}
}

// requiredLiveGatesLint requires a hil-check live-gate on every brief.
var requiredLiveGatesLint = LintConfig{RequiredLiveGates: map[string][]string{"": {"hil-check"}}}

// TestRequiredLiveGatesRunRefuses: run refuses a brief whose only match is a
// gate: line with rule required-live-gates (issue #845).
func TestRequiredLiveGatesRunRefuses(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go\nneeds: none\ngate: echo hil-check\n\n# TASK x\n")
	cfg := simConfig(fixturePath("clean.jsonl", t))
	lc := requiredLiveGatesLint
	cfg.Lint = &lc
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, RunOptions{Task: "T1"}); !errors.As(err, &rf) || rf.Rule != "required-live-gates" ||
		rf.Fix != "no live-gate matches required pattern hil-check; add a matching live-gate: line to the brief or change lint.required_live_gates" {
		t.Fatalf("Run() error = %v, want a required-live-gates refusal naming hil-check", err)
	}
	wantRefusedAppended(t, dir, before, "T1", "required-live-gates")
}

// TestRequiredLiveGatesValidateRefuses: validate refuses a brief missing the
// live-gate with or without --live before any gate runs, and runs the gates
// of one that has it (issue #845).
func TestRequiredLiveGatesValidateRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		live   string
		opt    bool
		refuse bool
	}{{"", false, true}, {"", true, true}, {"live-gate: echo hil-check\n", false, false}} {
		dir, err := initTask(t, []string{"echo x > marker.txt"})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		brief := "owns: a.go\nneeds: none\ngate: echo x > marker.txt\n" + tc.live + "\n# TASK: gauges\n"
		if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := simConfig(fixturePath("clean.jsonl", t))
		lc := requiredLiveGatesLint
		cfg.Lint = &lc
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		_, err = ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Live: tc.opt})
		_, statErr := os.Stat(filepath.Join(dir, "marker.txt"))
		var rf *RuleRefusal
		if tc.refuse && (!errors.As(err, &rf) || rf.Rule != "required-live-gates" || statErr == nil) {
			t.Errorf("live %v: ValidateTask() error = %v (marker %v), want a required-live-gates refusal before any gate", tc.opt, err, statErr)
		}
		if !tc.refuse && (errors.As(err, &rf) || statErr != nil) {
			t.Errorf("with live-gate: ValidateTask() error = %v (marker %v), want the gate run", err, statErr)
		}
	}
}
