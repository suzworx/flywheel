package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("audit", "re-measure a unit in a clean copy and check its record, from an independent session", runAudit)
	registerHelp("audit", "flywheel audit <task> --session S [--note TEXT] [--workdir PATH] [--json] [--dir DIR]", func() *flag.FlagSet { fs, _ := auditFlags(); return fs })
}

// auditOptions holds the parsed audit flags.
type auditOptions struct {
	dir     string
	workdir string
	session string
	note    string
	json    bool
}

// auditFlags defines audit's flags once, so help and run share them.
func auditFlags() (*flag.FlagSet, *auditOptions) {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &auditOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.workdir, "workdir", "", "git working tree to audit")
	fs.StringVar(&o.session, "session", "", "auditor session, distinct from every worker session and inspector session")
	fs.StringVar(&o.note, "note", "", "optional audit note")
	fs.BoolVar(&o.json, "json", false, "JSON output")
	return fs, o
}

// auditUsage prints the flywheel audit usage line.
func auditUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel audit <task> --session S [--note TEXT] [--workdir PATH] [--json] [--dir DIR]")
}

// runAudit implements `flywheel audit <task>`: re-measure the task's declared
// gates in a clean copy of the tree, check its record with the verify rules,
// and record the audited verdict. Each poka-yoke refusal exits 6 with the rule
// id and the fix; a usage error exits 2; any other error exits 1; nonconformance
// exits 5.
func runAudit(args []string) {
	fs, o := auditFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel audit: %v\n", err)
		auditUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel audit: exactly one task id is required\n")
		auditUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	res, err := flywheel.AuditTask(o.dir, task, flywheel.AuditOptions{
		Dir: o.dir, Workdir: o.workdir, Session: o.session, Note: o.note,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel audit: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		os.Exit(1)
	}
	if o.json {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel audit: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s\n", data)
	} else {
		fmt.Printf("%s audited: %s\n", task, res.Verdict)
		for _, finding := range res.Findings {
			fmt.Printf("  - %s\n", finding)
		}
	}
	if res.Verdict == "nonconformance" {
		os.Exit(5)
	}
}
