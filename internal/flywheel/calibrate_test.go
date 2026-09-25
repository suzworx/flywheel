package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadCalibrationCases(t *testing.T) {
	t.Parallel()
	cases, err := LoadCalibrationCases(filepath.Join("..", "..", "docs", "calibration", "external-review-bugs.json"))
	if err != nil {
		t.Fatalf("LoadCalibrationCases: %v", err)
	}
	if len(cases) < 100 {
		t.Fatalf("got %d cases, want the committed set (>= 100)", len(cases))
	}
	groups := groupCalibrationCases(cases)
	n := 0
	for _, g := range groups {
		for _, c := range g.Cases {
			if c.PR != g.PR || c.Commit != g.Commit {
				t.Fatalf("case %+v grouped under PR #%d %s", c, g.PR, g.Commit)
			}
			n++
		}
	}
	if n != len(cases) || len(groups) >= len(cases) {
		t.Fatalf("%d groups hold %d of %d cases", len(groups), n, len(cases))
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"cases":[{"pr":1,"path":"a.go","line":1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCalibrationCases(bad); err == nil {
		t.Fatal("a case with no commit was accepted")
	}
}

func TestMatchFindings(t *testing.T) {
	t.Parallel()
	exp := []CalibrationCase{
		{Path: "a/x.go", Line: 10, Claim: "one"},
		{Path: "a/x.go", Line: 30, Claim: "two"},
		{Path: "b.go", Line: 5, Claim: "three"},
	}
	found := []ReviewFinding{
		{File: "a/x.go", Line: 26},  // nearest is 30
		{File: "a/x.go", Line: 28},  // 30 taken; 10 is 18 away
		{File: "a\\x.go", Line: 12}, // backslashes normalised: hits 10
		{File: "c.go", Line: 5},     // no such case
	}
	hits, misses, extra := matchFindings(exp, found, 15)
	if len(hits) != 2 || hits[0].Claim != "one" || hits[1].Claim != "two" {
		t.Fatalf("hits = %+v", hits)
	}
	if len(misses) != 1 || misses[0].Claim != "three" {
		t.Fatalf("misses = %+v", misses)
	}
	if len(extra) != 2 || extra[0].Line != 28 || extra[1].File != "c.go" {
		t.Fatalf("extra = %+v", extra)
	}
	if hits, _, _ := matchFindings(exp[2:], []ReviewFinding{{File: "b.go", Line: 21}}, 15); len(hits) != 0 {
		t.Fatal("a finding 16 lines away matched with window 15")
	}
}

func TestCalibrateSample(t *testing.T) {
	t.Parallel()
	a, b := sampleGroups(76, 10, 7), sampleGroups(76, 10, 7)
	if !reflect.DeepEqual(a, b) || len(a) != 10 {
		t.Fatalf("sample not deterministic: %v vs %v", a, b)
	}
	seen := map[int]bool{}
	for i, v := range a {
		if v < 0 || v >= 76 || seen[v] || (i > 0 && v <= a[i-1]) {
			t.Fatalf("bad sample %v", a)
		}
		seen[v] = true
	}
	if reflect.DeepEqual(a, sampleGroups(76, 10, 8)) {
		t.Fatal("seeds 7 and 8 drew the same sample")
	}
	if all := sampleGroups(3, 10, 1); !reflect.DeepEqual(all, []int{0, 1, 2}) {
		t.Fatalf("n > groups = %v, want every index", all)
	}
}

// TestCalibratePerPersona: with a panel (issue #420) each persona reviews the
// same sampled PR states as the single reviewer would (same seed); the report
// carries each persona's hits, misses, extra findings and recall, a persona
// whose review fails is skipped alone, and the panel hits a case when any
// persona hit it. An unknown persona is refused before any review.
func TestCalibratePerPersona(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	initRepo(t, repo)
	git(t, repo, []string{"branch", "-M", "main"})
	git(t, repo, []string{"checkout", "-q", "-b", "feat"})
	for _, f := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(repo, f), []byte("package x\n\nfunc F() int { return 1 }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, []string{"add", "a.go", "b.go"})
	git(t, repo, []string{"commit", "-q", "-m", "feature"})
	commit := git(t, repo, []string{"rev-parse", "HEAD"})
	git(t, repo, []string{"checkout", "-q", "main"})
	cases := filepath.Join(t.TempDir(), "cases.json")
	body := `{"cases":[{"pr":7,"commit":"` + commit + `","path":"a.go","line":3,"claim":"A is wrong"},` +
		`{"pr":7,"commit":"` + commit + `","path":"b.go","line":3,"claim":"B is wrong"},` +
		`{"pr":8,"commit":"0123456789abcdef0123456789abcdef01234567","path":"a.go","line":1,"claim":"gone"}]}`
	if err := os.WriteFile(cases, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	canned := map[string][]ReviewFinding{
		"correctness": {{Severity: "major", File: "a.go", Line: 4, Claim: "wrong"}},
		"tests":       {{Severity: "major", File: "b.go", Line: 3, Claim: "untested"}, {Severity: "minor", File: "c.go", Line: 1, Claim: "noise"}},
		"":            {{Severity: "major", File: "a.go", Line: 3, Claim: "wrong"}},
	}
	var calls []string
	review := func(_, _, _, dimension string) ([]ReviewFinding, error) {
		calls = append(calls, dimension)
		if dimension == "errors" {
			return nil, errors.New("reviewer crashed")
		}
		return canned[dimension], nil
	}
	panel := []string{"correctness", "tests", "errors"}
	rep, err := Calibrate(repo, cases, CalibrateOptions{Session: "cal", Main: "main", Panel: panel, Review: review})
	if err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	if strings.Join(calls, ",") != "correctness,tests,errors" {
		t.Errorf("reviews run = %v, want each persona once on the one reachable PR state", calls)
	}
	want := []CalibrationPersona{
		{Persona: "correctness", Groups: 1, Cases: 2, Hits: 1, Misses: 1, Extra: 0, Recall: 0.5},
		{Persona: "tests", Groups: 1, Cases: 2, Hits: 1, Misses: 1, Extra: 1, Recall: 0.5},
		{Persona: "errors"},
	}
	if !reflect.DeepEqual(rep.Personas, want) {
		t.Errorf("Personas = %+v, want %+v", rep.Personas, want)
	}
	if rep.Cases != 2 || rep.Hits != 2 || rep.Recall != 1 || rep.Extra != 1 {
		t.Errorf("panel union = %d/%d recall %.2f extra %d, want 2/2 1.00 1", rep.Hits, rep.Cases, rep.Recall, rep.Extra)
	}
	gr := rep.Reports[0]
	if gr.Found != 3 || len(gr.Missed) != 0 || len(gr.Personas) != 3 || gr.Personas[2].Skipped == "" {
		t.Errorf("group report = %+v, want 3 found, none missed, errors skipped", gr)
	}
	md := rep.Markdown()
	for _, s := range []string{"## Per persona", "| correctness | 1 | 2 | 1 | 1 | 0 | 0.50 |", "| errors | 0 | 0 | 0 | 0 | 0 | 0.00 |",
		"| **panel (any persona)** | 1 | 2 | 2 | 0 | 1 | 1.00 |"} {
		if !strings.Contains(md, s) {
			t.Errorf("markdown lacks %q:\n%s", s, md)
		}
	}
	// The same seed samples the same PR state with and without a panel.
	for seed := int64(1); seed <= 4; seed++ {
		one, err1 := Calibrate(repo, cases, CalibrateOptions{Session: "cal", Main: "main", Sample: 1, Seed: seed, Review: review})
		many, err2 := Calibrate(repo, cases, CalibrateOptions{Session: "cal", Main: "main", Sample: 1, Seed: seed, Panel: panel, Review: review})
		if err1 != nil || err2 != nil || one.Reports[0].PR != many.Reports[0].PR || one.Personas != nil || strings.Contains(one.Markdown(), "Per persona") {
			t.Errorf("seed %d: single %+v (%v) vs panel %+v (%v)", seed, one.Reports, err1, many.Reports, err2)
		}
	}
	if _, err := Calibrate(repo, cases, CalibrateOptions{Session: "cal", Panel: []string{"nope"}, Review: review}); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("unknown persona: err = %v", err)
	}
}

func TestCalibrateRun(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	initRepo(t, repo)
	git(t, repo, []string{"branch", "-M", "main"})
	git(t, repo, []string{"checkout", "-q", "-b", "feat"})
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package x\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, []string{"commit", "-q", "-am", "feature"})
	commit := git(t, repo, []string{"rev-parse", "HEAD"})
	base := git(t, repo, []string{"rev-parse", "main"})
	git(t, repo, []string{"checkout", "-q", "main"})
	cases := filepath.Join(t.TempDir(), "cases.json")
	body := `{"cases":[{"pr":7,"commit":"` + commit + `","path":"a.go","line":3,"claim":"A returns the wrong value"},` +
		`{"pr":8,"commit":"0123456789abcdef0123456789abcdef01234567","path":"a.go","line":1,"claim":"gone"}]}`
	if err := os.WriteFile(cases, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(line int) (CalibrationReport, string) {
		var wt string
		rep, err := Calibrate(repo, cases, CalibrateOptions{Session: "cal", Main: "main",
			Review: func(ledgerDir, workdir, task, dimension string) ([]ReviewFinding, error) {
				if dimension != "" {
					t.Fatalf("no panel: dimension %q, want the single reviewer", dimension)
				}
				wt = workdir
				events, err := ReadEvents(ledgerDir)
				if err != nil {
					t.Fatalf("ReadEvents: %v", err)
				}
				if b := dispatchBase(events, task, ""); b != base {
					t.Fatalf("dispatch base %q, want %q", b, base)
				}
				if h := git(t, workdir, []string{"rev-parse", "HEAD"}); h != commit {
					t.Fatalf("worktree HEAD %s, want %s", h, commit)
				}
				if paths, err := unitChangedPaths(workdir, base, task); err != nil || !reflect.DeepEqual(paths, []string{"a.go"}) {
					t.Fatalf("changed paths %v (%v), want [a.go]", paths, err)
				}
				return []ReviewFinding{{Severity: "major", File: "a.go", Line: line, Claim: "wrong value"}}, nil
			}})
		if err != nil {
			t.Fatalf("Calibrate: %v", err)
		}
		return rep, wt
	}

	rep, wt := run(5)
	if rep.Cases != 1 || rep.Hits != 1 || rep.Recall != 1.0 {
		t.Fatalf("near finding: %+v", rep)
	}
	if len(rep.Reports) != 2 || rep.Reports[1].Skipped == "" || !strings.Contains(rep.Reports[1].Skipped, "not found") {
		t.Fatalf("missing commit not skipped: %+v", rep.Reports)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("temp worktree %s still exists (%v)", wt, err)
	}
	if list := git(t, repo, []string{"worktree", "list"}); strings.Count(list, "\n") != 0 {
		t.Fatalf("worktree left registered:\n%s", list)
	}

	rep, _ = run(40)
	if rep.Hits != 0 || rep.Recall != 0 || rep.Extra != 1 {
		t.Fatalf("far finding: %+v", rep)
	}
	md := rep.Markdown()
	for _, want := range []string{"## Missed claims", "A returns the wrong value", "| #7 |", "skipped:", "recall 0.00"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown lacks %q:\n%s", want, md)
		}
	}
	if _, err := Calibrate(repo, cases, CalibrateOptions{}); !IsRuleRefusal(err) {
		t.Fatalf("no session: err = %v, want a refusal", err)
	}
}
