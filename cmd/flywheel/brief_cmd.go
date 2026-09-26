package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

const briefUsageLine = "flywheel brief <task> --from-issue N [--repo OWNER/REPO] --owns a,b [--needs t1,t2] [--gate CMD]... [--kind K] [--force] [--no-plan] [--dir DIR]"

func init() {
	register("brief", "write a linted brief from a tracker issue and record it planned", runBrief)
	registerHelp("brief", briefUsageLine, func() *flag.FlagSet { fs, _ := briefFlags(); return fs })
}

// gateList is a repeatable string flag: each --gate adds one gate line.
type gateList []string

func (g *gateList) String() string { return strings.Join(*g, "; ") }

func (g *gateList) Set(s string) error {
	*g = append(*g, s)
	return nil
}

// briefOptions holds the parsed brief flags.
type briefOptions struct {
	dir       string
	fromIssue int
	repo      string
	owns      string
	needs     string
	gates     gateList
	kind      string
	force     bool
	noPlan    bool
}

// briefFlags defines brief's flags once, so help and run share them.
func briefFlags() (*flag.FlagSet, *briefOptions) {
	fs := flag.NewFlagSet("brief", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &briefOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.IntVar(&o.fromIssue, "from-issue", 0, "the tracker issue number the brief is written from (required)")
	fs.StringVar(&o.repo, "repo", "", "the issue's OWNER/REPO (default gh's repository for --dir)")
	fs.StringVar(&o.owns, "owns", "", "comma-separated owned paths, annotated (new) when the unit creates them (required)")
	fs.StringVar(&o.needs, "needs", "", "comma-separated task ids the unit needs (default none)")
	fs.Var(&o.gates, "gate", "a gate command, repeatable (default the Go gate when --dir has go.mod)")
	fs.StringVar(&o.kind, "kind", "", "the brief's kind: line (default none)")
	fs.BoolVar(&o.force, "force", false, "replace an existing brief")
	fs.BoolVar(&o.noPlan, "no-plan", false, "write the brief only; do not record planned")
	return fs, o
}

// splitCSV splits a comma-separated flag value into trimmed, non-empty entries.
func splitCSV(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// runBrief implements `flywheel brief <task> --from-issue N` (issue #457).
func runBrief(args []string) {
	os.Exit(briefMain(args, os.Stdout, os.Stderr))
}

// briefMain fetches issue --from-issue with gh, writes and lints the brief,
// prints its path and, unless --no-plan, records task planned with the issue.
// It returns 0 ok, 1 error (gh, write, lint, record), 2 usage.
func briefMain(args []string, stdout, stderr io.Writer) int {
	fs, o := briefFlags()
	pos, err := parseArgs(fs, args)
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "flywheel brief: %s\nusage: %s\n", msg, briefUsageLine)
		return 2
	}
	switch {
	case err != nil:
		return usage(err.Error())
	case len(pos) != 1:
		return usage("exactly one task id is required")
	case o.fromIssue <= 0:
		return usage("--from-issue N is required, N >= 1")
	case strings.TrimSpace(o.owns) == "":
		return usage("owns is required: pass --owns")
	}
	task := pos[0]
	iss, err := flywheel.GhTracker{Repo: o.repo}.Issue(o.fromIssue)
	if err != nil {
		fmt.Fprintf(stderr, "flywheel brief: %v\n", err)
		return 1
	}
	if iss.Number == 0 {
		iss.Number = o.fromIssue
	}
	rel, err := flywheel.BriefFromIssue(o.dir, task, iss, flywheel.BriefIssueOptions{
		Owns: splitCSV(o.owns), Needs: splitCSV(o.needs), Gates: o.gates, Kind: o.kind, Force: o.force,
	})
	if err != nil {
		fmt.Fprintf(stderr, "flywheel brief: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, rel)
	if o.noPlan {
		return 0
	}
	// The same warnings flywheel log --kind planned prints (issues #366, #476, #479).
	var warns []string
	for _, w := range []string{replanWarning(o.dir, task), branchWarning(o.dir, task)} {
		if w != "" {
			warns = append(warns, w)
		}
	}
	drift := flywheel.PlanDriftWarning(o.dir, task, rel)
	if err := flywheel.RecordPlannedBy(o.dir, task, rel, flywheel.PlanMeta{Issue: o.fromIssue}); err != nil {
		fmt.Fprintf(stderr, "flywheel brief: %v\n", err)
		return 1
	}
	if drift != "" {
		fmt.Fprintf(stderr, "flywheel brief: warning: %s\n", drift)
	}
	for _, w := range warns {
		fmt.Fprintf(stderr, "flywheel brief: %s\n", w)
	}
	if _, err := flywheel.WriteState(o.dir); err != nil {
		fmt.Fprintf(stderr, "flywheel brief: %v\n", err)
		return 1
	}
	return 0
}
