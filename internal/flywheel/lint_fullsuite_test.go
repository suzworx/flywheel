package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fullSuiteGoRE is the go.mod default full-suite pattern (issue #462).
const fullSuiteGoRE = `go test\b.*\./\.\.\.`

// fullSuiteMsg is the full-suite miss message lint reports when
// lint.full_suite_required is set (issue #652).
const fullSuiteMsg = "no gate runs the full suite (want a gate matching " + fullSuiteGoRE + "; lint.full_suite_required is set)"

// TestFullSuiteRequiredUnsetWarns: without lint.full_suite_required, a
// targeted gate is still only a warning (issue #652).
func TestFullSuiteRequiredUnsetWarns(t *testing.T) {
	t.Parallel()
	res := lintWith(t, map[string]string{"go.mod": "module m\n"}, "owns: a.go\nneeds: none\ngate: go test -run X ./a/\n\n# TASK x\n", noGoList)
	want := "no gate runs the full suite (want a gate matching " + fullSuiteGoRE + "; set lint.full_suite to change it)"
	if !slices.Contains(res.Warnings, want) {
		t.Errorf("warnings = %v, want %q", res.Warnings, want)
	}
	if slices.ContainsFunc(res.Problems, func(p string) bool { return strings.Contains(p, "full suite") }) {
		t.Errorf("problems = %v, want no full-suite problem", res.Problems)
	}
}

// TestFullSuiteRequiredProblem: with lint.full_suite_required set, a brief
// whose gates miss the full suite, or that has no gates, is a problem (issue
// #652).
func TestFullSuiteRequiredProblem(t *testing.T) {
	t.Parallel()
	for name, brief := range map[string]string{
		"targeted gate": "owns: a.go\nneeds: none\ngate: go test -run X ./a/\n\n# TASK x\n",
		"no gates":      "owns: a.go\nneeds: none\n\n# TASK x\n",
	} {
		res := lintWith(t, map[string]string{"go.mod": "module m\n", ".flywheel/config.json": lintConfigJSON(`{"full_suite_required":true}`)}, brief, noGoList)
		if !slices.Contains(res.Problems, fullSuiteMsg) {
			t.Errorf("%s: problems = %v, want %q", name, res.Problems, fullSuiteMsg)
		}
		if slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "full suite") }) {
			t.Errorf("%s: warnings = %v, want no full-suite warning", name, res.Warnings)
		}
	}
}

// TestFullSuiteRequiredMatched: with lint.full_suite_required set, a gate
// running the full suite is neither a problem nor a warning (issue #652).
func TestFullSuiteRequiredMatched(t *testing.T) {
	t.Parallel()
	res := lintWith(t, map[string]string{"go.mod": "module m\n", ".flywheel/config.json": lintConfigJSON(`{"full_suite_required":true}`)},
		"owns: a.go\nneeds: none\ngate: go test -count=1 ./...\n\n# TASK x\n", noGoList)
	for _, s := range append(slices.Clone(res.Problems), res.Warnings...) {
		if strings.Contains(s, "full suite") {
			t.Errorf("lint reported %q, want no full-suite finding", s)
		}
	}
}

// fullSuiteTask is refusedTask with lint.full_suite_required set and the go
// default pattern (the temp dir has no go.mod).
func fullSuiteTask(t *testing.T, text string) string {
	t.Helper()
	dir := refusedTask(t, text)
	cfg := simConfig(fixturePath("clean.jsonl", t))
	cfg.Lint = &LintConfig{FullSuite: fullSuiteGoRE, FullSuiteRequired: true}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	return dir
}

// wantFullSuiteRefusal fails t unless run refuses with rule full-suite,
// appending only one dispatch_refused and no dispatched.
func wantFullSuiteRefusal(t *testing.T, dir string, o RunOptions) {
	t.Helper()
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, o); !errors.As(err, &rf) || rf.Rule != "full-suite" || !strings.Contains(rf.Fix, "want a gate matching "+fullSuiteGoRE) {
		t.Fatalf("Run() error = %v, want a full-suite refusal naming %s", err, fullSuiteGoRE)
	}
	wantRefusedAppended(t, dir, before, "T1", "full-suite")
}

