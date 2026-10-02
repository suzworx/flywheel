package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestParseStage checks stage: lines (issue #781): a valid line is
// normalised, two are kept in order, and every invalid form is kept out of
// Stage and in stageInvalid.
func TestParseStage(t *testing.T) {
	t.Parallel()
	h, err := ParseBriefHeaderBytes([]byte("owns: a\nstage:  staging\\claude -> .claude//\nstage: s/mcp/ -> m/\n\n# TASK\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []StageMap{{"staging/claude/", ".claude/"}, {"s/mcp/", "m/"}}
	if !slices.Equal(h.Stage, want) || len(h.stageInvalid) != 0 {
		t.Errorf("Stage = %q, invalid %q; want %q", h.Stage, h.stageInvalid, want)
	}
	for _, bad := range []string{"staging/claude/ .claude/", " -> .claude/", "staging/ -> ", "/abs/ -> .claude/",
		"C:/x/ -> .claude/", "a/../b/ -> .claude/", "a/ -> a/", "a/ -> a/b/", "a/b/ -> a/", ".claude/x/ -> y/"} {
		h, err := ParseBriefHeaderBytes([]byte("owns: a\nstage: " + bad + "\n\n# TASK\n"))
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Stage) != 0 || !slices.Equal(h.stageInvalid, []string{strings.TrimSpace(bad)}) {
			t.Errorf("stage: %q: Stage = %q, invalid %q; want it invalid", bad, h.Stage, h.stageInvalid)
		}
	}
}

// TestApplyStage checks applyStage (issue #781): nested files are copied with
// their parents created, an identical destination is reported Copied false
// and left alone, a missing From yields nothing, and SHA256 is the content's.
func TestApplyStage(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		fp := filepath.Join(wd, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("staging/claude/agents/x.md", "x\n")
	write("staging/claude/skills/s/SKILL.md", "s\n")
	write(".claude/skills/s/SKILL.md", "s\n")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	same := filepath.Join(wd, ".claude", "skills", "s", "SKILL.md")
	if err := os.Chtimes(same, old, old); err != nil {
		t.Fatal(err)
	}
	maps := []StageMap{{"staging/claude/", ".claude/"}, {"missing/", "elsewhere/"}}
	files, err := applyStage(wd, maps)
	if err != nil {
		t.Fatalf("applyStage() error = %v", err)
	}
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	want := []StagedFile{
		{From: "staging/claude/agents/x.md", To: ".claude/agents/x.md", SHA256: sum("x\n"), Copied: true},
		{From: "staging/claude/skills/s/SKILL.md", To: ".claude/skills/s/SKILL.md", SHA256: sum("s\n"), Copied: false},
	}
	if !slices.Equal(files, want) {
		t.Errorf("applyStage() = %+v, want %+v", files, want)
	}
	if b, err := os.ReadFile(filepath.Join(wd, ".claude", "agents", "x.md")); err != nil || string(b) != "x\n" {
		t.Errorf(".claude/agents/x.md = %q, %v; want the staged copy", b, err)
	}
	if fi, err := os.Stat(same); err != nil || !fi.ModTime().Equal(old) {
		t.Errorf("identical destination was rewritten: %v, %v", fi, err)
	}
	if files, err := applyStage(wd, maps[1:]); err != nil || len(files) != 0 {
		t.Errorf("applyStage(missing) = %+v, %v; want nothing", files, err)
	}
}

// stageTask is a flywheel dir whose brief owns owns and has the header line
// extra, plus an uncommitted staging/claude/agents/x.md the unit wrote.
func stageTask(t *testing.T, owns, extra string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	brief := "owns: " + owns + "\nneeds: none\n" + extra + "\ngate: exit 0\n\n# TASK: stage\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-10-02T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	staged := filepath.Join(dir, "staging", "claude", "agents", "x.md")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestValidateStage checks validate applies stage: lines before the gates
// (issue #781): the destination is written, a staged event is recorded and
// the owns check treats the destination as owned through its owned source;
// without that owns entry the destination is outside owns; an invalid line
// refuses with rule stage before any gate.
func TestValidateStage(t *testing.T) {
	t.Parallel()
	const line = "stage: staging/claude/ -> .claude/"
	t.Run("owned", func(t *testing.T) {
		t.Parallel()
		dir := stageTask(t, "staging/claude/", line)
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		if err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		if b, err := os.ReadFile(filepath.Join(dir, ".claude", "agents", "x.md")); err != nil || string(b) != "agent\n" {
			t.Errorf(".claude/agents/x.md = %q, %v; want the staged copy", b, err)
		}
		sum := sha256.Sum256([]byte("agent\n"))
		want := []StagedFile{{From: "staging/claude/agents/x.md", To: ".claude/agents/x.md", SHA256: hex.EncodeToString(sum[:]), Copied: true}}
		if ev := kindEvents(t, dir, "staged"); len(ev) != 1 || !slices.Equal(ev[0].Staged, want) || ev[0].Attempt != "r1" {
			t.Errorf("staged events = %+v, want one carrying %+v", ev, want)
		}
		if len(res.Outside) != 0 || !res.OwnsOK {
			t.Errorf("Outside = %q, OwnsOK %v; want nothing outside", res.Outside, res.OwnsOK)
		}
		if ev := kindEvents(t, dir, "owns_checked"); len(ev) != 1 || !slices.Equal(ev[0].Staged, want) {
			t.Errorf("owns_checked events = %+v, want Staged %+v", ev, want)
		}
	})
	t.Run("not owned", func(t *testing.T) {
		t.Parallel()
		dir := stageTask(t, "a.go", line)
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		if err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		if !slices.Contains(res.Outside, ".claude/agents/x.md") || res.OwnsOK {
			t.Errorf("Outside = %q, want .claude/agents/x.md outside owns", res.Outside)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		dir := stageTask(t, "staging/claude/", "stage: staging/claude/ .claude/")
		_, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		var rr *RuleRefusal
		if !errors.As(err, &rr) || rr.Rule != "stage" || !strings.Contains(rr.Fix, "staging/claude/ .claude/") {
			t.Fatalf("ValidateTask() error = %v, want a stage RuleRefusal naming the line", err)
		}
		if ev := kindEvents(t, dir, "validated"); len(ev) != 0 {
			t.Errorf("validated events = %+v, want no gate run", ev)
		}
	})
}

// TestLintStage checks lint on stage: lines (issue #781): an invalid line is
// a problem, a valid one whose From the unit owns nothing under a warning.
func TestLintStage(t *testing.T) {
	t.Parallel()
	brief := func(owns, stage string) string {
		return "owns: " + owns + " (new)\nneeds: none\ngate: true\nstage: " + stage + "\n\n# TASK: x\n## Checks\nreport\n"
	}
	res := lintWith(t, map[string]string{}, brief("staging/claude/", "staging/claude/ .claude/"), noGoList)
	if !slices.ContainsFunc(res.Problems, func(p string) bool {
		return strings.HasPrefix(p, "stage: staging/claude/ .claude/ is invalid: ")
	}) {
		t.Errorf("problems = %q, want the invalid stage line", res.Problems)
	}
	res = lintWith(t, map[string]string{}, brief("a.go", "staging/claude/ -> .claude/"), noGoList)
	if !slices.ContainsFunc(res.Warnings, func(w string) bool {
		return strings.HasPrefix(w, "stage maps staging/claude/ but owns nothing under it")
	}) {
		t.Errorf("warnings = %q, want the unowned stage warning", res.Warnings)
	}
	res = lintWith(t, map[string]string{}, brief("staging/claude/agents/x.md", "staging/claude/ -> .claude/"), noGoList)
	if slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.HasPrefix(w, "stage maps") }) {
		t.Errorf("warnings = %q, want no stage warning when owns has a path under From", res.Warnings)
	}
}
