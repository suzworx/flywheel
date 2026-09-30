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
