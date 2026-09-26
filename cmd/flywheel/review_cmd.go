package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// reviewUsageLine is the flywheel review usage: a verdict passed in, or the
// review agent (--agent, issue #389).
const reviewUsageLine = "usage: flywheel review <task> --verdict pass|correct|reject --session <session> [--model M] [--note NOTE] [--check TEXT]... [--dir DIR] [--workdir PATH]\n" +
	"       flywheel review <task> --agent --session <session> [--worker NAME] [--round N] [--dir DIR] [--workdir PATH]\n" +
	"       flywheel review <task> --agent --fix --session <session> [--rounds N] [--worker NAME] [--fix-worker NAME] [--worktree] [--allow-overlap] [--dir DIR]\n" +
	"       flywheel review <task> --agent --panel --session <session> [--round N] [--fix [--rounds N] [--fix-worker NAME] [--worktree] [--allow-overlap]] [--dir DIR] [--workdir PATH]\n" +
	"       flywheel review --group <goal|tasks:a,b> --agent --session <session> [--base REF] [--worker NAME] [--dir DIR]\n" +
	"       flywheel review <task> --dismiss <finding-id> --session <lead> --note <why> [--dir DIR]\n" +
	"       flywheel review calibrate --cases FILE --session <reviewer> [--sample N] [--seed S] [--worker NAME] [--window L] [--main REF] [--panel [dims]] [--out FILE] [--dir DIR]"

func init() {
	register("review", "re-run a task's gates in an isolated worktree, or run the review agent (--agent)", runReview)
	registerHelp("review", reviewUsageLine[len("usage: "):], func() *flag.FlagSet { fs, _ := reviewFlags(); return fs })
}

// reviewOptions holds the parsed review flags.
type reviewOptions struct {
	dir       string
	workdir   string
	verdict   string
	session   string
	model     string
	note      string
	checklist repeatable
	agent     bool
	panel     bool
	worker    string
	round     int
	fix       bool
	rounds    int
	fixWorker string
	worktree  bool
	overlap   bool
	dismiss   string
	group     string
	base      string
}

// panelReviewerOrder is where a panel member's reviewer comes from, in order
// (flywheel.PanelReviewerSource).
const panelReviewerOrder = "a member's adapter/model in review.panel, else its worker, else staffing.reviewer, else the default worker"

// reviewFlags defines review's flags once, so help and run share them.
func reviewFlags() (*flag.FlagSet, *reviewOptions) {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &reviewOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.workdir, "workdir", "", "git working tree to review")
	fs.StringVar(&o.verdict, "verdict", "", "pass, correct, or reject")
	fs.StringVar(&o.session, "session", "", "reviewer session, distinct from every worker session")
	fs.StringVar(&o.model, "model", "", "the reviewer's model, recorded on the reviewed event")
	fs.StringVar(&o.note, "note", "", "optional review note")
	fs.Var(&o.checklist, "check", "domain checklist confirmation line (repeatable)")
	fs.BoolVar(&o.agent, "agent", false, "run the review agent: it reads the unit's diff and records findings and a verdict")
	fs.StringVar(&o.worker, "worker", "", "with --agent: the worker that reviews (default: the staffing reviewer role, else the default worker)")
	fs.IntVar(&o.round, "round", 0, "with --agent: the review round (default: the task's next round)")
	fs.BoolVar(&o.panel, "panel", false, "with --agent: run the review panel (review.panel), one persona per dimension, and print the verdict matrix; each member's reviewer is "+panelReviewerOrder)
	fs.BoolVar(&o.fix, "fix", false, "with --agent: loop review, send open blocking findings back to the worker, review again")
	fs.IntVar(&o.rounds, "rounds", 3, "with --fix: review rounds at most")
	fs.StringVar(&o.fixWorker, "fix-worker", "", "with --fix: the worker that corrects (default: the worker that built the unit)")
	fs.BoolVar(&o.worktree, "worktree", false, "with --fix: run the correction in the task's own git worktree")
	fs.BoolVar(&o.overlap, "allow-overlap", false, "with --fix: skip the owns-collision refusal for the correction dispatch; the dispatched note records the overlap")
	fs.StringVar(&o.dismiss, "dismiss", "", "record the lead's dismissal of this finding id (needs --session and --note)")
	fs.StringVar(&o.group, "group", "", "with --agent: review a group together, a goal id or tasks:<a>,<b>,... (issue #420)")
	fs.StringVar(&o.base, "base", "", "with --group: the ref the integration tree starts from (default main)")
	return fs, o
}

