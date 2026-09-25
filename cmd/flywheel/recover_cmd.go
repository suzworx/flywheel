package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("recover", "report where every unit is, whether the world matches the ledger, and the next safe action", runRecover)
	registerHelp("recover", recoverUsageLine, func() *flag.FlagSet { fs, _ := recoverFlags(); return fs })
}

const recoverUsageLine = "flywheel recover [--json] [--apply] [--all] [--dormant-after DUR] [--dir DIR]"

// recoverOptions holds the parsed recover flags.
type recoverOptions struct {
	dir          string
	jsonOut      bool
	apply        bool
	all          bool
	dormantAfter time.Duration
}

// recoverFlags defines recover's flags once, so help and run share them.
func recoverFlags() (*flag.FlagSet, *recoverOptions) {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &recoverOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.jsonOut, "json", false, "print machine-readable JSON")
	fs.BoolVar(&o.apply, "apply", false, "run the safe actions only (mark-lost, re-validate, a conflict-free rebase) on units that are not dormant, and record a recovered event")
	fs.BoolVar(&o.all, "all", false, "list landed and dormant units and every history item instead of summary lines")
	fs.DurationVar(&o.dormantAfter, "dormant-after", 168*time.Hour, "a unit not landed with no event for longer than this is dormant: reported, never acted on by --apply (0 disables)")
	return fs, o
}

// recoverUsage prints the flywheel recover usage line.
func recoverUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+recoverUsageLine)
}

// runRecover implements `flywheel recover` (issue #422).
func runRecover(args []string) {
	os.Exit(recoverMain(args, os.Stdout, os.Stderr, time.Now()))
}

// recoverMain prints the recover report (read-only unless --apply) and
// returns the exit code: 0 when integrity passes and no unit needs
// investigate, 1 otherwise or on any error, 2 usage.
func recoverMain(args []string, stdout, stderr io.Writer, now time.Time) int {
	fs, o := recoverFlags()
	pos, err := parseArgs(fs, args)
	if err != nil || len(pos) != 0 {
		if err == nil {
			err = fmt.Errorf("unexpected argument %q", pos[0])
		}
		fmt.Fprintf(stderr, "flywheel recover: %v\n", err)
		recoverUsage(stderr)
		return 2
	}
	root := o.dir // parseArgs already moved a task worktree's --dir to the main ledger
	var rep flywheel.RecoverReport
	var out any
	ro := flywheel.RecoverOptions{DormantAfter: o.dormantAfter, All: o.all}
	if o.apply {
		applied, err := flywheel.RecoverApply(root, now, ro)
		if err != nil {
			fmt.Fprintf(stderr, "flywheel recover: %v\n", err)
			return 1
		}
		rep, out = applied.Report, applied
		if !o.jsonOut {
			for _, a := range applied.Applied {
				fmt.Fprintf(stdout, "applied: %s\n", a)
			}
			dormant := 0
			for _, l := range applied.Left {
				if strings.HasPrefix(l, "dormant ") && !o.all {
					dormant++
					continue
				}
				fmt.Fprintf(stdout, "left for the lead: %s\n", l)
			}
			if dormant > 0 {
				fmt.Fprintf(stdout, "left for the lead: %d dormant unit(s), never applied (--all lists them)\n", dormant)
			}
		}
	} else {
		if rep, err = flywheel.Recover(root, now, ro); err != nil {
			fmt.Fprintf(stderr, "flywheel recover: %v\n", err)
			return 1
		}
		out = rep
	}
	if o.jsonOut {
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "flywheel recover: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(b))
	} else {
		fmt.Fprint(stdout, rep.Text())
	}
	if !rep.OK() {
		return 1
	}
	return 0
}
