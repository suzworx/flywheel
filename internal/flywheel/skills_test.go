package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// installSkill writes <root>/<rel>/<name>/SKILL.md.
func installSkill(t *testing.T, root, rel, name string) {
	t.Helper()
	d := filepath.Join(root, rel, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSkillsHeader checks skills: lines accumulate, keep order and drop
// duplicates, and an empty skills: line is flagged (issue #695).
func TestSkillsHeader(t *testing.T) {
	t.Parallel()
	h, err := ParseBriefHeaderBytes([]byte("skills: tdd, engineering:debug\nskills: go-style ,tdd\n\n# TASK x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"tdd", "engineering:debug", "go-style"}; !slices.Equal(h.Skills, want) || h.skillsEmpty {
		t.Errorf("Skills = %q empty=%v, want %q not empty", h.Skills, h.skillsEmpty, want)
	}
	if h, _ := ParseBriefHeaderBytes([]byte("skills:\n\n# TASK x\n")); !h.skillsEmpty || len(h.Skills) != 0 {
		t.Errorf("empty skills: line: Skills = %q empty=%v, want none, flagged", h.Skills, h.skillsEmpty)
	}
}

// TestSkillsMissing checks where each adapter looks for installed skills.
func TestSkillsMissing(t *testing.T) {
	t.Parallel()
	tree, home := t.TempDir(), t.TempDir()
	installSkill(t, tree, ".claude/skills", "intree")
	installSkill(t, home, ".claude/skills", "inhome")
	installSkill(t, tree, ".opencode/skill", "oc")
	if err := os.MkdirAll(filepath.Join(tree, ".claude", "skills", "nofile"), 0o755); err != nil {
		t.Fatal(err)
	}
	names := []string{"intree", "inhome", "absent", "x:y", "nofile", "oc"}
	got, ok := missingSkills("claude", tree, home, names)
	if want := []string{"absent", "nofile", "oc"}; !ok || !slices.Equal(got, want) {
		t.Errorf("claude missing = %q checkable=%v, want %q", got, ok, want)
	}
	got, ok = missingSkills("opencode", tree, home, names)
	if want := []string{"absent", "nofile"}; !ok || !slices.Equal(got, want) {
		t.Errorf("opencode missing = %q checkable=%v, want %q", got, ok, want)
	}
	if got, ok := missingSkills("codex", tree, home, names); ok || got != nil {
		t.Errorf("codex missing = %q checkable=%v, want not checkable", got, ok)
	}
	p := skillsProblem(Worker{Name: "w", Adapter: "claude"}, tree, home, names)
	for _, want := range []string{`skills absent, nofile, oc not installed for worker "w" (claude): looked in `, filepath.Join(home, ".claude", "skills"), "drop it from skills:"} {
		if !strings.Contains(p, want) {
			t.Errorf("skillsProblem() = %q, want it to contain %q", p, want)
		}
	}
	if p := skillsProblem(Worker{Name: "w", Adapter: "claude"}, tree, home, []string{"intree", "x:y"}); p != "" {
		t.Errorf("skillsProblem() = %q, want none", p)
	}
	if w := skillsWarning(Worker{Name: "cx", Adapter: "codex"}, []string{"a"}); w != `skills: cannot check installed skills for worker "cx" (codex)` {
		t.Errorf("skillsWarning(codex) = %q", w)
	}
	if w := skillsWarning(Worker{Name: "w", Adapter: "claude"}, []string{"a"}); w != "" {
		t.Errorf("skillsWarning(claude) = %q, want none", w)
	}
}

// TestSkillsLint checks lint reports a missing skill for a claude default
// worker, none once it is installed in the tree, and an empty skills: line.
func TestSkillsLint(t *testing.T) {
	t.Parallel()
	cfg := `{"version":1,"workers":[{"name":"w","adapter":"claude","model":"m"}]}`
	brief := "owns: a.go\nneeds: none\nskills: fw-695-not-installed\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	want := `skills fw-695-not-installed not installed for worker "w" (claude)`
	has := func(ps []string, s string) bool {
		return slices.ContainsFunc(ps, func(p string) bool { return strings.HasPrefix(p, s) })
	}
	if res := lintWith(t, map[string]string{".flywheel/config.json": cfg}, brief, noGoList); !has(res.Problems, want) {
		t.Errorf("problems = %q, want %q", res.Problems, want)
	}
	installed := map[string]string{".flywheel/config.json": cfg, ".claude/skills/fw-695-not-installed/SKILL.md": "x"}
	if res := lintWith(t, installed, brief, noGoList); has(res.Problems, "skills ") {
		t.Errorf("problems = %q, want no skills problem", res.Problems)
	}
	empty := strings.Replace(brief, "skills: fw-695-not-installed", "skills:", 1)
	if res := lintWith(t, map[string]string{".flywheel/config.json": cfg}, empty, noGoList); !has(res.Problems, "skills: line is empty") {
		t.Errorf("problems = %q, want the empty skills: line", res.Problems)
	}
}

// TestSkillsFreshPrompt checks the dispatch prompt names the skills to load,
// after the increment sentence, and is unchanged without skills.
func TestSkillsFreshPrompt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		r    RunRequest
		want string
	}{
		{RunRequest{Skills: []string{"tdd", "x:y"}}, freshMessage + " Load these skills before any other work: tdd, x:y."},
		{RunRequest{Increment: 2, Skills: []string{"tdd"}}, freshMessage + " Do increment 2 only, then report and STOP. Load these skills before any other work: tdd."},
		{RunRequest{}, freshMessage},
	}
	for _, c := range cases {
		if got := freshPrompt(c.r); got != c.want {
			t.Errorf("freshPrompt(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
}

// TestSkillsEventValidate checks only a dispatched event may carry skills.
func TestSkillsEventValidate(t *testing.T) {
	t.Parallel()
	e := Event{TS: "2026-09-30T00:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Skills: []string{"tdd"}}
	if err := Validate(e); err == nil || !strings.Contains(err.Error(), "only a dispatched event may carry them") {
		t.Errorf("finished with skills: Validate() = %v, want refused", err)
	}
	d := Event{TS: "2026-09-30T00:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "sim", Model: "m", Skills: []string{"tdd"}}
	if err := Validate(d); err != nil {
		t.Errorf("dispatched with skills: Validate() = %v, want skills accepted", err)
	}
}

// TestSkillsRunRefused checks a claude dispatch naming a skill that is not
// installed is refused with rule skills before dispatched, and the sim worker
// (not checkable) dispatches and records the skills on dispatched.
func TestSkillsRunRefused(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go (new)\nneeds: none\nskills: fw-695-not-installed\ngate: go version\n\n# TASK x\n")
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workers = append(cfg.Workers, Worker{Name: "c", Adapter: "claude", Model: "m"})
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, RunOptions{Task: "T1", Worker: "c"}); !errors.As(err, &rf) || rf.Rule != "skills" || !strings.Contains(rf.Fix, `skills fw-695-not-installed not installed for worker "c" (claude)`) {
		t.Fatalf("Run() error = %v, want a skills refusal", err)
	}
	wantRefusedAppended(t, dir, before, "T1", "skills")
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("sim Run() error = %v, want a dispatch", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(events, func(e Event) bool { return e.Kind == "dispatched" })
	if i < 0 || !slices.Equal(events[i].Skills, []string{"fw-695-not-installed"}) {
		t.Errorf("dispatched event skills missing; events = %+v", events)
	}
}
