package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillNameVersionLine checks the frontmatter version-line parser: a
// missing version key, a malformed (non X.Y.Z) version, and a valid one.
func TestSkillNameVersionLine(t *testing.T) {
	cases := []struct {
		name    string
		lines   []string
		wantOK  bool
		wantVer string
	}{
		{"missing", []string{"---", "name: demo", "metadata:", "---"}, false, ""},
		{"malformed", []string{"---", "name: demo", "metadata:", "  version: v1", "---"}, false, ""},
		{"valid", []string{"---", "name: demo", "metadata:", "  version: 0.3.0", "---"}, true, "0.3.0"},
		{"annotated", []string{"---", "name: demo", "metadata:", "  version: 0.3.0 # x-release-please-version", "---"}, true, "0.3.0"},
	}
	for _, c := range cases {
		name, ver, ok := skillNameVersion(c.lines)
		if ok != c.wantOK {
			t.Errorf("%s: skillNameVersion ok = %v, want %v", c.name, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if name != "demo" || ver != c.wantVer {
			t.Errorf("%s: skillNameVersion = (%q, %q), want (%q, %q)", c.name, name, ver, "demo", c.wantVer)
		}
	}
}

// TestParseFrontmatterLineStripsComment checks that an inline "# comment" —
// release-please's generic-updater annotation in particular — is stripped
// from the value, along with the space before it, so the bare version
// remains for semver matching.
func TestParseFrontmatterLineStripsComment(t *testing.T) {
	key, value, ok := parseFrontmatterLine("  version: 0.3.0 # x-release-please-version")
	if !ok || key != "version" || value != "0.3.0" {
		t.Errorf("parseFrontmatterLine(annotated) = (%q, %q, %v), want (%q, %q, true)", key, value, ok, "version", "0.3.0")
	}
}

// TestSkillNameVersionBeyondTenLines checks the scan stops at the 10th line,
// so a version past it is never found.
func TestSkillNameVersionBeyondTenLines(t *testing.T) {
	lines := []string{"---", "name: demo", "1", "2", "3", "4", "5", "6", "7", "8", "  version: 0.3.0"}
	if _, _, ok := skillNameVersion(lines); ok {
		t.Errorf("skillNameVersion found a version past the first 10 lines")
	}
}

// TestOlderVersion checks the numeric X.Y.Z comparison: an older skill
// version is older, but an equal or newer one is not, and an unparsable
// version on either side is never older.
func TestOlderVersion(t *testing.T) {
	cases := []struct {
		sv, bv string
		want   bool
	}{
		{"0.3.0", "0.4.0", true},
		{"0.4.0", "0.4.0", false},
		{"0.5.0", "0.4.0", false},
		{"0.3.9", "0.4.0", true},
		{"1.0.0", "0.4.0", false},
		{"v1.0.0", "2.0.0", false},
		{"1.0.0", "dev", false},
	}
	for _, c := range cases {
		if got := olderVersion(c.sv, c.bv); got != c.want {
			t.Errorf("olderVersion(%q, %q) = %v, want %v", c.sv, c.bv, got, c.want)
		}
	}
}

// TestWarnStaleSkillsDevBuildSkip checks that a dev build ("dev" does not
// match X.Y.Z) prints no warnings at all, even with stale skills present.
func TestWarnStaleSkillsDevBuildSkip(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "demo", "0.1.0")
	var buf bytes.Buffer
	warnStaleSkills(&buf, dir, "dev")
	if buf.Len() != 0 {
		t.Errorf("warnStaleSkills with dev build wrote %q, want nothing", buf.String())
	}
}

// TestWarnStaleSkillsReportsOnlyOlder checks warnStaleSkills warns for a
// stale skill, stays silent for an up-to-date and a newer one, and the
// warning line matches the exact format.
func TestWarnStaleSkillsReportsOnlyOlder(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "demo", "0.3.0")
	writeSkill(t, dir, "ok", "0.4.0")
	writeSkill(t, dir, "future", "0.5.0")
	var buf bytes.Buffer
	warnStaleSkills(&buf, dir, "0.4.0")
	got := buf.String()
	want := "warning: skill demo is version 0.3.0, older than flywheel 0.4.0\n"
	if got != want {
		t.Errorf("warnStaleSkills output = %q, want %q", got, want)
	}
	if strings.Count(got, "warning:") != 1 {
		t.Errorf("warnStaleSkills output has %d warnings, want 1: %q", strings.Count(got, "warning:"), got)
	}
}

// writeSkill creates dir/skills/<name>/SKILL.md with a minimal frontmatter
// naming the skill and its metadata version.
func writeSkill(t *testing.T, dir, name, version string) {
	t.Helper()
	skillDir := filepath.Join(dir, "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	content := "---\nname: " + name + "\nmetadata:\n  version: " + version + "\n---\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}
