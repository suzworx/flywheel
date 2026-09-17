package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("goal", "manage goals on the factory floor", runGoal)
	registerHelp("goal", goalUsageLines(), func() *flag.FlagSet { fs, _ := goalFlags(); return fs })
}

// repeatable is a flag.Value collecting repeated string flags.
type repeatable []string

func (r *repeatable) String() string { return strings.Join(*r, ",") }
func (r *repeatable) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// goalOptions holds the parsed goal flags.
type goalOptions struct {
	dir     string
	id      string
	status  string
	note    string
	asJSON  bool
	accept  repeatable
	require repeatable
}

// goalFlags defines goal's flags once, so help and run share them.
func goalFlags() (*flag.FlagSet, *goalOptions) {
	fs := flag.NewFlagSet("goal", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &goalOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.id, "id", "", "goal id (defaults to goal-<n>)")
	fs.StringVar(&o.status, "status", "", "set the status (met, failed, or abandoned)")
	fs.StringVar(&o.note, "note", "", "free-form note")
	fs.BoolVar(&o.asJSON, "json", false, "print the goals as JSON")
	fs.Var(&o.accept, "accept", "acceptance gate command (repeatable)")
	fs.Var(&o.require, "require", "required task id (repeatable)")
	return fs, o
}

// goalUsageLines is the single source of truth for the four goal subcommand
// usage lines (without the leading "usage: ", which both goalUsage and the
// help registration add themselves), so `flywheel goal <bad args>` and
// `flywheel help goal` show identical text.
func goalUsageLines() string {
	return strings.Join([]string{
		"flywheel goal add <title> [--id ID] [--accept CMD]... [--require TASK]... [--dir DIR]",
		"       flywheel goal list [--json] [--dir DIR]",
		"       flywheel goal show <id> [--json] [--dir DIR]",
		"       flywheel goal set <id> --status met|failed|abandoned [--note TEXT] [--dir DIR]",
	}, "\n")
}

// goalUsage prints the flywheel goal usage lines to w.
func goalUsage(w io.Writer) {
	fmt.Fprintf(w, "usage: %s\n", goalUsageLines())
}

// runGoal dispatches to the goal subcommands.
func runGoal(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "flywheel goal: a subcommand is required\n")
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "add":
		runGoalAdd(args[1:])
	case "list":
		runGoalList(args[1:])
	case "show":
		runGoalShow(args[1:])
	case "set":
		runGoalSet(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "flywheel goal: unknown subcommand %q\n", args[0])
		goalUsage(os.Stderr)
		os.Exit(2)
	}
}

// runGoalAdd appends a goal event with status active. The id defaults to
// goal-<n> for the next goal; an existing id is refused with exit 6.
func runGoalAdd(args []string) {
	fs, o := goalFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal add: %v\n", err)
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel goal add: exactly one title is required\n")
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	gs := knownGoals(o.dir)
	id := o.id
	if id == "" {
		id = fmt.Sprintf("goal-%d", len(gs)+1)
	}
	for _, g := range gs {
		if g.ID == id {
			fmt.Fprintf(os.Stderr, "flywheel goal add: id %q already exists\n", id)
			os.Exit(6)
		}
	}
	if err := flywheel.AppendEvent(o.dir, flywheel.Event{
		Kind: "goal",
		Goal: &flywheel.GoalSpec{
			ID: id, Title: pos[0], Acceptance: o.accept, Required: o.require, Status: "active",
		},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal add: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s  %s\n", id, pos[0])
}

// runGoalList prints every goal, sorted by id.
func runGoalList(args []string) {
	fs, o := goalFlags()
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal list: %v\n", err)
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "flywheel goal list: unexpected argument %q\n", fs.Arg(0))
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	gs := knownGoals(o.dir)
	if o.asJSON {
		printJSON(gs)
		return
	}
	for _, g := range gs {
		fmt.Printf("%s  %s  %s\n", g.ID, g.Title, g.Progress)
	}
}

// runGoalShow prints one goal's derived view.
func runGoalShow(args []string) {
	fs, o := goalFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal show: %v\n", err)
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel goal show: exactly one goal id is required\n")
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	g, ok := findGoal(o.dir, pos[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "flywheel goal show: unknown goal %q\n", pos[0])
		os.Exit(1)
	}
	if o.asJSON {
		printJSON([]flywheel.GoalView{g})
		return
	}
	fmt.Printf("%s  %s\n", g.ID, g.Title)
	fmt.Printf("status: %s\n", g.Status)
	fmt.Printf("%s\n", g.Progress)
	fmt.Printf("accepted %d  in-flight %d  not-started %d  blocked %d  total %d\n",
		g.Accepted, g.InFlight, g.NotStarted, g.Blocked, g.Total)
	if len(g.Required) > 0 {
		fmt.Printf("required: %s\n", strings.Join(g.Required, ", "))
	}
	if len(g.Acceptance) > 0 {
		fmt.Printf("acceptance: %s\n", strings.Join(g.Acceptance, ", "))
	}
}

// runGoalSet appends an updated goal event carrying the goal's spec forward.
func runGoalSet(args []string) {
	fs, o := goalFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal set: %v\n", err)
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel goal set: exactly one goal id is required\n")
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	if o.status != "met" && o.status != "failed" && o.status != "abandoned" {
		fmt.Fprintf(os.Stderr, "flywheel goal set: --status must be met, failed, or abandoned\n")
		goalUsage(os.Stderr)
		os.Exit(2)
	}
	g, ok := findGoal(o.dir, pos[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "flywheel goal set: unknown goal %q\n", pos[0])
		os.Exit(1)
	}
	if err := flywheel.AppendEvent(o.dir, flywheel.Event{
		Kind: "goal", Note: o.note,
		Goal: &flywheel.GoalSpec{
			ID: g.ID, Title: g.Title, Acceptance: g.Acceptance, Required: g.Required, Status: o.status,
		},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal set: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s  %s\n", g.ID, o.status)
}

// knownGoals returns the currently recorded goals, sorted by id.
func knownGoals(dir string) []flywheel.GoalView {
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal: %v\n", err)
		os.Exit(1)
	}
	return flywheel.Goals(events)
}

// findGoal returns the goal view for id, or ok=false when unknown.
func findGoal(dir, id string) (flywheel.GoalView, bool) {
	for _, g := range knownGoals(dir) {
		if g.ID == id {
			return g, true
		}
	}
	return flywheel.GoalView{}, false
}

// printJSON prints v as indented JSON.
func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel goal: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
}
