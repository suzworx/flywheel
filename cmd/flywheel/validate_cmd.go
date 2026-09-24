package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("validate", "run a task's gates and check owns", runValidate)
	registerHelp("validate", "flywheel validate <task> [--dir DIR] [--workdir PATH] [--carry PATH]... [--live]", func() *flag.FlagSet { fs, _ := validateFlags(); return fs })
}

// validateOptions holds the parsed validate flags.
type validateOptions struct {
	dir     string
	workdir string
	carry   repeatable
	live    bool
}

// validateFlags defines validate's flags once, so help and run share them.
func validateFlags() (*flag.FlagSet, *validateOptions) {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &validateOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.workdir, "workdir", "", "git working tree the gates run in")
	fs.Var(&o.carry, "carry", "repo-relative path to copy from dir into workdir before gates run (repeatable)")
	fs.BoolVar(&o.live, "live", false, "also run the brief's declared live-gate: lines (the lead's verification pass)")
	return fs, o
}

// validateUsage prints the flywheel validate usage line.
func validateUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel validate <task> [--dir DIR] [--workdir PATH] [--carry PATH]... [--live]")
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
	res, err := flywheel.ValidateTask(o.dir, task, flywheel.ValidateOptions{Dir: o.dir, Workdir: o.workdir, Carry: o.carry, Live: o.live})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel validate: %v\n", err)
		os.Exit(1)
	}
	if res.Refused != "" {
		fmt.Printf("validate: %s\n", res.Refused)
		os.Exit(5)
	}
	if len(res.BriefPaths) > 1 {
		fmt.Printf("validate: brief %s + delta %s\n", res.BriefPaths[0], res.BriefPaths[1])
	} else if len(res.BriefPaths) == 1 {
		fmt.Printf("validate: brief %s\n", res.BriefPaths[0])
	}
	for _, g := range res.Gates {
		if g.Live {
			continue
		}
		if g.HostBlocked {
			fmt.Printf("%s gate %s: %s\n", task, g.Gate, g.Note)
		} else if g.Inconclusive {
			fmt.Printf("%s gate %s: inconclusive (%s)\n", task, g.Gate, g.Note)
		} else if g.RC == 0 {
			fmt.Printf("%s gate %s: pass (%dms)\n", task, g.Gate, g.DurationMS)
		} else {
			fmt.Printf("%s gate %s: failed (rc=%d)\n", task, g.Gate, g.RC)
		}
	}
	for _, g := range res.Gates {
		if !g.Live {
			continue
		}
		n := strings.TrimPrefix(g.Gate, "live")
		if g.HostBlocked {
			fmt.Printf("%s live-gate %s: %s\n", task, n, g.Note)
		} else if g.Inconclusive {
			fmt.Printf("%s live-gate %s: inconclusive (%s)\n", task, n, g.Note)
		} else if g.RC == 0 {
			fmt.Printf("%s live-gate %s: pass (%dms)\n", task, n, g.DurationMS)
		} else {
			fmt.Printf("%s live-gate %s: failed (rc=%d)\n", task, n, g.RC)
		}
	}
	if res.LiveDeclared > 0 && !res.LiveRun {
		fmt.Printf("%s live-gate: %d declared, not run (--live)\n", task, res.LiveDeclared)
	}
	for _, f := range res.Files {
		line := fmt.Sprintf("%s file %s: %d lines", task, f.Path, f.Lines)
		if strings.HasSuffix(strings.ToLower(f.Path), ".md") && f.Headings > 0 {
			line += fmt.Sprintf(", %d headings", f.Headings)
		}
		fmt.Println(line)
	}
	if len(res.Attributed) > 0 {
		fmt.Printf("%s owns: attributed %s\n", task, strings.Join(res.Attributed, ", "))
	}
	if len(res.Outside) == 0 {
		fmt.Printf("%s owns: ok\n", task)
	} else {
		fmt.Printf("%s owns: outside %s\n", task, strings.Join(res.Outside, ", "))
		// A sibling entry reads "<worktree path>: <path>"; another session's
		// edit there can be claimed from this ledger (issue #362).
		for _, o := range res.Outside {
			if wt, p, ok := strings.Cut(o, ": "); ok {
				fmt.Printf("%s owns: hint: a path in a sibling worktree changed after dispatch; if another session made it, claim it: flywheel claim-edit --worktree %s --paths %s --session <your session>\n", task, wt, p)
				break
			}
		}
	}
	if n := len(res.Ignored); n > 0 {
		shown := strings.Join(res.Ignored, ", ")
		if n > 10 {
			shown = strings.Join(res.Ignored[:10], ", ") + fmt.Sprintf(", ... (+%d more)", n-10)
		}
		fmt.Printf("%s owns: warning: %d owned path(s) are git-ignored and will never be committed: %s\n", task, n, shown)
	}
	if res.OK() {
		os.Exit(0)
	}
	os.Exit(5)
}
