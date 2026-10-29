package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// worktreeUsageLine is worktree's usage: remove (issue #851) and commit
// (issue #897).
const worktreeUsageLine = "flywheel worktree remove <path> [--discard --session S] [--note TEXT] [--dir DIR]\n" +
	"       flywheel worktree commit <task> --session S [--note TEXT] [--dir DIR]"

func init() {
	register("worktree", "remove a lead's linked worktree, refusing to drop uncommitted edits off the record; or recommit a task worktree's attempt against its amended owns", runWorktree)
	registerHelp("worktree", worktreeUsageLine, func() *flag.FlagSet { fs, _ := worktreeFlags(); return fs })
}

// worktreeOptions holds the parsed worktree flags.
type worktreeOptions struct {
	dir     string
	discard bool
	session string
	note    string
}

// worktreeFlags defines worktree's flags once, so help and run share them.
func worktreeFlags() (*flag.FlagSet, *worktreeOptions) {
	fs := flag.NewFlagSet("worktree", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &worktreeOptions{}
	fs.StringVar(&o.dir, "dir", ".", "the repository whose linked worktree is removed or whose task worktree is committed")
	fs.BoolVar(&o.discard, "discard", false, "remove: drop a dirty tree's edits, recorded first as a worktree_discarded event")
	fs.StringVar(&o.session, "session", "", "the lead session recorded on the discard or the recommit; required with --discard and with commit")
	fs.StringVar(&o.note, "note", "", "why the edits are discarded, or why the attempt is recommitted")
	return fs, o
}

// runWorktree implements `flywheel worktree` (issue #851).
func runWorktree(args []string) {
	os.Exit(worktreeMain(args, os.Stdout, os.Stderr))
}

// worktreeMain runs one worktree subcommand and returns the exit code: 0
// removed or recommitted, 1 any error (not a linked worktree, not a task
// worktree, no task in the log, the record failed), 2 usage, 6 a dirty tree
// without --discard (rule worktree-dirty).
func worktreeMain(args []string, stdout, stderr io.Writer) int {
	fs, o := worktreeFlags()
	pos, err := parseArgs(fs, args)
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "flywheel worktree: %s\nusage: %s\n", msg, worktreeUsageLine)
		return 2
	}
	switch {
	case err != nil:
		return usage(err.Error())
	case len(pos) == 0 || pos[0] != "remove" && pos[0] != "commit":
		return usage("the subcommand is remove or commit")
	case pos[0] == "commit":
		return worktreeCommit(pos, o, stdout, stderr, usage)
	case len(pos) != 2:
		return usage("remove takes exactly one <path>")
	case o.discard && o.session == "":
		return usage("--discard needs --session")
	}
	res, err := flywheel.WorktreeRemove(o.dir, pos[1], flywheel.WorktreeRemoveOptions{
		Discard: o.discard, Session: o.session, Note: o.note,
	})
	if err != nil {
		fmt.Fprintf(stderr, "flywheel worktree remove: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			return 6
		}
		return 1
	}
	if len(res.Discarded) > 0 {
		fmt.Fprintf(stdout, "discarded %d path(s); removed %s\n", len(res.Discarded), res.Path)
	} else {
		fmt.Fprintf(stdout, "removed %s\n", res.Path)
	}
	return 0
}

// worktreeCommit runs `flywheel worktree commit <task>` (issue #897): the
// attempt commit re-run against the task's current owns, recorded as a
// recommitted event.
func worktreeCommit(pos []string, o *worktreeOptions, stdout, stderr io.Writer, usage func(string) int) int {
	switch {
	case len(pos) != 2:
		return usage("commit takes exactly one <task>")
	case o.discard:
		return usage("--discard is remove's flag")
	case o.session == "":
		return usage("commit needs --session")
	}
	task := pos[1]
	res, err := flywheel.WorktreeCommit(o.dir, task, flywheel.WorktreeCommitOptions{Session: o.session, Note: o.note})
	if err != nil {
		fmt.Fprintf(stderr, "flywheel worktree commit: %v\n", err)
		return 1
	}
	if res.Commit != "" {
		fmt.Fprintf(stdout, "recommitted %s %s: %s\n", task, res.Attempt, res.Commit)
	} else {
		fmt.Fprintf(stdout, "recommitted %s %s: nothing to commit\n", task, res.Attempt)
	}
	if len(res.Uncommitted) > 0 {
		fmt.Fprintf(stdout, "still uncommitted (outside owns): %s\n", strings.Join(res.Uncommitted, ", "))
	}
	return 0
}
