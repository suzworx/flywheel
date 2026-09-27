package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

const (
	suspendUsageLine = "flywheel suspend --session S [--reason TEXT] [--until TIME] [--stop] [--dir DIR]"
	resumeUsageLine  = "flywheel resume --session S [--note TEXT] [--no-redispatch] [--dir DIR]"
)

func init() {
	register("suspend", "freeze the factory: every dispatch refuses until flywheel resume; --stop also stops every live worker", runSuspend)
	registerHelp("suspend", suspendUsageLine, func() *flag.FlagSet { fs, _ := suspendFlags(); return fs })
	register("resume", "thaw a suspended factory and re-dispatch every unit a --stop suspension stopped", runResume)
	registerHelp("resume", resumeUsageLine, func() *flag.FlagSet { fs, _ := resumeFlags(); return fs })
}

// suspendOptions holds the parsed suspend and resume flags.
type suspendOptions struct {
	dir          string
	session      string
	reason       string
	until        string
	note         string
	stop         bool
	noRedispatch bool
}

// suspendFlags defines suspend's flags once, so help and run share them.
func suspendFlags() (*flag.FlagSet, *suspendOptions) {
	fs := flag.NewFlagSet("suspend", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &suspendOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.session, "session", "", "session id of whoever freezes the factory (required)")
	fs.StringVar(&o.reason, "reason", "", "why the factory is frozen")
	fs.StringVar(&o.until, "until", "", "thaw time: RFC3339, or HH:MM local (today, or tomorrow once past)")
	fs.BoolVar(&o.stop, "stop", false, "also stop every live worker at its next lease tick, checkpointed with its session kept (finished reason suspended); flywheel resume continues each")
	return fs, o
}

// resumeFlags defines resume's flags once, so help and run share them.
func resumeFlags() (*flag.FlagSet, *suspendOptions) {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &suspendOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.session, "session", "", "session id of whoever thaws the factory (required)")
	fs.StringVar(&o.note, "note", "", "free-form note")
	fs.BoolVar(&o.noRedispatch, "no-redispatch", false, "only thaw: do not re-dispatch the units a --stop suspension stopped")
	return fs, o
}

// parseUntil reads --until at now: an RFC3339 time, or HH:MM in now's zone,
// today when that is still ahead and tomorrow otherwise.
func parseUntil(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	hm, err := time.Parse("15:04", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("--until %q is neither RFC3339 nor HH:MM", s)
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

// suspendArgs parses args with fs and exits 2 on a usage error: a bad flag,
// a positional, or a missing --session.
func suspendArgs(name, usage string, fs *flag.FlagSet, o *suspendOptions, args []string) {
	pos, err := parseArgs(fs, args)
	switch {
	case err != nil:
	case len(pos) != 0:
		err = fmt.Errorf("unexpected argument %q", pos[0])
	case o.session == "":
		err = fmt.Errorf("--session is required")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel %s: %v\nusage: %s\n", name, err, usage)
		os.Exit(2)
	}
}

// exitSuspend reports err for command name: exit 6 on a rule refusal, 1 on
// any other error.
func exitSuspend(name string, err error) {
	fmt.Fprintf(os.Stderr, "flywheel %s: %v\n", name, err)
	if flywheel.IsRuleRefusal(err) {
		os.Exit(6)
	}
	os.Exit(1)
}

// runSuspend implements `flywheel suspend`: it appends a suspended event.
// Exit 0 ok, 1 error, 2 usage, 6 refusal (already suspended).
func runSuspend(args []string) {
	fs, o := suspendFlags()
	suspendArgs("suspend", suspendUsageLine, fs, o, args)
	var until time.Time
	if o.until != "" {
		t, err := parseUntil(o.until, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel suspend: %v\nusage: %s\n", err, suspendUsageLine)
			os.Exit(2)
		}
		until = t
	}
	if err := flywheel.SuspendWith(o.dir, o.session, flywheel.SuspendOptions{Reason: o.reason, Until: until, Stop: o.stop}); err != nil {
		exitSuspend("suspend", err)
	}
	if o.stop {
		fmt.Println("factory suspended; every live worker stops at its next lease tick")
		return
	}
	fmt.Println("factory suspended")
}

// runResume implements `flywheel resume`: it appends an unsuspended event
// and, unless --no-redispatch, re-dispatches every unit a --stop suspension
// stopped. Exit 0 ok, 1 error, 2 usage, 6 refusal (not suspended).
func runResume(args []string) {
	fs, o := resumeFlags()
	suspendArgs("resume", resumeUsageLine, fs, o, args)
	if err := resumeFactory(o, superviseStarter(o.dir, o.session, false), os.Stdout); err != nil {
		exitSuspend("resume", err)
	}
}

// resumeFactory thaws the factory and, unless o.noRedispatch, continues
// every unit a stopping suspension stopped through start (supervise's
// detached `flywheel run <task> --resume` starter; tests inject their own).
func resumeFactory(o *suspendOptions, start func(string) error, w io.Writer) error {
	units, err := flywheel.Unsuspend(o.dir, o.session, o.note)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "factory resumed")
	if o.noRedispatch {
		return nil
	}
	return redispatchSuspended(o.dir, units, start, w)
}

// redispatchSuspended continues each unit a stopping suspension stopped:
// it writes the unit's continue delta (.flywheel/briefs/<task>.delta.txt,
// the default prompt of flywheel run --resume) and starts `flywheel run
// <task> --resume` detached through start, printing one line per unit. A
// unit that fails is reported and the rest still start; the first error is
// returned.
func redispatchSuspended(dir string, units []flywheel.TaskAttempt, start func(string) error, w io.Writer) error {
	var first error
	for _, u := range units {
		_, err := flywheel.WriteSuspendDelta(dir, u.Task)
		if err == nil {
			err = start(u.Task)
		}
		if err != nil {
			fmt.Fprintf(w, "%s %s not resumed: %v\n", u.Task, u.Attempt, err)
			if first == nil {
				first = err
			}
			continue
		}
		fmt.Fprintf(w, "resumed %s %s (log %s)\n", u.Task, u.Attempt, filepath.ToSlash(autoResumeLog(u.Task)))
	}
	return first
}
