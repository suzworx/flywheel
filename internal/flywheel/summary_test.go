package flywheel

import (
	"os/exec"
	"strings"
	"testing"
)

func TestFactorySummaryFreshInit(t *testing.T) {
	dir := t.TempDir()

	_, _, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}

	text, err := FactorySummary(dir)
	if err != nil {
		t.Fatalf("FactorySummary() error = %v", err)
	}

	if !strings.Contains(text, "factory: ") {
		t.Errorf("FactorySummary() missing 'factory: '")
	}
	if !strings.Contains(text, "workers: ") {
		t.Errorf("FactorySummary() missing 'workers: '")
	}
	if !strings.Contains(text, "first-article gate off") {
		t.Errorf("FactorySummary() missing 'first-article gate off'")
	}
	if !strings.Contains(text, "agent hooks missing — flywheel init --hooks") {
		t.Errorf("FactorySummary() missing 'agent hooks missing — flywheel init --hooks'")
	}
	if !strings.Contains(text, "git hooks: missing") {
		t.Errorf("FactorySummary() missing 'git hooks: missing'")
	}
	if !strings.Contains(text, "CI audit missing") {
		t.Errorf("FactorySummary() missing 'CI audit missing'")
	}
}

func TestFactorySummaryShowsLimits(t *testing.T) {
	dir := t.TempDir()

	cfg := DefaultConfig()
	cfg.Limits.PerHost = 2
	cfg.Limits.Budget = &Budget{WaveCostUSD: 5.0}
	cfg.Limits.Breaker = &Breaker{Errors: 3, Cooldown: "10m"}
	cfg.Audit = &AuditPolicy{FirstArticle: true}

	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	text, err := FactorySummary(dir)
	if err != nil {
		t.Fatalf("FactorySummary() error = %v", err)
	}

	if !strings.Contains(text, "per_host 2") {
		t.Errorf("FactorySummary() missing 'per_host 2'")
	}
	if !strings.Contains(text, "budget $5.00") {
		t.Errorf("FactorySummary() missing 'budget $5.00'")
	}
	if !strings.Contains(text, "breaker 3 errors/10m") {
		t.Errorf("FactorySummary() missing 'breaker 3 errors/10m'")
	}
	if !strings.Contains(text, "first-article gate on") {
		t.Errorf("FactorySummary() missing 'first-article gate on'")
	}
}

func TestFactorySummaryDetectsHooks(t *testing.T) {
	dir := t.TempDir()

	if err := exec.Command("git", "-c", "core.autocrlf=false", "init", "-q", dir).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	_, _, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}

	if _, _, err := InitHooks(dir); err != nil {
		t.Fatalf("InitHooks() error = %v", err)
	}

	if _, _, err := InitGitHooks(dir); err != nil {
		t.Fatalf("InitGitHooks() error = %v", err)
	}

	text, err := FactorySummary(dir)
	if err != nil {
		t.Fatalf("FactorySummary() error = %v", err)
	}

	if !strings.Contains(text, "agent hooks installed") {
		t.Errorf("FactorySummary() missing 'agent hooks installed'; got:\n%s", text)
	}
	if !strings.Contains(text, "git hooks: installed") {
		t.Errorf("FactorySummary() missing 'git hooks: installed'; got:\n%s", text)
	}
}

func TestFactorySummaryMaxParallelZeroIsOne(t *testing.T) {
	dir := t.TempDir()

	cfg := DefaultConfig()
	cfg.Workers[0].MaxParallel = 0

	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	text, err := FactorySummary(dir)
	if err != nil {
		t.Fatalf("FactorySummary() error = %v", err)
	}

	if !strings.Contains(text, "max_parallel 1") {
		t.Errorf("FactorySummary() with MaxParallel=0 missing 'max_parallel 1'; got:\n%s", text)
	}
}
