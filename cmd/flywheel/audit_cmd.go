package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("audit", "re-measure a unit in a clean copy and check its record, from an independent session", runAudit)
	registerHelp("audit", auditUsageLine, func() *flag.FlagSet { fs, _ := auditFlags(); return fs })
}

// auditUsageLine is audit's usage: the unit forms and the release form
// (issue #420).
const auditUsageLine = "flywheel audit (<task> | --sample RATE | --first-article | --wave) --session S [--seed N] [--list] [--note TEXT] [--workdir PATH] [--json] [--dir DIR]\n" +
	"       flywheel audit --release VERSION --session S [--prev TAG] [--artifact ZIP --checksums FILE] [--notes FILE]... [--calibration FILE --min-recall F] [--json] [--dir DIR]"

// auditOptions holds the parsed audit flags.
type auditOptions struct {
	dir          string
	workdir      string
	session      string
	note         string
	json         bool
	sample       float64
	firstArticle bool
	wave         bool
	seed         int64
	list         bool
	// The release audit's flags (issue #420).
	release     string
	prev        string
	artifact    string
	checksums   string
	notes       repeatable
	calibration string
	minRecall   float64
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
	fs.Float64Var(&o.sample, "sample", -1, "audit a random sample of passed, unaudited units at this rate (0..1)")
	fs.BoolVar(&o.firstArticle, "first-article", false, "audit the first unit each worker adapter/model built")
	fs.BoolVar(&o.wave, "wave", false, "audit every passed, unaudited unit in the ledger (the wave)")
	fs.Int64Var(&o.seed, "seed", 0, "sample seed; 0 picks one from the clock and prints it")
	fs.BoolVar(&o.list, "list", false, "print the selection and exit without auditing")
	fs.StringVar(&o.release, "release", "", "audit the published release VERSION: changelog, binary, commands, docs, calibration")
	fs.StringVar(&o.prev, "prev", "", "previous release tag for --release; default the highest lower vA.B.C tag")
	fs.StringVar(&o.artifact, "artifact", "", "local release zip for --release instead of the download (needs --checksums)")
	fs.StringVar(&o.checksums, "checksums", "", "local checksums.txt for --artifact")
	fs.Var(&o.notes, "notes", "release-notes file whose flywheel code spans --release checks (repeatable)")
	fs.StringVar(&o.calibration, "calibration", "", "review calibrate report for --release --min-recall")
	fs.Float64Var(&o.minRecall, "min-recall", -1, "minimum calibration recall for --release; <0 skips the check")
	return fs, o
}

// auditUsage prints the flywheel audit usage line.
func auditUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: "+auditUsageLine)
}

