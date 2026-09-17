package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// lintCheck runs LintBrief on a brief written in dir with the given owns
// files created under dir, and returns the result.
func lintCheck(t *testing.T, dir string, ownsFiles []string, content string) LintResult {
	t.Helper()
	path := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	for _, f := range ownsFiles {
		p := filepath.Join(dir, f)
		if strings.HasSuffix(f, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", p, err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	res, err := LintBrief(dir, path)
	if err != nil {
		t.Fatalf("LintBrief() error = %v", err)
	}
	return res
}

// want asserts res has exactly the problems and warnings in want, in any
// order.
func want(t *testing.T, res LintResult, wantProblems, wantWarnings []string) {
	t.Helper()
	slices.Sort(res.Problems)
	w := append([]string(nil), wantProblems...)
	slices.Sort(w)
	if !slices.Equal(res.Problems, w) {
		t.Errorf("problems = %v, want %v", res.Problems, wantProblems)
	}
	slices.Sort(res.Warnings)
	w = append([]string(nil), wantWarnings...)
	slices.Sort(w)
	if !slices.Equal(res.Warnings, w) {
		t.Errorf("warnings = %v, want %v", res.Warnings, wantWarnings)
	}
}

func TestLintBriefMissingOwnsAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), nil,
		"needs: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"missing owns: line"}, nil)
}

func TestLintBriefNoGoalAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\n\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"no # TASK heading"}, nil)
}

func TestLintBriefNoGateAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"no gate: line"}, nil)
}

func TestLintBriefNoChecksAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\n\n# TASK: x\nAt most one write per response\nreport\n")
	want(t, res, []string{"no ## Checks section"}, nil)
}

func TestLintBriefOwnsMissingFileAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), nil,
		"owns: missing.go\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"owns path missing.go does not exist"}, nil)
}

func TestLintBriefSeveralErrorsTogether(t *testing.T) {
	res := lintCheck(t, t.TempDir(), nil,
		"needs: none\n\nbody without a goal, a gate or a checks section\n")
	want(t, res, []string{
		"missing owns: line",
		"no # TASK heading",
		"no gate: line",
		"no ## Checks section",
	}, []string{`write rule "At most one write per response" is absent`})
}

func TestLintBriefNewAndDirectoryEntriesPass(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"sub/"},
		"owns: missing.go (new), sub/\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefWarningAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nreport\n")
	want(t, res, nil, []string{`write rule "At most one write per response" is absent`})
}

func TestLintBriefMissingNeedsIsWarning(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, []string{"no needs: line"})
}

func TestLintBriefOwnsPatternMatchPasses(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"src/", "src/a.test.ts"},
		"owns: src/*.test.ts\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefOwnsPatternNoMatchAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), nil,
		"owns: src/*.none.ts\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"owns pattern src/*.none.ts matches no file"}, nil)
}

func TestLintBriefOwnsPatternNewSkipsCheck(t *testing.T) {
	res := lintCheck(t, t.TempDir(), nil,
		"owns: src/*.none.ts (new)\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefLiveGateRepeatsGateAlone(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\nlive-gate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"live-gate 1 repeats gate 1; a live gate must run the real path, not the mocked one"}, nil)
}

func TestLintBriefDistinctLiveGatePasses(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\nlive-gate: exit 0\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefUnreadable(t *testing.T) {
	dir := t.TempDir()
	_, err := LintBrief(dir, filepath.Join(dir, "nope.txt"))
	if err == nil {
		t.Fatal("LintBrief() error = nil, want one for an unreadable brief")
	}
}

func TestLintBriefEmptyExclusiveEntry(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nexclusive:   \nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"exclusive: entry is empty"}, nil)
}

func TestLintBriefNormalExclusiveEntryPasses(t *testing.T) {
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nexclusive: db\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}
