package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
)

var version = "dev" // release builds set it with -ldflags "-X main.version=<tag>"

// command is one flywheel subcommand: its entry point and one-line summary.
// help, when set, overrides the generated usage and flag listing shown by
// `flywheel help <name>`.
type command struct {
	run     func([]string)
	summary string
	usage   string
	flags   func() *flag.FlagSet
}

// commands holds every registered subcommand. Each command file registers
// itself from its own init() function, so adding a command never touches
// main.go.
var commands = map[string]command{}

// register adds a subcommand under name.
func register(name string, summary string, run func([]string)) {
	commands[name] = command{run: run, summary: summary}
}

// registerHelp attaches help text to an already-registered command. usage is
// the one-line usage after "usage: " (e.g. "flywheel log [flags]"), and flags
// returns a FlagSet with the command's real flags defined, or nil for a
// command with no flags.
func registerHelp(name string, usage string, flags func() *flag.FlagSet) {
	c := commands[name]
	c.usage = usage
	c.flags = flags
	commands[name] = c
}

// helpText renders a command's help: usage, summary and, when it has flags,
// the flag defaults from its own FlagSet definition.
func helpText(name string) string {
	c := commands[name]
	usage := c.usage
	if usage == "" {
		usage = "flywheel " + name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "usage: %s\n\n", usage)
	fmt.Fprintln(&b, c.summary)
	if c.flags != nil {
		fmt.Fprintln(&b, "\nflags:")
		fs := c.flags()
		fs.SetOutput(&b)
		fs.PrintDefaults()
	}
	return b.String()
}

// hasHelpFlag reports whether args ask for help (-h, --help or -help)
// anywhere before a literal "--".
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" || a == "-help" {
			return true
		}
	}
	return false
}

// parseArgs parses args with fs, accepting flags before, between and after
// positionals: it re-parses after each positional, and a literal "--" ends
// flag parsing so everything after it is positional. It returns the
// positionals in order. A --dir inside a unit's worktree is resolved to the
// main ledger (resolveLedgerDir).
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	pos, err := parseFlags(fs, args)
	if err != nil {
		return nil, err
	}
	resolveLedgerDir(fs, os.Stderr)
	return pos, nil
}

// resolveLedgerDir points fs's dir flag, when it has one, at the main
// checkout when its value is a `flywheel run --worktree` task worktree
// (flywheel.LedgerRoot): that worktree holds a stale copy of the ledger, so a
// command run there would read and write the wrong events (issue #395). It
// says so once on w. workdir flags are left alone: they address the unit's
// working tree on purpose.
func resolveLedgerDir(fs *flag.FlagSet, w io.Writer) {
	f := fs.Lookup("dir")
	if f == nil {
		return
	}
	root, task, err := flywheel.LedgerRoot(f.Value.String())
	if err != nil || task == "" {
		return
	}
	if err := fs.Set("dir", root); err == nil {
		fmt.Fprintf(w, "flywheel: using the main ledger at %s (this is %s's worktree)\n", root, task)
	}
}

// parseFlags is parseArgs without the ledger resolution.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos, nil
}

// bareAction decides what a bare `flywheel` (no arguments) does: open the
// factory view when ./.flywheel exists, print the global help otherwise.
func bareAction(flywheelDirExists bool) string {
	if flywheelDirExists {
		return "factory"
	}
	return "help"
}

// flywheelDirExists reports whether ./.flywheel is present.
func flywheelDirExists() bool {
	_, err := os.Stat(".flywheel")
	return err == nil
}

func main() {
	if strings.TrimSuffix(strings.ToLower(filepath.Base(os.Args[0])), ".exe") == "git" {
		os.Exit(flywheel.GitGuard(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) // issue #319
	}
	args := os.Args[1:]
	if len(args) == 0 {
		if bareAction(flywheelDirExists()) == "factory" {
			runFactory(nil)
			return
		}
		fmt.Print(globalHelp())
		os.Exit(0)
	}
	if isHelpArg(args[0]) {
		if len(args) == 1 {
			fmt.Print(globalHelp())
			os.Exit(0)
		}
		cmd := args[1]
		if _, ok := commands[cmd]; !ok {
			fmt.Fprintf(os.Stderr, "flywheel: unknown command %q\n", cmd)
			os.Exit(2)
		}
		fmt.Print(helpText(cmd))
		os.Exit(0)
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "flywheel: unknown subcommand %q\n", args[0])
		fmt.Fprintln(os.Stderr, "Run 'flywheel help' for the list.")
		os.Exit(2)
	}
	if hasHelpFlag(args[1:]) {
		fmt.Print(helpText(args[0]))
		os.Exit(0)
	}
	c.run(args[1:])
}

func isHelpArg(a string) bool {
	return a == "-h" || a == "--help" || a == "-help" || a == "help"
}

func globalHelp() string {
	var b strings.Builder
	fmt.Fprintln(&b, "usage: flywheel <subcommand>")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "subcommands:")
	names := make([]string, 0, len(commands))
	for k := range commands {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, name := range names {
		fmt.Fprintf(&b, "  %-10s %s\n", name, commands[name].summary)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "Run 'flywheel help <command>' for its flags.")
	return b.String()
}

func usage(w io.Writer) {
	fmt.Fprint(w, globalHelp())
}
