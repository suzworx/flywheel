package main

import (
	"flag"
	"io"
	"strings"
	"testing"
)

// parseArgsOptions mirrors a minimal two-flag command for the parseArgs tests.
type parseArgsOptions struct {
	dir    string
	resume bool
}

// TestParseArgsInterleavesFlagsAndPositionals is a table test over parseArgs:
// flags may appear before, between or after positionals, --flag=value works,
// and a literal -- ends flag parsing so the rest is positional.
func TestParseArgsInterleavesFlagsAndPositionals(t *testing.T) {
	cases := []struct {
		args   []string
		pos    []string
		dir    string
		resume bool
	}{
		{[]string{"T1", "--dir", "X"}, []string{"T1"}, "X", false},
		{[]string{"--dir", "X", "T1"}, []string{"T1"}, "X", false},
		{[]string{"--dir", "X", "T1", "--resume"}, []string{"T1"}, "X", true},
		{[]string{"T1", "--resume", "--dir", "X"}, []string{"T1"}, "X", true},
		{[]string{"--dir=X", "T1"}, []string{"T1"}, "X", false},
		{[]string{"T1", "--", "--literal"}, []string{"T1", "--literal"}, ".", false},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("parseArgs", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		o := &parseArgsOptions{}
		fs.StringVar(&o.dir, "dir", ".", "target directory")
		fs.BoolVar(&o.resume, "resume", false, "resume the task's last session")
		pos, err := parseArgs(fs, c.args)
		if err != nil {
			t.Errorf("parseArgs(%v): unexpected error: %v", c.args, err)
			continue
		}
		if len(pos) != len(c.pos) {
			t.Errorf("parseArgs(%v) positionals = %v, want %v", c.args, pos, c.pos)
			continue
		}
		for i := 0; i < len(pos); i++ {
			if pos[i] != c.pos[i] {
				t.Errorf("parseArgs(%v) positionals = %v, want %v", c.args, pos, c.pos)
				break
			}
		}
		if o.dir != c.dir {
			t.Errorf("parseArgs(%v) dir = %q, want %q", c.args, o.dir, c.dir)
		}
		if o.resume != c.resume {
			t.Errorf("parseArgs(%v) resume = %v, want %v", c.args, o.resume, c.resume)
		}
	}
}

// TestHelpTextAllCommands ranges over the commands map itself: every
// registered command must have registerHelp, its help must start with the
// "flywheel <name>" usage, and the help must name every flag its FlagSet
// defines.
func TestHelpTextAllCommands(t *testing.T) {
	for name := range commands {
		c := commands[name]
		if c.usage == "" {
			t.Errorf("%s: registered without registerHelp", name)
			continue
		}
		h := helpText(name)
		wantPrefix := "usage: flywheel " + name
		if !strings.HasPrefix(h, wantPrefix) {
			t.Errorf("%s: helpText = %q, want prefix %q", name, h, wantPrefix)
		}
		if c.flags == nil {
			continue
		}
		c.flags().VisitAll(func(f *flag.Flag) {
			if !strings.Contains(h, f.Name) {
				t.Errorf("%s: helpText missing flag %q\n%s", name, f.Name, h)
			}
		})
	}
}

// TestLogHelpNamesBothVerdictSets checks the log --verdict description names
// each verdict set with its event kind, so a lead knows inspected and
// reviewed take different verdicts.
func TestLogHelpNamesBothVerdictSets(t *testing.T) {
	h := helpText("log")
	for _, want := range []string{
		"inspected verdict (pass, rework, scrap, or escalate)",
		"reviewed verdict (pass, correct, or reject)",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("helpText(log) missing %q\n%s", want, h)
		}
	}
}

// TestGoalHelpShowsSubcommandArguments checks `flywheel help goal` names the
// positional argument each subcommand takes, not just the bare subcommand
// names (issue #127).
func TestGoalHelpShowsSubcommandArguments(t *testing.T) {
	h := helpText("goal")
	for _, want := range []string{"goal add <title>", "goal show <id>"} {
		if !strings.Contains(h, want) {
			t.Errorf("helpText(goal) missing %q\n%s", want, h)
		}
	}
}

// TestBareAction checks the bare `flywheel` decision: open the factory view
// when ./.flywheel exists, print the global help otherwise.
func TestBareAction(t *testing.T) {
	if got := bareAction(true); got != "factory" {
		t.Errorf("bareAction(true) = %q, want %q", got, "factory")
	}
	if got := bareAction(false); got != "help" {
		t.Errorf("bareAction(false) = %q, want %q", got, "help")
	}
}

