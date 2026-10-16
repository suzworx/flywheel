package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// requiredObservesProblems lints brief under the lint config cfg and returns
// the problems that mention lint.required_observes (issue #848).
func requiredObservesProblems(t *testing.T, cfg, brief string) []string {
	t.Helper()
	res := lintWith(t, map[string]string{".flywheel/config.json": lintConfigJSON(cfg)}, brief, noGoList)
	var probs []string
	for _, p := range res.Problems {
		if strings.Contains(p, "required_observes") {
			probs = append(probs, p)
		}
	}
	return probs
}

// TestRequiredObservesLint: a brief owning a path under a prefix needs an
// observe line matching each pattern; a gate: or live-gate: line never counts,
// a prefix no owns path is under does not apply, and "" always does (issue #848).
func TestRequiredObservesLint(t *testing.T) {
	t.Parallel()
	cfg := `{"required_observes":{"fw/audio/":["listen|voice"]}}`
	want := "no observe matches required pattern listen|voice for fw/audio/ (lint.required_observes)"
	if probs := requiredObservesProblems(t, cfg, "owns: fw/audio/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n"); !slices.Equal(probs, []string{want}) {
		t.Errorf("no observe: problems = %v, want %q", probs, want)
	}
	if probs := requiredObservesProblems(t, cfg, "owns: fw/audio/a.c\nneeds: none\ngate: echo listen\nlive-gate: echo voice\n\n# TASK x\n"); !slices.Equal(probs, []string{want}) {
		t.Errorf("gate: and live-gate: match only: problems = %v, want %q", probs, want)
	}
	if probs := requiredObservesProblems(t, cfg, "owns: fw/audio/a.c\nneeds: none\ngate: echo x\nobserve: listen to the speaker play the chime\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("matching observe: problems = %v, want none", probs)
	}
	if probs := requiredObservesProblems(t, cfg, "owns: src/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("src/ under no key: problems = %v, want none", probs)
	}
	all := `{"required_observes":{"":["listen"]}}`
	if probs := requiredObservesProblems(t, all, "owns: src/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n"); !slices.Equal(probs, []string{"no observe matches required pattern listen (lint.required_observes)"}) {
		t.Errorf(`"" key: problems = %v, want one miss`, probs)
	}
}

// TestRequiredObservesConfigInvalid: an invalid pattern fails config load
// naming lint.required_observes[key][i], and lint reports it (issue #848).
func TestRequiredObservesConfigInvalid(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "m"}}, Lint: &LintConfig{RequiredObserves: map[string][]string{"fw/": {"ok", "("}}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `lint.required_observes["fw/"][1]: "(" is not a valid regular expression`) {
		t.Errorf("Validate() error = %v, want one naming lint.required_observes[\"fw/\"][1]", err)
	}
	probs := requiredObservesProblems(t, `{"required_observes":{"fw/":["("]}}`, "owns: fw/a.c\nneeds: none\ngate: echo x\n\n# TASK x\n")
	if len(probs) != 1 || !strings.Contains(probs[0], `lint.required_observes["fw/"][0]`) {
		t.Errorf("problems = %v, want one naming lint.required_observes[\"fw/\"][0]", probs)
	}
}

// requiredObservesLint requires a listen observe line on every brief.
var requiredObservesLint = LintConfig{RequiredObserves: map[string][]string{"": {"listen"}}}

// TestRequiredObservesRunRefuses: run refuses a brief whose only matches are
// gate: and live-gate: lines with rule required-observes (issue #848).
func TestRequiredObservesRunRefuses(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go\nneeds: none\ngate: echo listen\nlive-gate: echo listen\n\n# TASK x\n")
	cfg := simConfig(fixturePath("clean.jsonl", t))
	lc := requiredObservesLint
	cfg.Lint = &lc
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, RunOptions{Task: "T1"}); !errors.As(err, &rf) || rf.Rule != "required-observes" ||
		rf.Fix != "no observe matches required pattern listen; add a matching observe: line to the brief or change lint.required_observes" {
		t.Fatalf("Run() error = %v, want a required-observes refusal naming listen", err)
	}
	wantRefusedAppended(t, dir, before, "T1", "required-observes")
}

// TestRequiredObservesValidateRefuses: validate refuses a brief missing the
// observe line before any gate runs, and runs the gates of one that has it
// (issue #848).
func TestRequiredObservesValidateRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		extra  string
		refuse bool
	}{{"", true}, {"live-gate: echo listen\n", true}, {"observe: listen for the chime\n", false}} {
		dir, err := initTask(t, []string{"echo x > marker.txt"})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		brief := "owns: a.go\nneeds: none\ngate: echo x > marker.txt\n" + tc.extra + "\n# TASK: gauges\n"
		if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := simConfig(fixturePath("clean.jsonl", t))
		lc := requiredObservesLint
		cfg.Lint = &lc
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		_, err = ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		_, statErr := os.Stat(filepath.Join(dir, "marker.txt"))
		var rf *RuleRefusal
		if tc.refuse && (!errors.As(err, &rf) || rf.Rule != "required-observes" || statErr == nil) {
			t.Errorf("extra %q: ValidateTask() error = %v (marker %v), want a required-observes refusal before any gate", tc.extra, err, statErr)
		}
		if !tc.refuse && (errors.As(err, &rf) || statErr != nil) {
			t.Errorf("with observe: ValidateTask() error = %v (marker %v), want the gate run", err, statErr)
		}
	}
}
