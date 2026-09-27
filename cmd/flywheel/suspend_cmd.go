package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

const (
	suspendUsageLine = "flywheel suspend --session S [--reason TEXT] [--until TIME] [--dir DIR]"
	resumeUsageLine  = "flywheel resume --session S [--note TEXT] [--dir DIR]"
)

func init() {
	register("suspend", "freeze the factory: every dispatch refuses until flywheel resume", runSuspend)
	registerHelp("suspend", suspendUsageLine, func() *flag.FlagSet { fs, _ := suspendFlags(); return fs })
	register("resume", "thaw a suspended factory", runResume)
	registerHelp("resume", resumeUsageLine, func() *flag.FlagSet { fs, _ := resumeFlags(); return fs })
}

// suspendOptions holds the parsed suspend and resume flags.
type suspendOptions struct {
	dir     string
	session string
	reason  string
	until   string
	note    string
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
	if err := flywheel.Suspend(o.dir, o.session, o.reason, until); err != nil {
		exitSuspend("suspend", err)
	}
	fmt.Println("factory suspended")
}

// runResume implements `flywheel resume`: it appends an unsuspended event.
// Exit 0 ok, 1 error, 2 usage, 6 refusal (not suspended).
func runResume(args []string) {
	fs, o := resumeFlags()
	suspendArgs("resume", resumeUsageLine, fs, o, args)
	if err := flywheel.Unsuspend(o.dir, o.session, o.note); err != nil {
		exitSuspend("resume", err)
	}
	fmt.Println("factory resumed")
}
