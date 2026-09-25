package flywheel

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// CalibrationCase is one defect an external reviewer found on a past PR
// (issue #389): the PR, the head commit the reviewer saw, the file and line,
// and the reviewer's claim.
type CalibrationCase struct {
	PR     int    `json:"pr"`
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Claim  string `json:"claim"`
}

// CalibrationGroup is the cases of one PR state: one (pr, commit) pair.
type CalibrationGroup struct {
	PR     int
	Commit string
	Cases  []CalibrationCase
}

// LoadCalibrationCases reads a calibration case file: {"cases": [...]}.
// Paths are normalised to forward slashes; a case without a commit or a
// path is refused.
func LoadCalibrationCases(path string) ([]CalibrationCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Cases []CalibrationCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("calibration cases %s: %w", path, err)
	}
	for i := range file.Cases {
		c := &file.Cases[i]
		c.Path = filepath.ToSlash(strings.TrimSpace(c.Path))
		c.Commit = strings.TrimSpace(c.Commit)
		if c.Commit == "" || c.Path == "" {
			return nil, fmt.Errorf("calibration cases %s: case %d names no commit or no path", path, i+1)
		}
	}
	return file.Cases, nil
}

// groupCalibrationCases groups cases by (pr, commit), in first-seen order.
func groupCalibrationCases(cases []CalibrationCase) []CalibrationGroup {
	var groups []CalibrationGroup
	index := map[string]int{}
	for _, c := range cases {
		key := fmt.Sprintf("%d@%s", c.PR, c.Commit)
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, CalibrationGroup{PR: c.PR, Commit: c.Commit})
		}
		groups[i].Cases = append(groups[i].Cases, c)
	}
	return groups
}

