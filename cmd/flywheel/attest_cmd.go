package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("attest", "record gate readings a named external run measured on a commit", runAttest)
	registerHelp("attest", attestUsageLine, func() *flag.FlagSet { fs, _ := attestFlags(); return fs })
}

const attestUsageLine = "flywheel attest <task> --commit SHA --evidence URL --session S [--dir DIR]"

// attestOptions holds the parsed attest flags.
type attestOptions struct {
	dir      string
	commit   string
	evidence string
	session  string
}

// attestFlags defines attest's flags once, so help and run share them.
func attestFlags() (*flag.FlagSet, *attestOptions) {
	fs := flag.NewFlagSet("attest", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &attestOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.commit, "commit", "", "the commit the external run measured (7 to 40 hex characters)")
	fs.StringVar(&o.evidence, "evidence", "", "URL or reference of the external run whose gates passed")
	fs.StringVar(&o.session, "session", "", "the lead session recording the attestation (never a worker's)")
	return fs, o
}

// runAttest implements `flywheel attest <task>` (issue #367): a missing or
// malformed flag is a usage error (exit 2); a poka-yoke refusal exits 6; any
// other error exits 1.
func runAttest(args []string) {
	fs, o := attestFlags()
	usage := func(msg string) {
		fmt.Fprintf(os.Stderr, "flywheel attest: %s\n", msg)
		fmt.Fprintln(os.Stderr, "usage: "+attestUsageLine)
		os.Exit(2)
	}
	pos, err := parseArgs(fs, args)
	if err != nil {
		usage(err.Error())
	}
	if len(pos) != 1 {
		usage("exactly one task id is required")
	}
	task := pos[0]
	if !flywheel.CommitOK(o.commit) {
		usage(fmt.Sprintf("--commit %q is not 7 to 40 hex characters", o.commit))
	}
	if o.evidence == "" {
		usage("--evidence is required (the URL or reference of the run that measured the commit)")
	}
	if o.session == "" {
		usage("--session is required (the lead session)")
	}
	tree, err := flywheel.Attest(o.dir, task, o.commit, o.evidence, o.session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel attest: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	fmt.Printf("%s attested at %s (tree %s): %d gate reading(s) from %s\n", task, o.commit, tree, attestedReadings(o.dir, task, o.commit, o.evidence), o.evidence)
}

// attestedReadings counts the validated events of the attestation just
// appended: the trailing run of the task's external readings for commit and
// evidence.
func attestedReadings(dir, task, commit, evidence string) int {
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		return 0
	}
	n := 0
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Task != task || e.Source != "external" || e.Commit != commit || e.Evidence != evidence {
			break
		}
		if e.Kind == "validated" {
			n++
		}
	}
	return n
}
