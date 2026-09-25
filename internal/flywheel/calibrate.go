package flywheel

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
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
	Sample   int    // PR states to review; <= 0 means 10
	Seed     int64  // the sample's seed
	Worker   string // the reviewing worker (ReviewAgentOptions.Worker)
	Session  string // the reviewer session; required
	Window   int    // the matching window in lines; <= 0 means 15
	Main     string // the ref the merge-base is taken against; default origin/main
	Progress io.Writer
	// Review runs the reviewer over one synthetic unit; default: ReviewAgent.
	Review func(ledgerDir, workdir, task string) ([]ReviewFinding, error)
}

// CalibrationGroupReport is one PR state's result; Skipped names why a
// group was not reviewed.
type CalibrationGroupReport struct {
	PR      int
	Commit  string
	Cases   int
	Hits    int
	Found   int
	Recall  float64
	Missed  []CalibrationCase
	Extra   []ReviewFinding
	Skipped string
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
	if o.Review == nil {
		o.Review = func(ledgerDir, workdir, task string) ([]ReviewFinding, error) {
			res, err := ReviewAgent(ledgerDir, task, ReviewAgentOptions{
				Worker: o.Worker, Session: o.Session, Workdir: workdir, Round: 1, Progress: o.Progress,
			})
			return res.Findings, err
		}
	}
	all, err := LoadCalibrationCases(cases)
	if err != nil {
		return CalibrationReport{}, err
	}
	groups := groupCalibrationCases(all)
	rep := CalibrationReport{CasesFile: filepath.ToSlash(cases), Groups: len(groups), Seed: o.Seed, Window: o.Window}
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
		}
		rep.Reports = append(rep.Reports, gr)
	}
	if rep.Cases > 0 {
		rep.Recall = float64(rep.Hits) / float64(rep.Cases)
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
	found, err := o.Review(ledger, wt, task)
	if err != nil {
		gr.Skipped = fmt.Sprintf("review failed: %v", err)
		return gr
	}
	hits, misses, extra := matchFindings(g.Cases, found, o.Window)
	gr.Hits, gr.Found, gr.Missed, gr.Extra = len(hits), len(found), misses, extra
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
	b.WriteString("\nCost: each reviewed PR state is one reviewer run on the configured model (up to two when its first answer is refused).\n")
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
