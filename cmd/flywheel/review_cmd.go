package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("review", "re-run a task's gates and owns check in an isolated worktree", runReview)
	registerHelp("review", "flywheel review <task> --verdict pass|correct|reject --session <session> [--model M] [--note NOTE] [--check TEXT]... [--dir DIR] [--workdir PATH]", func() *flag.FlagSet { fs, _ := reviewFlags(); return fs })
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
	return fs, o
}

// reviewUsage prints the flywheel review usage line.
func reviewUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel review <task> --verdict pass|correct|reject --session <session> [--model M] [--note NOTE] [--check TEXT]... [--dir DIR] [--workdir PATH]")
}

// runReview implements `flywheel review <task>`: re-run the task's declared
// gates and owns check on an isolated copy of the tree and record the
// reviewed verdict. Each poka-yoke refusal exits 6 with the rule id and the
// fix; a usage error exits 2; any other error exits 1.
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
