package flywheel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeAgent writes <root>/.claude/agents/<name>.md.
func writeAgent(t *testing.T, root, name, content string) {
	t.Helper()
	d := filepath.Join(root, ".claude", "agents")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAgentParseFile checks the frontmatter keys read, the tools forms and the
// errors (issue #755).
func TestAgentParseFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in  string
		desc      string
		tools     []string
		prompt    string
		wantError string
	}{
		{"comma", "---\nname: r\ndescription: Reviews code\ntools: Read, Grep\n---\nYou review.\n", "Reviews code", []string{"Read", "Grep"}, "You review.", ""},
		{"bracket", "---\ntools: [Read, \"Bash\"]\n---\nbody", "", []string{"Read", "Bash"}, "body", ""},
		{"block", "---\ntools:\n  - Read\n  - Edit\ndescription: d\n---\n\n  body  \n", "d", []string{"Read", "Edit"}, "body", ""},
		{"crlf", "---\r\ndescription: 'Quoted one'\r\ntools: Read\r\n---\r\nline1\r\nline2\r\n", "Quoted one", []string{"Read"}, "line1\nline2", ""},
		{"quoted", "---\ndescription: \"Has: a colon\"\n---\nb", "Has: a colon", nil, "b", ""},
		{"model ignored", "---\nmodel: opus\ncolor: red\n---\nb", "", nil, "b", ""},
		{"no frontmatter", "You review.\n", "", nil, "", "no frontmatter"},
		{"unclosed", "---\ndescription: d\n", "", nil, "", "no closing"},
		{"empty body", "---\ndescription: d\n---\n  \n", "", nil, "", "empty body"},
	}
	for _, c := range cases {
		d, err := parseAgentFile([]byte(c.in))
		if c.wantError != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantError) {
				t.Errorf("%s: error = %v, want %q", c.name, err, c.wantError)
			}
			continue
		}
		if err != nil || d.Description != c.desc || !slices.Equal(d.Tools, c.tools) || d.Prompt != c.prompt {
			t.Errorf("%s: parseAgentFile() = %+v, %v; want %q %q %q", c.name, d, err, c.desc, c.tools, c.prompt)
		}
	}
}

// TestAgentFilePrecedence checks the tree's agent wins over the home's, the
// home is a fallback, and an empty home is skipped.
func TestAgentFilePrecedence(t *testing.T) {
	t.Parallel()
	tree, home := t.TempDir(), t.TempDir()
	writeAgent(t, tree, "both", "---\n---\ntree")
	writeAgent(t, home, "both", "---\n---\nhome")
	writeAgent(t, home, "homeonly", "---\n---\nhome")
	if p, ok := agentFile(tree, home, "both"); !ok || p != filepath.Join(tree, ".claude", "agents", "both.md") {
		t.Errorf("agentFile(both) = %q %v, want the tree's", p, ok)
	}
	if p, ok := agentFile(tree, home, "homeonly"); !ok || p != filepath.Join(home, ".claude", "agents", "homeonly.md") {
		t.Errorf("agentFile(homeonly) = %q %v, want the home's", p, ok)
	}
	if _, ok := agentFile(tree, "", "homeonly"); ok {
		t.Errorf("agentFile(homeonly, no home) found, want not found")
	}
}

// TestAgentInlineJSON checks the --agents value round-trips, tools are
// omitted when empty and a missing description becomes the name.
func TestAgentInlineJSON(t *testing.T) {
	t.Parallel()
	type entry struct {
		Description string   `json:"description"`
		Prompt      string   `json:"prompt"`
		Tools       []string `json:"tools"`
	}
	var got map[string]entry
	s := agentDef{Description: "d \"q\"", Tools: []string{"Read"}, Prompt: "line1\nline2"}.inlineJSON("rev")
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", s, err)
	}
	if e := got["rev"]; e.Description != "d \"q\"" || e.Prompt != "line1\nline2" || !slices.Equal(e.Tools, []string{"Read"}) {
		t.Errorf("inlineJSON() = %s, round-trip %+v", s, got)
	}
	if s := (agentDef{Prompt: "p"}).inlineJSON("rev"); s != `{"rev":{"description":"rev","prompt":"p"}}` {
		t.Errorf("inlineJSON(no description, no tools) = %s", s)
	}
}

// TestAgentProblem checks a non-claude worker and a missing file are
// problems and a resolving agent or no agent is none.
func TestAgentProblem(t *testing.T) {
	t.Parallel()
	tree, home := t.TempDir(), t.TempDir()
	writeAgent(t, tree, "ok", "---\ndescription: d\n---\nbody")
	writeAgent(t, tree, "bad", "no frontmatter")
	claude := Worker{Name: "c", Adapter: "claude"}
	if p := agentProblem(Worker{Name: "o", Adapter: "opencode"}, tree, home, "ok"); p != `agent: is only supported by the claude adapter; worker "o" is opencode` {
		t.Errorf("agentProblem(opencode) = %q", p)
	}
	p := agentProblem(claude, tree, home, "absent")
	for _, want := range []string{`agent "absent" not found`, filepath.Join(tree, ".claude", "agents", "absent.md"), filepath.Join(home, ".claude", "agents", "absent.md")} {
		if !strings.Contains(p, want) {
			t.Errorf("agentProblem(absent) = %q, want it to contain %q", p, want)
		}
	}
	if p := agentProblem(claude, tree, home, "bad"); !strings.Contains(p, "no frontmatter") {
		t.Errorf("agentProblem(bad) = %q, want the parse error", p)
	}
	if p := agentProblem(claude, tree, home, "ok"); p != "" {
		t.Errorf("agentProblem(ok) = %q, want none", p)
	}
	if p := agentProblem(Worker{Adapter: "sim"}, tree, home, ""); p != "" {
		t.Errorf("agentProblem(no agent) = %q, want none", p)
	}
}

