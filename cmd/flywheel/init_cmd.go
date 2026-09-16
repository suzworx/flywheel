package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"flywheel/internal/flywheel"
)

func init() {
	register("init", "scaffold flywheel state files into a directory", runInit)
	registerHelp("init", "flywheel init [flags]", func() *flag.FlagSet { fs, _ := initFlags(); return fs })
}

// initOptions holds the parsed init flags.
type initOptions struct {
	dir      string
	force    bool
	model    string
	variant  string
	track    bool
	ignore   bool
	agentsMD bool
}

// initFlags defines init's flags once, so help and run share them.
func initFlags() (*flag.FlagSet, *initOptions) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &initOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.model, "model", "", "seed a new .flywheel/config.json's default worker model")
	fs.StringVar(&o.variant, "variant", "", "seed a new .flywheel/config.json's default worker variant")
	fs.BoolVar(&o.force, "force", false, "reset an existing flywheel.md (a directory or symlink there is still refused)")
	fs.BoolVar(&o.track, "track", false, "keep flywheel.md a normal, commit-able file (default)")
	fs.BoolVar(&o.ignore, "ignore", false, "add flywheel.md to the target's root .gitignore so every worktree stays clean")
	fs.BoolVar(&o.agentsMD, "agents-md", false, "write/refresh AGENTS.md with a flywheel:agents block naming the installed skills and the persona each plays")
	return fs, o
}

func runInit(args []string) {
	fs, o := initFlags()
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel init: %v\n", err)
		usage(os.Stderr)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "flywheel init: unexpected argument %q\n", fs.Arg(0))
		usage(os.Stderr)
		os.Exit(2)
	}
	if o.track && o.ignore {
		fmt.Fprintf(os.Stderr, "flywheel init: --track and --ignore are mutually exclusive\n")
		usage(os.Stderr)
		os.Exit(2)
	}
	configExisted := false
	if _, err := os.Stat(filepath.Join(o.dir, ".flywheel", "config.json")); err == nil {
		configExisted = true
	}
	path, pieces, err := flywheel.InitSeeded(o.dir, o.force, o.model, o.variant, o.agentsMD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel init: %v\n", err)
		os.Exit(1)
	}
	anyAdded := false
	for _, p := range pieces {
		if p.Added {
			anyAdded = true
			break
		}
	}
	// --ignore ensures the target's root .gitignore hides flywheel.md; --track
	// and the default leave it a normal, commit-able file.
	var ignoreAdded bool
	if o.ignore {
		ignoreAdded, err = flywheel.IgnoreMarkdown(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel init: %v\n", err)
			os.Exit(1)
		}
	}
	if !anyAdded {
		names := make([]string, len(pieces))
		for i, p := range pieces {
			names[i] = p.Path
		}
		fmt.Printf("init: nothing to do (%s present)\n", strings.Join(names, ", "))
		if ignoreAdded {
			fmt.Println("ignored: flywheel.md")
		}
	} else {
		for _, p := range pieces {
			if p.Added {
				fmt.Printf("added: %s\n", p.Path)
			} else {
				fmt.Printf("present: %s\n", p.Path)
			}
		}
		if o.ignore {
			fmt.Println("ignored: flywheel.md")
		}
		fmt.Println("next: flywheel log --task <id> --kind planned --brief <path>")
	}
	if configExisted && (o.model != "" || o.variant != "") {
		fmt.Println("config.json exists; change it with: flywheel config set model|variant <value>")
	}
	ignored := flywheel.IgnoredStateFiles(path)
	if len(ignored) > 0 {
		for _, p := range ignored {
			fmt.Fprintf(os.Stderr, "warning: git ignores %s; flywheel's state would never be committed\n", p)
		}
		fmt.Fprintln(os.Stderr, "add to .gitignore:")
		for _, p := range ignored {
			fmt.Fprintln(os.Stderr, "!"+p)
		}
	}
}
