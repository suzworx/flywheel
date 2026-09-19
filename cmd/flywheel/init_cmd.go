package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/suzworx/flywheel/internal/flywheel"
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
	hooks    bool
	gitHooks bool
	ci       bool
	local    string
	localURL string
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
	fs.BoolVar(&o.hooks, "hooks", false, "write .claude/settings.json and .opencode/plugin/flywheel-session.mjs so every session records its start, its flywheel commands, and its end")
	fs.BoolVar(&o.gitHooks, "git-hooks", false, "install a commit-msg hook requiring a Flywheel-Task trailer and a pre-push hook running flywheel verify")
	fs.BoolVar(&o.ci, "ci", false, "write .github/workflows/flywheel-audit.yml: a CI job running flywheel verify --all --log on every pull request")
	fs.StringVar(&o.local, "local", "", "point OpenCode workers at a local OpenAI-compatible server serving this model, as worker \"local\"")
	fs.StringVar(&o.localURL, "local-url", flywheel.DefaultLocalURL, "the local server's OpenAI-compatible base URL (with --local)")
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
	localURLSet := false // explicitly passed, even with the default value (#327 review)
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "local-url" {
			localURLSet = true
		}
	})
	if localURLSet && o.local == "" {
		fmt.Fprintf(os.Stderr, "flywheel init: --local-url requires --local\n")
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
	}
	if o.hooks {
		_, hpieces, herr := flywheel.InitHooks(o.dir)
		if herr != nil {
			fmt.Fprintf(os.Stderr, "flywheel init: %v\n", herr)
			os.Exit(1)
		}
		for _, p := range hpieces {
			if p.Added {
				fmt.Printf("added: %s\n", p.Path)
			} else {
				fmt.Printf("present: %s\n", p.Path)
			}
		}
	}
	if o.gitHooks {
		_, ghpieces, gherr := flywheel.InitGitHooks(o.dir)
		if gherr != nil {
			fmt.Fprintf(os.Stderr, "flywheel init: %v\n", gherr)
			os.Exit(1)
		}
		for _, p := range ghpieces {
			if p.Added {
				fmt.Printf("added: %s\n", p.Path)
			} else {
				fmt.Printf("present: %s\n", p.Path)
			}
		}
	}
	if o.ci {
		_, cipieces, cierr := flywheel.InitCI(o.dir, version)
		if cierr != nil {
			fmt.Fprintf(os.Stderr, "flywheel init: %v\n", cierr)
			os.Exit(1)
		}
		for _, p := range cipieces {
			if p.Added {
				fmt.Printf("added: %s\n", p.Path)
			} else {
				fmt.Printf("present: %s\n", p.Path)
			}
		}
	}
	if o.local != "" {
		lpieces, lerr := flywheel.InitLocal(o.dir, o.local, o.localURL)
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "flywheel init: %v\n", lerr)
			os.Exit(1)
		}
		for _, p := range lpieces {
			fmt.Printf("updated: %s\n", p.Path)
		}
		fmt.Println("next: flywheel doctor, then flywheel run <task> --worker local")
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

	fmt.Println()
	summary, err := flywheel.FactorySummary(o.dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel init: summary: %v\n", err)
	} else {
		fmt.Println(summary)
	}

	fmt.Println("next: flywheel log --task <id> --kind planned --brief <path>")
}
