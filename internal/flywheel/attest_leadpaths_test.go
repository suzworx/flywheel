package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAttestConfig writes a minimal config.json whose attest block is attest
// (empty for none).
func writeAttestConfig(t *testing.T, dir, attest string) {
	t.Helper()
	body := `{"version": 1, "workers": [{"name": "w", "adapter": "claude", "model": "m"}]`
	if attest != "" {
		body += `, "attest": ` + attest
	}
	body += "}"
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAttestLeadPathsAccepted is issue #895: a lead-maintained file named in
// attest.lead_paths is skipped by attest's T3 path check.
func TestAttestLeadPathsAccepted(t *testing.T) {
	t.Parallel()
	dir, commit := attestSetup(t, []string{"true"}, "a.go", "TASKS.md")
	writeAttestConfig(t, dir, `{"lead_paths": ["TASKS.md"]}`)
	if _, err := Attest(dir, "T1", commit, "https://ci.example/run/7", "lead-1"); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}
}

// TestAttestLeadPathsUnconfiguredRefused: without attest.lead_paths the same
// commit is refused T3, and the Fix points at the key.
func TestAttestLeadPathsUnconfiguredRefused(t *testing.T) {
	t.Parallel()
	dir, commit := attestSetup(t, []string{"true"}, "a.go", "TASKS.md")
	writeAttestConfig(t, dir, "")
	_, err := Attest(dir, "T1", commit, "https://ci.example/run/7", "lead-1")
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T3" || !strings.Contains(r.Fix, "TASKS.md") || !strings.Contains(r.Fix, "attest.lead_paths") {
		t.Fatalf("Attest() error = %v, want a T3 refusal naming TASKS.md and attest.lead_paths", err)
	}
	assertNoExternal(t, dir)
}

// TestAttestLeadPathsPattern: a prefix entry and a shell pattern each skip a
// matching path; an unrelated path in the same commit is still refused.
func TestAttestLeadPathsPattern(t *testing.T) {
	t.Parallel()
	dir, _ := attestSetup(t, []string{"true"}, "a.go")
	writeAttestConfig(t, dir, `{"lead_paths": ["docs/ledger/", "*.ledger.md"]}`)
	if err := os.MkdirAll(filepath.Join(dir, "docs", "ledger"), 0o755); err != nil {
		t.Fatal(err)
	}
	commitPaths := func(content string, paths ...string) string {
		t.Helper()
		for _, p := range paths {
			if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(p)), []byte(content+"\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
			git(t, dir, []string{"add", p})
		}
		git(t, dir, []string{"commit", "-m", content})
		return git(t, dir, []string{"rev-parse", "HEAD"})
	}
	outside := commitPaths("one", "a.go", "docs/ledger/x.md", "y.ledger.md", "other.txt")
	_, err := Attest(dir, "T1", outside, "https://ci.example/run/7", "lead-1")
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T3" || !strings.Contains(r.Fix, "other.txt") {
		t.Fatalf("Attest() error = %v, want a T3 refusal naming other.txt", err)
	}
	if strings.Contains(r.Fix, "x.md") || strings.Contains(r.Fix, "y.ledger.md") {
		t.Errorf("T3 Fix = %q, want the lead paths skipped", r.Fix)
	}
	assertNoExternal(t, dir)
	clean := commitPaths("two", "a.go", "docs/ledger/x.md", "y.ledger.md")
	if _, err := Attest(dir, "T1", clean, "https://ci.example/run/7", "lead-1"); err != nil {
		t.Fatalf("Attest() error = %v", err)
	}
}

// TestConfigAttestLeadPathsValidated: LoadConfig refuses an empty, absolute,
// escaping, negated or malformed entry and accepts a plain path.
func TestConfigAttestLeadPathsValidated(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"", "/abs", "../x", "!x", "["} {
		dir := t.TempDir()
		writeAttestConfig(t, dir, `{"lead_paths": ["`+entry+`"]}`)
		if _, _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "attest.lead_paths") || !strings.Contains(err.Error(), `"`+entry+`"`) {
			t.Errorf("LoadConfig(lead_paths %q) error = %v, want one naming attest.lead_paths and the entry", entry, err)
		}
	}
	dir := t.TempDir()
	writeAttestConfig(t, dir, `{"lead_paths": ["TASKS.md"]}`)
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig(lead_paths TASKS.md) error = %v", err)
	}
	if cfg.Attest == nil || len(cfg.Attest.LeadPaths) != 1 || !cfg.attestLeadPath("TASKS.md") {
		t.Errorf("LoadConfig() attest = %+v, want lead_paths [TASKS.md]", cfg.Attest)
	}
}
