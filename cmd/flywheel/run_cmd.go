package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func init() {
	register("run", "dispatch a worker for a task", runRun)
	registerHelp("run", "flywheel run <task> [--dir DIR] [--worker NAME] [--model MODEL] [--resume] [--force-model] [--delta FILE] [--allow-overlap] [--strict-brief] [--increment N] [--worktree] [--start-timeout DURATION] [--stall-timeout DURATION] [--notify CMD]", func() *flag.FlagSet { fs, _ := runFlags(); return fs })
}

// runOptions holds the parsed `flywheel run` flags.
type runOptions struct {
	dir          string
	worker       string
	model        string
	resume       bool
	forceModel   bool
	delta        string
	allowOverlap bool
	strictBrief  bool
	increment    int
	worktree     bool
	startTimeout time.Duration
	stallTimeout time.Duration
	notify       string
	session      string
}

// runUsage prints the flywheel run usage line.
func runUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: flywheel run <task> [--dir DIR] [--worker NAME] [--model MODEL] [--resume] [--force-model] [--delta FILE] [--allow-overlap] [--strict-brief] [--increment N] [--worktree] [--start-timeout DURATION] [--stall-timeout DURATION] [--notify CMD] [--session ID]")
}

// runFlags defines run's flags once, so help and run share them.
func runFlags() (*flag.FlagSet, *runOptions) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	o := &runOptions{}
	fs.StringVar(&o.dir, "dir", ".", "target directory")
	fs.StringVar(&o.worker, "worker", "", "worker name")
	fs.StringVar(&o.model, "model", "", "model name")
	fs.BoolVar(&o.resume, "resume", false, "resume the task's last session with its delta (default .flywheel/briefs/<task>.delta.txt)")
	fs.BoolVar(&o.forceModel, "force-model", false, "bypass the L-03 refusal when --resume --model names a model that isn't an approved fallback")
	fs.StringVar(&o.delta, "delta", "", "delta brief file; always sent as the prompt and dispatched as a correction c<N>; without --resume a fresh session is started")
	fs.BoolVar(&o.allowOverlap, "allow-overlap", false, "skip the owns-collision refusal at dispatch; the dispatched event's note records the overlap")
	fs.BoolVar(&o.strictBrief, "strict-brief", false, "refuse a dispatch whose brief has drifted from the hash its last dispatch recorded (RuleRefusal T1, exit 6) instead of warning")
	fs.IntVar(&o.increment, "increment", 0, "dispatch only increment N of the brief as a fresh session (N >= 1); not with --resume or --delta")
	fs.BoolVar(&o.worktree, "worktree", false, "run the worker in the task's own git worktree (.flywheel/worktrees/<task>, branch fw/<task>)")
	fs.DurationVar(&o.startTimeout, "start-timeout", 60*time.Second, "startup timeout")
	fs.DurationVar(&o.stallTimeout, "stall-timeout", 0, "stall timeout for a run gone silent mid-stream (0 = the worker's configured stall_timeout, default 600s)")
	fs.StringVar(&o.notify, "notify", "", "shell command run after the run returns on any path, with FLYWHEEL_FINISHED=\"<task> <attempt> reason=<r> exit=<code>\"; its failure only warns")
	fs.StringVar(&o.session, "session", "", "the lead session dispatching; recorded as the dispatched event's lead (default $FLYWHEEL_SESSION)")
	return fs, o
}

// runRun implements `flywheel run <task>`: dispatch the configured worker,
// stream the run into .flywheel/runs/, and record every transition as an
// event. Exit codes: 0 clean stop, 3 start timeout, 7 stalled mid-stream, 4
// failed run (nonzero rc, capped, or error), 2 usage, 1 any other error.
func runRun(args []string) {
	fs, o := runFlags()
	pos, err := parseArgs(fs, args)
	if err != nil {
		if err == flag.ErrHelp {
			runUsage(os.Stderr)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "flywheel run: %v\n", err)
		runUsage(os.Stderr)
		os.Exit(2)
	}
	if len(pos) != 1 {
		fmt.Fprintf(os.Stderr, "flywheel run: exactly one task id is required\n")
		runUsage(os.Stderr)
		os.Exit(2)
	}
	task := pos[0]
	code, attempt, reason := runDispatch(fs, o, task)
	if o.notify != "" {
		runNotify(o.notify, finishedLine(task, attempt, reason, code), os.Stderr)
	}
	os.Exit(code)
}

// runDispatch validates the dispatch flags and runs the task, returning the
// exit code and the attempt and reason --notify reports (issue #393).
func runDispatch(fs *flag.FlagSet, o *runOptions, task string) (code int, attempt, reason string) {
	// Increment validation: --increment flag must be >= 1, and not combined with --resume or --delta
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "increment" {
			set = true
		}
	})
	if set && o.increment < 1 {
		fmt.Fprintf(os.Stderr, "flywheel run: --increment must be >= 1\n")
		runUsage(os.Stderr)
		return 2, "", "usage"
	}
	if o.increment > 0 && (o.resume || o.delta != "") {
		fmt.Fprintf(os.Stderr, "flywheel run: --increment cannot be combined with --resume or --delta\n")
		runUsage(os.Stderr)
		return 2, "", "usage"
	}

	lead := o.session
	if lead == "" {
		lead = os.Getenv("FLYWHEEL_SESSION")
	}
	// A worker cut off by a rate limit is resumed after the reset (issue #380).
	res, err := flywheel.RunResumingLimits(o.dir, flywheel.RunOptions{
		Task: task, Worker: o.worker, Model: o.model, Resume: o.resume, ForceModel: o.forceModel,
		DeltaPath: o.delta, AllowOverlap: o.allowOverlap, StrictBrief: o.strictBrief, Increment: o.increment,
		Worktree: o.worktree, StartTimeout: o.startTimeout, StallTimeout: o.stallTimeout, Lead: lead,
		Progress: os.Stdout, Stderr: os.Stderr,
	}, time.Sleep, time.Now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flywheel run: %v\n", err)
		if flywheel.IsNoWorkerSession(err) {
			return 2, res.Attempt, "no-session"
		}
		if flywheel.IsRuleRefusal(err) {
			return 6, res.Attempt, "refused"
		}
		return 1, res.Attempt, "error"
	}
	return flywheel.ExitCode(res), res.Attempt, res.Reason
}

// finishedLine is --notify's FLYWHEEL_FINISHED value:
// "<task> <attempt> reason=<r> exit=<code>", "-" for an attempt never made.
func finishedLine(task, attempt, reason string, code int) string {
	if attempt == "" {
		attempt = "-"
	}
	return fmt.Sprintf("%s %s reason=%s exit=%d", task, attempt, reason, code)
}

// runNotify runs cmd through the gates' shell (flywheel.ShellArgv: Git for
// Windows' bash on Windows, never the WSL launcher), with
// FLYWHEEL_FINISHED=finished in its environment. Its output goes to stderr;
// its failure only warns.
func runNotify(cmd, finished string, stderr io.Writer) {
	argv := flywheel.ShellArgv(cmd)
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), "FLYWHEEL_FINISHED="+finished)
	c.Stdout, c.Stderr = stderr, stderr
	if err := c.Run(); err != nil {
		fmt.Fprintf(stderr, "flywheel run: warning: --notify command failed: %v\n", err)
	}
}
