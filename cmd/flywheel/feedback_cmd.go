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
	register("feedback", "curate signals into learnings\n    add       record a learning\n    dismiss   dismiss a learning by id", runFeedback)
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
	return fs, o
}

// feedbackUsageLines is the single source of truth for feedback's three
// subcommand usage lines.
func feedbackUsageLines() string {
	return strings.Join([]string{
		"flywheel feedback [--dir DIR]",
		"       flywheel feedback add --task ID --severity P0|P1|P2 --title T --observed O --evidence E --ask A [--signals a,b] [--dir DIR]",
		"       flywheel feedback dismiss L-NN --reason WHY [--dir DIR]",
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
