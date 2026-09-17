package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"flywheel/internal/flywheel"
)

func init() {
	register("feedback", "curate signals into learnings\n    add       record a learning\n    dismiss   dismiss a learning by id\n    export    write a sanitised report of undismissed learnings\n    submit    send the report upstream (consent-first)", runFeedback)
	registerHelp("feedback", feedbackUsageLines(), func() *flag.FlagSet { fs, _ := feedbackFlags(); return fs })
}

// feedbackSeverities is the set of severities feedback add accepts.
var feedbackSeverities = map[string]bool{"P0": true, "P1": true, "P2": true}

// feedbackOptions holds the parsed feedback flags.
type feedbackOptions struct {
	dir      string
	task     string
	severity string
	title    string
	observed string
	evidence string
	ask      string
	signals  string
	reason   string
	out      string
	yes      bool
}

// feedbackFlags defines feedback's flags once, so help and run share them.
func feedbackFlags() (*flag.FlagSet, *feedbackOptions) {
	fs := flag.NewFlagSet("feedback", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &feedbackOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.task, "task", "", "task id the learning was observed on")
	fs.StringVar(&o.severity, "severity", "", "severity (P0, P1, or P2)")
	fs.StringVar(&o.title, "title", "", "short learning title")
	fs.StringVar(&o.observed, "observed", "", "what was observed")
	fs.StringVar(&o.evidence, "evidence", "", "evidence path")
	fs.StringVar(&o.ask, "ask", "", "the ask")
	fs.StringVar(&o.signals, "signals", "", "comma-separated signal names")
	fs.StringVar(&o.reason, "reason", "", "dismissal reason")
	fs.StringVar(&o.out, "out", "", "write the report to this file instead of stdout")
	fs.BoolVar(&o.yes, "yes", false, "send the report upstream without showing it first")
	return fs, o
}

// feedbackUsageLines is the single source of truth for feedback's subcommand
// usage lines.
func feedbackUsageLines() string {
	return strings.Join([]string{
		"flywheel feedback [--dir DIR]",
		"       flywheel feedback add --task ID --severity P0|P1|P2 --title T --observed O --evidence E --ask A [--signals a,b] [--dir DIR]",
		"       flywheel feedback dismiss L-NN --reason WHY [--dir DIR]",
		"       flywheel feedback export [--out PATH] [--dir DIR]",
		"       flywheel feedback submit [--yes] [--dir DIR]",
	}, "\n")
}

// feedbackUsage prints the flywheel feedback usage lines to w.
func feedbackUsage(w io.Writer) {
	fmt.Fprintf(w, "usage: %s\n", feedbackUsageLines())
}

// runFeedback dispatches to the feedback subcommands, defaulting to list.
func runFeedback(args []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "add":
			runFeedbackAdd(args[1:])
			return
		case "dismiss":
			runFeedbackDismiss(args[1:])
			return
		case "export":
			runFeedbackExport(args[1:])
			return
		case "submit":
			runFeedbackSubmit(args[1:])
			return
		default:
			fmt.Fprintf(os.Stderr, "flywheel feedback: unknown subcommand %q\n", args[0])
			feedbackUsage(os.Stderr)
			os.Exit(2)
		}
	}
	runFeedbackList(args)
}

// runFeedbackList prints one line per learning in log order, then the
// untriaged-signals line.
func runFeedbackList(args []string) {
	fs, o := feedbackFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback: %v\n", err)
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel feedback: unexpected argument %q\n", pos[0])
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	events, err := flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback: %v\n", err)
		os.Exit(1)
	}
	for _, l := range flywheel.Learnings(events) {
		line := fmt.Sprintf("%s %s %s", l.ID, l.Severity, l.Title)
		if l.Dismissed {
			line += fmt.Sprintf(" (dismissed: %s)", l.Reason)
		}
		fmt.Println(line)
	}
	fmt.Println("untriaged signals: none")
}

