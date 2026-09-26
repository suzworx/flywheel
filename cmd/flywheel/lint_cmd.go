package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("lint", "check a brief file for problems\n    each line is labelled problem: or warning:, then a count line\n    exit 1 on any problem; warnings alone exit 0", runLint)
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
// problem as `lint: <brief>: problem: <message>`, then every warning as
// `lint: <brief>: warning: <message>`, then a count line, all to stderr.
// Exit codes: 1 any problem or an unreadable brief, 0 otherwise (warnings
// alone exit 0), 2 usage (no brief argument).
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
	writeLint(os.Stderr, brief, res)
	if len(res.Problems) > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}

// writeLint prints res for brief: each problem labelled `problem:`, then each
// warning labelled `warning:`, then always a count line
// `lint: <brief>: N problem(s), M warning(s)`.
func writeLint(w io.Writer, brief string, res flywheel.LintResult) {
	for _, p := range res.Problems {
		fmt.Fprintf(w, "lint: %s: problem: %s\n", brief, p)
	}
	for _, m := range res.Warnings {
		fmt.Fprintf(w, "lint: %s: warning: %s\n", brief, m)
	}
	fmt.Fprintf(w, "lint: %s: %s, %s\n", brief,
		plural(len(res.Problems), "problem"), plural(len(res.Warnings), "warning"))
}

// plural renders n with noun, adding "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
