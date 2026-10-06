package flywheel

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// PanelPersonas lists every embedded panel persona (review dimension),
// sorted. The integration persona is embedded too but reviews a group, never
// a unit (ReviewGroup), so it is not a panel dimension.
func PanelPersonas() []string {
	entries, err := reviewPersonas.ReadDir("review_personas")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".md"); ok && !e.IsDir() && name != IntegrationPersona {
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
// #420): for each dimension of panel, the verdict of the latest agent or
// crashed reviewed event of task with that dimension (Category) on tree —
// pass, correct, or crashed (issue #469; never pass) — else missing. A
// correct whose blocking findings of that dimension are all closed (a lead's
// dismissal) counts as pass: the lead has ruled on them.
func VerdictMatrix(events []Event, task, tree string, panel []string) map[string]string {
	latest := map[string]string{}
	for _, e := range events {
		if e.Task == task && (agentReviewed(e) || crashedReview(e)) && e.Category != "" && e.Tree == tree {
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
		case v == "pass" || v == "correct" || v == "crashed":
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
// agent reviewed event carrying a dimension, or a crashed one (issue #469).
func panelReviewed(events []Event, task string) bool {
	for _, e := range events {
		if e.Task == task && (agentReviewed(e) || crashedReview(e)) && e.Category != "" {
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
	Base     string        // the ref the unit's diff starts from; default: reviewBase's (issue #789)
	Round    int           // the panel round; <= 0 means the task's next round
	Progress io.Writer
	Stdout   io.Writer
	Stderr   io.Writer

	// review runs one member; nil means ReviewAgent (a test seam).
	review func(dir, task string, o ReviewAgentOptions) (ReviewAgentResult, error)
}

// PanelResult is what one panel review recorded: each member's run, in panel
// order, the dimensions whose run crashed (issue #469), in panel order, and
// the verdict matrix on the tree the members reviewed.
type PanelResult struct {
	Round   int
	Panel   []string
	Members []ReviewAgentResult
	Crashed []string
	Tree    string
	Matrix  map[string]string
}

// crashedReview reports whether e records a panel member whose run crashed
// (issue #469). It names no adapter, so it is never an agent review: it
// closes no finding and counts as no round.
func crashedReview(e Event) bool {
	return e.Kind == "reviewed" && e.Verdict == "crashed" && e.Category != ""
}

// recordCrashed appends the reviewed crashed event of dimension, on the tree
// the other members reviewed (else the workdir's tree), noted with cause and
// carrying spent, what the member's failed runs spent (issue #459).
func recordCrashed(dir, task, dimension string, o ReviewPanelOptions, res *PanelResult, cause error, spent reviewSpend) error {
	if res.Tree == "" {
		events, err := ReadEvents(dir)
		if err != nil {
			return err
		}
		if res.Tree, err = treeHash(wd(o.Workdir, dir, events, task)); err != nil {
			return err
		}
	}
	note := clipLine(cause.Error(), maxFailureCause)
	if err := AppendEvent(dir, Event{Task: task, Kind: "reviewed", Verdict: "crashed", Persona: "reviewer",
		Session: o.Session, Category: dimension, Tree: res.Tree, Note: note, Tokens: spent.tokens(), Cost: spent.Cost}); err != nil {
		return err
	}
	_, _ = WriteState(dir)
	res.Crashed = append(res.Crashed, dimension)
	progress(o.Progress, fmt.Sprintf("%s review panel %s: crashed twice; recorded, the panel continues: %s", task, dimension, note))
	return nil
}

// ReviewPanel runs the review panel over a unit (issue #420): ReviewAgent
// once per member, sequentially (the host is memory-constrained), each with
// its persona prompt, its worker or adapter and model, and one shared round,
// so every member's findings must carry its dimension as category. A member
// whose run fails (a reviewRunError) runs once more; failing again, its
// dimension is recorded crashed and the panel continues (issue #469). Any
// other error stops the panel: the members before it stay recorded and the
// error names the dimension. The result's matrix is VerdictMatrix over the
// ledger after the last member, on the tree they reviewed.
func ReviewPanel(dir, task string, o ReviewPanelOptions) (PanelResult, error) {
	panel := o.Panel
	minLines := 0
	if len(panel) == 0 {
		cfg, _, err := LoadConfig(dir)
		if err != nil {
			return PanelResult{}, err
		}
		panel = cfg.ReviewPanel()
		minLines = cfg.ReviewPanelMinLines()
	}
	for _, m := range panel {
		if !personaKnown(m.Persona) {
			return PanelResult{}, fmt.Errorf("review panel: no persona %q; known: %s", m.Persona, strings.Join(PanelPersonas(), ", "))
		}
	}
	// One base for the whole panel (issue #789): an empty diff is refused
	// once, before scoping or running any member. A workdir git cannot list
	// is left to the members, each of which reports that error itself.
	events, err := ReadEvents(dir)
	if err != nil {
		return PanelResult{}, err
	}
	workdir := wd(o.Workdir, dir, events, task)
	base, err := reviewBase(workdir, events, task, o.Base)
	if err != nil {
		return PanelResult{}, err
	}
	if changed, err := unitChangedPaths(workdir, base, task); err == nil && len(changed) == 0 {
		return PanelResult{}, emptyReviewRefusal(task, base)
	}
	if minLines > 0 {
		if panel, err = scopePanel(dir, task, panel, minLines, base, o); err != nil {
			return PanelResult{}, err
		}
	}
	res := PanelResult{Round: o.Round}
	for _, m := range panel {
		res.Panel = append(res.Panel, m.Persona)
	}
	if res.Round <= 0 {
		events, err := ReadEvents(dir)
		if err != nil {
			return PanelResult{}, err
		}
		res.Round = nextReviewRound(events, task)
	}
	review := o.review
	if review == nil {
		review = ReviewAgent
	}
	for _, m := range panel {
		ao := ReviewAgentOptions{
			Worker: m.Worker, Adapter: m.Adapter, Model: m.Model, Dimension: m.Persona,
			Session: o.Session, Workdir: o.Workdir, Base: o.Base, Round: res.Round,
			Progress: o.Progress, Stdout: o.Stdout, Stderr: o.Stderr,
		}
		r, err := review(dir, task, ao)
		var run *reviewRunError
		var spent reviewSpend
		if errors.As(err, &run) {
			spent = run.spend
			progress(o.Progress, fmt.Sprintf("%s review panel %s: run failed; retrying once: %s", task, m.Persona, clipLine(err.Error(), maxFailureCause)))
			r, err = review(dir, task, ao)
		}
		if errors.As(err, &run) {
			spent.plus(run.spend)
			if err := recordCrashed(dir, task, m.Persona, o, &res, err, spent); err != nil {
				return res, fmt.Errorf("review panel %s: %w", m.Persona, err)
			}
			continue
		}
		if err != nil {
			return res, fmt.Errorf("review panel %s: %w", m.Persona, err)
		}
		res.Members = append(res.Members, r)
		res.Tree = r.Tree
	}
	events, err = ReadEvents(dir)
	if err != nil {
		return res, err
	}
	res.Matrix = VerdictMatrix(events, task, res.Tree, res.Panel)
	return res, nil
}

// scopePanel applies review.panel_min_lines (issue #459): it counts the
// unit's changed lines on the tree the members are about to review
// (unitChangedLines, from base, the review's resolved base, as reviewDiff
// does) and, below minLines, returns only the correctness member (else the
// first), after recording a panel_scoped event for that tree. Otherwise it
// returns panel unchanged and records nothing.
func scopePanel(dir, task string, panel []PanelMember, minLines int, base string, o ReviewPanelOptions) ([]PanelMember, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	workdir := wd(o.Workdir, dir, events, task)
	n, err := unitChangedLines(workdir, base, task)
	if err != nil {
		return nil, err
	}
	if n >= minLines {
		return panel, nil
	}
	tree, err := treeHash(workdir)
	if err != nil {
		return nil, err
	}
	one := panel[0]
	for _, m := range panel {
		if m.Persona == "correctness" {
			one = m
			break
		}
	}
	why := fmt.Sprintf("%d changed lines < review.panel_min_lines %d", n, minLines)
	if err := AppendEvent(dir, Event{Task: task, Kind: "panel_scoped", Session: o.Session, Tree: tree,
		Panel: []string{one.Persona}, Note: why}); err != nil {
		return nil, err
	}
	progress(o.Progress, fmt.Sprintf("%s review panel: %s; one reviewer: %s", task, why, one.Persona))
	return []PanelMember{one}, nil
}

// unitChangedLines counts the unit's changed lines (issue #459): added plus
// deleted lines of git diff --numstat from base (HEAD when none; HEAD again
// when git cannot use base) over unitChangedPaths, plus the lines of each new
// untracked file among those paths. A binary file ("-") counts 0. Flywheel's
// own files (isFlywheelOwnPath) are not counted (issue #729).
func unitChangedLines(workdir, base, task string) (int, error) {
	all, err := unitChangedPaths(workdir, base, task)
	if err != nil {
		return 0, err
	}
	var paths []string
	for _, p := range all {
		if !isFlywheelOwnPath(p) {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return 0, nil
	}
	from := base
	if from == "" {
		from = "HEAD"
	}
	stat, err := gitRead(workdir, append([]string{"diff", "--numstat", from, "--"}, paths...))
	if err != nil && from != "HEAD" {
		stat, err = gitRead(workdir, append([]string{"diff", "--numstat", "HEAD", "--"}, paths...))
	}
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(stat, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) < 3 {
			continue
		}
		added, _ := strconv.Atoi(f[0]) // "-" (binary) parses to 0
		deleted, _ := strconv.Atoi(f[1])
		n += added + deleted
	}
	untracked, err := gitRead(workdir, []string{"ls-files", "-z", "--others", "--exclude-standard"})
	if err != nil {
		return 0, err
	}
	changed := map[string]bool{}
	for _, p := range paths {
		changed[p] = true
	}
	for _, p := range strings.Split(untracked, "\x00") {
		if p = filepath.ToSlash(p); p == "" || !changed[p] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(p)))
		if err != nil {
			return 0, fmt.Errorf("read new file %s: %w", p, err)
		}
		n += bytes.Count(data, []byte("\n"))
		if len(data) > 0 && data[len(data)-1] != '\n' {
			n++
		}
	}
	return n, nil
}

// panelFor is the panel task's tree needs (issue #459): the Panel of the
// latest panel_scoped event of task on exactly tree, else configured. A tree
// that changed after a scoped review has no such record: the full panel.
func panelFor(events []Event, task, tree string, configured []string) []string {
	out := configured
	for _, e := range events {
		if e.Kind == "panel_scoped" && e.Task == task && e.Tree == tree && len(e.Panel) > 0 {
			out = e.Panel
		}
	}
	return out
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
