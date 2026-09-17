package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"flywheel/internal/flywheel"
)

func init() {
	register("land", "record a landing for a passed task", runLand)
	registerHelp("land", "flywheel land <task> --commit <sha> [--by-lead --reason TEXT] [--note TEXT] [--dir DIR]", func() *flag.FlagSet { fs, _ := landFlags(); return fs })
}

// landOptions holds the parsed land flags.
type landOptions struct {
	dir    string
	commit string
	note   string
	byLead bool
	reason string
}

// landFlags defines land's flags once, so help and run share them.
func landFlags() (*flag.FlagSet, *landOptions) {
	fs := flag.NewFlagSet("land", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &landOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.commit, "commit", "", "commit id (7 to 40 hex characters)")
	fs.StringVar(&o.note, "note", "", "optional landing note")
	fs.BoolVar(&o.byLead, "by-lead", false, "record this landing as lead-implemented (requires --reason)")
	fs.StringVar(&o.reason, "reason", "", "why the lead implemented this unit directly (requires --by-lead)")
	return fs, o
}

// landUsage prints the flywheel land usage line.
func landUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel land <task> --commit <sha> [--by-lead --reason TEXT] [--note TEXT] [--dir DIR]")
}

// runLand implements `flywheel land <task>`. A malformed commit is a usage
// error (exit 2); a poka-yoke refusal exits 6; any other error exits 1.
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
	err = flywheel.LandTask(o.dir, task, o.commit, o.note, o.byLead, o.reason)
	if err != nil {
		if errors.Is(err, flywheel.ErrAlreadyLanded) {
			fmt.Printf("%s already landed %s\n", task, o.commit)
			return
		}
		fmt.Fprintf(os.Stderr, "flywheel land: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	fmt.Printf("%s landed %s\n", task, o.commit)
}
