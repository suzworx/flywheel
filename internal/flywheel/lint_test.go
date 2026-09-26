package flywheel

import (
	"errors"
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

// TestLintGateDiffCheck is issue #470: the attempt commit moves HEAD before
// validate, so `git diff --check` with no revision sees nothing and lint
// warns. A revision ("$FLYWHEEL_BASE", HEAD~1), --no-index, or a diff without
// --check (a regenerated tree checked against HEAD) does not warn; a pathspec
// after -- is not a revision and still warns.
func TestLintGateDiffCheck(t *testing.T) {
	t.Parallel()
	const warning = `gate 1 runs "git diff" against HEAD; after the attempt commit it sees nothing — diff against "$FLYWHEEL_BASE"`
	cases := []struct {
		name, gate string
		warns      bool
	}{
		{"bare", "git diff --check", true},
		{"pathspec after dashdash", "git diff --check -- a.go", true},
		{"after another command", "go build ./... && git diff --check", true},
		{"piped", "git diff --check|cat", true},
		{"flywheel base", `git diff --check "$FLYWHEEL_BASE"`, false},
		{"revision", "git diff --check HEAD~1", false},
		{"no index", "git diff --no-index --check /dev/null x", false},
		{"exit code regenerated", "node gen.mjs && git diff --exit-code -- a.json", false},
		{"rev in next command only", "git diff --check; git log HEAD~1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
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

// suiteBrief is a structurally clean brief with the given owns and one gate.
func suiteBrief(owns, gate string) string {
	return "owns: " + owns + "\nneeds: none\ngate: " + gate + "\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
}

// lintWith writes files (path to content) and brief under a new temp dir and
// runs lintBrief with list as the go list call.
func lintWith(t *testing.T, files map[string]string, brief string, list func(string) (string, error)) LintResult {
	t.Helper()
	dir := t.TempDir()
	files["brief.txt"] = brief
	for p, c := range files {
		fp := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, err := lintBrief(dir, filepath.Join(dir, "brief.txt"), list)
	if err != nil {
		t.Fatalf("lintBrief() error = %v", err)
	}
	return res
}

// noGoList is a go list call a test does not expect.
func noGoList(string) (string, error) { return "", errors.New("go list not expected") }

// lintConfigJSON is a valid config.json with the given lint section.
func lintConfigJSON(lint string) string {
	return `{"version":1,"workers":[{"name":"w","adapter":"sim","model":"m"}],"lint":` + lint + `}`
}

// TestLintFullSuiteGate checks the full-suite warning (issue #462): the go.mod
// default, the package.json default, a lint.full_suite override, and no check
// without either file.
func TestLintFullSuiteGate(t *testing.T) {
	t.Parallel()
	goWarn := `no gate runs the full suite (want a gate matching go test\b.*\./\.\.\.; set lint.full_suite to change it)`
	for _, tc := range []struct {
		name  string
		files map[string]string
		gate  string
		warns []string
	}{
		{"go.mod targeted gate", map[string]string{"go.mod": "module m\n"}, "go test -run X ./a/", []string{goWarn}},
		{"go.mod full suite", map[string]string{"go.mod": "module m\n"}, "go test -count=1 ./...", nil},
		{"package.json npm test", map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, "npm test", nil},
		{"package.json no test gate", map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, "npx tsc",
			[]string{`no gate runs the full suite (want a gate matching \b(npm|pnpm|yarn|bun)( run)? test\b; set lint.full_suite to change it)`}},
		{"override unmatched", map[string]string{"go.mod": "module m\n", ".flywheel/config.json": lintConfigJSON(`{"full_suite":"make check"}`)},
			"go test ./...", []string{"no gate runs the full suite (want a gate matching make check; set lint.full_suite to change it)"}},
		{"override matched", map[string]string{"go.mod": "module m\n", ".flywheel/config.json": lintConfigJSON(`{"full_suite":"make check"}`)}, "make check", nil},
		{"no toolchain", map[string]string{}, "true", nil},
	} {
		tc.files["README.md"] = "x\n"
		res := lintWith(t, tc.files, suiteBrief("README.md", tc.gate), noGoList)
		if len(res.Problems) != 0 || !slices.Equal(res.Warnings, tc.warns) {
			t.Errorf("%s: problems %v warnings %v, want no problems and warnings %v", tc.name, res.Problems, res.Warnings, tc.warns)
		}
	}
}

// TestLintFullSuiteInvalidRegex checks an invalid lint.full_suite is a problem
// naming the key (issue #462).
func TestLintFullSuiteInvalidRegex(t *testing.T) {
	t.Parallel()
	res := lintWith(t, map[string]string{"README.md": "x\n", ".flywheel/config.json": lintConfigJSON(`{"full_suite":"("}`)},
		suiteBrief("README.md", "true"), noGoList)
	if len(res.Problems) != 1 || !strings.HasPrefix(res.Problems[0], `config lint.full_suite "(" is not a valid regular expression`) {
		t.Errorf("problems = %v, want one naming lint.full_suite", res.Problems)
	}
}

// importerFiles is a module where m/b imports m/a and has a test.
func importerFiles() map[string]string {
	return map[string]string{
		"go.mod":        "module m\n\ngo 1.21\n",
		"a/a.go":        "package a\n\nfunc A() int { return 1 }\n",
		"b/b.go":        "package b\n\nimport \"m/a\"\n\nfunc B() int { return a.A() }\n",
		"b/b_test.go":   "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {}\n",
		"a/a_x_test.go": "package a_test\n",
	}
}

// TestLintImporters checks the importer-coverage warning from injected go
// list output (issue #462): an unowned importer test warns, owning or
// negating it does not, lint.importers false turns it off, and a go list
// failure is a skipped warning.
func TestLintImporters(t *testing.T) {
	t.Parallel()
	out := "m/a\tD/a\t\t\tm/a\t\ta_x_test.go\nm/b\tD/b\tm/a\ttesting\t\tb_test.go\t\n"
	list := func(string) (string, error) { return out, nil }
	gate := "go test -count=1 ./..."
	warn := "owns changes m/a, imported by m/b whose tests are not owned"
	for _, tc := range []struct {
		name, owns string
		config     string
		list       func(string) (string, error)
		warns      []string
	}{
		{"unowned importer test", "a/a.go", "", list, []string{warn}},
		{"owned importer test", "a/a.go, b/b_test.go", "", list, nil},
		{"negated importer test", "a/a.go, b/, !b/b_test.go", "", list, nil},
		{"importers off", "a/a.go", lintConfigJSON(`{"importers":false}`), noGoList, nil},
		{"go list fails", "a/a.go", "", func(string) (string, error) { return "", errors.New("boom") }, []string{"importer check skipped: go list: boom"}},
		{"no owned Go package", "b/b_test.go", "", noGoList, nil},
	} {
		files := importerFiles()
		if tc.config != "" {
			files[".flywheel/config.json"] = tc.config
		}
		res := lintWith(t, files, suiteBrief(tc.owns, gate), tc.list)
		if len(res.Problems) != 0 || !slices.Equal(res.Warnings, tc.warns) {
			t.Errorf("%s: problems %v warnings %v, want no problems and warnings %v", tc.name, res.Problems, res.Warnings, tc.warns)
		}
	}
}

// TestLintImportersRealGoList runs the real go list on a tiny module to prove
// goListFormat parses (issue #462).
func TestLintImportersRealGoList(t *testing.T) {
	t.Parallel()
	res := lintWith(t, importerFiles(), suiteBrief("a/a.go", "go test ./..."), goList)
	want(t, res, nil, []string{"owns changes m/a, imported by m/b whose tests are not owned"})
}