// runFeedbackAdd appends a learning event and rewrites learnings.md.
func runFeedbackAdd(args []string) {
	fs, o := feedbackFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback add: %v\n", err)
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel feedback add: unexpected argument %q\n", pos[0])
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if o.task == "" || o.title == "" || o.observed == "" || o.evidence == "" || o.ask == "" {
		fmt.Fprintf(os.Stderr, "flywheel feedback add: --task, --title, --observed, --evidence, and --ask are required\n")
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if !feedbackSeverities[o.severity] {
		fmt.Fprintf(os.Stderr, "flywheel feedback add: --severity must be P0, P1, or P2\n")
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	var signals []string
	if o.signals != "" {
		for _, s := range strings.Split(o.signals, ",") {
			signals = append(signals, strings.TrimSpace(s))
		}
	}
	if err := flywheel.AppendEvent(o.dir, flywheel.Event{
		Task: o.task, Kind: "learning", Severity: o.severity, Title: o.title,
		Observed: o.observed, Evidence: o.evidence, Ask: o.ask, Signals: signals,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback add: %v\n", err)
		os.Exit(1)
	}
	events, err := flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback add: %v\n", err)
		os.Exit(1)
	}
	views := flywheel.Learnings(events)
	if err := flywheel.WriteLearningsFile(o.dir, views); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback add: %v\n", err)
		os.Exit(1)
	}
	last := views[len(views)-1]
	fmt.Printf("%s %s\n", last.ID, last.Title)
}

// runFeedbackDismiss appends a dismissed event for an existing learning id
// and rewrites learnings.md.
func runFeedbackDismiss(args []string) {
	fs, o := feedbackFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: %v\n", err)
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: exactly one learning id is required\n")
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if o.reason == "" {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: --reason is required\n")
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	id := pos[0]
	events, err := flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: %v\n", err)
		os.Exit(1)
	}
	views := flywheel.Learnings(events)
	var task string
	found := false
	for _, l := range views {
		if l.ID == id {
			task = l.Task
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: unknown learning %q\n", id)
		os.Exit(1)
	}
	if err := flywheel.AppendEvent(o.dir, flywheel.Event{
		Task: task, Kind: "dismissed", ID: id, Note: o.reason,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: %v\n", err)
		os.Exit(1)
	}
	events, err = flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: %v\n", err)
		os.Exit(1)
	}
	if err := flywheel.WriteLearningsFile(o.dir, flywheel.Learnings(events)); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback dismiss: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s dismissed\n", id)
}

// runFeedbackExport prints a Markdown report of every undismissed learning
// with its free-text fields sanitised; with --out it writes the file instead
// and prints its path.
func runFeedbackExport(args []string) {
	fs, o := feedbackFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback export: %v\n", err)
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel feedback export: unexpected argument %q\n", pos[0])
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	events, err := flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback export: %v\n", err)
		os.Exit(1)
	}
	report := flywheel.FeedbackReport(version, flywheel.Learnings(events))
	if o.out == "" {
		fmt.Print(report)
		return
	}
	if err := flywheel.WriteFeedbackReport(o.out, report); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback export: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(o.out)
}

// runFeedbackSubmit sends the sanitised report upstream. Consent is not
// optional: without --yes it prints the exact text it would send and refuses
// (exit 2) without sending anything. feedback.submit "never" is a RuleRefusal
// (exit 6); an unset feedback.upstream is a usage error (exit 2). When gh is
// missing or fails, the report is parked in the offline outbox and the command
// still exits 0.
func runFeedbackSubmit(args []string) {
	fs, o := feedbackFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback submit: %v\n", err)
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel feedback submit: unexpected argument %q\n", pos[0])
		feedbackUsage(os.Stderr)
		os.Exit(2)
	}
	events, err := flywheel.ReadEvents(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback submit: %v\n", err)
		os.Exit(1)
	}
	cfg, _, err := flywheel.LoadConfig(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback submit: %v\n", err)
		os.Exit(1)
	}
	res, err := flywheel.FeedbackSubmit(o.dir, cfg, version, flywheel.Learnings(events), flywheel.FeedbackSubmitOptions{Yes: o.yes})
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel feedback submit: %v\n", err)
		if flywheel.IsRuleRefusal(err) {
			os.Exit(6)
		}
		if errors.Is(err, flywheel.ErrFeedbackNoUpstream) {
			feedbackUsage(os.Stderr)
			os.Exit(2)
		}
		os.Exit(1)
	}
	if res.NotSent {
		fmt.Print(res.Report)
		fmt.Fprintf(os.Stderr, "flywheel feedback submit: not sent; re-run with --yes to send it\n")
		os.Exit(2)
	}
	if res.Sent {
		fmt.Printf("submitted to %s\n", cfg.Feedback.Upstream)
		return
	}
	fmt.Printf("gh failed; report parked at %s\n", res.Outbox)
}
