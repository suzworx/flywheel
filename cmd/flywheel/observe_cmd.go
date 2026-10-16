package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("observe", "record a check only a person can make (an observe: line) on the unit's tree", runObserve)
	registerHelp("observe", observeUsageLine, func() *flag.FlagSet { fs, _ := observeFlags(); return fs })
}

const observeUsageLine = "flywheel observe <task> --check N --result pass|fail --evidence REF --session S [--dir DIR]"

// observeOptions holds the parsed observe flags.
type observeOptions struct {
	dir      string
	check    int
	result   string
	evidence string
	session  string
}

// observeFlags defines observe's flags once, so help and run share them.
func observeFlags() (*flag.FlagSet, *observeOptions) {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &observeOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.IntVar(&o.check, "check", 0, "the 1-based number of the brief's observe: line looked at")
	fs.StringVar(&o.result, "result", "", "pass or fail: what the person saw")
	fs.StringVar(&o.evidence, "evidence", "", "the photo, recording or reference of what was seen")
	fs.StringVar(&o.session, "session", "", "the lead session recording the observation (never a worker's)")
	return fs, o
}

// runObserve implements `flywheel observe <task>` (issue #842): a missing or
// invalid --check, a --result other than pass or fail, or a missing
// --evidence is a usage error (exit 2); a poka-yoke refusal exits 6; any
// other error exits 1.
func runObserve(args []string) {
	fs, o := observeFlags()
	usage := func(msg string) {
		fmt.Fprintf(os.Stderr, "flywheel observe: %s\n", msg)
		fmt.Fprintln(os.Stderr, "usage: "+observeUsageLine)
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
	if o.check < 1 {
		usage("--check N is required (the 1-based number of the brief's observe: line)")
	}
	if o.result != "pass" && o.result != "fail" {
		usage(fmt.Sprintf("--result %q is not pass or fail", o.result))
	}
	if o.evidence == "" {
		usage("--evidence is required (the photo or reference of what was seen)")
	}
	tree, err := flywheel.Observe(o.dir, task, o.check, o.result == "pass", o.evidence, o.session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel observe: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	fmt.Printf("observed %s obs%d %s on tree %s\n", task, o.check, o.result, tree)
}