// matchFindings matches the reviewer's findings to the expected cases: a
// finding matches a case when the paths are equal (forward slashes) and the
// lines are at most window apart; the nearest unmatched case wins and each
// case is matched at most once. It returns the cases hit and missed (in the
// expected order) and the findings that matched no case.
func matchFindings(expected []CalibrationCase, found []ReviewFinding, window int) (hits, misses []CalibrationCase, extra []ReviewFinding) {
	used := make([]bool, len(expected))
	for _, f := range found {
		file := strings.ReplaceAll(strings.TrimSpace(f.File), `\`, "/")
		best, bestDist := -1, 0
		for i, c := range expected {
			if used[i] || c.Path != file {
				continue
			}
			d := c.Line - f.Line
			if d < 0 {
				d = -d
			}
			if d <= window && (best < 0 || d < bestDist) {
				best, bestDist = i, d
			}
		}
		if best < 0 {
			extra = append(extra, f)
			continue
		}
		used[best] = true
	}
	for i, c := range expected {
		if used[i] {
			hits = append(hits, c)
		} else {
			misses = append(misses, c)
		}
	}
	return hits, misses, extra
}

// sampleGroups picks n of groups indices, deterministically for a seed, in
// ascending order; n <= 0 or n >= groups picks every index.
func sampleGroups(groups, n int, seed int64) []int {
	if n <= 0 || n >= groups {
		n = groups
	}
	picked := rand.New(rand.NewSource(seed)).Perm(groups)[:n]
	sort.Ints(picked)
	return picked
}

// CalibrateOptions configures one calibration run (issue #389).
type CalibrateOptions struct {
	Sample  int    // PR states to review; <= 0 means 10
	Seed    int64  // the sample's seed
	Worker  string // the reviewing worker (ReviewAgentOptions.Worker)
	Session string // the reviewer session; required
	Window  int    // the matching window in lines; <= 0 means 15
	Main    string // the ref the merge-base is taken against; default origin/main
	// Panel calibrates each of these personas (issue #420) over the same
	// sampled groups instead of the single reviewer; a case counts as hit
	// by the panel when any persona hit it. Empty: the single reviewer.
	Panel    []string
	Progress io.Writer
	// Review runs the reviewer over one synthetic unit as dimension (a
	// panel persona, or "" for the single reviewer); default: ReviewAgent.
	Review func(ledgerDir, workdir, task, dimension string) ([]ReviewFinding, error)
}

// CalibrationPersona is one panel persona's calibration (issue #420): in a
// group report, its result on that PR state (Skipped when its review
// failed); in the run's report, its totals over the PR states it reviewed.
type CalibrationPersona struct {
	Persona string
	Groups  int // PR states it reviewed
	Cases   int
	Hits    int
	Misses  int
	Extra   int
	Recall  float64
	Skipped string
}

// CalibrationGroupReport is one PR state's result; Skipped names why a
// group was not reviewed. With a panel, Hits and Missed are the union (a
// case any persona hit), Found and Extra every persona's findings, and
// Personas each persona's own result.
type CalibrationGroupReport struct {
	PR       int
	Commit   string
	Cases    int
	Hits     int
	Found    int
	Recall   float64
	Missed   []CalibrationCase
	Extra    []ReviewFinding
	Skipped  string
	Personas []CalibrationPersona
}

// CalibrationReport is a calibration run: per group and overall, over the
// groups actually reviewed.
type CalibrationReport struct {
	CasesFile string
	Groups    int // PR states in the case file
	Seed      int64
	Window    int
	Reports   []CalibrationGroupReport
	Cases     int
	Hits      int
	Extra     int
	Recall    float64
	Panel     []string             // the personas calibrated; empty for the single reviewer
	Personas  []CalibrationPersona // each persona's totals, in Panel order
}

// Calibrate measures the review agent against the external reviewer's cases
// (issue #389): for each sampled (pr, commit) group it checks out the commit
// in a temporary detached worktree of repo, builds a synthetic ledger whose
// brief owns the files changed since the merge-base with Main, runs the
// review, and matches its findings to the cases. A group whose commit cannot
// be found or whose review fails is reported as skipped.
func Calibrate(repo, cases string, o CalibrateOptions) (CalibrationReport, error) {
	if o.Session == "" {
		return CalibrationReport{}, &RuleRefusal{Rule: "T4", Fix: "a reviewer --session is required"}
	}
	if o.Sample <= 0 {
		o.Sample = 10
	}
	if o.Window <= 0 {
		o.Window = 15
	}
	if o.Main == "" {
		o.Main = "origin/main"
	}
	for _, d := range o.Panel {
		if !personaKnown(d) {
			return CalibrationReport{}, fmt.Errorf("calibrate --panel: no persona %q; known: %s", d, strings.Join(PanelPersonas(), ", "))
		}
	}
	if o.Review == nil {
		// A panel persona is run as review.panel configures it (its worker,
		// or adapter and model) unless --worker names one for every persona.
		members := map[string]PanelMember{}
		if cfg, _, err := LoadConfig(repo); err == nil {
			for _, m := range cfg.ReviewPanel() {
				members[m.Persona] = m
			}
		}
		o.Review = func(ledgerDir, workdir, task, dimension string) ([]ReviewFinding, error) {
			ro := ReviewAgentOptions{Worker: o.Worker, Dimension: dimension, Session: o.Session, Workdir: workdir, Round: 1, Progress: o.Progress}
			if m := members[dimension]; dimension != "" && o.Worker == "" {
				ro.Worker, ro.Adapter, ro.Model = m.Worker, m.Adapter, m.Model
			}
			res, err := ReviewAgent(ledgerDir, task, ro)
			return res.Findings, err
		}
	}
	all, err := LoadCalibrationCases(cases)
	if err != nil {
		return CalibrationReport{}, err
	}
	groups := groupCalibrationCases(all)
	rep := CalibrationReport{CasesFile: filepath.ToSlash(cases), Groups: len(groups), Seed: o.Seed, Window: o.Window, Panel: o.Panel}
	totals := map[string]*CalibrationPersona{}
	for _, d := range o.Panel {
		totals[d] = &CalibrationPersona{Persona: d}
	}
	for _, i := range sampleGroups(len(groups), o.Sample, o.Seed) {
		g := groups[i]
		progress(o.Progress, fmt.Sprintf("calibrate: PR #%d at %.12s (%d case(s))", g.PR, g.Commit, len(g.Cases)))
		gr := calibrateGroup(repo, g, o)
		if gr.Skipped != "" {
			progress(o.Progress, fmt.Sprintf("calibrate: PR #%d skipped: %s", g.PR, gr.Skipped))
		} else {
			rep.Cases += gr.Cases
			rep.Hits += gr.Hits
			rep.Extra += len(gr.Extra)
			for _, p := range gr.Personas {
				if t := totals[p.Persona]; t != nil && p.Skipped == "" {
					t.Groups++
					t.Cases += p.Cases
					t.Hits += p.Hits
					t.Misses += p.Misses
					t.Extra += p.Extra
				}
			}
		}
		rep.Reports = append(rep.Reports, gr)
	}
	if rep.Cases > 0 {
		rep.Recall = float64(rep.Hits) / float64(rep.Cases)
	}
	for _, d := range o.Panel {
		p := totals[d]
		if p.Cases > 0 {
			p.Recall = float64(p.Hits) / float64(p.Cases)
		}
		rep.Personas = append(rep.Personas, *p)
	}
	return rep, nil
}

// calibrateGroup reviews one PR state in a temporary worktree and ledger,
// both removed afterwards.
func calibrateGroup(repo string, g CalibrationGroup, o CalibrateOptions) CalibrationGroupReport {
	gr := CalibrationGroupReport{PR: g.PR, Commit: g.Commit, Cases: len(g.Cases)}
	if _, err := gitRead(repo, []string{"cat-file", "-e", g.Commit + "^{commit}"}); err != nil {
		_, _ = gitRead(repo, []string{"fetch", "-q", "origin", fmt.Sprintf("pull/%d/head", g.PR)})
		if _, err := gitRead(repo, []string{"cat-file", "-e", g.Commit + "^{commit}"}); err != nil {
			gr.Skipped = fmt.Sprintf("commit %s not found, even after fetching pull/%d/head", g.Commit, g.PR)
			return gr
		}
	}
	base, err := gitRead(repo, []string{"merge-base", g.Commit, o.Main})
	if err != nil {
		gr.Skipped = fmt.Sprintf("no merge-base with %s: %v", o.Main, err)
		return gr
	}
	base = strings.TrimSpace(base)
	names, err := gitRead(repo, []string{"diff", "--name-only", "--no-renames", base, g.Commit})
	if err != nil {
		gr.Skipped = fmt.Sprintf("changed files: %v", err)
		return gr
	}
	var owns []string
	for _, p := range strings.Split(strings.ReplaceAll(names, "\r\n", "\n"), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			owns = append(owns, p)
		}
	}
	if len(owns) == 0 {
		gr.Skipped = "no files changed between the merge-base and the commit"
		return gr
	}
	tmp, err := os.MkdirTemp("", "flywheel-calibrate-")
	if err != nil {
		gr.Skipped = err.Error()
		return gr
	}
	defer os.RemoveAll(tmp)
	wt := filepath.Join(tmp, "wt")
	if _, err := gitRead(repo, []string{"worktree", "add", "-q", "--detach", wt, g.Commit}); err != nil {
		gr.Skipped = fmt.Sprintf("worktree: %v", err)
		return gr
	}
	defer func() { _, _ = gitRead(repo, []string{"worktree", "remove", "--force", wt}) }()
	ledger := filepath.Join(tmp, "ledger")
	task := fmt.Sprintf("cal-pr%d", g.PR)
	if err := calibrationLedger(repo, ledger, task, base, wt, owns); err != nil {
		gr.Skipped = fmt.Sprintf("synthetic ledger: %v", err)
		return gr
	}
	dims := o.Panel
	if len(dims) == 0 {
		dims = []string{""}
	}
	hitBy := make([]bool, len(g.Cases))
	var failures []string
	reviewed := 0
	for _, d := range dims {
		found, err := o.Review(ledger, wt, task, d)
		if err != nil {
			failures = append(failures, strings.TrimSpace(d+" review failed: "+err.Error()))
			if d != "" {
				gr.Personas = append(gr.Personas, CalibrationPersona{Persona: d, Skipped: err.Error()})
			}
			continue
		}
		reviewed++
		hits, misses, extra := matchFindings(g.Cases, found, o.Window)
		for i, c := range g.Cases {
			hitBy[i] = hitBy[i] || slices.Contains(hits, c)
		}
		gr.Found += len(found)
		gr.Extra = append(gr.Extra, extra...)
		if d != "" {
			gr.Personas = append(gr.Personas, CalibrationPersona{Persona: d, Groups: 1, Cases: len(g.Cases), Hits: len(hits),
				Misses: len(misses), Extra: len(extra), Recall: float64(len(hits)) / float64(len(g.Cases))})
		}
	}
	if reviewed == 0 {
		gr.Skipped = strings.Join(failures, "; ")
		return gr
	}
	for i, c := range g.Cases {
		if hitBy[i] {
			gr.Hits++
		} else {
			gr.Missed = append(gr.Missed, c)
		}
	}
	gr.Recall = float64(gr.Hits) / float64(gr.Cases)
	return gr
}

// Markdown renders the report: a table per PR state, the totals, the skipped
// groups and the missed claims, the defects the reviewer did not find and so
// the input for improving review_prompt.md.
func (r CalibrationReport) Markdown() string {
	var b strings.Builder
	reviewed := 0
	for _, g := range r.Reports {
		if g.Skipped == "" {
			reviewed++
		}
	}
	fmt.Fprintf(&b, "# Review agent calibration\n\n")
	fmt.Fprintf(&b, "Cases: `%s`, %d PR state(s), %d sampled (seed %d), %d reviewed; match window ±%d lines.\n\n",
		r.CasesFile, r.Groups, len(r.Reports), r.Seed, reviewed, r.Window)
	b.WriteString("| PR | commit | cases | hits | recall | findings | extra | note |\n")
	b.WriteString("|---:|---|---:|---:|---:|---:|---:|---|\n")
	for _, g := range r.Reports {
		if g.Skipped != "" {
			fmt.Fprintf(&b, "| #%d | %.12s | %d | - | - | - | - | skipped: %s |\n", g.PR, g.Commit, g.Cases, mdCell(g.Skipped))
			continue
		}
		fmt.Fprintf(&b, "| #%d | %.12s | %d | %d | %.2f | %d | %d | |\n", g.PR, g.Commit, g.Cases, g.Hits, g.Recall, g.Found, len(g.Extra))
	}
	fmt.Fprintf(&b, "\n**Total: %d/%d cases found, recall %.2f, %d extra finding(s).**\n", r.Hits, r.Cases, r.Recall, r.Extra)
	if len(r.Panel) > 0 {
		// Per persona (issue #420): the same sampled PR states, each persona
		// alone, then the panel, which hits a case when any persona does.
		b.WriteString("\n## Per persona\n\n")
		b.WriteString("| persona | PR states | cases | hits | misses | extra | recall |\n")
		b.WriteString("|---|---:|---:|---:|---:|---:|---:|\n")
		for _, p := range r.Personas {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %.2f |\n", p.Persona, p.Groups, p.Cases, p.Hits, p.Misses, p.Extra, p.Recall)
		}
		fmt.Fprintf(&b, "| **panel (any persona)** | %d | %d | %d | %d | %d | %.2f |\n", reviewed, r.Cases, r.Hits, r.Cases-r.Hits, r.Extra, r.Recall)
		fmt.Fprintf(&b, "\nCost: each reviewed PR state is one reviewer run per persona (%d), each up to two when its first answer is refused.\n", len(r.Panel))
	} else {
		b.WriteString("\nCost: each reviewed PR state is one reviewer run on the configured model (up to two when its first answer is refused).\n")
	}
	b.WriteString("\n## Missed claims\n\n")
	missed := 0
	for _, g := range r.Reports {
		for _, c := range g.Missed {
			missed++
			fmt.Fprintf(&b, "- PR #%d `%s:%d`: %s\n", c.PR, c.Path, c.Line, mdCell(c.Claim))
		}
	}
	if missed == 0 {
		b.WriteString("(none)\n")
	}
	return b.String()
}

// mdCell flattens text to one line with no table pipes.
func mdCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

// calibrationLedger writes a synthetic ledger in dir for one PR state: the
// repository's worker config, a brief owning the changed files with no
// gates, and the planned and dispatched events naming the base and workdir.
func calibrationLedger(repo, dir, task, base, workdir string, owns []string) error {
	fw := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(filepath.Join(fw, "briefs"), 0o755); err != nil {
		return err
	}
	if cfg, err := os.ReadFile(filepath.Join(repo, ".flywheel", configFileName)); err == nil {
		if err := os.WriteFile(filepath.Join(fw, configFileName), cfg, 0o644); err != nil {
			return err
		}
	}
	brief := filepath.ToSlash(filepath.Join(".flywheel", "briefs", task+".txt"))
	text := "owns: " + strings.Join(owns, ", ") + "\nneeds: none\n\n# Calibration\n\n" +
		"Review the change of this pull request (base " + base + ") for defects.\n"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(brief)), []byte(text), 0o644); err != nil {
		return err
	}
	return AppendEvents(dir, []Event{
		{Task: task, Kind: "planned", Brief: brief},
		{Task: task, Kind: "dispatched", Attempt: "r1", Session: "calibration-worker", Base: base, Workdir: workdir},
	})
}
