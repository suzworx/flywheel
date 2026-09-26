package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// shipUsageLine is ship's usage, shared by help and the usage error.
const shipUsageLine = "flywheel ship <task> [--integration BRANCH] [--workdir PATH] [--remote NAME] [--message TEXT] [--dir DIR]"

func init() {
	register("ship", "commit a passed unit's leftovers, merge the integration branch into fw/<task> and re-run its gates (local half; push, PR, CI, merge, land and close are not in this version yet)", runShip)
	registerHelp("ship", shipUsageLine, func() *flag.FlagSet { fs, _ := shipFlags(); return fs })
}

// shipOptions holds the parsed ship flags.
type shipOptions struct {
	dir         string
	integration string
	workdir     string
	remote      string
	message     string
}

// shipFlags defines ship's flags once, so help and run share them.
func shipFlags() (*flag.FlagSet, *shipOptions) {
	fs := flag.NewFlagSet("ship", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &shipOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.integration, "integration", "", "the branch to merge in (default integration.branch, else main)")
	fs.StringVar(&o.workdir, "workdir", "", "the task worktree (default .flywheel/worktrees/<task>, with fw/<task> checked out)")
	fs.StringVar(&o.remote, "remote", "origin", "the remote to fetch the integration branch from")
	fs.StringVar(&o.message, "message", "", `the commit message for leftover owned changes (default "<task> ship")`)
	return fs, o
}

// shipUsage prints the usage line and what this version leaves out.
func shipUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+shipUsageLine)
	fmt.Fprintln(w, "the remote half (push, PR, CI, merge, land, close) is not in this version yet")
}

// runShip implements `flywheel ship <task>` (issue #457).
func runShip(args []string) {
	os.Exit(shipMain(args, os.Stdout, os.Stderr))
}

// shipMain runs the local ship steps and returns the exit code: 0 every step
// ok or skip, 1 an error (git, fetch, a merge conflict), 2 usage, 5 the gates
// failed on the merged tree, 6 a preflight rule refusal.
func shipMain(args []string, stdout, stderr io.Writer) int {
	fs, o := shipFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "flywheel ship: %v\n", err)
		shipUsage(stderr)
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "flywheel ship: exactly one task id is required\n")
		shipUsage(stderr)
		return 2
	}
	_, err = flywheel.Ship(o.dir, pos[0], flywheel.ShipOptions{
		Integration: o.integration, Workdir: o.workdir, Message: o.message, Remote: o.remote, Progress: stdout,
	})
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "flywheel ship: %v\n", err)
	var rr *flywheel.RuleRefusal
	switch {
	case errors.As(err, &rr):
		return 6
	case errors.Is(err, flywheel.ErrShipGates):
		return 5
	}
	return 1
}
