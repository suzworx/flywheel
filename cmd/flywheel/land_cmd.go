package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("land", "record a landing for a passed task", runLand)
	registerHelp("land", landUsageLine, func() *flag.FlagSet { fs, _ := landFlags(); return fs })
}

// landOptions holds the parsed land flags.
type landOptions struct {
	dir            string
	commit         string
	note           string
	byLead         bool
	reason         string
	exception      string
	session        string
	allowUntriaged string
	merge          bool
	onto           string
	correct        string
}

// landFlags defines land's flags once, so help and run share them.
func landFlags() (*flag.FlagSet, *landOptions) {
	fs := flag.NewFlagSet("land", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &landOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.commit, "commit", "", "commit id (7 to 40 hex characters); it must exist, be on the integration branch and touch the unit's files (exit 8 when that cannot be checked)")
	fs.StringVar(&o.note, "note", "", "optional landing note")
	fs.BoolVar(&o.byLead, "by-lead", false, "record this landing as lead-implemented (requires --reason)")
	fs.StringVar(&o.reason, "reason", "", "why the lead implemented this unit directly (requires --by-lead), or why the landing is corrected (with --correct)")
	fs.StringVar(&o.exception, "exception", "", "land a unit that is not passed on a recorded exception: the evidence the lead verified by hand (requires --session)")
	fs.StringVar(&o.session, "session", "", "the lead session recording the exception (requires --exception) or the correction (with --correct)")
	fs.StringVar(&o.allowUntriaged, "allow-untriaged", "", "land although the task has untriaged signals, recording this reason (rule T9)")
	fs.BoolVar(&o.merge, "merge", false, "land a passed unit from its worktree through the local queue: rebase onto the integration branch, re-run the gates, fast-forward")
	fs.StringVar(&o.onto, "onto", "", "integration branch (default: the branch checked out in --dir)")
	fs.StringVar(&o.correct, "correct", "", "correct a wrong landing to this commit, recording a land_corrected event (requires --reason and --session; the old landing stays in the log)")
	return fs, o
}

// landUsageLine is the flywheel land usage, shared by help and errors.
const landUsageLine = "flywheel land <task> [--merge [--onto BRANCH]] [--commit <sha> [--by-lead --reason TEXT] [--exception TEXT --session S] [--allow-untriaged REASON]] [--correct <sha> --reason TEXT --session S] [--note TEXT] [--dir DIR]"

// landUsage prints the flywheel land usage line.
func landUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+landUsageLine)
}

