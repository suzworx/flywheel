package flywheel

import (
	"fmt"
	"os/exec"
	"strings"
)

// claudeModels lists the claude model IDs flywheel knows, and claudeAliases
// the short names the claude CLI accepts (it has no "haiku" alias, issue
// #275).
var (
	claudeModels  = []string{"claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5", "claude-sonnet-5", "claude-haiku-4-5-20251001"}
	claudeAliases = []string{"fable", "opus", "sonnet"}
)

// initAgents lists, in detection order, the agent CLIs init looks for on
// PATH, and the model each one's default worker gets.
var initAgents = []struct{ Adapter, Model string }{
	{"claude", "claude-sonnet-5"},
	{"opencode", DefaultConfig().Workers[0].Model},
	{"codex", "gpt-5-codex"},
	{"pi", "anthropic/claude-sonnet-5"},
}

// lookPath finds an agent CLI on PATH; tests replace it.
var lookPath = exec.LookPath

// CheckModel reports whether model is usable with adapter: a known claude ID
// or alias, an opencode "<provider>/<model>", any codex model without spaces,
// a pi "<provider>/<id>[:<thinking>]", or a sim path. An unknown claude
// model's error names the closest known ID and the full list.
func CheckModel(adapter, model string) error {
	if model == "" {
		return fmt.Errorf("model must not be empty")
	}
	switch adapter {
	case "claude":
		for _, m := range append(append([]string{}, claudeModels...), claudeAliases...) {
			if model == m {
				return nil
			}
		}
		best := closestModel(model, claudeModels)
		return fmt.Errorf("model %q is not a claude model flywheel knows; did you mean %q? known: %s (set allow_unknown_model to accept a new one)",
			model, best, strings.Join(append(append([]string{}, claudeModels...), claudeAliases...), ", "))
	case "opencode":
		if !strings.Contains(model, "/") || strings.ContainsAny(model, " \t") {
			return fmt.Errorf("model %q must be <provider>/<model> for opencode, e.g. %q", model, initAgents[1].Model)
		}
	case "codex":
		if strings.ContainsAny(model, " \t") {
			return fmt.Errorf("model %q must not contain spaces", model)
		}
	case "pi":
		// pi's --model is <provider>/<id>, optionally :<thinking>.
		provider, id, _ := strings.Cut(model, "/")
		if provider == "" || id == "" || strings.HasPrefix(id, ":") || strings.ContainsAny(model, " \t") {
			return fmt.Errorf("model %q must be <provider>/<id>[:<thinking>] for pi, e.g. %q", model, initAgents[3].Model)
		}
	}
	return nil
}

// closestModel returns the known model nearest to model: the first one
// containing it (so a bare "haiku" finds the haiku ID), else the one sharing
// the longest prefix with it, ties broken by the smallest edit distance (plain
// edit distance favours the shortest IDs over a mistyped long one).
func closestModel(model string, known []string) string {
	for _, m := range known {
		if strings.Contains(m, model) {
			return m
		}
	}
	prefix := func(m string) int {
		n := 0
		for n < len(m) && n < len(model) && m[n] == model[n] {
			n++
		}
		return n
	}
	best := known[0]
	for _, m := range known[1:] {
		if p, bp := prefix(m), prefix(best); p > bp || p == bp && editDistance(model, m) < editDistance(model, best) {
			best = m
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// DetectAgents returns the agent CLIs found on PATH, in the order claude,
// opencode, codex, pi.
func DetectAgents() []string {
	var found []string
	for _, a := range initAgents {
		if _, err := lookPath(a.Adapter); err == nil {
			found = append(found, a.Adapter)
		}
	}
	return found
}

// InitAdapters lists the adapters `flywheel init --adapter` accepts.
func InitAdapters() []string {
	var out []string
	for _, a := range initAgents {
		out = append(out, a.Adapter)
	}
	return out
}

// defaultWorkerModel returns the default worker model for adapter, and
// whether adapter is one init can choose.
func defaultWorkerModel(adapter string) (string, bool) {
	for _, a := range initAgents {
		if a.Adapter == adapter {
			return a.Model, true
		}
	}
	return "", false
}

// AgentChoice reports which agent CLIs init found and which adapter and model
// it chose for a fresh config's default worker.
type AgentChoice struct {
	Found    []string // nil when adapter was chosen with --adapter
	Detected bool     // false when --adapter chose
	Adapter  string
	Model    string
}

// String renders the choice as init's summary line.
func (c AgentChoice) String() string {
	found := "none found"
	if !c.Detected {
		found = "not detected (--adapter " + c.Adapter + ")"
	} else if len(c.Found) > 0 {
		found = strings.Join(c.Found, ", ") + " found"
	}
	return fmt.Sprintf("agents: %s; worker default uses %s (%s)", found, c.Adapter, c.Model)
}
