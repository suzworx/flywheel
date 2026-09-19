package flywheel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FactorySummary renders the factory in dir as a few plain lines (issue #69):
//
//	factory: <absolute dir>
//	  workers: <name> (<adapter> <model>, max_parallel <n>); ...
//	  limits: per_host <n|none> · budget <$x|none> · tokens <n|none> · breaker <n errors/cooldown|none> · rate <n/min|none>
//	  audit: first-article gate <on|off>
//	  enforcement: agent hooks <installed|missing — flywheel init --hooks> · git hooks <installed|missing — flywheel init --git-hooks> · CI audit <installed|missing — flywheel init --ci>
//	  view: flywheel (the live floor) · flywheel watch · flywheel next
//
// max_parallel 0 prints as 1. Fields that do not exist in Config yet (for
// example a token budget) print "none". Read-only.
func FactorySummary(dir string) (string, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve dir: %w", err)
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("factory: %s", absDir))

	// Workers line
	var workerStrs []string
	for _, w := range cfg.Workers {
		maxParallel := w.MaxParallel
		if maxParallel == 0 {
			maxParallel = 1
		}
		workerStrs = append(workerStrs, fmt.Sprintf("%s (%s %s, max_parallel %d)", w.Name, w.Adapter, w.Model, maxParallel))
	}
	lines = append(lines, fmt.Sprintf("  workers: %s", strings.Join(workerStrs, "; ")))

	// Limits line
	var limitStrs []string
	if cfg.Limits.PerHost > 0 {
		limitStrs = append(limitStrs, fmt.Sprintf("per_host %d", cfg.Limits.PerHost))
	} else {
		limitStrs = append(limitStrs, "per_host none")
	}
	if cfg.Limits.Budget != nil && cfg.Limits.Budget.WaveCostUSD > 0 {
		limitStrs = append(limitStrs, fmt.Sprintf("budget $%.2f", cfg.Limits.Budget.WaveCostUSD))
	} else {
		limitStrs = append(limitStrs, "budget none")
	}
	limitStrs = append(limitStrs, "tokens none")
	if cfg.Limits.Breaker != nil && cfg.Limits.Breaker.Errors > 0 {
		limitStrs = append(limitStrs, fmt.Sprintf("breaker %d errors/%s", cfg.Limits.Breaker.Errors, cfg.Limits.Breaker.Cooldown))
	} else {
		limitStrs = append(limitStrs, "breaker none")
	}
	limitStrs = append(limitStrs, "rate none")
	lines = append(lines, fmt.Sprintf("  limits: %s", strings.Join(limitStrs, " · ")))

	// Audit line
	auditStatus := "off"
	if cfg.Audit != nil && cfg.Audit.FirstArticle {
		auditStatus = "on"
	}
	lines = append(lines, fmt.Sprintf("  audit: first-article gate %s", auditStatus))

	// Enforcement line
	enforcements := []string{
		detectedAgentHooks(dir),
		detectedGitHooks(dir),
		detectedCIAudit(dir),
	}
	lines = append(lines, fmt.Sprintf("  enforcement: %s", strings.Join(enforcements, " · ")))

	// View line
	lines = append(lines, "  view: flywheel (the live floor) · flywheel watch · flywheel next")

	return strings.Join(lines, "\n"), nil
}

// detectedAgentHooks checks if agent hooks are installed.
func detectedAgentHooks(dir string) string {
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err == nil {
		return "agent hooks installed"
	}
	return "agent hooks missing — flywheel init --hooks"
}

// detectedGitHooks checks if git hooks are installed.
func detectedGitHooks(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-path", "hooks")
	out, err := cmd.Output()
	if err != nil {
		return "git hooks: missing — flywheel init --git-hooks"
	}
	hooksDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(dir, hooksDir)
	}
	commitMsgPath := filepath.Join(hooksDir, "commit-msg")
	content, err := os.ReadFile(commitMsgPath)
	if err != nil {
		return "git hooks: missing — flywheel init --git-hooks"
	}
	if strings.Contains(string(content), "Flywheel-Task") {
		return "git hooks: installed"
	}
	return "git hooks: missing — flywheel init --git-hooks"
}

// detectedCIAudit checks if CI audit is installed.
func detectedCIAudit(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "CI audit missing — flywheel init --ci"
	}
	toplevel := strings.TrimSpace(string(out))
	workflowPath := filepath.Join(toplevel, ".github", "workflows", "flywheel-audit.yml")
	if _, err := os.Stat(workflowPath); err == nil {
		return "CI audit installed"
	}
	return "CI audit missing — flywheel init --ci"
}
