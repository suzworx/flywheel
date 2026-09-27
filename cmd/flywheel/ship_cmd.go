package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// shipUsageLine is ship's usage, shared by help and the usage error.
const shipUsageLine = "flywheel ship <task> [--integration BRANCH] [--workdir PATH] [--remote NAME] [--message TEXT] [--title TEXT] [--body-file PATH] [--no-merge] [--ci-timeout DUR] [--poll DUR] [--ignore-check NAME]... [--requeue N] [--no-signature] [--repo OWNER/REPO] [--dir DIR]"

func init() {
	register("ship", "take a passed unit to landed: commit its leftovers, merge the integration branch into fw/<task>, re-run its gates, push, open or reuse the PR, wait for CI, squash merge, record the landing and close the issue", runShip)
	registerHelp("ship", shipUsageLine, func() *flag.FlagSet { fs, _ := shipFlags(); return fs })
}

// shipOptions holds the parsed ship flags.
type shipOptions struct {
	dir          string
	integration  string
	workdir      string
	remote       string
	message      string
	title        string
	bodyFile     string
	noMerge      bool
	ciTimeout    time.Duration
	poll         time.Duration
	ignoreChecks repeatable
	repo         string
	requeue      int
	noSignature  bool
}

// shipFlags defines ship's flags once, so help and run share them.
func shipFlags() (*flag.FlagSet, *shipOptions) {
	fs := flag.NewFlagSet("ship", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &shipOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.integration, "integration", "", "the branch to merge in and open the PR against (default integration.branch, else main)")
	fs.StringVar(&o.workdir, "workdir", "", "the task worktree (default .flywheel/worktrees/<task>, with fw/<task> checked out)")
	fs.StringVar(&o.remote, "remote", "origin", "the remote to fetch the integration branch from and push fw/<task> to")
	fs.StringVar(&o.message, "message", "", `the commit message for leftover owned changes (default "<task> ship")`)
	fs.StringVar(&o.title, "title", "", `the PR title (default the brief's "# TASK:" text, else the task id)`)
	fs.StringVar(&o.bodyFile, "body-file", "", "a file holding the PR body (default a generated summary: the task, its gates, the ship steps and Fixes #N for its issue)")
	fs.BoolVar(&o.noMerge, "no-merge", false, "stop after ci: leave merge, landed and closed unrun")
	fs.DurationVar(&o.ciTimeout, "ci-timeout", 45*time.Minute, "how long ci waits for the PR's checks")
	fs.DurationVar(&o.poll, "poll", 30*time.Second, "how often ci reads the PR's checks")
	fs.Var(&o.ignoreChecks, "ignore-check", "a check name ci disregards (repeatable)")
	fs.StringVar(&o.repo, "repo", "", "OWNER/REPO for gh (default gh's own repository resolution)")
	fs.IntVar(&o.requeue, "requeue", 0, "times to re-merge and re-run CI when the integration branch moves before merge (default 2, 0 never)")
	fs.BoolVar(&o.noSignature, "no-signature", false, "leave flywheel's signature (the Shipped-by: trailer and the PR footer) out of this run; config ship.signature false turns it off always")
	return fs, o
}

// shipRequeue maps --requeue to ShipOptions.Requeue, where 0 is the default
// 2: the flag absent stays 0, an explicit 0 (or less) is never, -1.
func shipRequeue(fs *flag.FlagSet, n int) int {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "requeue" })
	if set && n <= 0 {
		return -1
	}
	return n
}

// shipUsage prints the usage line and the exit codes.
func shipUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+shipUsageLine)
	fmt.Fprintln(w, "exit: 0 ok, 1 error, 5 gates or CI failed or the integration branch kept moving, 6 rule refusal")
}

// runShip implements `flywheel ship <task>` (issue #457).
func runShip(args []string) {
	os.Exit(shipMain(args, os.Stdout, os.Stderr))
}

// shipMain runs the ship steps and returns the exit code: 0 every step ok or
// skip, 1 an error (git, fetch, a merge conflict, gh, a merge not MERGED), 2
// usage, 5 the gates failed on the merged tree, CI failed or the integration
// branch still moved after the last requeue, 6 a rule refusal.
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
	body := ""
	if o.bodyFile != "" {
		b, err := os.ReadFile(o.bodyFile)
		if err != nil {
			fmt.Fprintf(stderr, "flywheel ship: --body-file: %v\n", err)
			return 2
		}
		body = string(b)
	}
	_, err = flywheel.Ship(o.dir, pos[0], flywheel.ShipOptions{
		Integration: o.integration, Workdir: o.workdir, Message: o.message, Remote: o.remote, Progress: stdout,
		Repo: o.repo, Title: o.title, Body: body, NoMerge: o.noMerge, CITimeout: o.ciTimeout, Poll: o.poll,
		IgnoreChecks: o.ignoreChecks, Requeue: shipRequeue(fs, o.requeue), Version: version, NoSignature: o.noSignature,
	})
	if err == nil {
		return 0
	}
	fmt.Fprintf(stderr, "flywheel ship: %v\n", err)
	var rr *flywheel.RuleRefusal
	switch {
	case errors.As(err, &rr):
		return 6
	case errors.Is(err, flywheel.ErrShipGates), errors.Is(err, flywheel.ErrShipCI), errors.Is(err, flywheel.ErrShipStale):
		return 5
	}
	return 1
}
