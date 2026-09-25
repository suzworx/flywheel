package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

const checkpointUsageLine = "flywheel checkpoint list [<task>] | diff|restore|drop <task> [<attempt>] [--force] [--dir DIR]"

func init() {
	register("checkpoint", "list, diff, restore or drop the snapshots of interrupted attempts", runCheckpoint)
	registerHelp("checkpoint", checkpointUsageLine, func() *flag.FlagSet { fs, _ := checkpointFlags(); return fs })
}

// checkpointOptions holds the parsed checkpoint flags.
type checkpointOptions struct {
	dir   string
	force bool
}

// checkpointFlags defines checkpoint's flags once, so help and run share them.
func checkpointFlags() (*flag.FlagSet, *checkpointOptions) {
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &checkpointOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.force, "force", false, "restore over uncommitted changes to the checkpoint's paths")
	return fs, o
}

// runCheckpoint implements `flywheel checkpoint` (issue #422).
func runCheckpoint(args []string) {
	os.Exit(checkpointMain(args, os.Stdout, os.Stderr))
}

// checkpointMain runs one checkpoint subcommand and returns the exit code: 0
// done, 1 any error or refusal, 2 usage. The attempt defaults to the task's
// latest checkpoint.
func checkpointMain(args []string, stdout, stderr io.Writer) int {
	fs, o := checkpointFlags()
	pos, err := parseArgs(fs, args)
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "flywheel checkpoint: %s\nusage: %s\n", msg, checkpointUsageLine)
		return 2
	}
	if err != nil {
		return usage(err.Error())
	}
	if len(pos) == 0 {
		return usage("a subcommand is required")
	}
	sub, rest := pos[0], pos[1:]
	if len(rest) > 2 || sub != "list" && len(rest) == 0 {
		return usage(sub + " takes <task> [<attempt>]")
	}
	task, attempt := "", ""
	if len(rest) > 0 {
		task = rest[0]
	}
	if len(rest) > 1 {
		attempt = rest[1]
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "flywheel checkpoint %s: %v\n", sub, err)
		return 1
	}
	switch sub {
	case "list":
		cps, err := flywheel.ListCheckpoints(o.dir, task)
		if err != nil {
			return fail(err)
		}
		for _, cp := range cps {
			fmt.Fprintf(stdout, "%s %s %s %s\n", cp.Task, cp.Attempt, cp.SHA, strings.Join(cp.Paths, ","))
		}
	case "diff":
		d, err := flywheel.DiffCheckpoint(o.dir, task, attempt)
		if err != nil {
			return fail(err)
		}
		if d != "" {
			fmt.Fprintln(stdout, d)
		}
	case "restore":
		paths, err := flywheel.RestoreCheckpoint(o.dir, task, attempt, o.force)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "restored %s\n", strings.Join(paths, ", "))
	case "drop":
		if err := flywheel.DropCheckpoint(o.dir, task, attempt); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "dropped the checkpoint of %s %s\n", task, attempt)
	default:
		return usage("unknown subcommand " + sub)
	}
	return 0
}
