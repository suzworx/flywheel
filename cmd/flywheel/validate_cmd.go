package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"flywheel/internal/flywheel"
)

func init() {
	register("validate", "run a task's gates and check owns", runValidate)
	registerHelp("validate", "flywheel validate <task> [--dir DIR] [--workdir PATH]", func() *flag.FlagSet { fs, _ := validateFlags(); return fs })
}

// validateOptions holds the parsed validate flags.
type validateOptions struct {
	dir     string
	workdir string
}

// validateFlags defines validate's flags once, so help and run share them.
func validateFlags() (*flag.FlagSet, *validateOptions) {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &validateOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.workdir, "workdir", "", "git working tree the gates run in")
	return fs, o
}

// validateUsage prints the flywheel validate usage line.
func validateUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel validate <task> [--dir DIR] [--workdir PATH]")
}

// runValidate implements `flywheel validate <task>`: run the task's declared
// gates on the exact tree and check that every changed path sits inside owns.
// Exit codes: 0 all gates pass and nothing is outside owns, 5 otherwise, 2
// usage, 1 any other error.
func runValidate(args []string) {
	fs, o := validateFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel validate: %v\n", err)
		validateUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel validate: exactly one task id is required\n")
		validateUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	res, err := flywheel.ValidateTask(o.dir, task, flywheel.ValidateOptions{Dir: o.dir, Workdir: o.workdir})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel validate: %v\n", err)
		os.Exit(1)
	}
	if len(res.BriefPaths) > 1 {
		fmt.Printf("validate: brief %s + delta %s\n", res.BriefPaths[0], res.BriefPaths[1])
	} else if len(res.BriefPaths) == 1 {
		fmt.Printf("validate: brief %s\n", res.BriefPaths[0])
	}
	for _, g := range res.Gates {
		if g.HostBlocked {
			fmt.Printf("%s gate %s: %s\n", task, g.Gate, g.Note)
		} else if g.RC == 0 {
			fmt.Printf("%s gate %s: pass (%dms)\n", task, g.Gate, g.DurationMS)
		} else {
			fmt.Printf("%s gate %s: failed (rc=%d)\n", task, g.Gate, g.RC)
		}
	}
	if len(res.Outside) == 0 {
		fmt.Printf("%s owns: ok\n", task)
	} else {
		fmt.Printf("%s owns: outside %s\n", task, strings.Join(res.Outside, ", "))
	}
	if res.OK() {
		os.Exit(0)
	}
	os.Exit(5)
}
