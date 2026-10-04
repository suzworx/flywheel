package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ownsCompanionsFindings lints brief under the lint config cfg and returns the
// problems that mention lint.owns_companions (issue #785).
func ownsCompanionsFindings(t *testing.T, cfg, brief string) []string {
	t.Helper()
	res := lintWith(t, map[string]string{".flywheel/config.json": lintConfigJSON(cfg)}, brief, noGoList)
	var probs []string
	for _, p := range res.Problems {
		if strings.Contains(p, "owns_companions") {
			probs = append(probs, p)
		}
	}
	return probs
}

// TestOwnsCompanionsPrefix: a brief owning a path under a prefix must own its
// companions; one owning nothing under it needs none (issue #785).
func TestOwnsCompanionsPrefix(t *testing.T) {
	t.Parallel()
	cfg := `{"owns_companions":{".github/workflows/":[".env.github.example"]}}`
	probs := ownsCompanionsFindings(t, cfg, "owns: .github/workflows/ci.yml\nneeds: none\ngate: echo x\n\n# TASK x\n")
	want := "owns no companion .env.github.example required for .github/workflows/ (lint.owns_companions)"
	if !slices.Equal(probs, []string{want}) {
		t.Errorf("problems = %v, want only %q", probs, want)
	}
	if probs := ownsCompanionsFindings(t, cfg, "owns: .github/workflows/ci.yml, .env.github.example\nneeds: none\ngate: echo x\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none with the companion owned", probs)
	}
	if probs := ownsCompanionsFindings(t, cfg, "owns: src/a.go\nneeds: none\ngate: echo x\n\n# TASK x\n"); len(probs) != 0 {
		t.Errorf("problems = %v, want none: src/ is under no key", probs)
	}
}

// TestOwnsCompanionsMatching: a companion owned through a dir/ prefix or a
// glob counts, a negated entry excluding it does not, and the "" key applies
// to every brief (issue #785).
func TestOwnsCompanionsMatching(t *testing.T) {
	t.Parallel()
	lc := &LintConfig{OwnsCompanions: map[string][]string{"src/": {"docs/a.md"}}}
	for _, tc := range []struct {
		owns []string
		miss bool
	}{
		{[]string{"src/x.go", "docs/"}, false},
		{[]string{"src/x.go", "docs/*.md"}, false},
		{[]string{"src/x.go", "docs/", "!docs/a.md"}, true},
		{[]string{"src/x.go"}, true},
	} {
		if got := ownsCompanionsMissing(lc, tc.owns); (len(got) != 0) != tc.miss {
			t.Errorf("ownsCompanionsMissing(%v) = %v, want miss %v", tc.owns, got, tc.miss)
		}
	}
	all := &LintConfig{OwnsCompanions: map[string][]string{"": {"CHANGES.md"}}}
	got := ownsCompanionsMissing(all, []string{"anything.go"})
	if len(got) != 1 || got[0].String() != "owns no companion CHANGES.md required for every brief" {
		t.Errorf("ownsCompanionsMissing(\"\" key) = %v, want one miss for every brief", got)
	}
	if probs := ownsCompanionsFindings(t, `{"owns_companions":{"":["CHANGES.md"]}}`, "owns: lib/z.go\nneeds: none\ngate: echo x\n\n# TASK x\n"); len(probs) != 1 {
		t.Errorf("problems = %v, want one from the \"\" key", probs)
	}
}

// TestOwnsCompanionsRefusal: the refusal names every miss under rule
// owns-companions, and is nil when satisfied or unconfigured (issue #785).
func TestOwnsCompanionsRefusal(t *testing.T) {
	t.Parallel()
	lc := &LintConfig{OwnsCompanions: map[string][]string{"a/": {"z.txt", "y.txt"}}}
	r := ownsCompanionsRefusal(lc, []string{"a/x.go"})
	if r == nil || r.Rule != "owns-companions" || !strings.Contains(r.Fix, "owns no companion y.txt required for a/; owns no companion z.txt required for a/") || !strings.HasSuffix(r.Fix, "; add each to the brief's owns: or change lint.owns_companions") {
		t.Errorf("ownsCompanionsRefusal() = %+v, want rule owns-companions naming y.txt and z.txt", r)
	}
	if r := ownsCompanionsRefusal(lc, []string{"a/x.go", "y.txt", "z.txt"}); r != nil {
		t.Errorf("ownsCompanionsRefusal(satisfied) = %+v, want nil", r)
	}
	if r := ownsCompanionsRefusal(nil, []string{"a/x.go"}); r != nil {
		t.Errorf("ownsCompanionsRefusal(nil) = %+v, want nil", r)
	}
}

// TestOwnsCompanionsValidate: validate refuses a brief missing a companion
// before any gate runs (issue #785).
func TestOwnsCompanionsValidate(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"echo x > marker.txt"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	cfg := simConfig(fixturePath("clean.jsonl", t))
	cfg.Lint = &LintConfig{OwnsCompanions: map[string][]string{"": {"b.go"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err = ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "owns-companions" || !strings.Contains(rf.Fix, "b.go") {
		t.Errorf("ValidateTask() error = %v, want an owns-companions RuleRefusal naming b.go", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "marker.txt")); statErr == nil {
		t.Error("gate ran despite the refusal")
	}
}
