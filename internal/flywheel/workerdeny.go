package flywheel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// gitWriteCore is the part of the built-in deny list a custom
// workers[].disallowed_tools must keep (issue #692).
var gitWriteCore = []string{"Bash(git commit:*)", "Bash(git push:*)", "Bash(git reset:*)"}

// WorkerPolicyRefusal is a dispatch refused because worker_policy.deny cannot
// be enforced on the worker's adapter (issue #692). It is returned before any
// event is appended; the CLI maps it to exit 2 (usage).
type WorkerPolicyRefusal struct {
	Msg string
}

func (e *WorkerPolicyRefusal) Error() string { return e.Msg }

// IsWorkerPolicyRefusal reports whether err is a WorkerPolicyRefusal.
func IsWorkerPolicyRefusal(err error) bool {
	var e *WorkerPolicyRefusal
	return errors.As(err, &e)
}

// denyPrefixes returns worker_policy.deny with each entry's whitespace
// collapsed to single spaces, nil when unset.
func (p *WorkerPolicy) denyPrefixes() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Deny))
	for _, d := range p.Deny {
		out = append(out, strings.Join(strings.Fields(d), " "))
	}
	return out
}

// problems reports worker_policy.deny entries that are empty or carry
// pattern syntax (*, (, ) or :), which flywheel adds itself.
func (p *WorkerPolicy) problems() []string {
	if p == nil {
		return nil
	}
	var out []string
	for i, d := range p.Deny {
		switch {
		case strings.TrimSpace(d) == "":
			out = append(out, fmt.Sprintf("worker_policy.deny[%d] must not be empty", i))
		case strings.ContainsAny(d, "*():"):
			out = append(out, fmt.Sprintf("worker_policy.deny[%d] %q must be a plain command prefix: *, (, ) and : are pattern syntax flywheel adds itself", i, d))
		}
	}
	return out
}

// disallowedToolsProblems reports each worker whose disallowed_tools list,
// which replaces the built-in deny list, omits part of the git-write core.
// Advisory: Validate reports it, loading a config does not fail on it.
func (c Config) disallowedToolsProblems() []string {
	var out []string
	for i, w := range c.Workers {
		if len(w.DisallowedTools) == 0 {
			continue
		}
		var missing []string
		for _, g := range gitWriteCore {
			if !slices.Contains(w.DisallowedTools, g) {
				missing = append(missing, g)
			}
		}
		if len(missing) > 0 {
			out = append(out, fmt.Sprintf("workers[%d].disallowed_tools replaces the built-in git-write deny list and omits %s; list the defaults too or use worker_policy.deny", i, strings.Join(missing, ", ")))
		}
	}
	return out
}

// claudeDisallowed is the claude adapter's resolved --disallowedTools list:
// w.disallowedTools() followed by Bash(<prefix>:*) for each worker_policy.deny
// entry, appended and deduplicated, never replacing the worker's list.
func claudeDisallowed(w Worker, p *WorkerPolicy) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, d := range w.disallowedTools() {
		add(d)
	}
	for _, d := range p.denyPrefixes() {
		add("Bash(" + d + ":*)")
	}
	return out
}

// checkWorkerPolicy refuses a dispatch on adapter when worker_policy.deny is
// set and not enforced: codex and pi have no command deny list, and an
// opencode worker's policy file (.flywheel/opencode-worker.json, or the
// embedded default flywheel writes when it is missing) must hold
// "<prefix>*": "deny" under permission.bash for every entry. claude and sim
// pass. No deny entries means no check.
func checkWorkerPolicy(dir, adapter string, p *WorkerPolicy) error {
	deny := p.denyPrefixes()
	if len(deny) == 0 {
		return nil
	}
	switch adapter {
	case "codex", "pi":
		return &WorkerPolicyRefusal{fmt.Sprintf("worker_policy.deny is set but the %s adapter cannot enforce a command deny list; use a claude or opencode worker", adapter)}
	case "opencode":
		path := filepath.Join(dir, ".flywheel", "opencode-worker.json")
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			b = []byte(workerPermissionPolicy)
		} else if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		missing, err := opencodeDenyMissing(b, deny)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if len(missing) > 0 {
			return &WorkerPolicyRefusal{fmt.Sprintf("worker_policy.deny is set but %s permission.bash lacks %s; add them after the \"*\": \"allow\" line", path, strings.Join(missing, ", "))}
		}
	}
	return nil
}

// opencodeDenyMissing returns the "<prefix>*" patterns, quoted, that the
// OpenCode policy b does not map to "deny" under permission.bash. A
// permission.bash that is absent or not an object lacks them all.
func opencodeDenyMissing(b []byte, deny []string) ([]string, error) {
	var pol struct {
		Permission struct {
			Bash json.RawMessage `json:"bash"`
		} `json:"permission"`
	}
	if err := json.Unmarshal(b, &pol); err != nil {
		return nil, err
	}
	var bash map[string]any
	if len(pol.Permission.Bash) > 0 {
		_ = json.Unmarshal(pol.Permission.Bash, &bash) // a string rule is no map
	}
	var missing []string
	for _, d := range deny {
		if v, _ := bash[d+"*"].(string); v != "deny" {
			missing = append(missing, fmt.Sprintf("%q: \"deny\"", d+"*"))
		}
	}
	return missing, nil
}
