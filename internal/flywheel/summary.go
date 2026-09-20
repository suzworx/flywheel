package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
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

	// Staffing lines
	var staffingLines []string
	for _, role := range cfg.Staffing.roles() {
		if line := StaffingLine(role.Name, role.Cfg); line != "" {
			staffingLines = append(staffingLines, "  "+line)
		}
	}

	// Limits line
	var limitStrs []string
	if cfg.Limits.PerHost > 0 {
		limitStrs = append(limitStrs, fmt.Sprintf("per_host %d", cfg.Limits.PerHost))
	} else {
		limitStrs = append(limitStrs, "per_host none")
	}
	if cfg.Limits.Budget != nil && cfg.Limits.Budget.WaveCostUSD > 0 {
		// Exact, trailing zeroes trimmed: a sub-cent cap must not print $0.00 (#328 review).
		limitStrs = append(limitStrs, "budget $"+strconv.FormatFloat(cfg.Limits.Budget.WaveCostUSD, 'f', -1, 64))
	} else {
		limitStrs = append(limitStrs, "budget none")
	}
	if cfg.Limits.Budget != nil && cfg.Limits.Budget.WaveTokens > 0 {
		limitStrs = append(limitStrs, fmt.Sprintf("tokens %d", cfg.Limits.Budget.WaveTokens))
	} else {
		limitStrs = append(limitStrs, "tokens none")
	}
	if cfg.Limits.Breaker != nil && cfg.Limits.Breaker.Errors > 0 {
		// The effective cooldown: an omitted one is 10m (#328 review).
		d, err := cfg.Limits.Breaker.CooldownDuration()
		if err != nil {
			d = 10 * time.Minute
		}
		cooldown := shortDuration(d)
		limitStrs = append(limitStrs, fmt.Sprintf("breaker %d errors/%s", cfg.Limits.Breaker.Errors, cooldown))
	} else {
		limitStrs = append(limitStrs, "breaker none")
	}
	if cfg.Limits.RatePerMinute > 0 {
		limitStrs = append(limitStrs, fmt.Sprintf("rate %d/min", cfg.Limits.RatePerMinute))
	} else {
		limitStrs = append(limitStrs, "rate none")
	}
	lines = append(lines, fmt.Sprintf("  limits: %s", strings.Join(limitStrs, " · ")))

	// Add staffing lines
	lines = append(lines, staffingLines...)

	// Does the floor agree with the configured roles? A ledger that cannot be
	// read is reported rather than silently skipped (#354 review).
	if len(staffingLines) > 0 {
		events, err := ReadEvents(dir)
		if err != nil {
			lines = append(lines, fmt.Sprintf("  ! staffing: the event log could not be read, so the floor was not checked: %v", err))
		} else {
			for _, m := range StaffingMismatch(cfg, events) {
				lines = append(lines, fmt.Sprintf("  ! %s", m))
			}
		}
	}

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
// fileContains reports whether path exists and contains every one of subs.
func fileContains(path string, subs ...string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, sub := range subs {
		if !strings.Contains(string(b), sub) {
			return false
		}
	}
	return true
}

// Each detector looks for the content flywheel generated, not just a file at
// the path, and a layer counts as installed only when all its parts are
// there (#328 review).

// detectedAgentHooks: the Claude Code Stop hook running flywheel gate and the
// OpenCode session plugin, as flywheel init --hooks writes them.
func detectedAgentHooks(dir string) string {
	claude := fileContains(filepath.Join(dir, ".claude", "settings.json"), "flywheel gate")
	opencode := fileContains(filepath.Join(dir, ".opencode", "plugin", "flywheel-session.mjs"), "flywheel")
	switch {
	case claude && opencode:
		return "agent hooks installed"
	case claude || opencode:
		return "agent hooks partial — flywheel init --hooks"
	}
	return "agent hooks missing — flywheel init --hooks"
}

// detectedGitHooks: both hooks flywheel init --git-hooks writes.
func detectedGitHooks(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return "git hooks: missing — flywheel init --git-hooks"
	}
	hooksDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(dir, hooksDir)
	}
	commitMsg := fileContains(filepath.Join(hooksDir, "commit-msg"), "Flywheel-Task")
	prePush := fileContains(filepath.Join(hooksDir, "pre-push"), "flywheel verify")
	switch {
	case commitMsg && prePush:
		return "git hooks: installed"
	case commitMsg || prePush:
		return "git hooks: partial — flywheel init --git-hooks (existing hooks are never overwritten)"
	}
	return "git hooks: missing — flywheel init --git-hooks"
}

// detectedCIAudit: the flywheel-audit workflow, targeting this factory's
// directory (a nested factory's workflow does not cover this one).
func detectedCIAudit(dir string) string {
	top, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "CI audit missing — flywheel init --ci"
	}
	prefix, err := exec.Command("git", "-C", dir, "rev-parse", "--show-prefix").Output()
	if err != nil {
		return "CI audit missing — flywheel init --ci"
	}
	target := strings.TrimSuffix(strings.TrimSpace(string(prefix)), "/")
	if target == "" {
		target = "."
	}
	quoted, _ := json.Marshal(target)
	wf := filepath.Join(strings.TrimSpace(string(top)), ".github", "workflows", "flywheel-audit.yml")
	if fileContains(wf, "flywheel verify", "FLYWHEEL_DIR: "+string(quoted)) {
		return "CI audit installed"
	}
	return "CI audit missing — flywheel init --ci"
}

// shortDuration prints whole hours as "2h" and whole minutes as "10m", and
// anything else as time.Duration prints it.
func shortDuration(d time.Duration) string {
	switch {
	case d > 0 && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d > 0 && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}
