package flywheel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// agentNameRE is the shape of an agent: name (issue #755): a file name under
// .claude/agents/ with no path separator.
var agentNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// agentDef is a Claude Code agent file's definition as flywheel passes it to
// claude --agents (issue #755). The file's model and every other key are
// ignored: the worker config's model wins.
type agentDef struct {
	Description string
	Tools       []string
	Prompt      string
}

// agentFile returns the agent file for name (issue #755):
// <tree>/.claude/agents/<name>.md, else <home>/.claude/agents/<name>.md; an
// empty home skips the second.
func agentFile(tree, home, name string) (path string, ok bool) {
	for _, root := range []string{tree, home} {
		if root == "" {
			continue
		}
		p := filepath.Join(root, ".claude", "agents", name+".md")
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// agentPaths are the paths agentFile looks in, for a problem message.
func agentPaths(tree, home, name string) []string {
	ps := []string{filepath.Join(tree, ".claude", "agents", name+".md")}
	if home != "" {
		ps = append(ps, filepath.Join(home, ".claude", "agents", name+".md"))
	}
	return ps
}

// parseAgentFile parses an agent file (issue #755): YAML-like frontmatter
// between a first line "---" and the next "---" line, of which description
// and tools are read, then the body, trimmed, as the prompt.
func parseAgentFile(b []byte) (agentDef, error) {
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return agentDef{}, errors.New("no frontmatter: the first line must be ---")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return agentDef{}, errors.New("no frontmatter: no closing --- line")
	}
	var d agentDef
	lastKey := ""
	for _, line := range lines[1:end] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			// A block list item under the last key.
			if item, ok := strings.CutPrefix(strings.TrimSpace(line), "-"); ok && lastKey == "tools" {
				if t := unquote(strings.TrimSpace(item)); t != "" {
					d.Tools = append(d.Tools, t)
				}
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			lastKey = ""
			continue
		}
		lastKey = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch lastKey {
		case "description":
			d.Description = unquote(val)
		case "tools":
			val = strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
			for _, t := range strings.Split(val, ",") {
				if t = unquote(strings.TrimSpace(t)); t != "" {
					d.Tools = append(d.Tools, t)
				}
			}
		}
	}
	d.Prompt = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	if d.Prompt == "" {
		return agentDef{}, errors.New("empty body: the agent's prompt follows the frontmatter")
	}
	return d, nil
}

// inlineJSON is d as the claude --agents value for name (issue #755):
// {"<name>":{"description":...,"prompt":...,"tools":[...]}}, tools omitted
// when empty and a missing description replaced by name.
func (d agentDef) inlineJSON(name string) string {
	type entry struct {
		Description string   `json:"description"`
		Prompt      string   `json:"prompt"`
		Tools       []string `json:"tools,omitempty"`
	}
	desc := d.Description
	if desc == "" {
		desc = name
	}
	b, err := json.Marshal(map[string]entry{name: {Description: desc, Prompt: d.Prompt, Tools: d.Tools}})
	if err != nil {
		return ""
	}
	return string(b)
}

// loadAgent finds and parses the agent file for name (issue #755): its path,
// bytes and definition, or a problem naming what is wrong.
func loadAgent(tree, home, name string) (path string, b []byte, d agentDef, problem string) {
	path, ok := agentFile(tree, home, name)
	if !ok {
		return "", nil, agentDef{}, fmt.Sprintf("agent %q not found: looked in %s; add the agent file or drop agent:",
			name, strings.Join(agentPaths(tree, home, name), ", "))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, agentDef{}, fmt.Sprintf("agent %q: %v", name, err)
	}
	d, err = parseAgentFile(b)
	if err != nil {
		return "", nil, agentDef{}, fmt.Sprintf("agent %q: %s: %v", name, path, err)
	}
	return path, b, d, ""
}

// agentProblem is what stops a brief's agent: name from running on w (issue
// #755): "" when name is empty or the agent resolves; a problem when w is not
// a claude worker, no agent file is found, or the file does not parse. Lint
// reports it as a problem; Run refuses with rule agent.
func agentProblem(w Worker, tree, home, name string) string {
	if name == "" {
		return ""
	}
	if w.Adapter != "claude" {
		return fmt.Sprintf("agent: is only supported by the claude adapter; worker %q is %s", w.Name, w.Adapter)
	}
	_, _, _, p := loadAgent(tree, home, name)
	return p
}

// unquote strips one pair of matching surrounding quotes.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
