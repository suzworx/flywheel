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
	register("claim-edit", "declare a lead's own mid-wave edit so the owns check attributes it", runClaimEdit)
	registerHelp("claim-edit", "flywheel claim-edit --paths P1,P2 --session S [--note TEXT] [--dir DIR]", func() *flag.FlagSet { fs, _ := claimEditFlags(); return fs })
}

// claimEditOptions holds the parsed claim-edit flags.
type claimEditOptions struct {
	dir     string
	paths   string
	session string
	note    string
}

// claimEditFlags defines claim-edit's flags once, so help and run share them.
func claimEditFlags() (*flag.FlagSet, *claimEditOptions) {
	fs := flag.NewFlagSet("claim-edit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &claimEditOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.paths, "paths", "", "comma-separated repo-relative paths the lead edited")
	fs.StringVar(&o.session, "session", "", "the lead session that made the edits")
	fs.StringVar(&o.note, "note", "", "free-form note")
	return fs, o
}

// claimEditUsage prints the flywheel claim-edit usage line.
func claimEditUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel claim-edit --paths P1,P2 --session S [--note TEXT] [--dir DIR]")
}

// runClaimEdit implements `flywheel claim-edit`: it appends one lead_edit
// event declaring that the lead's own session edited the given paths after
// dispatch, so the owns check attributes them instead of refusing every unit
// dispatched before the edit.
func runClaimEdit(args []string) {
	fs, o := claimEditFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: %v\n", err)
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: unexpected argument %q\n", pos[0])
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	if o.session == "" {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: --session is required\n")
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	var owns []string
	for _, p := range strings.Split(o.paths, ",") {
		if p = strings.TrimSpace(p); p != "" {
			owns = append(owns, p)
		}
	}
	if len(owns) == 0 {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: --paths is required\n")
		claimEditUsage(os.Stderr)
		os.Exit(2)
	}
	if err := flywheel.AppendEvent(o.dir, flywheel.Event{Kind: "lead_edit", Session: o.session, Owns: owns, Note: o.note}); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: %v\n", err)
		os.Exit(1)
	}
	if _, err := flywheel.WriteState(o.dir); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel claim-edit: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("claimed lead edit: %s -> session %s\n", strings.Join(owns, ", "), o.session)
}
