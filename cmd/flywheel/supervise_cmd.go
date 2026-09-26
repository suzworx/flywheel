package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("supervise", "validate every finished unit that has not been measured yet; --resume-limited re-dispatches rate-limited units", runSupervise)
	registerHelp("supervise", "flywheel supervise [--once] [--interval D] [--resume-limited] [--session ID] [--json] [--dir DIR]", func() *flag.FlagSet { fs, _ := superviseFlags(); return fs })
}

// superviseOptions holds the parsed supervise flags.
type superviseOptions struct {
	dir           string
	once          bool
	interval      time.Duration
	json          bool
	resumeLimited bool
	session       string
}

// superviseFlags defines supervise's flags once, so help and run share them.
func superviseFlags() (*flag.FlagSet, *superviseOptions) {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &superviseOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.BoolVar(&o.once, "once", false, "run one pass and exit")
	fs.DurationVar(&o.interval, "interval", 0, "repeat a pass every D until interrupted (e.g. 60s)")
	fs.BoolVar(&o.json, "json", false, "print each pass as JSON")
	fs.BoolVar(&o.resumeLimited, "resume-limited", false, "re-dispatch a rate-limited unit once its model's reset has passed: flywheel run <task> --resume in the background, at most limits.rate_limit_retries times per planned unit")
	fs.StringVar(&o.session, "session", "", "the session recorded on the auto-resume recovered event and passed to the resumed run (default $FLYWHEEL_SESSION, else supervise)")
	return fs, o
}

// superviseUsage prints the flywheel supervise usage line.
func superviseUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel supervise [--once] [--interval D] [--resume-limited] [--session ID] [--json] [--dir DIR]")
}

// autoResumeLog is the file a resumed run's stdout and stderr go to.
func autoResumeLog(task string) string {
	return filepath.Join(".flywheel", "runs", task+".autoresume.log")
}

// superviseStarter returns the SuperviseOptions.Start that runs
// `flywheel run <task> --resume` in the background, its output appended to
// autoResumeLog. With reap it waits for the child in a goroutine (--interval);
// without, the child outlives the --once pass.
func superviseStarter(dir, session string, reap bool) func(string) error {
	return func(task string) error {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("os.Executable: %w", err)
		}
		logPath := filepath.Join(dir, autoResumeLog(task))
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		cmd := exec.Command(exe, "run", task, "--resume", "--dir", dir, "--session", session)
		cmd.Stdout, cmd.Stderr = f, f
		err = cmd.Start()
		f.Close() // the child holds its own copy
		if err != nil {
			return fmt.Errorf("start %s: %w", exe, err)
		}
		if reap {
			go cmd.Wait() //nolint:errcheck // the child's outcome is in the ledger and its log
		}
		return nil
	}
}

// runSupervise implements `flywheel supervise [--once] [--interval D] [--resume-limited] [--session ID] [--json] [--dir DIR]`:
// finds every task whose worker finished and whose current attempt has not been
// measured since, and runs flywheel validate on it. --once runs one pass (exit 5
// if any unit's gauges fail); --interval D repeats forever. --resume-limited
// also re-dispatches rate-limited units whose model's reset has passed. Never
// inspects or lands.
func runSupervise(args []string) {
	fs, o := superviseFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel supervise: %v\n", err)
		superviseUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 0 {
		fmt.Fprintf(os.Stderr, "flywheel supervise: no positional arguments\n")
		superviseUsage(os.Stderr)
		os.Exit(2)
	}

	// Exactly one of --once or --interval D must be given
	// A zero or negative interval would spin without sleeping (#298 review).
	if (!o.once && o.interval <= 0) || (o.once && o.interval != 0) {
		fmt.Fprintf(os.Stderr, "flywheel supervise: exactly one of --once or --interval D (D > 0) must be given\n")
		superviseUsage(os.Stderr)
		os.Exit(2)
	}

	session := o.session
	if session == "" {
		session = os.Getenv("FLYWHEEL_SESSION")
	}
	if session == "" {
		session = "supervise"
	}
	pass := func() (flywheel.SuperviseResult, error) {
		return flywheel.SuperviseWith(o.dir, flywheel.SuperviseOptions{ResumeLimited: o.resumeLimited, Now: time.Now(),
			Start: superviseStarter(o.dir, session, !o.once), Session: session})
	}

	if o.once {
		// Run one pass
		result, err := pass()
		if err != nil {
			fmt.Fprintf(os.Stderr, "flywheel supervise: %v\n", err)
			os.Exit(1)
		}

		if o.json {
			b, jerr := json.MarshalIndent(result, "", "  ")
			if jerr != nil {
				fmt.Fprintf(os.Stderr, "flywheel supervise: encode JSON: %v\n", jerr)
				os.Exit(1)
			}
			fmt.Printf("%s\n", b)
		} else {
			printSuperviseResult(result)
		}

		// Exit 5 if any measured task failed or errored
		for _, st := range result.Measured {
			if st.Error != "" || !st.OK {
				os.Exit(5)
			}
		}
		os.Exit(0)
	} else {
		// Loop with --interval D until interrupted
		for {
			result, err := pass()
			if err != nil {
				fmt.Fprintf(os.Stderr, "flywheel supervise: %v\n", err)
				os.Exit(1)
			}

			if o.json {
				b, jerr := json.MarshalIndent(result, "", "  ")
				if jerr != nil {
					fmt.Fprintf(os.Stderr, "flywheel supervise: encode JSON: %v\n", jerr)
					os.Exit(1)
				}
				fmt.Printf("%s\n", b)
			} else {
				printSuperviseResult(result)
			}

			time.Sleep(o.interval)
		}
	}
}

// printSuperviseResult prints a supervise result in human-readable format.
func printSuperviseResult(result flywheel.SuperviseResult) {
	if len(result.Measured) == 0 && len(result.Resumed) == 0 {
		fmt.Println("supervise: nothing to measure")
		return
	}

	for _, st := range result.Measured {
		if st.Error != "" {
			fmt.Printf("%s error: %s\n", st.Task, st.Error)
		} else if st.OK {
			fmt.Printf("%s %s pass\n", st.Task, st.Attempt)
		} else {
			fmt.Printf("%s %s fail\n", st.Task, st.Attempt)
		}
	}
	for _, r := range result.Resumed {
		if r.Started {
			fmt.Printf("%s %s auto-resumed (log %s)\n", r.Task, r.Attempt, filepath.ToSlash(autoResumeLog(r.Task)))
		} else {
			fmt.Printf("%s %s not resumed: %s\n", r.Task, r.Attempt, r.Reason)
		}
	}
}