// reviewUsage prints the flywheel review usage lines.
func reviewUsage(w io.Writer) {
	fmt.Fprintln(w, reviewUsageLine)
}

// runReview implements `flywheel review <task>`: re-run the task's declared
// gates and owns check on an isolated copy of the tree and record the
// reviewed verdict, or with --agent run the review agent. Each poka-yoke
// refusal exits 6 with the rule id and the fix; a usage error exits 2; any
// other error exits 1.
func runReview(args []string) {
	if len(args) > 0 && args[0] == "calibrate" {
		runReviewCalibrate(args[1:])
		return
	}
	fs, o := reviewFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	if o.group != "" {
		runReviewGroup(pos, o)
		return
	}
	if o.base != "" {
		fmt.Fprintf(os.Stderr, "flywheel review: --base needs --group\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel review: exactly one task id is required\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	if o.dismiss != "" {
		runReviewDismiss(task, o)
		return
	}
	if o.panel {
		if !o.agent {
			fmt.Fprintf(os.Stderr, "flywheel review: --panel needs --agent\n")
			reviewUsage(os.Stderr)
			os.Exit(2)
		}
		runReviewPanel(task, o)
		return
	}
	if o.agent && o.fix {
		runReviewFix(task, o)
		return
	}
	if o.fix || o.fixWorker != "" || o.worktree || o.rounds != 3 {
		fmt.Fprintf(os.Stderr, "flywheel review: --fix needs --agent; --rounds, --fix-worker and --worktree need --fix\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	if o.agent {
		runReviewAgent(task, o)
		return
	}
	if o.worker != "" || o.round != 0 {
		fmt.Fprintf(os.Stderr, "flywheel review: --worker and --round need --agent\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	res, err := flywheel.ReviewTask(o.dir, task, flywheel.ReviewOptions{
		Dir: o.dir, Workdir: o.workdir, Verdict: o.verdict, Session: o.session, Model: o.model, Note: o.note,
		Checklist: o.checklist,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	reason := res.FinishedReason
	if !res.Finished {
		reason = "none"
	}
	fmt.Printf("%s finished: %s\n", task, reason)
	if res.Report {
		fmt.Printf("%s report: %s\n", task, res.ReportPath)
	}
	if o.verdict == "pass" && len(res.Checklist) > 0 {
		for _, c := range res.Checklist {
			fmt.Printf("checked: %s\n", c)
		}
	}
	fmt.Printf("%s reviewed %s\n", task, o.verdict)
}

// runReviewAgent implements `flywheel review <task> --agent`: one line per
// finding, then the verdict. It exits 0 on pass and 1 when the verdict is
// correct (so a script can loop), 6 on a rule refusal, 2 on a usage error,
// and 1 on any other error.
func runReviewAgent(task string, o *reviewOptions) {
	if o.verdict != "" || o.note != "" || o.model != "" || len(o.checklist) > 0 {
		fmt.Fprintf(os.Stderr, "flywheel review: --agent records its own verdict; drop --verdict, --note, --model and --check\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	if o.round < 0 {
		fmt.Fprintf(os.Stderr, "flywheel review: --round %d must be >= 1\n", o.round)
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	res, err := flywheel.ReviewAgent(o.dir, task, flywheel.ReviewAgentOptions{
		Worker: o.worker, Session: o.session, Workdir: o.workdir, Round: o.round, Progress: os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	for _, f := range res.Findings {
		fmt.Printf("[%s] %s:%d %s\n", f.Severity, f.File, f.Line, f.Claim)
	}
	fmt.Printf("review: %s (%s)\n", res.Verdict, res.Note)
	if res.Verdict != "pass" {
		os.Exit(1)
	}
}

// runReviewFix implements `flywheel review <task> --agent --fix` (issue
// #389): the review loop, with the review agent reviewing and the worker's
// session resumed on each findings delta. It prints each open blocking
// finding left and exits 0 when none is, 1 when some are or on an error, 6
// on a rule refusal and 2 on a usage error.
func runReviewFix(task string, o *reviewOptions) {
	if o.verdict != "" || o.note != "" || o.model != "" || len(o.checklist) > 0 || o.round != 0 {
		fmt.Fprintf(os.Stderr, "flywheel review: --fix records its own rounds and verdicts; drop --verdict, --note, --model, --check and --round\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	if o.rounds < 1 {
		fmt.Fprintf(os.Stderr, "flywheel review: --rounds %d must be >= 1\n", o.rounds)
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	worker, err := fixWorker(o.dir, task, o.fixWorker, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		os.Exit(1)
	}
	res, err := flywheel.ReviewLoop(o.dir, task, flywheel.ReviewLoopOptions{
		Rounds: o.rounds, ReviewSession: o.session, Worker: worker, ReviewWorker: o.worker, Progress: os.Stderr,
		Review: func(round int) (flywheel.ReviewAgentResult, error) {
			return flywheel.ReviewAgent(o.dir, task, flywheel.ReviewAgentOptions{
				Worker: o.worker, Session: o.session, Workdir: o.workdir, Round: round, Progress: os.Stderr,
			})
		},
		Correct: func(delta string) (flywheel.Result, error) {
			return correctFix(task, worker, delta, o)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	for _, f := range res.Open {
		fmt.Printf("OPEN %s [%s] %s:%d %s\n", f.Finding, f.Severity, f.Path, f.LineNo, f.Title)
	}
	printNeedsOwner(res.NeedsOwner)
	fmt.Printf("review loop: %s after %d review(s), %d correction(s)\n", res.Verdict, res.Reviews, res.Corrections)
	if res.Verdict != "pass" {
		os.Exit(1)
	}
}

// fixWorker resolves who corrects in a --fix loop (issue #469): --fix-worker
// when given, else the worker that built the unit (BuilderWorker). When the
// builder is not a configured worker it returns "" (Run's default worker, or
// the unit's line) and says so on w.
func fixWorker(dir, task, named string, w io.Writer) (string, error) {
	if named != "" {
		return named, nil
	}
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		return "", err
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		return "", err
	}
	if name, ok := flywheel.BuilderWorker(cfg, events, task); ok {
		return name, nil
	}
	if last, ok := flywheel.LastDispatched(events, task); ok {
		fmt.Fprintf(w, "review: the unit's builder (%s/%s) is not a configured worker; corrections use the default worker %s\n", last.Adapter, last.Model, cfg.DefaultWorker().Name)
	}
	return "", nil
}

// fixResumes reports whether a correction by worker (resolved as Run would,
// "" being the default worker) resumes the last attempt's session: only when
// both ran on the same adapter, since a session does not carry across
// adapters. Otherwise the correction is a fresh session reading the delta,
// and w says so.
func fixResumes(dir, task, worker string, w io.Writer) (bool, error) {
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		return false, err
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		return false, err
	}
	fw := cfg.DefaultWorker()
	if worker != "" {
		var ok bool
		if fw, ok = cfg.Worker(worker); !ok {
			return false, fmt.Errorf("no worker named %q in .flywheel/config.json", worker)
		}
	}
	last, ok := flywheel.LastDispatched(events, task)
	if !ok || last.Adapter == "" || last.Adapter == fw.Adapter {
		return true, nil
	}
	fmt.Fprintf(w, "review: attempt %s ran on adapter %s and fix worker %s is adapter %s; the correction starts a fresh session that reads the delta\n", last.Attempt, last.Adapter, fw.Name, fw.Adapter)
	return false, nil
}

// correctFix dispatches one --fix correction on worker with delta, resuming
// the worker's session only when fixResumes allows it.
func correctFix(task, worker, delta string, o *reviewOptions) (flywheel.Result, error) {
	resume, err := fixResumes(o.dir, task, worker, os.Stderr)
	if err != nil {
		return flywheel.Result{}, err
	}
	return flywheel.RunResumingLimits(o.dir, flywheel.RunOptions{
		Task: task, Worker: worker, Resume: resume, DeltaPath: delta, Worktree: o.worktree, AllowOverlap: o.overlap,
		Progress: os.Stderr, Stderr: os.Stderr,
	}, time.Sleep, time.Now)
}

// printPanelSources prints who runs each panel member's review and where
// that came from, before the panel runs (issue #469).
func printPanelSources(dir string, w io.Writer) {
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		return
	}
	for _, m := range cfg.ReviewPanel() {
		if rw, source, err := flywheel.PanelReviewerSource(cfg, m); err == nil {
			fmt.Fprintf(w, "panel %s: %s/%s (from %s)\n", m.Persona, rw.Adapter, rw.Model, source)
		}
	}
}

// printNeedsOwner prints one line per open blocking finding outside the
// unit's owns (issue #458): the fix loop never sends it to the worker.
func printNeedsOwner(findings []flywheel.Event) {
	for _, f := range findings {
		fmt.Printf("NEEDS-OWNER %s [%s] %s %s:%d %s\n", f.Finding, f.Severity, f.Category, f.Path, f.LineNo, f.Title)
	}
}

// reviewCalibrateOptions holds the parsed `flywheel review calibrate` flags.
type reviewCalibrateOptions struct {
	dir     string
	cases   string
	session string
	sample  int
	seed    int64
	worker  string
	window  int
	main    string
	out     string
	panel   panelList
}

// reviewCalibrateFlags defines the calibrate flags once, so help and run
// share them.
func reviewCalibrateFlags() (*flag.FlagSet, *reviewCalibrateOptions) {
	fs := flag.NewFlagSet("review calibrate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &reviewCalibrateOptions{}
	fs.StringVar(&o.dir, "dir", ".", "the repository whose past PR states are reviewed")
	fs.StringVar(&o.cases, "cases", "docs/calibration/external-review-bugs.json", "the calibration case file")
	fs.StringVar(&o.session, "session", "", "reviewer session (required)")
	fs.IntVar(&o.sample, "sample", 10, "PR states to review, a deterministic sample")
	fs.Int64Var(&o.seed, "seed", 1, "the sample's seed")
	fs.StringVar(&o.worker, "worker", "", "the worker that reviews (default: the staffing reviewer role, else the default worker)")
	fs.IntVar(&o.window, "window", 15, "a finding matches a case within this many lines")
	fs.StringVar(&o.main, "main", "origin/main", "the ref each PR's merge-base is taken against")
	fs.StringVar(&o.out, "out", "", "report file (default .flywheel/reviews/calibration-<UTC date>.md)")
	fs.Var(&o.panel, "panel", "calibrate each panel persona and the panel as a whole: bare, the configured review.panel; --panel=a,b, those dimensions")
	return fs, o
}

// panelList is `review calibrate --panel [dims]` (issue #420): bare, the
// configured review.panel; with a value, those comma-separated dimensions.
type panelList struct {
	set  bool
	dims []string
}

func (p *panelList) String() string {
	if p == nil {
		return ""
	}
	return strings.Join(p.dims, ",")
}

func (p *panelList) Set(v string) error {
	p.set, p.dims = v != "false", nil
	if v == "true" || v == "false" {
		return nil
	}
	for _, d := range strings.Split(v, ",") {
		if d = strings.TrimSpace(d); d != "" {
			p.dims = append(p.dims, d)
		}
	}
	return nil
}

// IsBoolFlag lets --panel stand alone.
func (p *panelList) IsBoolFlag() bool { return true }

// runReviewCalibrate implements `flywheel review calibrate` (issue #389): it
// runs the review agent over a sample of past PR states and prints and
// writes the recall report. It exits 0 on a report, 2 on a usage error, 6 on
// a rule refusal and 1 on any other error.
func runReviewCalibrate(args []string) {
	fs, o := reviewCalibrateFlags()
	pos, err := parseArgs(fs, args)
	if err == nil && o.panel.set && len(o.panel.dims) == 0 && len(pos) == 1 {
		// --panel a,b: the bare flag's dimensions as the next argument.
		err = o.panel.Set(pos[0])
		pos = nil
	}
	if err == nil && len(pos) > 0 {
		err = fmt.Errorf("unexpected argument %q", pos[0])
	}
	if err == nil && (o.sample < 1 || o.window < 1) {
		err = fmt.Errorf("--sample and --window must be >= 1")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review calibrate: %v\n", err)
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	panel := o.panel.dims
	if o.panel.set && len(panel) == 0 {
		cfg, _, cerr := flywheel.LoadConfig(o.dir)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "flywheel review calibrate: %v\n", cerr)
			os.Exit(1)
		}
		panel = cfg.PanelDimensions()
	}
	rep, err := flywheel.Calibrate(o.dir, o.cases, flywheel.CalibrateOptions{
		Sample: o.sample, Seed: o.seed, Worker: o.worker, Session: o.session, Window: o.window, Main: o.main,
		Panel: panel, Progress: os.Stderr,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review calibrate: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	out := o.out
	if out == "" {
		out = filepath.Join(o.dir, ".flywheel", "reviews", "calibration-"+time.Now().UTC().Format("2006-01-02")+".md")
	}
	md := rep.Markdown()
	fmt.Print(md)
	if err = os.MkdirAll(filepath.Dir(out), 0o755); err == nil {
		err = os.WriteFile(out, []byte(md), 0o644)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review calibrate: write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "calibration report: %s\n", out)
}

// runReviewDismiss implements `flywheel review <task> --dismiss <id>`: the
// lead's decision that a finding is closed (issue #389). A worker session of
// the task is refused (T4, exit 6).
func runReviewDismiss(task string, o *reviewOptions) {
	if o.agent || o.panel || o.fix || o.verdict != "" || o.model != "" || len(o.checklist) > 0 {
		fmt.Fprintf(os.Stderr, "flywheel review: --dismiss takes only --session, --note and --dir\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	if err := flywheel.DismissFinding(o.dir, task, o.dismiss, o.session, o.note); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	fmt.Printf("%s finding %s dismissed by %s\n", task, o.dismiss, o.session)
}

// runReviewPanel implements `flywheel review <task> --agent --panel` (issue
// #420): the review panel, one persona per configured dimension, run
// sequentially; with --fix, the review loop with a whole panel as each
// round's review. It prints each finding (or each open blocking finding left
// with --fix), then the verdict matrix, and exits 0 when every dimension is
// pass, 1 when one is not or on an error, 6 on a rule refusal and 2 on a
// usage error.
func runReviewPanel(task string, o *reviewOptions) {
	switch {
	case o.verdict != "" || o.note != "" || o.model != "" || len(o.checklist) > 0:
		fmt.Fprintf(os.Stderr, "flywheel review: --panel records its own verdicts; drop --verdict, --note, --model and --check\n")
	case o.worker != "":
		fmt.Fprintf(os.Stderr, "flywheel review: --panel members name their own reviewer: %s; drop --worker\n", panelReviewerOrder)
	case o.round < 0:
		fmt.Fprintf(os.Stderr, "flywheel review: --round %d must be >= 1\n", o.round)
	case o.fix && o.round != 0:
		fmt.Fprintf(os.Stderr, "flywheel review: --fix records its own rounds; drop --round\n")
	case !o.fix && (o.fixWorker != "" || o.worktree || o.rounds != 3):
		fmt.Fprintf(os.Stderr, "flywheel review: --rounds, --fix-worker and --worktree need --fix\n")
	case o.rounds < 1:
		fmt.Fprintf(os.Stderr, "flywheel review: --rounds %d must be >= 1\n", o.rounds)
	default:
		runReviewPanelChecked(task, o)
		return
	}
	reviewUsage(os.Stderr)
	os.Exit(2)
}

// runReviewPanelChecked runs the panel once its flags are checked.
func runReviewPanelChecked(task string, o *reviewOptions) {
	printPanelSources(o.dir, os.Stderr)
	var last flywheel.PanelResult
	panel := func(round int) error {
		res, err := flywheel.ReviewPanel(o.dir, task, flywheel.ReviewPanelOptions{
			Session: o.session, Workdir: o.workdir, Round: round, Progress: os.Stderr,
		})
		last = res
		return err
	}
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	if !o.fix {
		if err := panel(o.round); err != nil {
			fail(err)
		}
		for _, m := range last.Members {
			for _, f := range m.Findings {
				fmt.Printf("[%s] %s %s:%d %s\n", f.Severity, f.Category, f.File, f.Line, f.Claim)
			}
		}
	} else {
		worker, err := fixWorker(o.dir, task, o.fixWorker, os.Stderr)
		if err != nil {
			fail(err)
		}
		res, err := flywheel.ReviewLoop(o.dir, task, flywheel.ReviewLoopOptions{
			Rounds: o.rounds, ReviewSession: o.session, Worker: worker, Progress: os.Stderr,
			Review: func(round int) (flywheel.ReviewAgentResult, error) {
				err := panel(round)
				return flywheel.ReviewAgentResult{Round: round, Tree: last.Tree, Crashed: last.Crashed}, err
			},
			Correct: func(delta string) (flywheel.Result, error) {
				return correctFix(task, worker, delta, o)
			},
		})
		if err != nil {
			fail(err)
		}
		for _, f := range res.Open {
			fmt.Printf("OPEN %s [%s] %s %s:%d %s\n", f.Finding, f.Severity, f.Category, f.Path, f.LineNo, f.Title)
		}
		printNeedsOwner(res.NeedsOwner)
		fmt.Printf("review loop: %s after %d panel review(s), %d correction(s)\n", res.Verdict, res.Reviews, res.Corrections)
	}
	fmt.Printf("review panel round %d on tree %s:\n%s", last.Round, last.Tree, flywheel.FormatMatrix(last.Matrix, last.Panel))
	for _, d := range last.Panel {
		if last.Matrix[d] != "pass" {
			os.Exit(1)
		}
	}
}

// runReviewGroup implements `flywheel review --group <goal|tasks:a,b> --agent`
// (issue #420): the group's members merged into one integration tree, the
// group gates run there and the integration reviewer over the combined diff.
// It prints the members, the conflicts, the group gate results and the
// findings by the task each was routed to, and exits 0 on pass, 1 on correct
// or an error, 6 on a rule refusal and 2 on a usage error.
func runReviewGroup(pos []string, o *reviewOptions) {
	switch {
	case len(pos) > 0:
		fmt.Fprintf(os.Stderr, "flywheel review: --group takes no task id\n")
	case !o.agent:
		fmt.Fprintf(os.Stderr, "flywheel review: --group needs --agent\n")
	case o.panel || o.fix || o.dismiss != "" || o.verdict != "" || o.note != "" || o.model != "" || len(o.checklist) > 0 ||
		o.round != 0 || o.workdir != "" || o.fixWorker != "" || o.worktree || o.rounds != 3:
		fmt.Fprintf(os.Stderr, "flywheel review: --group takes only --agent, --session, --base, --worker and --dir\n")
	default:
		res, err := flywheel.ReviewGroup(o.dir, o.group, flywheel.ReviewGroupOptions{
			Session: o.session, Base: o.base, Worker: o.worker, Progress: os.Stderr,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
			if flywheel.IsRuleRefusal(err) {
				os.Exit(6)
			}
			os.Exit(1)
		}
		fmt.Printf("group %s round %d: members %s\n", res.Task, res.Round, strings.Join(res.Members, ", "))
		for _, m := range res.Missing {
			fmt.Printf("missing %s: no branch and no finished commit; not merged\n", m)
		}
		for _, m := range res.Members {
			if c := res.Conflicts[m]; len(c) > 0 {
				fmt.Printf("conflict %s: %s\n", m, strings.Join(c, ", "))
			}
		}
		for _, g := range res.Gates {
			fmt.Printf("gate %s rc=%d: %s\n", g.Gate, g.RC, g.Command)
		}
		for _, owner := range append(append([]string(nil), res.Members...), res.Task) {
			for _, f := range res.Findings {
				if f.Task == owner {
					fmt.Printf("%s: %s [%s] %s:%d %s\n", owner, f.Finding, f.Severity, f.Path, f.LineNo, f.Title)
				}
			}
		}
		fmt.Printf("group review: %s on tree %s (%s); thread %s\n", res.Verdict, res.Tree, res.Note, flywheel.GroupThreadPath(res.Group))
		if res.Verdict != "pass" {
			os.Exit(1)
		}
		return
	}
	reviewUsage(os.Stderr)
	os.Exit(2)
}
