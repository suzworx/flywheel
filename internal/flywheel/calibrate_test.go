package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadCalibrationCases(t *testing.T) {
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

func TestCalibrateRun(t *testing.T) {
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
			Review: func(ledgerDir, workdir, task string) ([]ReviewFinding, error) {
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
