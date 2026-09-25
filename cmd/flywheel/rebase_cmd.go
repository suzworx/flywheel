package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("rebase", "move a unit's branch off a base that landed as a squash, onto main", runRebase)
	registerHelp("rebase", "flywheel rebase <task> [--onto REF] [--dir DIR]", func() *flag.FlagSet { fs, _ := rebaseFlags(); return fs })
}

// rebaseOptions holds the parsed rebase flags.
type rebaseOptions struct {
	dir  string
	onto string
}

// rebaseFlags defines rebase's flags once, so help and run share them.
func rebaseFlags() (*flag.FlagSet, *rebaseOptions) {
	fs := flag.NewFlagSet("rebase", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &rebaseOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.onto, "onto", "", "the ref to rebase onto (default main, else master)")
	return fs, o
}

// rebaseUsage prints the flywheel rebase usage line.
func rebaseUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel rebase <task> [--onto REF] [--dir DIR]")
}

// runRebase implements `flywheel rebase <task>` (issue #414).
func runRebase(args []string) {
	os.Exit(rebaseMain(args, os.Stdout, os.Stderr))
}

// rebaseMain rebases the unit's branch fw/<task> in its task worktree off
// its recorded base onto --onto and returns the exit code: 0 rebased, 1
// conflicts (the rebase is aborted and the paths listed) or any other error,
// 2 usage.
func rebaseMain(args []string, stdout, stderr io.Writer) int {
	fs, o := rebaseFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "flywheel rebase: %v\n", err)
		rebaseUsage(stderr)
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "flywheel rebase: exactly one task id is required\n")
		rebaseUsage(stderr)
		return 2
	}
	task := pos[0]
	newBase, conflicts, err := flywheel.RebaseUnit(o.dir, task, o.onto)
	if err != nil {
		fmt.Fprintf(stderr, "flywheel rebase: %v\n", err)
		return 1
	}
	if len(conflicts) > 0 {
		fmt.Fprintf(stderr, "flywheel rebase: %s conflicts in %d path(s); the rebase was aborted and fw/%s is unchanged:\n", task, len(conflicts), task)
		for _, p := range conflicts {
			fmt.Fprintf(stderr, "  %s\n", p)
		}
		return 1
	}
	fmt.Fprintf(stdout, "%s rebased onto %s\n", task, newBase)
	return 0
}
