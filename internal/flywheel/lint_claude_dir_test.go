package flywheel

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestClaudeDirOwns checks which owns entries claudeDirOwns reports: any with
// a path segment exactly ".claude", patterns and backslashes too, never a
// negated entry, .claudeignore or docs/claude (issue #696).
func TestClaudeDirOwns(t *testing.T) {
	t.Parallel()
	owns := []string{
		".claude/skills/x/SKILL.md", "src/a.go", ".claude/**", ".claudeignore",
		"apps/web/.claude/settings.json", "docs/claude/x", `apps\.claude\a.md`, "!.claude/keep.md",
	}
	want := []string{".claude/skills/x/SKILL.md", ".claude/**", "apps/web/.claude/settings.json", `apps\.claude\a.md`}
	if got := claudeDirOwns(owns); !slices.Equal(got, want) {
		t.Errorf("claudeDirOwns() = %q, want %q", got, want)
	}
	if got := claudeDirOwns([]string{"a.go", ".claudeignore"}); len(got) != 0 {
		t.Errorf("claudeDirOwns() = %q, want none", got)
	}
}

// TestClaudeDirLint checks lint reports owns under .claude/ as a problem for a
// claude default worker and not for an opencode one (issue #696).
func TestClaudeDirLint(t *testing.T) {
	t.Parallel()
	brief := "owns: .claude/skills/x/SKILL.md (new)\nneeds: none\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	want := `owns .claude/skills/x/SKILL.md under .claude/: worker "w" (claude) cannot write there`
	for _, adapter := range []string{"claude", "opencode"} {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()
			cfg := fmt.Sprintf(`{"version":1,"workers":[{"name":"w","adapter":%q,"model":"m"}]}`, adapter)
			res := lintWith(t, map[string]string{".flywheel/config.json": cfg}, brief, noGoList)
			got := slices.ContainsFunc(res.Problems, func(p string) bool { return strings.HasPrefix(p, want) })
			if got != (adapter == "claude") {
				t.Errorf("claude-dir problem present = %v for %s; problems = %q", got, adapter, res.Problems)
			}
		})
	}
}

// TestClaudeDirRunRefused checks a dispatch to a claude worker whose owns has
// a .claude/ entry is refused with rule claude-dir before dispatched, and the
// same brief on the sim worker is not (issue #696).
func TestClaudeDirRunRefused(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: .claude/skills/a.md (new)\nneeds: none\ngate: go version\n\n# TASK x\n")
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
	if _, err := Run(dir, RunOptions{Task: "T1", Worker: "c"}); !errors.As(err, &rf) || rf.Rule != "claude-dir" || !strings.Contains(rf.Fix, "staging/claude/") {
		t.Fatalf("Run() error = %v, want a claude-dir refusal", err)
	}
	wantRefusedAppended(t, dir, before, "T1", "claude-dir")
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("sim Run() error = %v, want a dispatch", err)
	}
}

// TestClaudeDirDenialAttribution checks a denied write under .claude/ names
// the protection and any other denial is unchanged (issue #696).
func TestClaudeDirDenialAttribution(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Write /x/.claude/skills/a.md":    "Write /x/.claude/skills/a.md (.claude/ is protected by Claude Code; own a staging path)",
		`Edit C:\x\.claude\settings.json`: `Edit C:\x\.claude\settings.json (.claude/ is protected by Claude Code; own a staging path)`,
		"Write /x/src/a.go":               "Write /x/src/a.go",
		"Read /x/.claude/a.md":            "Read /x/.claude/a.md",
		"Write /x/.claudeignore":          "Write /x/.claudeignore",
	}
	for in, want := range cases {
		if got := attributeFileDenial(in); got != want {
			t.Errorf("attributeFileDenial(%q) = %q, want %q", in, got, want)
		}
	}
}
