package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setIntegration writes dir's config with integration.branch = branch,
// unvalidated, so a bad value reaches LoadConfig.
func setIntegration(t *testing.T, dir, branch string) {
	t.Helper()
	c := DefaultConfig()
	c.Integration = &IntegrationConfig{Branch: branch}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", configFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// branchRepo is a one-commit repository whose only branch is name.
func branchRepo(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	initRepo(t, dir)
	git(t, dir, []string{"branch", "-M", name})
	return dir
}

// TestIntegrationBranch: a configured branch wins even when it does not
// resolve; unset falls back to main, then master, then ""; a bad value fails
// validation naming the key and loads as unset (issue #456).
func TestIntegrationBranch(t *testing.T) {
	t.Parallel()
	dir := branchRepo(t, "main")
	setIntegration(t, dir, "main2")
	if b, ok := IntegrationBranch(dir); b != "main2" || !ok {
		t.Errorf("configured = %q, %v; want main2, true", b, ok)
	}
	for name, want := range map[string]string{"main": "main", "master": "master", "trunk": ""} {
		if b, ok := IntegrationBranch(branchRepo(t, name)); b != want || ok {
			t.Errorf("only %s: = %q, %v; want %q, false", name, b, ok, want)
		}
	}
	for _, bad := range []string{"", "  ", "a b", "-x"} {
		c := DefaultConfig()
		c.Integration = &IntegrationConfig{Branch: bad}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "integration.branch") {
			t.Errorf("Validate(branch %q) = %v; want an integration.branch error", bad, err)
		}
	}
	setIntegration(t, dir, "-x")
	if b, ok := IntegrationBranch(dir); b != "main" || ok {
		t.Errorf("invalid config = %q, %v; want main, false", b, ok)
	}
}

// TestSquashedBaseIntegrationBranch: a base squash-merged onto main2 is seen
// only when integration.branch names main2; rebase onto an unresolved
// configured branch names the key.
func TestSquashedBaseIntegrationBranch(t *testing.T) {
	t.Parallel()
	dir, base, squash := stackedRepo(t)
	git(t, dir, []string{"branch", "-m", "main", "main2"})
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok := SquashedBase(dir, events, "B"); ok {
		t.Fatalf("SquashedBase(B) ok with integration.branch unset and no main")
	}
	setIntegration(t, dir, "main2")
	gotBase, landedAs, baseTask, ok := SquashedBase(dir, events, "B")
	if !ok || gotBase != base || landedAs != squash || baseTask != "A" {
		t.Fatalf("SquashedBase(B) = %q, %q, %q, %v; want %q, %q, A, true", gotBase, landedAs, baseTask, ok, base, squash)
	}
	setIntegration(t, dir, "nope")
	if _, _, err := RebaseUnit(dir, "B", ""); err == nil || !strings.Contains(err.Error(), `integration.branch "nope" does not resolve`) {
		t.Fatalf("RebaseUnit onto unresolved integration.branch err = %v", err)
	}
	setIntegration(t, dir, "main2")
	if nb, conflicts, err := RebaseUnit(dir, "B", ""); err != nil || len(conflicts) != 0 || nb != squash {
		t.Fatalf("RebaseUnit() = %q, %v, %v; want %q onto main2", nb, conflicts, err, squash)
	}
}

// TestIntegrationTreeDefaultBase: an empty base is integration.branch.
func TestIntegrationTreeDefaultBase(t *testing.T) {
	t.Parallel()
	dir := branchRepo(t, "main2")
	git(t, dir, []string{"checkout", "-q", "-b", "fw/A"})
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "b.txt"})
	git(t, dir, []string{"commit", "-q", "-m", "A"})
	git(t, dir, []string{"checkout", "-q", "main2"})
	if _, cleanup, _, err := IntegrationTree(dir, nil, []string{"A"}, ""); err == nil {
		cleanup()
		t.Fatalf("IntegrationTree with no main and integration.branch unset succeeded")
	}
	setIntegration(t, dir, "main2")
	wt, cleanup, _, err := IntegrationTree(dir, nil, []string{"A"}, "")
	if err != nil {
		t.Fatalf("IntegrationTree: %v", err)
	}
	defer cleanup()
	if data, err := os.ReadFile(filepath.Join(wt, "b.txt")); err != nil || string(data) != "b\n" {
		t.Errorf("b.txt = %q, %v; want fw/A merged onto main2", data, err)
	}
}

// TestInitCIIntegrationBranch: init --ci's push trigger is integration.branch,
// main when unset.
func TestInitCIIntegrationBranch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ branch, configured, want string }{
		{"main2", "main2", `branches: ["main2"]`},
		{"main", "", `branches: ["main"]`},
	} {
		dir := branchRepo(t, tc.branch)
		if tc.configured != "" {
			setIntegration(t, dir, tc.configured)
		}
		root, _, err := InitCI(dir, "")
		if err != nil {
			t.Fatalf("InitCI: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "flywheel-audit.yml"))
		if err != nil || !strings.Contains(string(data), tc.want) {
			t.Errorf("configured %q: workflow lacks %s (%v):\n%s", tc.configured, tc.want, err, data)
		}
	}
}

// TestDoctorIntegrationBranch: doctor names the branch and its source, and
// warns when a configured branch does not resolve.
func TestDoctorIntegrationBranch(t *testing.T) {
	t.Parallel()
	dir := branchRepo(t, "main2")
	setIntegration(t, dir, "main2")
	if line, w := DoctorIntegrationBranch(dir); line != "integration branch: main2 (integration.branch)" || w != "" {
		t.Errorf("configured and resolving = %q, %q", line, w)
	}
	setIntegration(t, dir, "nope")
	if line, w := DoctorIntegrationBranch(dir); line != "integration branch: nope (integration.branch)" || !strings.Contains(w, `integration.branch "nope" does not resolve`) {
		t.Errorf("configured and missing = %q, %q; want a warning", line, w)
	}
	if line, w := DoctorIntegrationBranch(branchRepo(t, "main")); line != "integration branch: main (detected)" || w != "" {
		t.Errorf("detected = %q, %q", line, w)
	}
}