// TestFullSuiteRequiredRunRefuses: run refuses a brief with only a targeted
// gate, and dispatches one whose gate runs the full suite (issue #652).
func TestFullSuiteRequiredRunRefuses(t *testing.T) {
	t.Parallel()
	wantFullSuiteRefusal(t, fullSuiteTask(t, "owns: a.go\nneeds: none\ngate: go test -run X ./a/\n\n# TASK x\n"), RunOptions{Task: "T1"})
	dir := fullSuiteTask(t, "owns: a.go\nneeds: none\ngate: go test ./...\n\n# TASK x\n")
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() with a full-suite gate error = %v, want a dispatch", err)
	}
	if n := len(kindEvents(t, dir, "dispatched")); n != 1 {
		t.Errorf("dispatched events = %d, want 1", n)
	}
}

// fullSuitePathsConfig is a lint config requiring the full suite, with the
// global pattern make check and apps/ and apps/api/ prefixes (issue #652).
var fullSuitePathsConfig = lintConfigJSON(`{"full_suite_required":true,"full_suite":"make check","full_suite_paths":{"apps/":"apps-suite","apps/api/":"api-suite","apps/web/":"web-suite"}}`)

// fullSuiteFindings lints brief under fullSuitePathsConfig (or cfg when not
// "") and returns the problems and warnings that mention the full suite.
func fullSuiteFindings(t *testing.T, cfg, brief string) (probs, warns []string) {
	t.Helper()
	if cfg == "" {
		cfg = fullSuitePathsConfig
	}
	res := lintWith(t, map[string]string{".flywheel/config.json": cfg}, brief, noGoList)
	has := func(s string) bool { return strings.Contains(s, "full suite") || strings.Contains(s, "full_suite") }
	for _, p := range res.Problems {
		if has(p) {
			probs = append(probs, p)
		}
	}
	for _, w := range res.Warnings {
		if has(w) {
			warns = append(warns, w)
		}
	}
	return probs, warns
}

// TestFullSuitePathsPrefix: an owns path under a prefix needs that prefix's
// pattern, and longest prefix wins (apps/api/ over apps/) (issue #652).
func TestFullSuitePathsPrefix(t *testing.T) {
	t.Parallel()
	probs, _ := fullSuiteFindings(t, "", "owns: apps/api/x.go\nneeds: none\ngate: echo apps-suite\n\n# TASK x\n")
	want := "no gate runs the full suite for apps/api/ (want a gate matching api-suite; lint.full_suite_required is set)"
	if !slices.Equal(probs, []string{want}) {
		t.Errorf("problems = %v, want [%q]", probs, want)
	}
	if probs, _ := fullSuiteFindings(t, "", "owns: apps/api/x.go\nneeds: none\ngate: echo api-suite\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none with the api-suite gate", probs)
	}
	if probs, _ := fullSuiteFindings(t, "", "owns: apps/other/x.go\nneeds: none\ngate: echo apps-suite\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none: apps/ selects apps-suite", probs)
	}
}

// TestFullSuitePathsFallback: owns under no prefix fall back to the global
// pattern; alongside a prefixed owns path they add nothing (issue #652).
func TestFullSuitePathsFallback(t *testing.T) {
	t.Parallel()
	probs, _ := fullSuiteFindings(t, "", "owns: tools/x.go\nneeds: none\ngate: echo api-suite\n\n# TASK x\n")
	want := "no gate runs the full suite (want a gate matching make check; lint.full_suite_required is set)"
	if !slices.Equal(probs, []string{want}) {
		t.Errorf("problems = %v, want [%q]", probs, want)
	}
	if probs, _ := fullSuiteFindings(t, "", "owns: tools/x.go, apps/api/x.go\nneeds: none\ngate: echo api-suite\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none: tools/ adds nothing beside apps/api/", probs)
	}
}

// TestFullSuitePathsTwoPrefixes: owns under two prefixes need both patterns;
// one missing is exactly one problem, and unset required makes it a warning
// naming lint.full_suite_paths (issue #652).
func TestFullSuitePathsTwoPrefixes(t *testing.T) {
	t.Parallel()
	brief := "owns: apps/api/x.go, apps/web/y.ts\nneeds: none\ngate: echo api-suite\n\n# TASK x\n"
	probs, _ := fullSuiteFindings(t, "", brief)
	if want := "no gate runs the full suite for apps/web/ (want a gate matching web-suite; lint.full_suite_required is set)"; !slices.Equal(probs, []string{want}) {
		t.Errorf("problems = %v, want [%q]", probs, want)
	}
	probs, warns := fullSuiteFindings(t, lintConfigJSON(`{"full_suite_paths":{"apps/api/":"api-suite","apps/web/":"web-suite"}}`), brief)
	if want := "no gate runs the full suite for apps/web/ (want a gate matching web-suite; set lint.full_suite_paths to change it)"; len(probs) != 0 || !slices.Equal(warns, []string{want}) {
		t.Errorf("problems = %v, warnings = %v, want only the warning %q", probs, warns, want)
	}
}

