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
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"needs: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"missing owns: line"}, nil)
}

func TestLintBriefNoGoalAlone(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\n\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"no # TASK heading"}, nil)
}

func TestLintBriefNoGateAlone(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"no gate: line"}, nil)
}

func TestLintBriefNoChecksAlone(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\n\n# TASK: x\nAt most one write per response\nreport\n")
	want(t, res, []string{"no ## Checks section"}, nil)
}

func TestLintBriefOwnsMissingFileAlone(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: missing.go\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"owns path missing.go does not exist; if the unit creates it, annotate it: missing.go (new)"}, nil)
}

func TestLintBriefOwnsMissingFileNamesNewAnnotation(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: missing.go\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	if len(res.Problems) != 1 {
		t.Fatalf("problems = %v, want exactly one", res.Problems)
	}
	if !strings.Contains(res.Problems[0], "(new)") {
		t.Errorf("problem %q does not name the (new) annotation", res.Problems[0])
	}
}

func TestLintBriefSeveralErrorsTogether(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"sub/"},
		"owns: missing.go (new), sub/\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefWarningAlone(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nreport\n")
	want(t, res, nil, []string{`write rule "At most one write per response" is absent`})
}

func TestLintBriefMissingNeedsIsWarning(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, []string{"no needs: line"})
}

func TestLintBriefOwnsPatternMatchPasses(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"src/", "src/a.test.ts"},
		"owns: src/*.test.ts\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefOwnsPatternNoMatchAlone(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: src/*.none.ts\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"owns pattern src/*.none.ts matches no file; if the unit creates it, annotate it: src/*.none.ts (new)"}, nil)
}

func TestLintBriefOwnsPatternNoMatchNamesNewAnnotation(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: src/*.none.ts\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	if len(res.Problems) != 1 {
		t.Fatalf("problems = %v, want exactly one", res.Problems)
	}
	if !strings.Contains(res.Problems[0], "(new)") {
		t.Errorf("problem %q does not name the (new) annotation", res.Problems[0])
	}
}

// TestLintBriefOwnsPatternMalformedSyntax guards the malformed-pattern branch:
// filepath.Glob rejects the pattern and ownsContains matches it with
// path.Match ignoring its error, so it can never match a file. The (new)
// remedy would make lint pass while ownership stayed broken, so the message
// must not offer it.
func TestLintBriefOwnsPatternMalformedSyntax(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: src/[.go\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"owns pattern src/[.go is invalid: syntax error in pattern; correct the pattern"}, nil)
	if len(res.Problems) == 1 && strings.Contains(res.Problems[0], "(new)") {
		t.Errorf("problem %q names the (new) annotation for a malformed pattern", res.Problems[0])
	}
}

func TestLintBriefOwnsMissingFileNewSkipsCheck(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: missing.go (new)\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefOwnsExistingFileAnnotatedPasses(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go", "b.go"},
		"owns: a.go, b.go (new)\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefOwnsPatternNewSkipsCheck(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: src/*.none.ts (new)\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

// TestLintBriefOwnsPatternAnnotatedMalformedNewInvalid guards correction 2:
// (new) excuses a path that does not exist yet, not a pattern that can never
// match. ownsContains matches patterns with path.Match, which errors on a
// malformed pattern, so an annotated malformed pattern would pass lint while
// ownership stayed silently broken. The syntax check must run even when the
// entry is annotated (new).
func TestLintBriefOwnsPatternAnnotatedMalformedNewInvalid(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), nil,
		"owns: src/[.go (new)\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"owns pattern src/[.go is invalid: syntax error in pattern; correct the pattern"}, nil)
	if len(res.Problems) == 1 && strings.Contains(res.Problems[0], "(new)") {
		t.Errorf("problem %q names the (new) annotation for a malformed pattern", res.Problems[0])
	}
}

func TestLintBriefLiveGateRepeatsGateAlone(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\nlive-gate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"live-gate 1 repeats gate 1; a live gate must run the real path, not the mocked one"}, nil)
}

func TestLintBriefDistinctLiveGatePasses(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate: true\nlive-gate: exit 0\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

func TestLintBriefUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := LintBrief(dir, filepath.Join(dir, "nope.txt"))
	if err == nil {
		t.Fatal("LintBrief() error = nil, want one for an unreadable brief")
	}
}

func TestLintBriefEmptyExclusiveEntry(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nexclusive:   \nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, []string{"exclusive: entry is empty"}, nil)
}

func TestLintBriefNormalExclusiveEntryPasses(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nexclusive: db\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
}

// TestLintGateBacktick is issue #366: gates run under bash -c, so a backtick
// inside double quotes is command substitution and lint warns about it; single
// quotes, "$(...)" and an escaped backtick are fine.
func TestLintGateBacktick(t *testing.T) {
	t.Parallel()
	const warning = "gate 1 has a backtick inside double quotes: bash runs it as command substitution; use single quotes or a script file"
	cases := []struct {
		name, gate string
		warns      bool
	}{
		{"double quoted", "node -e \"x `a`\"", true},
		{"single quoted", "node -e 'x `a`'", false},
		{"command substitution", `[ -z "$(ls)" ]`, false},
		{"escaped in double quotes", "node -e \"x \\`a\\`\"", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := lintCheck(t, t.TempDir(), []string{"a.go"},
				"owns: a.go\nneeds: none\ngate: "+c.gate+"\n\n# TASK: x\n## Checks\nAt most one write per response\n")
			var wantWarnings []string
			if c.warns {
				wantWarnings = []string{warning}
			}
			want(t, res, nil, wantWarnings)
		})
	}
}

// TestLintNegated checks a negated owns entry (issue #388) is never
// existence-checked, and one no positive entry covers is a warning.
func TestLintNegated(t *testing.T) {
	t.Parallel()
	const tail = "\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	cases := []struct {
		name  string
		owns  string
		files []string
		warn  []string
	}{
		{"literal under a pattern", "apps/inc/*.h, !apps/inc/wake.h", []string{"apps/", "apps/inc/", "apps/inc/a.h"}, nil},
		{"dir under a dir", "apps/, !apps/gen/", []string{"apps/"}, nil},
		{"pattern under a dir", "src/, !src/*_gen.go", []string{"src/"}, nil},
		{"covers nothing", "apps/, !docs/x.md", []string{"apps/"},
			[]string{"owns: !docs/x.md excludes nothing: no positive entry covers it"}},
		{"negation alone", "a.go, !b/*.go", []string{"a.go"},
			[]string{"owns: !b/*.go excludes nothing: no positive entry covers it"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := lintCheck(t, t.TempDir(), tc.files, "owns: "+tc.owns+tail)
			want(t, res, nil, tc.warn)
		})
	}
}

// TestLintBriefQuietGateMarkers checks gate[quiet]: and live-gate[quiet]:
// pass cleanly and count as gates, while an unknown marker is a warning, not
// a problem (issue #411).
func TestLintBriefQuietGateMarkers(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate[quiet]: ./hil\nlive-gate[quiet]: ./probe\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, nil)
	res = lintCheck(t, t.TempDir(), []string{"a.go"},
		"owns: a.go\nneeds: none\ngate[loud]: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n")
	want(t, res, nil, []string{"gate[loud] has unknown marker [loud]; the known marker is [quiet], and the line runs as a plain gate"})
}
