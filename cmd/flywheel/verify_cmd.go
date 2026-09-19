package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("verify", "verify tasks against the poka-yoke rules", runVerify)
	registerHelp("verify", "flywheel verify [<task>...] [--all] [--json] [--log] [--dir DIR] [--workdir PATH]", func() *flag.FlagSet { fs, _ := verifyFlags(); return fs })
}

// verifyOptions holds the parsed verify flags.
type verifyOptions struct {
	dir     string
	all     bool
	jsonOut bool
	workdir string
	log     bool
}

// verifyFlags defines verify's flags once, so help and run share them.
func verifyFlags() (*flag.FlagSet, *verifyOptions) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &verifyOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.workdir, "workdir", "", "repository to resolve tree objects in (default: a workdir recorded on the events, else the target directory)")
	fs.BoolVar(&o.all, "all", false, "verify every task in the event log")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	fs.BoolVar(&o.log, "log", false, "also check the event log's hash chain (a record edited or removed breaks it)")
	return fs, o
}

// verifyUsage prints the flywheel verify usage line.
func verifyUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel verify [<task>...] [--all] [--json] [--log] [--dir DIR] [--workdir PATH]")
}

// verifyExit maps a verify result to the CLI exit code: 0 when every check
// passes, 6 when any check is an established violation, 8 when no violation
// is established but some check is inconclusive — the tree could not be
// resolved, so neither confirmed nor refused (issue #244). A violation always
// outranks an inconclusive.
func verifyExit(items []flywheel.VerifyItem) int {
	hasFailure, hasInconclusive := false, false
	for _, item := range items {
		if item.Inconclusive {
			hasInconclusive = true
		} else if !item.Pass {
			hasFailure = true
		}
	}
	switch {
	case hasFailure:
		return 6
	case hasInconclusive:
		return 8
	}
	return 0
}

// runVerify implements `flywheel verify`. It prints PASS or FAIL per check
// with the rule id and a reason. Exit 0 when every check passes, 6 on any
// established violation, 8 when every failing check is inconclusive, 2 on a
// usage error, 1 on any other error. --json emits the machine-readable result
// instead of the human lines.
func runVerify(args []string) {
	fs, o := verifyFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel verify: %v\n", perr)
		verifyUsage(os.Stderr)
		os.Exit(2)
	}
	tasks := pos
	res, err := flywheel.VerifyTasks(o.dir, flywheel.VerifyOptions{Dir: o.dir, Tasks: tasks, All: o.all, Workdir: o.workdir})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel verify: %v\n", err)
		os.Exit(1)
	}
	if o.log {
		chain, err := flywheel.VerifyLogChain(o.dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel verify: %v\n", err)
			os.Exit(1)
		}
		reason := ""
		if chain.OK() {
			if chain.Chained == 0 {
				reason = "no chained records yet"
			} else {
				reason = fmt.Sprintf("%d of %d records chained since line %d, no break", chain.Chained, chain.Lines, chain.FirstChained)
			}
		} else {
			reason = fmt.Sprintf("line %d: prev %s matches no earlier line (an earlier record was edited or removed)", chain.BreakLine, chain.BreakPrev[:min(12, len(chain.BreakPrev))])
		}
		res.Items = append(res.Items, flywheel.VerifyItem{Task: "-", Rule: "LOG", Pass: chain.OK(), Reason: reason})
		if !chain.OK() {
			res.Passed = false
		}
	}
	if o.jsonOut {
		b, _ := json.Marshal(res)
		fmt.Println(string(b))
		os.Exit(verifyExit(res.Items))
	}
	if len(res.Items) == 0 {
		fmt.Println("nothing to verify")
		os.Exit(0)
	}
	for _, item := range res.Items {
		status := "PASS"
		switch {
		case item.Inconclusive:
			status = "INCONCLUSIVE"
		case !item.Pass:
			status = "FAIL"
		}
		fmt.Printf("%s %s %s: %s\n", status, item.Task, item.Rule, item.Reason)
	}
	os.Exit(verifyExit(res.Items))
}
