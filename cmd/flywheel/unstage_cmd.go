package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("unstage", "remove a unit's staging copies once validate applied them", runUnstage)
	registerHelp("unstage", "flywheel unstage <task> [--workdir DIR] [--dir DIR]", func() *flag.FlagSet { fs, _ := unstageFlags(); return fs })
}

// unstageOptions holds the parsed unstage flags.
type unstageOptions struct {
	dir     string
	workdir string
}

// unstageFlags defines unstage's flags once, so help and run share them.
func unstageFlags() (*flag.FlagSet, *unstageOptions) {
	fs := flag.NewFlagSet("unstage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &unstageOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.workdir, "workdir", "", "the tree holding the staging copies (default: the task's recorded workdir)")
	return fs, o
}

// unstageUsage prints the flywheel unstage usage line.
func unstageUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel unstage <task> [--workdir DIR] [--dir DIR]")
}

// runUnstage implements `flywheel unstage` (issue #781): it removes the
// staging sources of the task's latest staged event, keeps their
// destinations, and records an unstaged event. Exit 6 on a rule refusal (the
// staged content changed since validate), 2 on usage, 1 otherwise.
func runUnstage(args []string) {
	fs, o := unstageFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel unstage: %v\n", err)
		unstageUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintln(os.Stderr, "flywheel unstage: exactly one task is required")
		unstageUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	files, err := flywheel.Unstage(o.dir, task, flywheel.UnstageOptions{Workdir: o.workdir})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel unstage: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	for _, f := range files {
		fmt.Printf("%s unstaged %s (%s kept)\n", task, f.From, f.To)
	}
}
