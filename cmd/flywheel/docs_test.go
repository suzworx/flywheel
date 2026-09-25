package main

import (
	"os"
	"strings"
	"testing"
)

// TestDocsCommandTable checks every registered command appears in the
// flywheel-operator skill's command table as "flywheel <name>", so the
// documented CLI never drifts from the implemented one.
func TestDocsCommandTable(t *testing.T) {
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
