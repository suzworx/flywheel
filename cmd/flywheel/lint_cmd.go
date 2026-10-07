package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("lint", "check a brief file for problems\n    each line is labelled problem: or warning:, then a count line\n    --probe also runs each gate: once on the base tree (a failing gate warns, one that cannot start is a problem;\n      a full-suite gate failing while CI is green on that commit is a problem, rule host-dependent-gate, lint.probe_ci)\n    --task ID with --probe records each probe in the ledger, so validate and explain can tell a broken gate from broken work\n    exit 1 on any problem; warnings alone exit 0", runLint)
	registerHelp("lint", "flywheel lint <brief> [--probe [--task ID]] [--dir DIR]", func() *flag.FlagSet { fs, _ := lintFlags(); return fs })
}

// lintOptions holds the parsed lint flags.
type lintOptions struct {
	dir   string
	probe bool
	task  string
}

// lintFlags defines lint's flags once, so help and run share them.
func lintFlags() (*flag.FlagSet, *lintOptions) {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &lintOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory (default: the nearest ancestor holding .flywheel/, else the git top level, else .)")
	fs.BoolVar(&o.probe, "probe", false, "run each gate: once in the target directory after a clean lint")
	fs.StringVar(&o.task, "task", "", "with --probe, record each probe as a gate_probed event for this task")
	return fs, o
}

// sameAbs reports whether a and b are the same path once made absolute and
// cleaned.
func sameAbs(a, b string) bool {
	aa, errA := filepath.Abs(a)
	ab, errB := filepath.Abs(b)
	return errA == nil && errB == nil && aa == ab
}

// lintUsage prints the flywheel lint usage line.
func lintUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel lint <brief> [--probe [--task ID]] [--dir DIR]")
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
	if o.task != "" && !o.probe {
		fmt.Fprintf(os.Stderr, "flywheel lint: --task records gate probes and needs --probe\n")
		lintUsage(os.Stderr)
		os.Exit(2)
	}
	if o.task != "" && !flywheel.TaskIDOK(o.task) {
		fmt.Fprintf(os.Stderr, "flywheel lint: task %q does not match ^[A-Za-z0-9._-]+$\n", o.task)
		lintUsage(os.Stderr)
		os.Exit(2)
	}
	// Without --dir, owns and probes resolve from the flywheel root, so lint
	// works from any subdirectory (issue #808).
	dirSet := false
	fs.Visit(func(f *flag.Flag) { dirSet = dirSet || f.Name == "dir" })
	if !dirSet {
		o.dir = flywheel.LintDir(".")
		if !sameAbs(o.dir, ".") {
			fmt.Fprintf(os.Stderr, "lint: resolving paths against %s (the flywheel root); pass --dir to override\n", o.dir)
		}
	}
	brief := pos[0]
	res, err := flywheel.LintBrief(o.dir, brief)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel lint: %v\n", err)
		os.Exit(1)
	}
	if o.probe && len(res.Problems) == 0 {
		header, err := flywheel.ParseBriefHeader(brief)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel lint: %v\n", err)
			os.Exit(1)
		}
		probes := flywheel.ProbeGatesEnv(o.dir, header.Gates, header.NeedsEnv)
		probeLint(os.Stderr, brief, probes, &res)
		cfg, _, err := flywheel.LoadConfig(o.dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel lint: %v\n", err)
			os.Exit(1)
		}
		if p := flywheel.RedFirstLintProblem(header.Kind, cfg.LintRedFirst(), probes); p != "" {
			res.Problems = append(res.Problems, p)
		}
		probs, warns := flywheel.HostDependentGateFindings(o.dir, cfg.Lint, header.Owns, header.Gates, probes,
			flywheel.HeadCommit(o.dir), cfg.LintProbeCI(), flywheel.GhIn(o.dir))
		res.Problems = append(res.Problems, probs...)
		res.Warnings = append(res.Warnings, warns...)
		if o.task != "" {
			if err := flywheel.RecordGateProbes(o.dir, o.task, header.Gates, probes); err != nil {
				fmt.Fprintf(os.Stderr, "flywheel lint: %v\n", err)
				os.Exit(1)
			}
		}
	}
	writeLint(os.Stderr, brief, res)
	if len(res.Problems) > 0 {
		os.Exit(1)
	}
	os.Exit(0)
}

// probeLint prints one line per gate probe for brief and adds a warning for
// each gate that fails on the base tree and a problem for each gate that
// cannot start (issue #544).
func probeLint(w io.Writer, brief string, probes []flywheel.GateProbe, res *flywheel.LintResult) {
	for _, p := range probes {
		switch {
		case p.CannotStart:
			detail := p.FirstLine
			if p.Err != nil {
				detail = p.Err.Error()
			}
			fmt.Fprintf(w, "lint: %s: gate %d: error: %s\n", brief, p.N, detail)
			res.Problems = append(res.Problems, fmt.Sprintf("gate %d cannot start (exit %d): %s", p.N, p.RC, detail))
		case p.RC != 0:
			fmt.Fprintf(w, "lint: %s: gate %d: fail (exit %d): %s\n", brief, p.N, p.RC, p.FirstLine)
			res.Warnings = append(res.Warnings, fmt.Sprintf("gate %d fails on the base tree (exit %d): %s; "+
				"a test-first gate is expected to fail, a wrong runner or path is not", p.N, p.RC, p.FirstLine))
		default:
			fmt.Fprintf(w, "lint: %s: gate %d: pass (%dms)\n", brief, p.N, p.DurMS)
		}
	}
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
