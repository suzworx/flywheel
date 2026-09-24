package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// reviewUsageLine is the flywheel review usage: a verdict passed in, or the
// review agent (--agent, issue #389).
const reviewUsageLine = "usage: flywheel review <task> --verdict pass|correct|reject --session <session> [--model M] [--note NOTE] [--check TEXT]... [--dir DIR] [--workdir PATH]\n" +
	"       flywheel review <task> --agent --session <session> [--worker NAME] [--round N] [--dir DIR] [--workdir PATH]"

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
	worker    string
	round     int
}

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
	fs, o := reviewFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel review: %v\n", err)
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel review: exactly one task id is required\n")
		reviewUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
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
