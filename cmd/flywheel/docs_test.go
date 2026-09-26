package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// TestDocsCommandTable checks every registered command appears in the
// flywheel-operator skill's command table as "flywheel <name>", so the
// documented CLI never drifts from the implemented one.
func TestDocsCommandTable(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../skills/flywheel-operator/SKILL.md")
	if err != nil {
		t.Errorf("read skills/flywheel-operator/SKILL.md: %v", err)
		return
	}
	for name := range commands {
		if !strings.Contains(string(data), "flywheel "+name) {
			t.Errorf("command %q is missing from the flywheel-operator SKILL.md command table; add `flywheel %s` to it", name, name)
		}
	}
}

// TestDocsProtocolCitations checks docs/PROTOCOL.md exists with the expected
// first line and that every skill file driving the loop still cites the
// protocol version it implements, so a skill that stops citing it (or a
// protocol doc that falls out of sync) fails the build instead of drifting
// silently.
func TestDocsProtocolCitations(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../docs/PROTOCOL.md")
	if err != nil {
		t.Fatalf("read docs/PROTOCOL.md: %v", err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	if !strings.HasPrefix(first, "# flywheel protocol v") {
		t.Errorf("docs/PROTOCOL.md first line = %q, want a %q prefix", first, "# flywheel protocol v")
	}
	files := []string{
		"../../skills/flywheel/SKILL.md",
		"../../skills/flywheel-operator/SKILL.md",
		"../../skills/flywheel-worker/SKILL.md",
		"../../skills/flywheel-foreman/SKILL.md",
		"../../skills/flywheel-inspector/SKILL.md",
		"../../skills/flywheel-reviewer/SKILL.md",
		"../../skills/flywheel-auditor/SKILL.md",
		"../../skills/flywheel-planner/SKILL.md",
		"../../skills/flywheel-steward/SKILL.md",
		"../../skills/flywheel/references/worker-brief.md",
		"../../skills/flywheel/references/factory.md",
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("read %s: %v", f, err)
			continue
		}
		if !strings.Contains(string(b), "protocol v1") {
			t.Errorf("%s does not cite %q; add a link to docs/PROTOCOL.md", f, "protocol v1")
		}
	}
}

// TestDocsSkillAnchors checks every `(path.md#anchor)` and `(#anchor)` link in
// the markdown files under skills/ resolves to a heading in the target file
// under GitHub's slug rule, so a renamed heading cannot silently break a link.
func TestDocsSkillAnchors(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "skills")
	link := regexp.MustCompile(`\]\(([^)\s#]*\.md)?#([^)\s]+)\)`)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range link.FindAllStringSubmatch(string(data), -1) {
			target := path
			if m[1] != "" {
				target = filepath.Join(filepath.Dir(path), filepath.FromSlash(m[1]))
			}
			slugs, err := headingSlugs(target)
			if err != nil {
				t.Errorf("%s: link %q: %v", path, m[0], err)
				continue
			}
			if !slugs[m[2]] {
				t.Errorf("%s: link %q: no heading in %s has slug %q", path, m[0], target, m[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk skills/: %v", err)
	}
}

// headingSlugs returns the GitHub anchor slugs of the headings in a markdown
// file, skipping fenced code blocks.
func headingSlugs(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	slugs := map[string]bool{}
	fenced := false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced || !strings.HasPrefix(line, "#") {
			continue
		}
		text := strings.TrimLeft(line, "#")
		if !strings.HasPrefix(text, " ") {
			continue
		}
		slugs[githubSlug(strings.TrimSpace(text))] = true
	}
	return slugs, nil
}

// githubSlug lowercases a heading, drops every character that is not a
// letter, digit, space, hyphen or underscore, and turns spaces into hyphens.
func githubSlug(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(heading) {
		switch {
		case r == ' ':
			b.WriteRune('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}
