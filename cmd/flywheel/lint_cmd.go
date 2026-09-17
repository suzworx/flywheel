package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("lint", "check a brief file for problems", runLint)
	registerHelp("lint", "flywheel lint <brief> [--dir DIR]", func() *flag.FlagSet { fs, _ := lintFlags(); return fs })
}

// lintOptions holds the parsed lint flags.
type lintOptions struct {
	dir string
}

// lintFlags defines lint's flags once, so help and run share them.
func lintFlags() (*flag.FlagSet, *lintOptions) {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &lintOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	return fs, o
}

// lintUsage prints the flywheel lint usage line.
func lintUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel lint <brief> [--dir DIR]")
}

// runLint implements `flywheel lint <brief>`: read one brief and print every
// problem and warning to stderr as `lint: <brief>: <message>`. Exit codes:
// 0 no problems (warnings alone stay 0), 1 any problem or an unreadable
// brief, 2 usage (no brief argument).
func runLint(args []string) {
	fs, o := lintFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel lint: %v\n", err)
		lintUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel lint: exactly one brief is required\n")
		lintUsage(os.Stderr)
		os.Exit(2)
	}
	brief := pos[0]
	res, err := flywheel.LintBrief(o.dir, brief)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel lint: %v\n", err)
		os.Exit(1)
	}
	for _, p := range res.Problems {
		fmt.Fprintf(os.Stderr, "lint: %s: %s\n", brief, p)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "lint: %s: %s\n", brief, w)
	}
	if len(res.Problems) > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}