// TestAgentHeaderAndLint checks the agent: header and its lint problems: an
// empty line, a second line, a malformed name and a missing agent file.
func TestAgentHeaderAndLint(t *testing.T) {
	t.Parallel()
	if h, err := ParseBriefHeaderBytes([]byte("agent:  rev \n\n# TASK x\n")); err != nil || h.Agent != "rev" || h.agentEmpty || h.agentRepeated {
		t.Errorf("Agent = %q empty=%v repeated=%v err=%v, want rev", h.Agent, h.agentEmpty, h.agentRepeated, err)
	}
	cfg := `{"version":1,"workers":[{"name":"w","adapter":"claude","model":"m"}]}`
	brief := "owns: a.go\nneeds: none\nagent: fw-755-absent\ngate: true\n\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	has := func(ps []string, s string) bool {
		return slices.ContainsFunc(ps, func(p string) bool { return strings.HasPrefix(p, s) })
	}
	cases := []struct{ brief, want string }{
		{brief, `agent "fw-755-absent" not found`},
		{strings.Replace(brief, "agent: fw-755-absent", "agent:", 1), "agent: line is empty"},
		{strings.Replace(brief, "agent: fw-755-absent", "agent: a\nagent: b", 1), "agent: appears more than once"},
		{strings.Replace(brief, "agent: fw-755-absent", "agent: ../x", 1), `agent name "../x" is not a valid agent name`},
	}
	for _, c := range cases {
		if res := lintWith(t, map[string]string{".flywheel/config.json": cfg}, c.brief, noGoList); !has(res.Problems, c.want) {
			t.Errorf("problems = %q, want %q", res.Problems, c.want)
		}
	}
	found := map[string]string{".flywheel/config.json": cfg, ".claude/agents/fw-755-absent.md": "---\n---\nbody"}
	if res := lintWith(t, found, brief, noGoList); has(res.Problems, "agent") {
		t.Errorf("problems = %q, want no agent problem", res.Problems)
	}
}

// TestAgentClaudeCommand checks the claude command passes --agents <json>
// --agent <name> on fresh and resumed runs, before --resume, and no agent
// flag without one.
func TestAgentClaudeCommand(t *testing.T) {
	t.Parallel()
	js := `{"rev":{"description":"d","prompt":"p"}}`
	for _, resume := range []bool{false, true} {
		_, args := claudeAdapter{}.Command(RunRequest{Model: "m", Agent: "rev", AgentJSON: js, Resume: resume, Session: "s1"})
		i := slices.Index(args, "--agents")
		if i < 0 || i+3 >= len(args) || args[i+1] != js || args[i+2] != "--agent" || args[i+3] != "rev" {
			t.Errorf("resume=%v args = %q, want --agents %s --agent rev", resume, args, js)
		}
		if r := slices.Index(args, "--resume"); resume && r < i {
			t.Errorf("args = %q, want --agent before --resume", args)
		}
		if !slices.Contains(args, "--append-system-prompt") {
			t.Errorf("args = %q, want the worker rules kept", args)
		}
	}
	if _, args := (claudeAdapter{}).Command(RunRequest{Model: "m"}); slices.Contains(args, "--agent") || slices.Contains(args, "--agents") {
		t.Errorf("args = %q, want no agent flag", args)
	}
}

// TestAgentRunRefused checks Run refuses with rule agent before dispatched: a
// claude worker whose agent file is missing, a sim worker naming an agent,
// and a correction naming a different agent than the base brief's.
func TestAgentRunRefused(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go (new)\nneeds: none\nagent: fw-755-absent\ngate: go version\n\n# TASK x\n")
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workers = append(cfg.Workers, Worker{Name: "c", Adapter: "claude", Model: "m"})
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	refused := func(o RunOptions, want string) {
		t.Helper()
		before, err := ReadEvents(dir)
		if err != nil {
			t.Fatal(err)
		}
		var rf *RuleRefusal
		if _, err := Run(dir, o); !errors.As(err, &rf) || rf.Rule != "agent" || !strings.Contains(rf.Fix, want) {
			t.Fatalf("Run(%+v) error = %v, want an agent refusal containing %q", o, err, want)
		}
		wantRefusedAppended(t, dir, before, "T1", "agent")
	}
	refused(RunOptions{Task: "T1", Worker: "c"}, `agent "fw-755-absent" not found`)
	refused(RunOptions{Task: "T1"}, "agent: is only supported by the claude adapter")

	writeAgent(t, dir, "fw-755-absent", "---\n---\nbody")
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Worker: "c", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	delta := filepath.Join(t.TempDir(), "delta.md")
	if err := os.WriteFile(delta, []byte("agent: other\n\n# fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused(RunOptions{Task: "T1", Worker: "c", DeltaPath: delta}, `the correction names agent "other"`)
}
