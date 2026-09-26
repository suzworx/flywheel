package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// briefIssueDir returns a temp dir holding the owned files a.go and b.go, and
// go.mod when goMod is set.
func briefIssueDir(t *testing.T, goMod bool) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{"a.go": "package x\n", "b.go": "package x\n"}
	if goMod {
		files["go.mod"] = "module example.com/x\n\ngo 1.22\n"
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var briefTestIssue = TrackerIssue{Number: 7, Title: "Add brief", Body: "Leads re-write glue.\r\nEvery time.  \r\n\r\n", URL: "https://github.com/o/r/issues/7"}

// TestBriefFromIssueWritesLintedBrief checks the written brief parses with the
// given owns, needs and gates, lints clean, and carries the title line, the
// URL and the body with CRLF normalised (issue #457).
func TestBriefFromIssueWritesLintedBrief(t *testing.T) {
	t.Parallel()
	dir := briefIssueDir(t, true)
	opts := BriefIssueOptions{Owns: []string{"a.go", "b.go"}, Needs: []string{"t1"}, Gates: []string{"go build ./...", "go test ./..."}}
	rel, err := BriefFromIssue(dir, "t7", briefTestIssue, opts)
	if err != nil {
		t.Fatalf("BriefFromIssue() error = %v", err)
	}
	if rel != ".flywheel/briefs/t7.txt" {
		t.Errorf("path = %q, want .flywheel/briefs/t7.txt", rel)
	}
	path := filepath.Join(dir, rel)
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if !reflect.DeepEqual(h.Owns, opts.Owns) || !reflect.DeepEqual(h.Needs, opts.Needs) || !reflect.DeepEqual(h.Gates, opts.Gates) {
		t.Errorf("header owns %v needs %v gates %v, want %v %v %v", h.Owns, h.Needs, h.Gates, opts.Owns, opts.Needs, opts.Gates)
	}
	res, err := LintBrief(dir, path)
	if err != nil || len(res.Problems) > 0 {
		t.Errorf("LintBrief() = %+v, %v; want no problems", res, err)
	}
	b, _ := os.ReadFile(path)
	text := string(b)
	for _, want := range []string{"# TASK: Add brief (issue #7)\n", "Issue: https://github.com/o/r/issues/7\n", "Leads re-write glue.\nEvery time.\n\n## Checks\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("brief lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\r") || !strings.HasSuffix(text, "exit code.\n") || strings.HasSuffix(text, "\n\n") {
		t.Errorf("brief has a CR or does not end in exactly one newline:\n%q", text)
	}
}

// TestBriefFromIssueDefaultsAndRefusals checks the default Go gate and needs,
// the refusal of an existing brief without Force and its replacement with it,
// and the refusals of missing owns and of no gate outside a Go module.
func TestBriefFromIssueDefaultsAndRefusals(t *testing.T) {
	t.Parallel()
	dir := briefIssueDir(t, true)
	rel, err := BriefFromIssue(dir, "t7", briefTestIssue, BriefIssueOptions{Owns: []string{"a.go"}})
	if err != nil {
		t.Fatalf("BriefFromIssue() error = %v", err)
	}
	h, _ := ParseBriefHeader(filepath.Join(dir, rel))
	if !reflect.DeepEqual(h.Gates, []string{defaultGoGate}) || !h.NeedsDeclared || len(h.Needs) != 0 {
		t.Errorf("gates %v needs %v declared %v, want the Go gate and needs: none", h.Gates, h.Needs, h.NeedsDeclared)
	}
	if _, err := BriefFromIssue(dir, "t7", briefTestIssue, BriefIssueOptions{Owns: []string{"b.go"}}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Errorf("existing brief without Force: err = %v, want a refusal naming --force", err)
	}
	if _, err := BriefFromIssue(dir, "t7", briefTestIssue, BriefIssueOptions{Owns: []string{"b.go"}, Force: true}); err != nil {
		t.Fatalf("existing brief with Force: err = %v", err)
	}
	if h, _ := ParseBriefHeader(filepath.Join(dir, rel)); !reflect.DeepEqual(h.Owns, []string{"b.go"}) {
		t.Errorf("forced brief owns = %v, want [b.go]", h.Owns)
	}
	if _, err := BriefFromIssue(dir, "t8", briefTestIssue, BriefIssueOptions{}); err == nil || !strings.Contains(err.Error(), "owns is required") {
		t.Errorf("missing owns: err = %v, want owns is required", err)
	}
	noMod := briefIssueDir(t, false)
	if _, err := BriefFromIssue(noMod, "t9", briefTestIssue, BriefIssueOptions{Owns: []string{"a.go"}}); err == nil || !strings.Contains(err.Error(), "--gate") {
		t.Errorf("no go.mod and no gate: err = %v, want a refusal naming --gate", err)
	}
}

// TestBriefFromIssueRemovesBriefWithLintProblems checks a brief lint rejects
// (an owned path that does not exist) is removed and the problem reported.
func TestBriefFromIssueRemovesBriefWithLintProblems(t *testing.T) {
	t.Parallel()
	dir := briefIssueDir(t, true)
	_, err := BriefFromIssue(dir, "t7", briefTestIssue, BriefIssueOptions{Owns: []string{"missing.go"}})
	if err == nil || !strings.Contains(err.Error(), "missing.go") {
		t.Errorf("err = %v, want a lint problem naming missing.go", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, ".flywheel", "briefs", "t7.txt")); !os.IsNotExist(serr) {
		t.Errorf("brief with lint problems still on disk (stat err=%v)", serr)
	}
}