// runLand implements `flywheel land <task>`. A malformed commit is a usage
// error (exit 2); a poka-yoke refusal exits 6; a commit land cannot verify
// (an InconclusiveError: it does not resolve) exits 8; any other error exits 1.
// Landing an already-landed task with the same commit is a no-op (exit 0).
func runLand(args []string) {
	fs, o := landFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel land: %v\n", err)
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel land: exactly one task id is required\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]

	// --correct: a correction of a wrong landing (issue #673).
	if o.correct != "" {
		runLandCorrect(task, o)
		return
	}

	// Validate flag combinations
	if o.merge && o.commit != "" {
		fmt.Fprintf(os.Stderr, "flywheel land: --merge and --commit are mutually exclusive\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if o.merge && o.byLead {
		fmt.Fprintf(os.Stderr, "flywheel land: --merge and --by-lead are mutually exclusive\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if o.merge && o.exception != "" {
		fmt.Fprintf(os.Stderr, "flywheel land: --merge and --exception are mutually exclusive\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if !o.merge && o.onto != "" {
		fmt.Fprintf(os.Stderr, "flywheel land: --onto requires --merge\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}

	// Handle --merge path
	if o.merge {
		result, err := flywheel.LandMerge(task, flywheel.LandMergeOptions{Dir: o.dir, Onto: o.onto, Note: o.note, AllowUntriaged: o.allowUntriaged})
		if err != nil {
			if len(result.Conflict) > 0 {
				fmt.Fprintf(os.Stderr, "%s conflicts landing onto %s: %s\n", task, result.Onto, strings.Join(result.Conflict, ", "))
				fmt.Fprintf(os.Stderr, "correction brief: %s\n", result.Delta)
			}
			fmt.Fprintf(os.Stderr, "flywheel land: %v\n", err)
			if flywheel.IsRuleRefusal(err) {
				os.Exit(6)
			}
			os.Exit(1)
		}
		fmt.Printf("%s landed %s onto %s\n", task, result.Commit, result.Onto)
		return
	}

	// Handle --commit path
	if o.commit == "" {
		fmt.Fprintf(os.Stderr, "flywheel land: --commit is required without --merge\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if !flywheel.CommitOK(o.commit) {
		fmt.Fprintf(os.Stderr, "flywheel land: commit %q is not 7 to 40 hex characters\n", o.commit)
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if o.reason != "" && !o.byLead {
		fmt.Fprintf(os.Stderr, "flywheel land: --reason requires --by-lead\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if o.byLead && o.reason == "" {
		fmt.Fprintf(os.Stderr, "flywheel land: --by-lead requires --reason\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if o.exception != "" && o.session == "" {
		fmt.Fprintf(os.Stderr, "flywheel land: --exception requires --session\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if o.session != "" && o.exception == "" {
		fmt.Fprintf(os.Stderr, "flywheel land: --session requires --exception\n")
		landUsage(os.Stderr)
		os.Exit(2)
	}
	err = flywheel.LandTaskWithException(o.dir, task, o.commit, o.note, o.byLead, o.reason, o.exception, o.session, o.allowUntriaged)
	if err != nil {
		if errors.Is(err, flywheel.ErrAlreadyLanded) {
			fmt.Printf("%s already landed %s\n", task, o.commit)
			return
		}
		var inc *flywheel.InconclusiveError
		if errors.As(err, &inc) {
			fmt.Fprintf(os.Stderr, "flywheel land: inconclusive: %s\n", inc.Fix)
			os.Exit(8)
		}
		fmt.Fprintf(os.Stderr, "flywheel land: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	if o.exception != "" {
		fmt.Printf("%s landed %s on a recorded exception\n", task, o.commit)
	} else {
		fmt.Printf("%s landed %s\n", task, o.commit)
	}
}

// landCorrectConflicts returns the first flag set alongside --correct that a
// correction cannot take, "" when there is none.
func landCorrectConflicts(o *landOptions) string {
	switch {
	case o.commit != "":
		return "--commit"
	case o.merge:
		return "--merge"
	case o.byLead:
		return "--by-lead"
	case o.exception != "":
		return "--exception"
	case o.allowUntriaged != "":
		return "--allow-untriaged"
	case o.onto != "":
		return "--onto"
	case o.note != "":
		return "--note"
	}
	return ""
}

// runLandCorrect implements `flywheel land <task> --correct <sha> --reason
// TEXT --session S`: a flag conflict, a malformed commit or a missing reason
// or session is a usage error (exit 2); a refusal exits 6; a commit that
// cannot be verified exits 8; any other error exits 1.
func runLandCorrect(task string, o *landOptions) {
	usage := func(msg string) {
		fmt.Fprintf(os.Stderr, "flywheel land: %s\n", msg)
		landUsage(os.Stderr)
		os.Exit(2)
	}
	if f := landCorrectConflicts(o); f != "" {
		usage("--correct and " + f + " are mutually exclusive")
	}
	if o.reason == "" || o.session == "" {
		usage("--correct requires --reason and --session")
	}
	if !flywheel.CommitOK(o.correct) {
		usage(fmt.Sprintf("commit %q is not 7 to 40 hex characters", o.correct))
	}
	if err := flywheel.CorrectLanding(o.dir, task, o.correct, o.reason, o.session); err != nil {
		var inc *flywheel.InconclusiveError
		if errors.As(err, &inc) {
			fmt.Fprintf(os.Stderr, "flywheel land: inconclusive: %s\n", inc.Fix)
			os.Exit(8)
		}
		fmt.Fprintf(os.Stderr, "flywheel land: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	fmt.Printf("%s landing corrected to %s\n", task, o.correct)
}