// runAudit implements `flywheel audit`: re-measure the task's declared gates
// in a clean copy of the tree, check its record with the verify rules, and
// record the audited verdict. Can audit a single task or select multiple tasks
// by first article or sample. Each poka-yoke refusal exits 6 with the rule id
// and the fix; a usage error exits 2; any other error exits 1; nonconformance
// exits 5 (when at least one selected task is nonconformance).
func runAudit(args []string) {
	fs, o := auditFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel audit: %v\n", err)
		auditUsage(os.Stderr)
		os.Exit(2)
	}
	if o.release != "" {
		os.Exit(auditReleaseRun(o, pos, os.Stdout, os.Stderr))
	}

	// Count how many modes are set
	modes := 0
	if len(pos) > 0 {
		modes++
	}
	if o.sample >= 0 {
		modes++
	}
	if o.firstArticle {
		modes++
	}
	if o.wave {
		modes++
	}

	if modes != 1 {
		fmt.Fprintf(os.Stderr, "flywheel audit: exactly one of a task id, --sample, --first-article or --wave is required\n")
		auditUsage(os.Stderr)
		os.Exit(2)
	}

	// Single task mode
	if len(pos) > 0 {
		if len(pos) != 1 {
			fmt.Fprintf(os.Stderr, "flywheel audit: exactly one task id is required\n")
			auditUsage(os.Stderr)
			os.Exit(2)
		}
		if o.list || o.seed != 0 {
			fmt.Fprintf(os.Stderr, "flywheel audit: --list and --seed cannot be used with a task id\n")
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
		return
	}

	// Selection mode: --session is needed to audit, not to preview; --json
	// prints the selection, so it goes with --list.
	if o.session == "" && !o.list {
		fmt.Fprintf(os.Stderr, "flywheel audit: --session is required to audit a selection (or preview it with --list)\n")
		auditUsage(os.Stderr)
		os.Exit(2)
	}
	if o.json && !o.list {
		fmt.Fprintf(os.Stderr, "flywheel audit: --json with --sample or --first-article needs --list\n")
		auditUsage(os.Stderr)
		os.Exit(2)
	}

	// Read events
	events, err := flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel audit: %v\n", err)
		os.Exit(1)
	}

	var selection flywheel.AuditSelection
	if o.firstArticle {
		selection = flywheel.SelectFirstArticles(events)
	} else if o.wave {
		selection = flywheel.SelectWave(events)
	} else {
		// --sample mode: validate rate
		if o.sample > 1 {
			fmt.Fprintf(os.Stderr, "flywheel audit: sample rate must be in range 0..1\n")
			auditUsage(os.Stderr)
			os.Exit(2)
		}
		if o.seed == 0 {
			o.seed = time.Now().UnixNano()
		}
		selection = flywheel.SelectSample(events, o.sample, o.seed)
	}

	if o.list && o.json {
		data, err := json.MarshalIndent(selection, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel audit: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s\n", data)
		os.Exit(0)
	}

	// Print selection summary
	if selection.Mode == "sample" {
		fmt.Printf("audit selection: sample rate=%.2f seed=%d -> %d unit(s):", selection.Rate, selection.Seed, len(selection.Tasks))
	} else if selection.Mode == "wave" {
		fmt.Printf("audit selection: wave -> %d unit(s):", len(selection.Tasks))
	} else {
		fmt.Printf("audit selection: first-article -> %d unit(s):", len(selection.Tasks))
	}
	for _, t := range selection.Tasks {
		fmt.Printf(" %s", t)
	}
	fmt.Printf("\n")

	if o.list {
		os.Exit(0)
	}

	// Audit each selected task
	anyNonconformance := false
	for _, task := range selection.Tasks {
		res, err := flywheel.AuditTask(o.dir, task, flywheel.AuditOptions{
			Dir: o.dir, Workdir: o.workdir, Session: o.session, Note: o.note,
			RequireCandidate: true,
		})
		if err != nil {
			if flywheel.IsRuleRefusal(err) {
				fmt.Printf("%s skipped: %v\n", task, err)
				continue
			}
			fmt.Fprintf(os.Stderr, "flywheel audit: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s audited: %s\n", task, res.Verdict)
		for _, finding := range res.Findings {
			fmt.Printf("  - %s\n", finding)
		}
		if res.Verdict == "nonconformance" {
			anyNonconformance = true
		}
	}

	if anyNonconformance {
		os.Exit(5)
	}
}

// auditReleaseMain parses args and runs the release audit, returning the
// exit code (the test entry point).
func auditReleaseMain(args []string, stdout, stderr io.Writer) int {
	fs, o := auditFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(stderr, "flywheel audit: %v\n", err)
		auditUsage(stderr)
		return 2
	}
	return auditReleaseRun(o, pos, stdout, stderr)
}

// auditReleaseRun implements `flywheel audit --release` (issue #420): exit 0
// on pass, 5 on fail (a nonconformance), 8 on inconclusive, 2 on a usage
// error, 6 on a rule refusal and 1 on any other error.
func auditReleaseRun(o *auditOptions, pos []string, stdout, stderr io.Writer) int {
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "flywheel audit: %s\n", msg)
		auditUsage(stderr)
		return 2
	}
	switch {
	case len(pos) > 0 || o.sample >= 0 || o.firstArticle || o.wave || o.list:
		return usage("--release cannot be used with a task id, --sample, --first-article, --wave or --list")
	case o.session == "":
		return usage("--session is required to audit a release")
	case (o.artifact == "") != (o.checksums == ""):
		return usage("--artifact and --checksums go together")
	case o.minRecall >= 0 && o.calibration == "":
		return usage("--min-recall needs --calibration")
	}
	res, err := flywheel.AuditRelease(o.dir, flywheel.ReleaseAuditOptions{
		Version: o.release, Prev: o.prev, Session: o.session, Artifact: o.artifact, Checksums: o.checksums,
		Notes: o.notes, Calibration: o.calibration, MinRecall: o.minRecall,
	})
	if err != nil {
		fmt.Fprintf(stderr, "flywheel audit: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			return 6
		}
		return 1
	}
	if o.json {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "flywheel audit: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", data)
	} else {
		for _, c := range res.Checks {
			fmt.Fprintf(stdout, "%s  %s  %s\n", c.Name, c.Status, c.Detail)
		}
		fmt.Fprintf(stdout, "release %s: %s\n", res.Tag, res.Verdict)
	}
	switch res.Verdict {
	case "fail":
		return 5
	case "inconclusive":
		return 8
	}
	return 0
}
