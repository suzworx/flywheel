package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

const retroUsageLine = "flywheel retro <task> --session S (--changed P1,P2 | --none) --note TEXT [--dir DIR]"

func init() {
	register("retro", "record the lead's retrospective on a landed unit: the agents, briefs or skills it changed, or none", runRetro)
	registerHelp("retro", retroUsageLine, func() *flag.FlagSet { fs, _ := retroFlags(); return fs })
}

// retroOptions holds the parsed retro flags.
type retroOptions struct {
	dir     string
	session string
	changed string
	none    bool
	note    string
}

// retroFlags defines retro's flags once, so help and run share them.
func retroFlags() (*flag.FlagSet, *retroOptions) {
	fs := flag.NewFlagSet("retro", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &retroOptions{}
	fs.StringVar(&o.dir, "dir", ".", "the repository whose ledger records the retro")
	fs.StringVar(&o.session, "session", "", "the lead session recorded on the retro; required")
	fs.StringVar(&o.changed, "changed", "", "comma-separated repo-relative paths of the agents, briefs or skills the retro changed")
	fs.BoolVar(&o.none, "none", false, "the retro changed nothing")
	fs.StringVar(&o.note, "note", "", "what the retro found; required")
	return fs, o
}

// runRetro implements `flywheel retro` (issue #854).
func runRetro(args []string) {
	os.Exit(retroMain(args, os.Stdout, os.Stderr))
}

// retroMain records one retro and returns the exit code: 0 recorded, 1 any
// error (a missing changed path, the record failed), 2 usage, 6 a task not
// landed (rule retro-not-landed).
func retroMain(args []string, stdout, stderr io.Writer) int {
	fs, o := retroFlags()
	pos, err := parseArgs(fs, args)
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "flywheel retro: %s\nusage: %s\n", msg, retroUsageLine)
		return 2
	}
	var changed []string
	for _, p := range strings.Split(o.changed, ",") {
		if p = strings.TrimSpace(p); p != "" {
			changed = append(changed, p)
		}
	}
	switch {
	case err != nil:
		return usage(err.Error())
	case len(pos) != 1:
		return usage("retro takes exactly one <task>")
	case (len(changed) > 0) == o.none:
		return usage("give exactly one of --changed and --none")
	case o.session == "":
		return usage("--session is required")
	case o.note == "":
		return usage("--note is required")
	}
	err = flywheel.Retro(o.dir, flywheel.RetroOptions{Task: pos[0], Session: o.session, Changed: changed, None: o.none, Note: o.note})
	if err != nil {
		fmt.Fprintf(stderr, "flywheel retro: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			return 6
		}
		return 1
	}
	if o.none {
		fmt.Fprintf(stdout, "retro recorded for %s: no changes\n", pos[0])
	} else {
		fmt.Fprintf(stdout, "retro recorded for %s: %s\n", pos[0], strings.Join(changed, ", "))
	}
	return 0
}
