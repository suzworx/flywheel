package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("inspect", "inspect a task against the poka-yoke rules", runInspect)
	registerHelp("inspect", inspectUsageLine, func() *flag.FlagSet { fs, _ := inspectFlags(); return fs })
}

// inspectOptions holds the parsed inspect flags.
type inspectOptions struct {
	dir       string
	workdir   string
	verdict   string
	session   string
	note      string
	commit    string
	exception string
	group     string
}

// inspectFlags defines inspect's flags once, so help and run share them.
func inspectFlags() (*flag.FlagSet, *inspectOptions) {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &inspectOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.workdir, "workdir", "", "git working tree to hash")
	fs.StringVar(&o.verdict, "verdict", "", "pass, rework, scrap, or escalate")
	fs.StringVar(&o.session, "session", "", "inspector session, distinct from every worker session")
	fs.StringVar(&o.note, "note", "", "optional inspection note")
	fs.StringVar(&o.commit, "commit", "", "inspect this commit's tree instead of the working tree (an attested, merged commit)")
	fs.StringVar(&o.exception, "exception", "", "why a lead-built unit over lead_built.max_changed_lines passes")
	fs.StringVar(&o.group, "group", "", "inspect every member of a group (a goal id or tasks:<a>,<b>): each is checked first and recorded only when all pass")
	return fs, o
}

// inspectUsageLine is flywheel inspect's usage, shared by help and errors.
const inspectUsageLine = "flywheel inspect <task>|--group <goal|tasks:a,b> --verdict pass|rework|scrap|escalate --session <session> [--commit SHA] [--exception WHY] [--note NOTE] [--dir DIR] [--workdir PATH]"

// inspectUsage prints the flywheel inspect usage line.
func inspectUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+inspectUsageLine)
}

// runInspect implements `flywheel inspect <task>` and `flywheel inspect
// --group <group>`. Each poka-yoke refusal exits 6 with the rule id and the
// fix; a usage error exits 2; any other error exits 1.
func runInspect(args []string) {
	fs, o := inspectFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel inspect: %v\n", err)
		inspectUsage(os.Stderr)
		os.Exit(2)
	}
	if o.group != "" && len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel inspect: --group takes no task id\n")
		inspectUsage(os.Stderr)
		os.Exit(2)
	}
	if o.group == "" && len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel inspect: exactly one task id is required\n")
		inspectUsage(os.Stderr)
		os.Exit(2)
	}
	if o.commit != "" && !flywheel.CommitOK(o.commit) {
		fmt.Fprintf(os.Stderr, "flywheel inspect: --commit %q is not 7 to 40 hex characters\n", o.commit)
		inspectUsage(os.Stderr)
		os.Exit(2)
	}
	opts := flywheel.InspectOptions{
		Dir: o.dir, Workdir: o.workdir, Verdict: o.verdict, Session: o.session, Note: o.note, Commit: o.commit, Exception: o.exception,
	}
	exit := func(err error) {
		fmt.Fprintf(os.Stderr, "flywheel inspect: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	if o.group != "" {
		tasks, err := flywheel.InspectGroup(o.dir, o.group, opts)
		for _, t := range tasks {
			fmt.Printf("%s inspected %s (group %s)\n", t, o.verdict, flywheel.GroupID(o.group))
		}
		if err != nil {
			exit(err)
		}
		return
	}
	task := pos[0]
	if err := flywheel.InspectTask(o.dir, task, opts); err != nil {
		exit(err)
	}
	fmt.Printf("%s inspected %s\n", task, o.verdict)
	if o.verdict == "pass" {
		// A vs-base pass's inherited debt is shown at inspection (issue #788);
		// the pass is already recorded, so a read error only warns.
		lines, err := flywheel.VsBaseSummary(o.dir, task)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel inspect: warning: vs-base summary: %v\n", err)
		}
		for _, l := range lines {
			fmt.Println(l)
		}
	}
}
