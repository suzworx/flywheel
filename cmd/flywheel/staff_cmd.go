package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("staff", "register a factory role on the floor", runStaff)
	registerHelp("staff", "flywheel staff --role lead --session S [--model M] [--note N] [--dir DIR]", func() *flag.FlagSet { fs, _ := staffFlags(); return fs })
}

// staffOptions holds the parsed staff flags.
type staffOptions struct {
	dir     string
	role    string
	session string
	model   string
	note    string
}

// staffFlags defines staff's flags once, so help and run share them.
func staffFlags() (*flag.FlagSet, *staffOptions) {
	fs := flag.NewFlagSet("staff", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &staffOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.role, "role", "", "factory role (lead, foreman, ...)")
	fs.StringVar(&o.session, "session", "", "session id of the role holder")
	fs.StringVar(&o.model, "model", "", "model name")
	fs.StringVar(&o.note, "note", "", "free-form note")
	return fs, o
}

// staffUsage prints the flywheel staff usage line.
func staffUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel staff --role lead --session S [--model M] [--note N] [--dir DIR]")
}

// runStaff implements `flywheel staff`: it appends a staffed event, derives
// state, and prints `<role> <session>`. Exit 2 on a usage error, 1 on any
// other error.
func runStaff(args []string) {
	fs, o := staffFlags()
	pos, perr := parseArgs(fs, args)
	if perr != nil {
		fmt.Fprintf(os.Stderr, "flywheel staff: %v\n", perr)
		staffUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel staff: unexpected argument %q\n", pos[0])
		staffUsage(os.Stderr)
		os.Exit(2)
	}
	if o.role == "" || o.session == "" {
		fmt.Fprintf(os.Stderr, "flywheel staff: --role and --session are required\n")
		staffUsage(os.Stderr)
		os.Exit(2)
	}
	if err := flywheel.AppendEvent(o.dir, flywheel.Event{
		Kind: "staffed", Session: o.session, Persona: o.role, Model: o.model, Note: o.note,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel staff: %v\n", err)
		os.Exit(1)
	}
	if _, err := flywheel.WriteState(o.dir); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel staff: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s %s\n", o.role, o.session)
}