// TestFullSuitePathsInvalidRegex: an invalid pattern in lint.full_suite_paths
// is a problem naming its key (issue #652).
func TestFullSuitePathsInvalidRegex(t *testing.T) {
	t.Parallel()
	probs, _ := fullSuiteFindings(t, lintConfigJSON(`{"full_suite_paths":{"apps/api/":"("}}`), "owns: apps/api/x.go\nneeds: none\ngate: echo x\n\n# TASK x\n")
	if len(probs) != 1 || !strings.HasPrefix(probs[0], `config lint.full_suite_paths["apps/api/"] "(" is not a valid regular expression`) {
		t.Errorf("problems = %v, want one naming lint.full_suite_paths[\"apps/api/\"]", probs)
	}
}

// fullSuitePathsLint requires the full suite with apps/api/ needing
// api-suite and everything else suite-marker (issue #652).
var fullSuitePathsLint = LintConfig{FullSuite: "suite-marker", FullSuiteRequired: true, FullSuitePaths: map[string]string{"apps/api/": "api-suite"}}

// TestFullSuitePathsRunRefuses: run refuses a brief owning apps/api/ without
// an api-suite gate, the Fix naming the prefix (issue #652).
func TestFullSuitePathsRunRefuses(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: apps/api/x.go\nneeds: none\ngate: echo suite-marker\n\n# TASK x\n")
	cfg := simConfig(fixturePath("clean.jsonl", t))
	lc := fullSuitePathsLint
	cfg.Lint = &lc
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, RunOptions{Task: "T1"}); !errors.As(err, &rf) || rf.Rule != "full-suite" || !strings.Contains(rf.Fix, "full suite for apps/api/ (want a gate matching api-suite)") {
		t.Fatalf("Run() error = %v, want a full-suite refusal naming apps/api/", err)
	}
	wantRefusedAppended(t, dir, before, "T1", "full-suite")
}

// TestFullSuiteValidateRefuses: with lint.full_suite_required set, validate
// refuses a brief whose gates miss the full suite before any gate runs, and
// runs the gates of one that has it (issue #652).
func TestFullSuiteValidateRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		gate   string
		refuse bool
	}{{"echo x > marker.txt", true}, {"echo suite-marker > marker.txt", false}} {
		dir, err := initTask(t, []string{tc.gate})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		cfg := simConfig(fixturePath("clean.jsonl", t))
		lc := fullSuitePathsLint
		cfg.Lint = &lc
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		var rf *RuleRefusal
		_, statErr := os.Stat(filepath.Join(dir, "marker.txt"))
		if tc.refuse {
			if !errors.As(err, &rf) || rf.Rule != "full-suite" || !strings.Contains(rf.Fix, "want a gate matching suite-marker") {
				t.Errorf("%s: ValidateTask() error = %v, want a full-suite RuleRefusal", tc.gate, err)
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

// TestFullSuiteRequiredDelta: over a base brief with a full-suite gate, a
// --delta replacing it with a targeted gate is refused, and one declaring no
// gate: lines (merged gates keep the base's full suite) dispatches (issue
// #652).
func TestFullSuiteRequiredDelta(t *testing.T) {
	t.Parallel()
	dir := fullSuiteTask(t, "owns: a.go\nneeds: none\ngate: go test ./...\n\n# TASK x\n")
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("fresh Run() error = %v", err)
	}
	bad := filepath.Join(dir, "bad.txt")
	if err := os.WriteFile(bad, []byte("owns: a.go\nneeds: none\ngate: go test -run X ./a/\n\n# TASK: delta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantFullSuiteRefusal(t, dir, RunOptions{Task: "T1", DeltaPath: bad})
	good := filepath.Join(dir, "good.txt")
	if err := os.WriteFile(good, []byte("owns: a.go\nneeds: none\n\n# TASK: delta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: good}); err != nil {
		t.Fatalf("delta Run() without gates error = %v, want a dispatch", err)
	}
	if n := len(kindEvents(t, dir, "dispatched")); n != 2 {
		t.Errorf("dispatched events = %d, want 2 (fresh and the gate-less delta)", n)
	}
}