// TestHasHelpFlag checks the help detector finds -h/--help/-help and stops at
// a literal "--".
func TestHasHelpFlag(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"-h"}, true},
		{[]string{"--help"}, true},
		{[]string{"-help"}, true},
		{[]string{"--task", "foo", "-h"}, true},
		{[]string{"-h", "--"}, true},
		{[]string{"--", "-h"}, false},
		{[]string{"--", "--help"}, false},
		{[]string{"x", "y"}, false},
		{[]string{}, false},
	}
	for _, c := range cases {
		if got := hasHelpFlag(c.args); got != c.want {
			t.Errorf("hasHelpFlag(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestGlobalHelpEndsWithHint checks the global usage points at help for flags.
func TestGlobalHelpEndsWithHint(t *testing.T) {
	g := globalHelp()
	if !strings.HasSuffix(strings.TrimRight(g, "\n"), "Run 'flywheel help <command>' for its flags.") {
		t.Errorf("globalHelp does not end with the help hint:\n%s", g)
	}
}

// TestRunFlagsInterleaved checks parseArgs plus runFlags give the same task
// and flag values whether the task comes first, last or between the flags.
func TestRunFlagsInterleaved(t *testing.T) {
	forms := []struct{ args []string }{
		{[]string{"T1", "--dir", "X", "--resume"}},
		{[]string{"--dir", "X", "--resume", "T1"}},
		{[]string{"--dir", "X", "T1", "--resume"}},
	}
	for _, f := range forms {
		fs, o := runFlags()
		pos, err := parseArgs(fs, f.args)
		if err != nil {
			t.Errorf("runFlags parseArgs(%v): unexpected error: %v", f.args, err)
			continue
		}
		if len(pos) != 1 || pos[0] != "T1" {
			t.Errorf("runFlags parseArgs(%v) positionals = %v, want [T1]", f.args, pos)
		}
		if o.dir != "X" {
			t.Errorf("runFlags parseArgs(%v) dir = %q, want %q", f.args, o.dir, "X")
		}
		if !o.resume {
			t.Errorf("runFlags parseArgs(%v) resume = %v, want true", f.args, o.resume)
		}
	}
}

// TestValidateFlagsInterleaved checks parseArgs plus validateFlags give the
// same task and flag values in the task-first, task-last and interleaved
// forms.
func TestValidateFlagsInterleaved(t *testing.T) {
	forms := []struct{ args []string }{
		{[]string{"T1", "--dir", "X", "--workdir", "W"}},
		{[]string{"--dir", "X", "--workdir", "W", "T1"}},
		{[]string{"--dir", "X", "T1", "--workdir", "W"}},
	}
	for _, f := range forms {
		fs, o := validateFlags()
		pos, err := parseArgs(fs, f.args)
		if err != nil {
			t.Errorf("validateFlags parseArgs(%v): unexpected error: %v", f.args, err)
			continue
		}
		if len(pos) != 1 || pos[0] != "T1" {
			t.Errorf("validateFlags parseArgs(%v) positionals = %v, want [T1]", f.args, pos)
		}
		if o.dir != "X" {
			t.Errorf("validateFlags parseArgs(%v) dir = %q, want %q", f.args, o.dir, "X")
		}
		if o.workdir != "W" {
			t.Errorf("validateFlags parseArgs(%v) workdir = %q, want %q", f.args, o.workdir, "W")
		}
	}
}

// TestInspectFlagsInterleaved checks parseArgs plus inspectFlags give the
// same task and flag values in the task-first, task-last and interleaved
// forms.
func TestInspectFlagsInterleaved(t *testing.T) {
	forms := []struct{ args []string }{
		{[]string{"T1", "--dir", "X", "--verdict", "pass", "--session", "S"}},
		{[]string{"--dir", "X", "--verdict", "pass", "--session", "S", "T1"}},
		{[]string{"--dir", "X", "T1", "--verdict", "pass", "--session", "S"}},
	}
	for _, f := range forms {
		fs, o := inspectFlags()
		pos, err := parseArgs(fs, f.args)
		if err != nil {
			t.Errorf("inspectFlags parseArgs(%v): unexpected error: %v", f.args, err)
			continue
		}
		if len(pos) != 1 || pos[0] != "T1" {
			t.Errorf("inspectFlags parseArgs(%v) positionals = %v, want [T1]", f.args, pos)
		}
		if o.dir != "X" {
			t.Errorf("inspectFlags parseArgs(%v) dir = %q, want %q", f.args, o.dir, "X")
		}
		if o.verdict != "pass" {
			t.Errorf("inspectFlags parseArgs(%v) verdict = %q, want %q", f.args, o.verdict, "pass")
		}
		if o.session != "S" {
			t.Errorf("inspectFlags parseArgs(%v) session = %q, want %q", f.args, o.session, "S")
		}
	}
}

// TestVerifyFlagsInterleaved checks parseArgs plus verifyFlags give the same
// task and flag values in the task-first, task-last and interleaved forms.
func TestVerifyFlagsInterleaved(t *testing.T) {
	forms := []struct{ args []string }{
		{[]string{"T1", "--dir", "X", "--json"}},
		{[]string{"--dir", "X", "--json", "T1"}},
		{[]string{"--dir", "X", "T1", "--json"}},
	}
	for _, f := range forms {
		fs, o := verifyFlags()
		pos, err := parseArgs(fs, f.args)
		if err != nil {
			t.Errorf("verifyFlags parseArgs(%v): unexpected error: %v", f.args, err)
			continue
		}
		if len(pos) != 1 || pos[0] != "T1" {
			t.Errorf("verifyFlags parseArgs(%v) positionals = %v, want [T1]", f.args, pos)
		}
		if o.dir != "X" {
			t.Errorf("verifyFlags parseArgs(%v) dir = %q, want %q", f.args, o.dir, "X")
		}
		if !o.jsonOut {
			t.Errorf("verifyFlags parseArgs(%v) json = %v, want true", f.args, o.jsonOut)
		}
	}
}
