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
	register("config", "read and validate the flywheel config\n    get <key>          print a config value (bare keys use the default worker)\n    set <key> <value>  set a config value (model, variant, adapter, max_parallel,\n                       feedback.upstream, feedback.submit, limits.per_host)\n    show               print the effective config as JSON\n    validate           check the config and list every problem", runConfig)
	registerHelp("config", "flywheel config <get|set|show|validate> [--dir DIR]", nil)
}

func runConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "flywheel config: missing subcommand (get, set, show, validate)")
		configUsage(os.Stderr)
		os.Exit(2)
	}
	switch args[0] {
	case "get":
		runConfigGet(args[1:])
	case "set":
		runConfigSet(args[1:])
	case "show":
		runConfigShow(args[1:])
	case "validate":
		runConfigValidate(args[1:])
	case "-h", "--help":
		configUsage(os.Stderr)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "flywheel config: unknown subcommand %q\n", args[0])
		configUsage(os.Stderr)
		os.Exit(2)
	}
}

// parseConfigArgs splits a config subcommand's arguments into the --dir value
// and the positional arguments, accepting flags before or after positionals.
func parseConfigArgs(args []string) (dir string, positional []string, err error) {
	dir = "."
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-dir" || a == "--dir":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("flag needs an argument: -dir")
			}
			i++
			dir = args[i]
		case strings.HasPrefix(a, "--dir="):
			dir = strings.TrimPrefix(a, "--dir=")
		case strings.HasPrefix(a, "-dir="):
			dir = strings.TrimPrefix(a, "-dir=")
		case a == "-h" || a == "--help":
			return "", nil, flag.ErrHelp
		case strings.HasPrefix(a, "-") && a != "-":
			return "", nil, fmt.Errorf("flag provided but not defined: %s", a)
		default:
			positional = append(positional, a)
		}
	}
	return dir, positional, nil
}

func configUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel config <get|set|show|validate>")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  get <key>      print a config value (bare keys use the default worker)")
	fmt.Fprintln(w, "  set <key> <value>  set a config value (model, variant, adapter, max_parallel,")
	fmt.Fprintln(w, "                 feedback.upstream, feedback.submit, limits.per_host)")
	fmt.Fprintln(w, "  show           print the effective config as JSON")
	fmt.Fprintln(w, "  validate       check the config and list every problem")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "flags:")
	fmt.Fprintln(w, "  --dir DIR      project directory")
}

func runConfigSet(args []string) {
	dir, positional, err := parseConfigArgs(args)
	if err != nil {
		configError("set", err)
	}
	if len(positional) != 2 {
		fmt.Fprintln(os.Stderr, "flywheel config set: key and value are required")
		configUsage(os.Stderr)
		os.Exit(2)
	}
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config set: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Set(positional[0], positional[1]); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config set: %v\n", err)
		os.Exit(1)
	}
	if err := flywheel.WriteConfig(dir, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config set: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s = %s\n", positional[0], positional[1])
}

func runConfigGet(args []string) {
	dir, positional, err := parseConfigArgs(args)
	if err != nil {
		configError("get", err)
	}
	if len(positional) != 1 {
		fmt.Fprintln(os.Stderr, "flywheel config get: exactly one key is required")
		configUsage(os.Stderr)
		os.Exit(2)
	}
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config get: %v\n", err)
		os.Exit(1)
	}
	val, err := cfg.Get(positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config get: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(val)
}

func runConfigShow(args []string) {
	dir, positional, err := parseConfigArgs(args)
	if err != nil {
		configError("show", err)
	}
	if len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "flywheel config show: unexpected argument %q\n", positional[0])
		configUsage(os.Stderr)
		os.Exit(2)
	}
	cfg, exists, err := flywheel.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config show: %v\n", err)
		os.Exit(1)
	}
	if !exists {
		fmt.Fprintln(os.Stderr, "flywheel config show: no .flywheel/config.json found; showing the built-in default")
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config show: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(b))
}

func runConfigValidate(args []string) {
	dir, positional, err := parseConfigArgs(args)
	if err != nil {
		configError("validate", err)
	}
	if len(positional) > 0 {
		fmt.Fprintf(os.Stderr, "flywheel config validate: unexpected argument %q\n", positional[0])
		configUsage(os.Stderr)
		os.Exit(2)
	}
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config validate: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel config validate: %v\n", err)
		os.Exit(1)
	}
	approved := 0
	for _, w := range cfg.Workers {
		for _, fb := range w.Fallbacks {
			if fb.Approved {
				approved++
			}
		}
	}
	fmt.Printf("config ok: %d worker(s), %d approved fallback(s)\n", len(cfg.Workers), approved)
}

// configError prints a usage-level error for a config subcommand and exits 2.
func configError(sub string, err error) {
	if err == flag.ErrHelp {
		configUsage(os.Stderr)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "flywheel config %s: %v\n", sub, err)
	configUsage(os.Stderr)
	os.Exit(2)
}
