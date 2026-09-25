package flywheel

import (
	"embed"
	"fmt"
	"io"
	"sort"
	"strings"
)

// reviewPersonas holds one persona prompt per review dimension (issue #420):
// review_personas/<dimension>.md, appended to the shared review_prompt.md.
//
//go:embed review_personas/*.md
var reviewPersonas embed.FS

// DefaultPanel is the review panel when review.panel is unset (issue #420);
// security and cross-os are opt-in.
var DefaultPanel = []string{"correctness", "tests", "errors", "contract", "docs"}

// PanelPersonas lists every embedded persona (review dimension), sorted.
func PanelPersonas() []string {
	entries, err := reviewPersonas.ReadDir("review_personas")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// personaKnown reports whether name is an embedded persona.
func personaKnown(name string) bool {
	for _, p := range PanelPersonas() {
		if p == name {
			return true
		}
	}
	return false
}

// personaPrompt is the reviewer instructions for one dimension: the shared
// review_prompt.md, then the persona file.
func personaPrompt(dimension string) (string, error) {
	data, err := reviewPersonas.ReadFile("review_personas/" + dimension + ".md")
	if err != nil {
		return "", fmt.Errorf("no review persona %q; known: %s", dimension, strings.Join(PanelPersonas(), ", "))
	}
	return reviewPrompt + "\n" + string(data), nil
}

// VerdictMatrix is the panel's verdict per dimension on one tree (issue
// #420): for each dimension of panel, the verdict of the latest agent reviewed
// event of task with that dimension (Category) on tree — pass, or correct —
// else missing. A correct whose blocking findings of that dimension are all
// closed (a lead's dismissal) counts as pass: the lead has ruled on them.
func VerdictMatrix(events []Event, task, tree string, panel []string) map[string]string {
	latest := map[string]string{}
	for _, e := range events {
		if e.Task == task && agentReviewed(e) && e.Category != "" && e.Tree == tree {
			latest[e.Category] = e.Verdict
		}
	}
	blocking := map[string]bool{}
	for _, f := range OpenFindings(events, task) {
		if blockingFinding(f) {
			blocking[f.Category] = true
		}
	}
	out := make(map[string]string, len(panel))
	for _, d := range panel {
		switch v := latest[d]; {
		case v == "":
			out[d] = "missing"
		case v == "correct" && !blocking[d]:
			out[d] = "pass"
		case v == "pass" || v == "correct":
			out[d] = v
		default:
			out[d] = "correct"
		}
	}
	return out
}

// panelIncomplete lists, in panel order, the dimensions of m that are not
// pass, each as "<dimension>=<verdict>".
func panelIncomplete(m map[string]string, panel []string) []string {
	var out []string
	for _, d := range panel {
		if m[d] != "pass" {
			out = append(out, d+"="+m[d])
		}
	}
	return out
}

// panelReviewed reports whether task was ever reviewed by a panel member: an
// agent reviewed event carrying a dimension.
func panelReviewed(events []Event, task string) bool {
	for _, e := range events {
		if e.Task == task && agentReviewed(e) && e.Category != "" {
			return true
		}
	}
	return false
}

// ReviewPanelOptions configures one panel review of a unit (issue #420).
type ReviewPanelOptions struct {
	Panel    []PanelMember // the members; default: the configured review.panel
	Session  string        // the reviewer session label, shared by every member
	Workdir  string        // the unit's worktree; default: the recorded workdir, else dir
	Round    int           // the panel round; <= 0 means the task's next round
	Progress io.Writer
	Stdout   io.Writer
	Stderr   io.Writer
}

// PanelResult is what one panel review recorded: each member's run, in panel
// order, and the verdict matrix on the tree the last member reviewed.
type PanelResult struct {
	Round   int
	Panel   []string
	Members []ReviewAgentResult
	Tree    string
	Matrix  map[string]string
}

// ReviewPanel runs the review panel over a unit (issue #420): ReviewAgent
// once per member, sequentially (the host is memory-constrained), each with
// its persona prompt, its worker or adapter and model, and one shared round,
// so every member's findings must carry its dimension as category. The first
// member that fails stops the panel: the members before it stay recorded and
// the error names the dimension. The result's matrix is VerdictMatrix over
// the ledger after the last member, on the tree it reviewed.
func ReviewPanel(dir, task string, o ReviewPanelOptions) (PanelResult, error) {
	panel := o.Panel
	if len(panel) == 0 {
		cfg, _, err := LoadConfig(dir)
		if err != nil {
			return PanelResult{}, err
		}
		panel = cfg.ReviewPanel()
	}
	res := PanelResult{Round: o.Round}
	for _, m := range panel {
		if !personaKnown(m.Persona) {
			return PanelResult{}, fmt.Errorf("review panel: no persona %q; known: %s", m.Persona, strings.Join(PanelPersonas(), ", "))
		}
		res.Panel = append(res.Panel, m.Persona)
	}
	if res.Round <= 0 {
		events, err := ReadEvents(dir)
		if err != nil {
			return PanelResult{}, err
		}
		res.Round = nextReviewRound(events, task)
	}
	for _, m := range panel {
		r, err := ReviewAgent(dir, task, ReviewAgentOptions{
			Worker: m.Worker, Adapter: m.Adapter, Model: m.Model, Dimension: m.Persona,
			Session: o.Session, Workdir: o.Workdir, Round: res.Round,
			Progress: o.Progress, Stdout: o.Stdout, Stderr: o.Stderr,
		})
		if err != nil {
			return res, fmt.Errorf("review panel %s: %w", m.Persona, err)
		}
		res.Members = append(res.Members, r)
		res.Tree = r.Tree
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return res, err
	}
	res.Matrix = VerdictMatrix(events, task, res.Tree, res.Panel)
	return res, nil
}

// FormatMatrix renders a verdict matrix one "<dimension>  <verdict>" line per
// dimension, in panel order.
func FormatMatrix(m map[string]string, panel []string) string {
	var b strings.Builder
	for _, d := range panel {
		fmt.Fprintf(&b, "%-12s %s\n", d, m[d])
	}
	return b.String()
}
