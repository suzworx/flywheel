package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// scheduleUsageLine is schedule's usage line, shared by help and errors.
const scheduleUsageLine = "flywheel schedule install [--every D] [--dir DIR] [--fleet [--notify CMD]] | status [--dir DIR] [--fleet] | remove [--dir DIR] [--fleet]"

func init() {
	register("schedule", "keep the controller waking with an OS scheduled task\n    install            run `flywheel controller --once` every --every (default 15m);\n                       --fleet: a separate per-user task running `flywheel fleet watch --once` (default 5m)\n    status             say whether the task is installed (--fleet: the fleet task)\n    remove             delete the task (--fleet: the fleet task)", runSchedule)
	registerHelp("schedule", scheduleUsageLine, func() *flag.FlagSet { fs, _ := scheduleFlags(true); return fs })
}

// scheduleOptions holds the parsed schedule flags.
type scheduleOptions struct {
	dir    string
	every  time.Duration
	fleet  bool
	notify string
}

// scheduleFlags defines schedule's flags once, so help and run share them;
// only install takes --every and --notify.
func scheduleFlags(install bool) (*flag.FlagSet, *scheduleOptions) {
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &scheduleOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.fleet, "fleet", false, "the per-user fleet watcher task instead of the repository's controller task")
	if install {
		fs.DurationVar(&o.every, "every", flywheel.DefaultScheduleEvery, "how often the task runs; at least 1m (--fleet default 5m)")
		fs.StringVar(&o.notify, "notify", "", "with --fleet: the command fleet watch runs per item")
	}
	return fs, o
}

// runSchedule implements `flywheel schedule <install|status|remove>`.
func runSchedule(args []string) {
	os.Exit(scheduleMain(args, os.Stdout, os.Stderr, flywheel.NewScheduler, flywheel.NewSchedulePlan, flywheel.NewFleetSchedulePlan))
}

// scheduleMain runs schedule against the scheduler newSched builds, with
// plan building the repository task and fleetPlan the fleet watcher task
// (every 0 meaning its default); exit 0, 1 on error, 2 on usage.
func scheduleMain(args []string, stdout, stderr io.Writer, newSched func(flywheel.Runner) (flywheel.Scheduler, error),
	plan func(string, time.Duration) (flywheel.SchedulePlan, error),
	fleetPlan func(time.Duration, string) (flywheel.SchedulePlan, error)) int {
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "flywheel schedule: "+format+"\n", a...)
		fmt.Fprintln(stderr, "usage: "+scheduleUsageLine)
		return 2
	}
	if len(args) == 0 {
		return usage("missing subcommand (install, status or remove)")
	}
	sub := args[0]
	if sub != "install" && sub != "status" && sub != "remove" {
		return usage("unknown subcommand %q", sub)
	}
	fs, o := scheduleFlags(sub == "install")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return usage("%s: %v", sub, err)
	}
	if len(pos) != 0 {
		return usage("%s: unexpected argument %q", sub, pos[0])
	}
	if o.every < time.Minute && sub == "install" {
		return usage("install: --every must be at least 1m, got %v", o.every)
	}
	if o.notify != "" && !o.fleet {
		return usage("install: --notify needs --fleet")
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "flywheel schedule %s: %v\n", sub, err)
		return 1
	}
	var p flywheel.SchedulePlan
	if o.fleet {
		every := time.Duration(0) // the fleet default unless --every was given
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "every" {
				every = o.every
			}
		})
		p, err = fleetPlan(every, o.notify)
	} else {
		p, err = plan(o.dir, o.every)
	}
	if err != nil {
		return fail(err)
	}
	s, err := newSched(flywheel.ExecRunner)
	if err != nil {
		return fail(err)
	}
	switch sub {
	case "install":
		if err := s.Install(p); err != nil {
			return fail(err)
		}
		words := append([]string{p.Exe}, p.Args...)
		for i, w := range words {
			if strings.ContainsAny(w, " \t") {
				words[i] = fmt.Sprintf("%q", w)
			}
		}
		fmt.Fprintf(stdout, "installed %s: every %v runs %s\n", p.Name, p.Every, strings.Join(words, " "))
	case "remove":
		if err := s.Remove(p.Name); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "removed %s\n", p.Name)
	default:
		ok, detail, err := s.Status(p.Name)
		if err != nil {
			return fail(err)
		}
		state := "not installed"
		if ok {
			state = "installed"
		}
		fmt.Fprintf(stdout, "%s: %s\n", p.Name, state)
		if detail != "" {
			fmt.Fprintln(stdout, detail)
		}
	}
	return 0
}
