package flywheel

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RunOptions configures one dispatch. StartTimeout bounds the wait for the
// first stdout line; zero means the default 60s. StallTimeout bounds the gap
// between two run-file lines once the run has started; zero means the
// worker's configured stall_timeout (itself defaulting to 600s).
type RunOptions struct {
	Task         string
	Worker       string // worker name; empty selects the default worker
	Model        string // override; empty uses the worker's model
	Resume       bool
	ForceModel   bool   // bypass the L-03 refusal when --resume --model names a model that is not an approved fallback
	DeltaPath    string // correction prompt; on a resume the default is .flywheel/briefs/<task>.delta.txt
	AllowOverlap bool   // skip the owns- and exclusive-collision refusals; the dispatched note records the overlap
	StrictBrief  bool   // a drifted brief is a T1 RuleRefusal instead of a warning (issue #135)
	Increment    int    // > 0: do only increment N of the brief, as a fresh session (issue #83)
	Worktree     bool   // run the worker in the task's own worktree (issue #45)
	StartTimeout time.Duration
	StallTimeout time.Duration
	Progress     io.Writer
	Stderr       io.Writer     // notices (e.g. the long-step warning); nil discards them
	SimDelay     time.Duration // unexported test hook: sim waits before its first line
	SimLineDelay time.Duration // unexported test hook: sim waits between lines
}

// Result reports what the dispatch observed.
type Result struct {
	Attempt string
	Session string
	RC      int
	Reason  string
	Steps   int
	Tokens  *Tokens
	Cost    float64
}

// workerPermissionPolicy is the embedded OpenCode permission policy written to
// .flywheel/opencode-worker.json when missing. Its bash rules stay
// byte-identical to skills/flywheel/references/worker-permissions.json: the
// catch-all allow for bash comes FIRST and the git denies follow, and
// OpenCode applies the last matching rule. This embedded copy additionally
// denies external_directory — OpenCode's documented permission for tool
// calls (read, edit, glob, grep and bash) that touch paths outside the
// working directory (issue #87) — a line the canonical reference file does
// not carry. It also carries "instructions", pointing at worker-rules.md
// next to it (issue #31), so every run loads the worker rules into the
// worker's context without a tool call — including under --pure, which
// skips only plugins, never instructions. The user's own opencode.json is
// never touched.
var workerPermissionPolicy = `{
  "instructions": ["worker-rules.md"],
  "permission": {
    "bash": {
      "*": "allow",
      "git stash*": "deny",
      "git reset*": "deny",
      "git checkout*": "deny",
      "git restore*": "deny",
      "git clean*": "deny",
      "git switch*": "deny",
      "git commit*": "deny",
      "git rebase*": "deny",
      "git merge*": "deny",
      "git cherry-pick*": "deny",
      "git pull*": "deny",
      "git push*": "deny",
      "git -C*": "deny",
      "git --work-tree*": "deny",
      "git --git-dir*": "deny"
    },
    "external_directory": "deny"
  }
}`

// workerRules is the embedded worker rules text written to
// .flywheel/worker-rules.md when missing (issue #31): the embedded policy's
// "instructions" key points at that file, so OpenCode loads these rules into
// every worker's context without a tool call — briefs no longer need to
// repeat them. skills/flywheel/references/worker-rules.md carries the same
// text.
const workerRules = `- Stay inside owns: and the worktree. At most one write per response (at most 120 lines); batch read-only calls together.
- Look up library APIs with the language's doc tool (go doc pkg.Symbol), never by reading or grepping library source, and never write probe programs.
- Build or typecheck after each file; run the full checks at the end.
- Report every command you ran and its real exit status; a claim is not evidence, the gauges re-measure it.
- Never commit, push, or write secrets.
- Your first message, before any tool call, starts with four plain-text lines: PLAN files-to-read: ..., PLAN files-to-change: ..., PLAN order: ..., PLAN checks: ... (no markdown).
`

// NoWorkerSession is the refusal returned by a resume when the task has no
// recorded worker session. The CLI maps it to exit 2 (usage); every other run
// error keeps its existing exit code.
type NoWorkerSession struct {
	Task string
}

func (e *NoWorkerSession) Error() string {
	return fmt.Sprintf("cannot resume task %q: no worker session recorded for it", e.Task)
}

// IsNoWorkerSession reports whether err is a NoWorkerSession refusal.
func IsNoWorkerSession(err error) bool {
	var e *NoWorkerSession
	return errors.As(err, &e)
}

// commandHook, when set, receives the dispatch RunRequest before the command
// is built. It is a test seam only; production code never sets it.
var commandHook func(RunRequest)

// Run dispatches one task to the configured worker, streams the run into
// .flywheel/runs/<task>.<attempt>.jsonl while parsing it, and records
// dispatched, started, worker_plan, no-plan, off-course, report and finished
// events. no-plan is appended once, at the 20th completed step, when no PLAN
// text has been seen yet. off-course is appended once, when a read, grep or
// glob tool call names the 5th distinct path outside the worktree (library
// source), naming the paths in its note. Each of those conditions is also
// recorded as a signal event (issue #37), as are the finish reasons length
// (capped), error (provider-error), silent and stalled, one per condition and
// attempt, after the event that detected them. Neither changes the run's
// outcome. Every path after the dispatched event records a finished event.
func Run(dir string, o RunOptions) (res Result, err error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return Result{}, err
	}
	worker := cfg.DefaultWorker()
	if o.Worker != "" {
		if w, ok := cfg.Worker(o.Worker); ok {
			worker = w
		} else {
			return Result{}, fmt.Errorf("no worker named %q in .flywheel/config.json", o.Worker)
		}
	}
	// Product line (issue #69): resolved from the task's brief header before
	// anything reads the worker or model, so every check below (L-03,
	// limits, budget, breaker, rate) sees the worker that will really run.
	// The line's worker staffs the unit unless --worker was given; the line
	// is recorded on the dispatch either way.
	var usedLine string
	if h, ok := lineHeader(dir, o); ok {
		line, lineFound, lineErr := cfg.LineFor(h)
		if lineErr != nil {
			return Result{}, lineErr
		}
		if lineFound {
			usedLine = line.Name
			if o.Worker == "" {
				lw, ok := cfg.Worker(line.Worker)
				if !ok {
					return Result{}, fmt.Errorf("line %q references worker %q, which is not in workers[]", line.Name, line.Worker)
				}
				worker = lw
			}
		}
	}
	model := o.Model
	if model == "" {
		model = worker.Model
	}
	adap, err := AdapterFor(worker.Adapter)
	if err != nil {
		return Result{}, err
	}

	// Increment validation (issue #83): --increment dispatches a fresh session
	// of the brief; it cannot be combined with --resume or --delta.
	if o.Increment < 0 || (o.Increment > 0 && (o.Resume || o.DeltaPath != "")) {
		return Result{}, fmt.Errorf("--increment dispatches a fresh session of the brief; it cannot be combined with --resume or --delta")
	}

	// L-03: a resume that switches the worker's model onto one nobody
	// approved is refused before any event is read or recorded, unless
	// --force-model overrides it. A fresh run's model choice is unrestricted.
	if o.Resume && model != worker.Model && !o.ForceModel {
		approved := false
		for _, f := range worker.Fallbacks {
			if f.Model == model && f.Approved {
				approved = true
				break
			}
		}
		if !approved {
			return Result{}, &RuleRefusal{
				Rule: "L-03",
				Fix:  fmt.Sprintf("model %q is not an approved fallback for worker %q; add it to the worker's fallbacks with \"approved\": true in .flywheel/config.json, or pass --force-model", model, worker.Name),
			}
		}
	}

	// T1: dispatched needs a planned event carrying the brief path.
	// Dispatch lock (issue #242): the log is read, the collision checks are
	// run and the dispatched event is appended under .flywheel/dispatch.lock,
	// so two dispatches started at the same moment serialise instead of both
	// reading a log in which neither has dispatched, both passing the checks
	// and both dispatching — taking the same exclusive resource or writing
	// the same owned file, the exact case the refusals exist to stop. The
	// lock is released as soon as dispatched lands, before the worker starts.
	releaseDispatchLock, err := acquireDispatchLock(dir)
	if err != nil {
		return Result{}, err
	}
	dispatchLockHeld := true
	defer func() {
		if dispatchLockHeld {
			releaseDispatchLock()
		}
	}()

	events, err := ReadEvents(dir)
	if err != nil {
		return Result{}, err
	}
	brief, _, _ := latestBaseBriefAndAttempt(events, o.Task)
	lastSession := ""
	for _, e := range events {
		if e.Task != o.Task {
			continue
		}
		if e.Session != "" && (e.Kind == "started" || e.Kind == "finished") {
			lastSession = e.Session
		}
	}
	if brief == "" {
		return Result{}, fmt.Errorf("task %q has no planned event; record one with: flywheel log --task %s --kind planned --brief <path>", o.Task, o.Task)
	}
	// An increment must exist in the brief (#295 review): a worker told to do
	// increment N of a brief without one would improvise.
	if o.Increment > 0 {
		text, rerr := os.ReadFile(resolveBriefPath(dir, brief))
		if rerr != nil {
			return Result{}, fmt.Errorf("read brief %s: %w", brief, rerr)
		}
		if !briefHasIncrement(string(text), o.Increment) {
			return Result{}, &RuleRefusal{
				Rule: "increment",
				Fix:  fmt.Sprintf("brief %s has no increment %d: add an \"## Increments\" section whose list has item %d (or an \"Increment %d\" heading)", brief, o.Increment, o.Increment, o.Increment),
			}
		}
	}

	// Owns collision (issue #164): a dispatch whose owns: overlaps an
	// in-flight task's owns: is refused before any event is recorded, so a
	// worker never starts knowing another unit is writing the same files.
	// --allow-overlap skips the refusal and records the crossing on the
	// dispatched note instead. A task with no readable brief contributes no
	// owns and never blocks.
	myOwns := []string{}
	myExclusive := []string{}
	if header, _, aerr := AttemptBrief(dir, events, o.Task); aerr == nil {
		myOwns = header.Owns
		myExclusive = header.Exclusive
	}
	overlap := ownsCollisionWith(dir, events, o.Task, myOwns)
	if !o.AllowOverlap && overlap != nil {
		return Result{}, &RuleRefusal{
			Rule: "owns",
			Fix:  fmt.Sprintf("owns collision with %s (%s): %s; wait for it to land, narrow this brief's owns:, or pass --allow-overlap", overlap.task, overlap.status, strings.Join(overlap.paths, ", ")),
		}
	}

	// Exclusive resource (issue #220): a dispatch whose exclusive: names a
	// resource an in-flight task already holds is refused the same way, so
	// one unit can never race another for a shared database, build cache or
	// device. The owns check runs first, so its more specific message wins
	// when both apply. --allow-overlap skips this refusal too and records
	// the crossing on the dispatched note.
	excl := exclusiveCollisionWith(dir, events, o.Task, myExclusive)
	if !o.AllowOverlap && excl != nil {
		return Result{}, &RuleRefusal{
			Rule: "exclusive",
			Fix:  fmt.Sprintf("exclusive resource %q is held by %s (%s); wait for it to land, or pass --allow-overlap", excl.name, excl.task, excl.status),
		}
	}

	// Limits (issue #46): refuse before anything is recorded when this host
	// already runs limits.per_host attempts, or when the ledger's recorded
	// spend has reached limits.budget.wave_cost_usd (the ledger is the wave).
	if cfg.Limits.PerHost > 0 {
		inFlight := 0
		for _, ts := range Derive(events).Tasks {
			// Every task in flight counts, this one included: a second fresh
			// run of a task already running is another attempt on the host
			// (a correction dispatches from needs-correction, not in flight).
			if ts.Status == "dispatched" || ts.Status == "running" {
				inFlight++
			}
		}
		if inFlight >= cfg.Limits.PerHost {
			return Result{}, &RuleRefusal{
				Rule: "limits",
				Fix:  fmt.Sprintf("%d attempt(s) already in flight and limits.per_host is %d; wait for one to finish, or raise it: flywheel config set limits.per_host <n>", inFlight, cfg.Limits.PerHost),
			}
		}
	}
	if b := cfg.Limits.Budget; b != nil && b.WaveCostUSD > 0 {
		spent := 0.0
		for _, e := range events {
			if e.Kind == "finished" {
				spent += e.Cost
			}
		}
		if spent >= b.WaveCostUSD {
			return Result{}, &RuleRefusal{
				Rule: "budget",
				Fix:  fmt.Sprintf("recorded spend $%.4f has reached limits.budget.wave_cost_usd $%.4f; raise the budget in .flywheel/config.json or start a new wave (a new ledger)", spent, b.WaveCostUSD),
			}
		}
	}
	if b := cfg.Limits.Budget; b != nil && b.WaveTokens > 0 {
		n := recordedTokens(events)
		if n >= b.WaveTokens {
			return Result{}, &RuleRefusal{
				Rule: "budget",
				Fix:  fmt.Sprintf("recorded tokens %d have reached limits.budget.wave_tokens %d; raise it in .flywheel/config.json or start a new wave (a new ledger)", n, b.WaveTokens),
			}
		}
	}

	if b := cfg.Limits.Breaker; b != nil {
		if open, until := breakerOpen(events, model, *b, now()); open {
			oldModel := model
			fallback := ""
			if o.Model == "" {
				fallback = breakerFallback(events, worker, *b, now())
			}
			if fallback != "" {
				model = fallback
				progress(o.Progress, fmt.Sprintf("%s breaker open for %s until %s; dispatching approved fallback %s", o.Task, oldModel, until.UTC().Format(time.RFC3339), fallback))
			} else {
				return Result{}, &RuleRefusal{
					Rule: "breaker",
					Fix:  fmt.Sprintf("model %s: the last %d attempts ended with provider errors; the breaker lets one dispatch through again at %s — dispatch another model with --model <m>%s, or wait", oldModel, b.Errors, until.UTC().Format(time.RFC3339), approvedFallbackHint(worker)),
				}
			}
		}
	}

	if cfg.Limits.RatePerMinute > 0 {
		if limited, until := rateLimited(events, model, cfg.Limits.RatePerMinute, now()); limited {
			return Result{}, &RuleRefusal{
				Rule: "rate",
				Fix:  fmt.Sprintf("model %s was dispatched %d times in the last minute (limits.rate_per_minute); dispatch again after %s", model, cfg.Limits.RatePerMinute, until.UTC().Format(time.RFC3339)),
			}
		}
	}

	// Attempt numbering: a fresh run is r<n+1>, a correction c<m+1>. The
	// delta, not the resume flag, makes a dispatch a correction: a given
	// --delta is always the prompt, with or without --resume.
	freshN := 0
	corrN := 0
	for _, e := range events {
		if e.Task != o.Task || e.Attempt == "" {
			continue
		}
		if e.Attempt[0] == 'r' {
			if n := attemptNum(e.Attempt); n > freshN {
				freshN = n
			}
		}
		if e.Attempt[0] == 'c' {
			if n := attemptNum(e.Attempt); n > corrN {
				corrN = n
			}
		}
	}
	attempt := fmt.Sprintf("r%d", freshN+1)
	if o.Resume || o.DeltaPath != "" {
		if o.Resume && lastSession == "" {
			return Result{}, &NoWorkerSession{Task: o.Task}
		}
		attempt = fmt.Sprintf("c%d", corrN+1)
	}

	// Brief drift (issue #135): a brief edited after its last dispatch is
	// caught here, at dispatch, instead of only at audit time (rule T1).
	// Drift warns and the dispatch proceeds — a lead mid-correction is the
	// common case — unless --strict-brief makes it a T1 RuleRefusal before
	// any event is appended.
	if drift := checkBriefDrift(dir, events, o.Task, brief); drift != nil {
		msg := fmt.Sprintf("brief on disk differs from the hash dispatched at %s; re-record it with: flywheel log --task %s --kind planned --brief %s", drift.attempt, o.Task, brief)
		if o.StrictBrief {
			return Result{}, &RuleRefusal{Rule: "T1", Fix: msg}
		}
		progress(o.Progress, fmt.Sprintf("%s %s brief-drift (%s)", o.Task, attempt, msg))
	}

	promptSrc, err := promptSource(dir, brief, o.DeltaPath, o.Task, o.Resume)
	if err != nil {
		return Result{}, err
	}
	promptB, err := os.ReadFile(promptSrc)
	if err != nil {
		return Result{}, fmt.Errorf("read prompt %s: %w", promptSrc, err)
	}
	// A prompt outside the worktree (a brief attached from another checkout)
	// is copied in byte-for-byte and attached from its in-worktree copy, so
	// no path in the dispatch points outside the worktree (issue #87). The
	// sha256 recorded on dispatched is unchanged: it hashes promptB, the same
	// bytes either way. A prompt already inside the workdir is attached as
	// is, with no copy made.
	if isOutsideWorktree(dir, promptSrc) {
		name := o.Task + ".txt"
		if o.Resume || o.DeltaPath != "" {
			name = o.Task + ".delta.txt"
		}
		briefsDir := filepath.Join(dir, ".flywheel", "briefs")
		if err := os.MkdirAll(briefsDir, 0o755); err != nil {
			return Result{}, fmt.Errorf("create %s: %w", briefsDir, err)
		}
		dest := filepath.Join(briefsDir, name)
		if err := os.WriteFile(dest, promptB, 0o644); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", dest, err)
		}
		promptSrc = dest
	}
	promptBriefField := promptBrief(dir, promptSrc)
	prompt := string(promptB)

	// The dispatched event carries the parsed header of the prompt it
	// actually sent (the planned brief on a fresh attempt, the delta on a
	// correction), so a later reader measures the attempt against the ledger,
	// not against whatever the file says now (issue #259). The header is
	// parsed from promptB — the exact bytes attached — so the recorded SHA256
	// and the recorded header always describe the same immutable content; a
	// concurrent editor between the read and this parse can never make them
	// disagree.
	promptHeader, err := ParseBriefHeaderBytes(promptB)
	if err != nil {
		return Result{}, fmt.Errorf("parse prompt %s: %w", promptSrc, err)
	}

	// Shared gate (issue #223): a gate line this dispatch will run that an
	// in-flight task's brief declares byte-identically means those units will
	// run that command at once — max_parallel caps concurrent workers, not
	// concurrent gates. A warning, never a refusal: sharing a gate is
	// legitimate and often unavoidable, and the operator needs to know the
	// real concurrency limit, not be stopped. The gates compared are the
	// prompt about to be sent — promptHeader, the exact bytes attached, the
	// same header recorded on the dispatched event — not AttemptBrief's
	// resolution of the previous attempt, which has no dispatched event for
	// this invocation yet and would describe a different prompt: a
	// correction's delta replaces the base gates when it declares any, and a
	// fresh dispatch sends the base brief as it is on disk now, edited or
	// not. Recorded on the dispatched note below and printed as a progress
	// line.
	gates := gateContentions(dir, events, o.Task, dispatchGates(dir, events, o.Task, o.DeltaPath != "" || o.Resume, promptHeader))
	for _, g := range gates {
		progress(o.Progress, o.Task+" "+attempt+" "+gateContentionLine(g))
	}

	// The run file the dispatched event points at.
	runsDir := filepath.Join(dir, ".flywheel", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create %s: %w", runsDir, err)
	}
	runName := o.Task + "." + attempt + ".jsonl"
	runRel := ".flywheel/runs/" + runName
	promptSHA := contentSHA([]byte(prompt))

	// Worker permission policy (issue #68): write the embedded default policy
	// if missing and point OPENCODE_CONFIG at it. Never edit the user's own
	// opencode.json.
	policySHA, err := workerPolicySHA(dir)
	if err != nil {
		return Result{}, err
	}
	if err := writeWorkerRules(dir); err != nil {
		return Result{}, err
	}

	// Worktree: when --worktree, create or reuse task's own worktree
	// (.flywheel/worktrees/<task>, branch fw/<task>) before computing baseline.
	var wt string
	if o.Worktree {
		var err error
		wt, err = TaskWorktree(dir, o.Task)
		if err != nil {
			return Result{}, err
		}
	} else {
		wt = dir
	}

	// Baseline: hash every path dirty at dispatch (the same read-only git
	// commands the owns check uses) so validate can tell this unit's edits
	// from the lead's or another worker's pre-existing ones. Not a git repo:
	// no baseline.
	baseline := computeBaseline(wt)
	base := headCommit(wt)

	// Snapshot the worktree's git history state before the worker runs (issue #314).
	histBefore, histOK := gitHistoryState(wt)

	// Worktree snapshot (issue #87): when dir sits inside a git repo that has
	// other worktrees, record each one's changed paths and shas so validate
	// can catch a worker that edited another checkout instead of staying in
	// this one. Not a git repo, or no other worktrees: nil (omitted).
	// The worker's own tree is excluded (with --worktree that is the task's
	// worktree, and the flywheel root becomes one of the others) (#333 review).
	worktrees := otherWorktrees(wt)

	if err := AppendEvent(dir, Event{
		TS: "", Task: o.Task, Kind: "dispatched", Attempt: attempt, Increment: o.Increment,
		Adapter: worker.Adapter, Model: model, Path: runRel, SHA256: promptSHA,
		Brief: promptBriefField, Note: dispatchedNote(policySHA, overlap, excl, gates),
		Baseline: baseline, Base: base, Worktrees: worktrees, Header: &promptHeader, Workdir: workdirField(wt, dir),
		Line: usedLine,
	}); err != nil {
		return Result{}, err
	}
	// The dispatched event is recorded and every check that read the log is
	// past: release the dispatch lock NOW, before the worker starts. A lock
	// held for the run would serialise the entire factory, which is the
	// opposite of what this repository is for.
	releaseDispatchLock()
	dispatchLockHeld = false
	progressLine := o.Task + " " + attempt + " dispatched " + worker.Adapter + " " + model
	if o.Increment > 0 {
		progressLine += fmt.Sprintf(" increment %d", o.Increment)
	}
	progress(o.Progress, progressLine)

	// The lease records that this run holds this attempt while the worker
	// runs; the renewer moves renewed_at and expires_at forward every
	// renew_interval and is stopped when the child exits. A flywheel run that
	// is killed leaves the lease in place, and it expires on its own.
	renewInterval, ttl := cfg.leaseTimings()
	host, _ := os.Hostname()
	leaseNow := now().UTC().Format(time.RFC3339Nano)
	lease := Lease{
		Task: o.Task, Attempt: attempt, PID: os.Getpid(), Host: host,
		StartedAt: leaseNow, RenewedAt: leaseNow,
		ExpiresAt: now().UTC().Add(ttl).Format(time.RFC3339Nano), RunFile: runRel,
	}
	if err := WriteLease(dir, lease); err != nil {
		return Result{}, fmt.Errorf("write lease for %s.%s: %w", o.Task, attempt, err)
	}
	var leaseTicker *time.Ticker
	var renewDone chan struct{}
	var renewerExited chan struct{}
	var stopOnce sync.Once
	stopRenewer := func() {
		stopOnce.Do(func() {
			if leaseTicker != nil {
				leaseTicker.Stop()
			}
			if renewDone != nil {
				close(renewDone)
				<-renewerExited
			}
		})
	}
	if renewInterval > 0 {
		renewDone = make(chan struct{})
		renewerExited = make(chan struct{})
		leaseTicker = time.NewTicker(renewInterval)
		go func() {
			defer close(renewerExited)
			for {
				select {
				case <-leaseTicker.C:
					l := lease
					l.RenewedAt = now().UTC().Format(time.RFC3339Nano)
					l.ExpiresAt = now().UTC().Add(ttl).Format(time.RFC3339Nano)
					_ = WriteLease(dir, l)
				case <-renewDone:
					return
				}
			}
		}()
	}

	// Stream stdout into the run file while parsing. Stdin stays nil so the
	// child reads the null device (closed stdin is mandatory).
	hasher := sha256.New()
	var runFile *os.File
	var errFile *os.File
	var cmd *exec.Cmd
	var fixture *os.File
	var watchdog *time.Timer
	var startNote string

	// Every path after dispatched records a finished event: if the function
	// returns an error, close the run's files, kill a still-running child and
	// record finished {reason: "error", note: <the error>}. A process that
	// failed to start records {reason: "start-failed", note: <the start
	// diagnostic>} instead: it never completed a step.
	defer func() {
		if err == nil {
			return
		}
		if watchdog != nil {
			watchdog.Stop()
		}
		if errFile != nil {
			errFile.Close()
		}
		if fixture != nil {
			fixture.Close()
		}
		if runFile != nil {
			runFile.Close()
		}
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		runSHA := hex.EncodeToString(hasher.Sum(nil))
		reason := "error"
		note := err.Error()
		if startNote != "" {
			reason = "start-failed"
			note = startNote
		}
		stopRenewer()
		gitWrote, gitNote := gitWriteNote(wt, histBefore, histOK)
		if aerr := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "finished", Attempt: attempt, Reason: reason, Note: joinNote(note, gitNote), SHA256: runSHA}); aerr == nil {
			progress(o.Progress, o.Task+" "+attempt+" finished rc=1 reason="+reason+" note="+note)
			if gitWrote {
				_ = flagGitWrite(dir, o.Task, attempt, "", runRel, gitNote, o.Progress)
			}
			_ = RemoveLease(dir, o.Task, attempt)
		}
		_, _ = WriteState(dir)
	}()

	runFile, err = os.OpenFile(filepath.Join(runsDir, runName), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("open %s: %w", filepath.Join(runsDir, runName), err)
	}
	var stream io.Reader
	killChild := func() {}

	timeout := o.StartTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	stallDur := o.StallTimeout
	if stallDur == 0 {
		stallDur = worker.stallTimeoutDuration()
	}
	halfDur := stallDur / 2

	// The command request. The session is passed only on a resume: a fresh
	// run must start a new conversation, never continue the previous one.
	sessionArg := ""
	if o.Resume {
		sessionArg = lastSession
	}
	req := RunRequest{
		Task: o.Task, Attempt: attempt, PromptFile: promptSrc, Model: model,
		Variant: worker.Variant, Session: sessionArg, Title: o.Task + "-" + attempt,
		Resume: o.Resume, Increment: o.Increment,
		AllowedTools: worker.allowedTools(), DisallowedTools: worker.disallowedTools(),
	}
	if commandHook != nil {
		commandHook(req)
	}

	// Every subprocess adapter launches the same adapter-agnostic way: exec
	// whatever bin/args its own Command returns. Only "sim" is excluded — it
	// replays a fixture in-process below instead of spawning anything
	// (issue #49: this used to read worker.Adapter == "opencode", which left
	// every other real adapter, including claude, valid in config but
	// unlaunchable). workerEnv's OPENCODE_CONFIG stays exactly as it is: an
	// OpenCode-specific env var a non-opencode child simply ignores;
	// reshaping per-adapter env handling is out of scope here.
	if worker.Adapter != "sim" {
		bin, args := adap.Command(req)
		cmd = exec.Command(bin, args...)
		cmd.Dir = wt
		guardBin, guardEnv, err := installGitGuard(wt, o.Task, attempt)
		if err != nil {
			// Fail closed: a worker never runs without the git guard (#325
			// review). The attempt is recorded as failed like any other
			// launch error.
			return Result{}, fmt.Errorf("git guard not installed (%w); workers never run git unguarded", err)
		}
		defer os.RemoveAll(guardBin)
		cmd.Env = workerEnv(dir)
		if len(guardEnv) > 0 {
			cmd.Env = append(cmd.Env, guardEnv...)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			return Result{}, fmt.Errorf("stdout pipe: %w", err)
		}
		stream = out
		errFile, err = os.OpenFile(filepath.Join(runsDir, o.Task+"."+attempt+".err"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return Result{}, fmt.Errorf("open %s: %w", filepath.Join(runsDir, o.Task+"."+attempt+".err"), err)
		}
		cmd.Stderr = errFile
		if err := cmd.Start(); err != nil {
			startNote = clipNote(fmt.Sprintf("start %s: %v", bin, err))
			return Result{}, fmt.Errorf("start %s: %w", bin, err)
		}
		killChild = func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		}
	}

	// Start check: no stdout line within the timeout is a silent run. The
	// watchdog is stopped when the first line arrives; if Stop reports the
	// timer already fired, the run is silent.
	var silent atomic.Bool
	watchdog = time.AfterFunc(timeout, func() {
		silent.Store(true)
		killChild()
	})

	// Stall check, armed once the run has started (issue #85): a run-file gap
	// of stallDur with the process still alive is stopped the same way a
	// silent start is, but recorded as reason=stalled, not silent. Both timers
	// are reset on every line so they measure the gap since the last line, not
	// since the run started; noticeOnce keeps the half-timeout warning to one
	// line even if the gap recurs later in the same run.
	var stalled atomic.Bool
	var stallTimer, halfTimer *time.Timer
	var noticeOnce sync.Once
	noticeLine := fmt.Sprintf("%s %s long step: no output for %ds; the stall timeout fires at %ds",
		o.Task, attempt, int(halfDur.Seconds()), int(stallDur.Seconds()))

	if worker.Adapter == "sim" {
		if o.SimDelay > 0 {
			time.Sleep(o.SimDelay)
		}
		src := worker.Model
		if !filepath.IsAbs(src) {
			src = filepath.Join(dir, src)
		}
		fixture, err = os.OpenFile(src, os.O_RDONLY, 0)
		if err != nil {
			return Result{}, fmt.Errorf("open fixture %s: %w", src, err)
		}
		stream = fixture
	}

	sc := bufio.NewScanner(stream)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	started := false
	planRecorded := false
	noPlanRecorded := false
	offCourseRecorded := false
	outsideSeen := map[string]bool{}
	var outsideOrder []string
	wroteSeen := map[string]bool{}
	var wroteOrder []string
	lastText := ""
	lastReason := ""
	seenError := false
	steps := 0
	session := ""
	var tok Tokens
	peak := 0
	cost := 0.0

	// recordPlan records a worker_plan event (and the plan file) when text
	// holds the PLAN check-in.
	recordPlan := func(text string) error {
		plan, isPlan := planText(text)
		if !isPlan {
			return nil
		}
		planRecorded = true
		planPath := filepath.Join(runsDir, o.Task+"."+attempt+".plan.md")
		if err := os.WriteFile(planPath, []byte(plan), 0o644); err != nil {
			return err
		}
		planSum := sha256.Sum256([]byte(plan))
		if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "worker_plan", Path: ".flywheel/runs/" + o.Task + "." + attempt + ".plan.md", SHA256: hex.EncodeToString(planSum[:])}); err != nil {
			return err
		}
		progress(o.Progress, o.Task+" "+attempt+" plan recorded")
		return nil
	}

	firstLine := true
	for sc.Scan() {
		if silent.Load() || stalled.Load() {
			break
		}
		line := sc.Bytes()
		if firstLine {
			// The start check ends when the first line arrives. If Stop
			// reports the timer already fired, the run is silent. Otherwise
			// the stall timers arm now, counting from this first line.
			firstLine = false
			if !watchdog.Stop() {
				silent.Store(true)
				break
			}
			stallTimer = time.AfterFunc(stallDur, func() {
				stalled.Store(true)
				killChild()
			})
			halfTimer = time.AfterFunc(halfDur, func() {
				noticeOnce.Do(func() { progress(o.Stderr, noticeLine) })
			})
		} else {
			stallTimer.Reset(stallDur)
			halfTimer.Reset(halfDur)
		}
		if _, err := runFile.Write(line); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", filepath.Join(runsDir, runName), err)
		}
		if _, err := runFile.Write([]byte("\n")); err != nil {
			return Result{}, fmt.Errorf("write %s: %w", filepath.Join(runsDir, runName), err)
		}
		_, _ = hasher.Write(line)
		_, _ = hasher.Write([]byte("\n"))
		obs, ok := adap.Parse(line)
		if !ok {
			continue
		}
		// A plan is recognised BEFORE the turn it arrives on is counted: the
		// claude adapter marks each assistant message (text included) as
		// ending a turn, so a plan in the 20th message must not first trip
		// the no-plan threshold (issue #284 review). A tool observation's
		// Text — the claude adapter's text written beside a tool call, where a
		// model puts its plan before its first tool (issue #360) — is checked
		// the same way.
		if (obs.Kind == "text" || (obs.Kind == "tool" && obs.Text != "")) && !planRecorded {
			if err := recordPlan(obs.Text); err != nil {
				return Result{}, err
			}
		}
		if obs.EndsTurn {
			steps++
			if steps == 20 && !planRecorded && !noPlanRecorded {
				noPlanRecorded = true
				if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "no-plan", Attempt: attempt}); err != nil {
					return Result{}, err
				}
				if err := recordSignal(dir, o.Task, attempt, session, "no-plan", runRel); err != nil {
					return Result{}, err
				}
				progress(o.Progress, o.Task+" "+attempt+" no-plan (no PLAN by step 20)")
			}
		}
		// Per-message usage on non-step observations (the claude adapter's
		// tool and text lines) feeds only the per-call reasoning peak; totals
		// come from step observations, so nothing is counted twice (#286).
		if obs.Kind != "step" && obs.Tokens != nil && obs.Tokens.Reasoning > peak {
			peak = obs.Tokens.Reasoning
		}
		switch obs.Kind {
		case "start":
			if !started {
				started = true
				session = obs.Session
				if session != "" {
					if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "started", Session: session}); err != nil {
						return Result{}, err
					}
					progress(o.Progress, o.Task+" "+attempt+" started "+session)
				}
			}
		case "text":
			lastText = obs.Text
		case "tool":
			if !offCourseRecorded && offCourseTools[obs.Tool] && isOutsideWorktree(dir, obs.Path) && !outsideSeen[obs.Path] {
				outsideSeen[obs.Path] = true
				outsideOrder = append(outsideOrder, obs.Path)
				if len(outsideOrder) == 5 {
					offCourseRecorded = true
					note := clipNote(strings.Join(outsideOrder, ", "))
					if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "off-course", Attempt: attempt, Note: note}); err != nil {
						return Result{}, err
					}
					if err := recordSignal(dir, o.Task, attempt, session, "off-course", runRel); err != nil {
						return Result{}, err
					}
					progress(o.Progress, o.Task+" "+attempt+" off-course (paths outside the worktree)")
				}
			}
			if obs.Tool == "edit" || obs.Tool == "write" || obs.Tool == "delete" {
				for _, p := range obsPaths(obs) {
					if p != "" && !wroteSeen[p] && len(wroteOrder) < 50 {
						wroteSeen[p] = true
						wroteOrder = append(wroteOrder, p)
					}
				}
			}
		case "step":
			if obs.Reason != "" {
				lastReason = obs.Reason
			}
			if obs.Tokens != nil {
				tok.Input += obs.Tokens.Input
				tok.Output += obs.Tokens.Output
				tok.Reasoning += obs.Tokens.Reasoning
				tok.CacheRead += obs.Tokens.CacheRead
				tok.CacheWrite += obs.Tokens.CacheWrite
				if !obs.Aggregate && obs.Tokens.Reasoning > peak {
					peak = obs.Tokens.Reasoning
				}
			}
			cost += obs.Cost
		case "error":
			seenError = true
			if lastReason == "" {
				lastReason = "error"
			}
		}
		if worker.Adapter == "sim" && o.SimLineDelay > 0 {
			time.Sleep(o.SimLineDelay)
		}
	}
	watchdog.Stop()
	if stallTimer != nil {
		stallTimer.Stop()
	}
	if halfTimer != nil {
		halfTimer.Stop()
	}
	stopRenewer()

	// wrote is the distinct edit/write paths collected during the stream,
	// sorted for a stable finished-event field (issue #163).
	wrote := append([]string(nil), wroteOrder...)
	sort.Strings(wrote)

	// Silent: no output within the start timeout; we already killed the process
	// we started. Every return path records a finished event.
	if silent.Load() {
		runFile.Close()
		if errFile != nil {
			errFile.Close()
		}
		if fixture != nil {
			fixture.Close()
		}
		if cmd != nil {
			_ = cmd.Wait()
		}
		errRel := ".flywheel/runs/" + o.Task + "." + attempt + ".err"
		note := firstStderrLine(filepath.Join(runsDir, o.Task+"."+attempt+".err"))
		runSHA := hex.EncodeToString(hasher.Sum(nil))
		gitWrote, gitNote := gitWriteNote(wt, histBefore, histOK)
		if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "finished", Attempt: attempt, Model: model, Reason: "silent", Note: joinNote(note, gitNote), SHA256: runSHA, Wrote: wrote}); err != nil {
			return Result{}, err
		}
		if err := recordSignal(dir, o.Task, attempt, session, "silent", runRel); err != nil {
			return Result{}, err
		}
		if gitWrote {
			if err := flagGitWrite(dir, o.Task, attempt, session, runRel, gitNote, o.Progress); err != nil {
				return Result{}, err
			}
		}
		line := o.Task + " " + attempt + " finished rc=-1 reason=silent model=" + model
		if note != "" {
			line += " note=" + note
		}
		line += " err=" + errRel
		progress(o.Progress, line)
		if wl := wroteProgressLine(o.Task, attempt, "silent", wrote); wl != "" {
			progress(o.Progress, wl)
		}
		_ = RemoveLease(dir, o.Task, attempt)
		_, _ = WriteState(dir)
		return Result{Attempt: attempt, RC: -1, Reason: "silent"}, nil
	}

	// Stalled: the run file stopped growing for stallDur while the process was
	// still alive; the stall timer already killed it. steps holds the last
	// completed step, same as any other mid-stream stop.
	if stalled.Load() {
		runFile.Close()
		if errFile != nil {
			errFile.Close()
		}
		if fixture != nil {
			fixture.Close()
		}
		if cmd != nil {
			_ = cmd.Wait()
		}
		runSHA := hex.EncodeToString(hasher.Sum(nil))
		gitWrote, gitNote := gitWriteNote(wt, histBefore, histOK)
		if err := AppendEvent(dir, Event{
			TS: "", Task: o.Task, Kind: "finished", Session: session, Attempt: attempt,
			Model: model, Reason: "stalled", Note: gitNote, Steps: steps, SHA256: runSHA, Wrote: wrote,
		}); err != nil {
			return Result{}, err
		}
		if err := recordSignal(dir, o.Task, attempt, session, "stalled", runRel); err != nil {
			return Result{}, err
		}
		if gitWrote {
			if err := flagGitWrite(dir, o.Task, attempt, session, runRel, gitNote, o.Progress); err != nil {
				return Result{}, err
			}
		}
		progress(o.Progress, fmt.Sprintf("%s %s finished rc=-1 reason=stalled model=%s steps=%d", o.Task, attempt, model, steps))
		if wl := wroteProgressLine(o.Task, attempt, "stalled", wrote); wl != "" {
			progress(o.Progress, wl)
		}
		_ = RemoveLease(dir, o.Task, attempt)
		_, _ = WriteState(dir)
		return Result{Attempt: attempt, Session: session, RC: -1, Reason: "stalled", Steps: steps}, nil
	}

	if errFile != nil {
		errFile.Close()
	}
	if fixture != nil {
		fixture.Close()
	}
	runFile.Close()
	rc := 0
	if cmd != nil {
		_ = cmd.Wait()
		if cmd.ProcessState != nil {
			rc = cmd.ProcessState.ExitCode()
		}
	}

	// A worker that exits before any completed step is start-failed: record
	// the first nonempty stderr line (trimmed, at most 200 characters) as the
	// note. Provider errors keep the "error" reason and a silent run already
	// returned above. The reason is final before the report/partial split
	// below, so both branches see it.
	note := ""
	reason := lastReason
	if seenError {
		reason = "error"
	} else if steps == 0 {
		reason = "start-failed"
		note = firstStderrLine(filepath.Join(runsDir, o.Task+"."+attempt+".err"))
	}

	// A clean stop records the reply as the worker's report, as always. Any
	// other reason (length, error, start-failed, ...) means the reply is a
	// mid-thought cut off, not a report: keep it as a partial file and record
	// no report event, so a cut-off run never reads as done.
	if reason == "stop" {
		if lastText != "" {
			reportPath := filepath.Join(runsDir, o.Task+"."+attempt+".report.md")
			if err := os.WriteFile(reportPath, []byte(lastText), 0o644); err != nil {
				return Result{}, err
			}
			reportSum := sha256.Sum256([]byte(lastText))
			if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "report", Path: ".flywheel/runs/" + o.Task + "." + attempt + ".report.md", SHA256: hex.EncodeToString(reportSum[:])}); err != nil {
				return Result{}, err
			}
			progress(o.Progress, o.Task+" "+attempt+" report recorded")
		}
	} else if lastText != "" {
		partialPath := filepath.Join(runsDir, o.Task+"."+attempt+".partial.md")
		if err := os.WriteFile(partialPath, []byte(lastText), 0o644); err != nil {
			return Result{}, err
		}
		partialRel := ".flywheel/runs/" + o.Task + "." + attempt + ".partial.md"
		progress(o.Progress, fmt.Sprintf("%s %s partial reply kept at %s (reason=%s)", o.Task, attempt, partialRel, reason))
	}

	rcPtr := new(int)
	*rcPtr = rc
	tokPtr := new(Tokens)
	*tokPtr = tok
	runSHA := hex.EncodeToString(hasher.Sum(nil))

	// Did the worker change git history? Every exit path runs the same check
	// before its finished event (issue #314, #318 review).
	gitWrote, gitNote := gitWriteNote(wt, histBefore, histOK)
	note = joinNote(note, gitNote)

	if err := AppendEvent(dir, Event{
		TS: "", Task: o.Task, Kind: "finished", Session: session, Attempt: attempt, Model: model,
		RC: rcPtr, Reason: reason, Note: note, Steps: steps, Tokens: tokPtr, Cost: cost, SHA256: runSHA,
		PeakReasoning: peak, Wrote: wrote,
	}); err != nil {
		return Result{}, err
	}

	if gitWrote {
		if err := flagGitWrite(dir, o.Task, attempt, session, runRel, gitNote, o.Progress); err != nil {
			return Result{}, err
		}
	}

	switch reason {
	case "length":
		if err := recordSignal(dir, o.Task, attempt, session, "capped", runRel); err != nil {
			return Result{}, err
		}
	case "error":
		if err := recordSignal(dir, o.Task, attempt, session, "provider-error", runRel); err != nil {
			return Result{}, err
		}
	}
	progress(o.Progress, o.Task+" "+attempt+fmt.Sprintf(" finished rc=%d reason=%s model=%s steps=%d tokens=%s cost=$%s", rc, reason, model, steps, tokensK(tok), costK(cost)))
	if wl := wroteProgressLine(o.Task, attempt, reason, wrote); wl != "" {
		progress(o.Progress, wl)
	}
	if reason == "length" {
		progress(o.Progress, fmt.Sprintf("%s %s hint: reason=length peak=%s reasoning tokens in one step; split files into named parts, use smaller increments, or try another variant", o.Task, attempt, tokensK(Tokens{Reasoning: peak})))
	}
	_ = RemoveLease(dir, o.Task, attempt)
	_, _ = WriteState(dir)
	return Result{Attempt: attempt, Session: session, RC: rc, Reason: reason, Steps: steps, Tokens: tokPtr, Cost: cost}, nil
}

// gateContention describes one shared-gate finding at dispatch: this task's
// gate-th gate line is byte-identical to one declared by an in-flight task,
// so that unit and this one will run it at once and contend on it (issue
// #223).
type gateContention struct {
	gate  int // 1-based index of this task's gate line
	units int // number of in-flight tasks declaring the identical line
}

// dispatchGates returns the gate lines the dispatch about to start will run,
// for the shared-gate comparison (issue #223): the prompt header's gates on a
// fresh dispatch — the base brief's own, edited or not, since that is the
// prompt sent — and on a correction the delta's gates when the delta declares
// any, otherwise the base brief's. The last branch matches AttemptBrief's
// merge rule exactly: a delta's gates replace the base gates when it declares
// any, and the base gates stand when it declares none. The base brief is
// resolved the same way AttemptBrief resolves it, from the latest
// planned-or-amended event's recorded header when it carries one, the file at
// its path otherwise (issue #259). Deriving the set from the prompt about to
// be sent rather than from AttemptBrief's resolution of the previous attempt
// keeps the warning about the command the worker will actually run.
func dispatchGates(dir string, events []Event, task string, correction bool, prompt BriefHeader) []string {
	if !correction || len(prompt.Gates) > 0 {
		return prompt.Gates
	}
	basePath, _, baseEv := latestBaseBriefAndAttempt(events, task)
	if basePath == "" {
		return nil
	}
	base, err := briefHeaderAt(dir, basePath, baseEv)
	if err != nil {
		return nil
	}
	return base.Gates
}

// gateContentions returns one entry per gate line this task declares that an
// in-flight task's brief declares byte-identically, in gate order, or nil.
// In-flight means a derived status of dispatched or running, the same window
// the owns and exclusive checks guard: that worker is still executing, so it
// is still running the gate. A task whose brief cannot be read contributes
// nothing, exactly as it does for owns. The comparison is deliberately
// byte-identical: normalising or parsing the shell command to detect
// "similar" gates is a guess, and a wrong warning is worse than none.
func gateContentions(dir string, events []Event, task string, gates []string) []gateContention {
	if len(gates) == 0 {
		return nil
	}
	st := Derive(events)
	status := make(map[string]string, len(st.Tasks))
	for _, ts := range st.Tasks {
		status[ts.ID] = ts.Status
	}
	var out []gateContention
	for i, gate := range gates {
		units := 0
		for _, other := range inFlightOwners(events, task) {
			switch status[other] {
			case "dispatched", "running":
			default:
				continue
			}
			header, _, err := AttemptBrief(dir, events, other)
			if err != nil {
				continue
			}
			for _, g := range header.Gates {
				if g == gate {
					units++
					break
				}
			}
		}
		if units > 0 {
			out = append(out, gateContention{gate: i + 1, units: units})
		}
	}
	return out
}

// gateContentionLine renders one shared-gate warning the way the operator
// must read it: which gate line, and how many in-flight units will run it at
// once (issue #223).
func gateContentionLine(g gateContention) string {
	return fmt.Sprintf("gate %d is shared with %d in-flight units; they will contend", g.gate, g.units)
}

// ownsCollision describes one owns: overlap between the task being dispatched
// and an in-flight task: the other task, its derived status, and the
// colliding paths.
type ownsCollision struct {
	task   string
	status string
	paths  []string
}

// acquireDispatchLock takes the repository-scoped dispatch lock, held across
// "read the log -> run the collision checks -> append dispatched" (issue
// #242). The mechanics — the O_EXCL create, the token-checked release, the
// heartbeat and the stale takeover — are the shared repository lock in
// lock.go; this wrapper names the dispatch.lock file and keeps its own
// default timings (Run's values and behaviour are unchanged).
func acquireDispatchLock(dir string) (release func(), err error) {
	return acquireRepoLock(dir, "dispatch.lock", defaultRepoLockTimings())
}

// briefDrift is a dispatch-time finding that the brief on disk differs from
// the content hash a previous dispatch recorded (issue #135).
type briefDrift struct {
	attempt string // the attempt whose dispatched hash differs from the brief on disk
}

// checkBriefDrift reports whether the brief on disk at brief — a task's base
// brief path — has drifted from the hash its last dispatch recorded, with no
// later planned event re-recording the brief's current content. The hash
// compared is the last FRESH dispatch's (r*): a correction dispatches its
// delta, not the brief, so its recorded hash is a delta hash that must never
// read as the brief's (ruleT1 compares the same way). A task with no previous
// dispatch cannot drift. A planned event recorded after that dispatch whose
// brief file hashes to the brief's current content means a lead legitimately
// re-planned; that stays silent. An unreadable brief never drifts — the
// dispatch itself fails on it shortly after.
func checkBriefDrift(dir string, events []Event, task, brief string) *briefDrift {
	last := -1
	lastAttempt := ""
	lastHash := ""
	for i, e := range events {
		if e.Task != task || e.Kind != "dispatched" || e.SHA256 == "" || isCorrection(e.Attempt) {
			continue
		}
		last = i
		lastAttempt = e.Attempt
		lastHash = e.SHA256
	}
	if last < 0 || lastHash == "" {
		return nil
	}
	b, err := os.ReadFile(resolveBriefPath(dir, brief))
	if err != nil {
		return nil
	}
	current := contentSHA(b)
	if current == lastHash {
		return nil
	}
	for _, e := range events[last+1:] {
		if e.Task != task || e.Kind != "planned" || e.Brief == "" {
			continue
		}
		if pb, err := os.ReadFile(resolveBriefPath(dir, e.Brief)); err == nil && contentSHA(pb) == current {
			return nil
		}
	}
	return &briefDrift{attempt: lastAttempt}
}

// ownsCollisionWith returns the first owns: overlap between owns and an
// in-flight task's owns:, or nil. In-flight means a derived status of
// dispatched or running: that worker is still writing, so an overlapping
// owns: list can silently lose an edit (issue #164). A task with no planned
// brief, or one whose brief cannot be read, is skipped rather than erroring:
// an unreadable brief must never block a dispatch. Two owns: entries collide
// when they are equal, when either is a directory prefix (trailing /)
// containing the other, or when either matches the other as a shell pattern —
// the same matching rule ownsContains applies, checked in both directions so
// the relation is symmetric. The colliding paths are the entries involved,
// deduplicated and sorted.
func ownsCollisionWith(dir string, events []Event, task string, owns []string) *ownsCollision {
	st := Derive(events)
	status := make(map[string]string, len(st.Tasks))
	for _, ts := range st.Tasks {
		status[ts.ID] = ts.Status
	}
	for _, other := range inFlightOwners(events, task) {
		switch status[other] {
		case "dispatched", "running":
		default:
			continue
		}
		header, _, err := AttemptBrief(dir, events, other)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		var paths []string
		add := func(p string) {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
		for _, a := range owns {
			if ownsContains(header.Owns, a) {
				add(a)
			}
		}
		for _, b := range header.Owns {
			if ownsContains(owns, b) {
				add(b)
			}
		}
		if len(paths) == 0 {
			continue
		}
		sort.Strings(paths)
		return &ownsCollision{task: other, status: status[other], paths: paths}
	}
	return nil
}

// exclusiveCollision describes one exclusive: overlap between the task being
// dispatched and an in-flight task: the other task, its derived status, and
// the colliding resource name.
type exclusiveCollision struct {
	task   string
	status string
	name   string
}

// exclusiveCollisionWith returns the first exclusive: overlap between names
// and an in-flight task's exclusive: names, or nil. In-flight means a derived
// status of dispatched or running, the same window ownsCollisionWith guards:
// that worker is still running, so it still holds the resource (issue #220).
// A task with no planned brief, or one whose brief cannot be read, is skipped
// rather than erroring, exactly like the owns check. Names compare exactly,
// case-sensitively, after trimming surrounding whitespace.
func exclusiveCollisionWith(dir string, events []Event, task string, names []string) *exclusiveCollision {
	held := make(map[string]bool, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			held[n] = true
		}
	}
	if len(held) == 0 {
		return nil
	}
	st := Derive(events)
	status := make(map[string]string, len(st.Tasks))
	for _, ts := range st.Tasks {
		status[ts.ID] = ts.Status
	}
	for _, other := range inFlightOwners(events, task) {
		switch status[other] {
		case "dispatched", "running":
		default:
			continue
		}
		header, _, err := AttemptBrief(dir, events, other)
		if err != nil {
			continue
		}
		for _, n := range header.Exclusive {
			if n = strings.TrimSpace(n); n != "" && held[n] {
				return &exclusiveCollision{task: other, status: status[other], name: n}
			}
		}
	}
	return nil
}

// dispatchedNote builds the dispatched event's note: the policy sha256 always,
// and the owns-overlap record when --allow-overlap crossed an in-flight task's
// owns: so a deliberate overlap stays visible in the ledger (issue #164). An
// exclusive resource crossed under --allow-overlap is recorded the same way
// (issue #220), and every shared gate (issue #223) is recorded the same way
// too: the operator must see in the ledger which gates the dispatched unit
// will contend on, even though the dispatch is never refused for them.
func dispatchedNote(policySHA string, overlap *ownsCollision, excl *exclusiveCollision, gates []gateContention) string {
	note := "policy sha256=" + policySHA
	if overlap != nil {
		note += "; owns-overlap: " + strings.Join(overlap.paths, ", ") + " with " + overlap.task
	}
	if excl != nil {
		note += "; exclusive-overlap: " + excl.name + " with " + excl.task
	}
	for _, g := range gates {
		note += "; shared-gate: " + gateContentionLine(g)
	}
	return note
}

// progress writes a run transition line to w, when w is set.
func progress(w io.Writer, line string) {
	if w != nil {
		fmt.Fprintln(w, line)
	}
}

// recordSignal appends one signal event naming a run condition already
// detected and recorded under its own kind (issue #37): the same attempt, the
// session when known, and the run file as Path so the evidence is one field
// away. The caller records it after the event that detected the condition, so
// the log reads in causal order, and only once per condition per attempt.
// flagGitWrite records the git-write signal for an attempt whose git history
// changed and says so on the progress stream (issue #314).
func flagGitWrite(dir, task, attempt, session, runRel, note string, w io.Writer) error {
	if err := recordSignal(dir, task, attempt, session, "git-write", runRel); err != nil {
		return err
	}
	progress(w, task+" "+attempt+" git-write: "+note+"; workers never commit, stash, reset, checkout or push")
	return nil
}

func recordSignal(dir, task, attempt, session, condition, runRel string) error {
	return AppendEvent(dir, Event{
		TS: "", Task: task, Kind: "signal", Signal: condition,
		Attempt: attempt, Session: session, Path: runRel,
	})
}

// ExitCode maps a result to the CLI exit code: 0 when the worker exited 0
// with reason stop, 4 when it exited nonzero or ended capped or with an
// error, 3 on a start timeout, 7 on a mid-stream stall.
func ExitCode(r Result) int {
	if r.Reason == "silent" {
		return 3
	}
	if r.Reason == "stalled" {
		return 7
	}
	if r.RC == 0 && r.Reason == "stop" {
		return 0
	}
	return 4
}

// promptSource resolves the absolute path of the file to attach: the --delta
// file when one is given, the default .flywheel/briefs/<task>.delta.txt on a
// resume, otherwise the planned brief.
func promptSource(dir, brief, delta, task string, resume bool) (string, error) {
	src := brief
	if delta != "" || resume {
		src = delta
		if src == "" {
			src = filepath.Join(dir, ".flywheel", "briefs", task+".delta.txt")
		}
	}
	if !filepath.IsAbs(src) {
		src = filepath.Join(dir, src)
	}
	return filepath.Abs(src)
}

// promptBrief returns the prompt path to record in dispatched.Brief: a
// repo-relative slash path when the prompt lives inside dir, otherwise the
// absolute external path unchanged.
func promptBrief(dir, src string) string {
	if abs, aerr := filepath.Abs(dir); aerr == nil {
		if rel, err := filepath.Rel(abs, src); err == nil {
			rel = filepath.ToSlash(rel)
			if rel != ".." && !strings.HasPrefix(rel, "../") {
				return rel
			}
		}
	}
	return src
}

// offCourseTools is the set of read-only tools whose Path can point at
// library source outside the worktree: read, grep and glob (issue #72).
var offCourseTools = map[string]bool{"read": true, "grep": true, "glob": true}

// isOutsideWorktree reports whether p, from a tool_use observation, names a
// location outside the worktree rooted at dir: an absolute path that is not
// under dir's absolute path (compared case-insensitively on Windows, where
// paths are case-insensitive), or a relative path starting with "..". An
// empty path is never outside.
func isOutsideWorktree(dir, p string) bool {
	if p == "" {
		return false
	}
	if !isRootedPath(p) {
		rel := filepath.ToSlash(p)
		return rel == ".." || strings.HasPrefix(rel, "../")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	a := strings.TrimSuffix(filepath.ToSlash(absDir), "/")
	q := strings.TrimSuffix(filepath.ToSlash(p), "/")
	if runtime.GOOS == "windows" {
		a = strings.ToLower(a)
		q = strings.ToLower(q)
	}
	return q != a && !strings.HasPrefix(q, a+"/")
}

// isRootedPath reports whether p is rooted: recognized as absolute by
// filepath.IsAbs, or leading with a path separator. A tool call can report a
// POSIX-style path (leading "/") verbatim even on a Windows host — e.g. one
// built from a Git Bash temp dir — where filepath.IsAbs does not consider it
// absolute; such a path is still rooted outside the worktree.
func isRootedPath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`)
}

// otherWorktrees snapshots every OTHER worktree in the git repo containing
// dir (git worktree list --porcelain, read-only): for each, its changed
// paths and file shas, the same read-only changedPaths/fileSHA the baseline
// uses (issue #87). Nil when dir is not inside a git repo, or the repo has
// no other worktrees.
func otherWorktrees(dir string) map[string]map[string]string {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	rc, out, _, err := runCmdSplit(dir, gitArgs([]string{"worktree", "list", "--porcelain"}), nil)
	if err != nil || rc != 0 {
		return nil
	}
	var others []string
	for _, line := range strings.Split(string(out), "\n") {
		p, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		p = strings.TrimSpace(p)
		if p == "" || samePath(p, absDir) {
			continue
		}
		others = append(others, p)
	}
	if len(others) == 0 {
		return nil
	}
	snap := map[string]map[string]string{}
	for _, p := range others {
		changed, err := changedPaths(p)
		if err != nil {
			continue
		}
		files := map[string]string{}
		for _, cp := range changed {
			files[cp] = fileSHA(p, cp)
		}
		snap[p] = files
	}
	if len(snap) == 0 {
		return nil
	}
	return snap
}

// samePath reports whether a and b name the same location. Each is resolved
// with resolvePath first, so a symlinked temp root — macOS's /var ->
// /private/var, or an equivalent form a Windows CI runner reports — does not
// read as a different worktree from the one git itself is rooted at (issue
// #87); comparison is case-insensitive on Windows, the same way
// isOutsideWorktree compares paths.
func samePath(a, b string) bool {
	a = resolvePath(a)
	b = resolvePath(b)
	if runtime.GOOS == "windows" {
		a = strings.ToLower(a)
		b = strings.ToLower(b)
	}
	return a == b
}

// resolvePath makes p absolute and, when possible, resolves symlinks in it
// (filepath.EvalSymlinks); a path that cannot be resolved (for instance, one
// that does not exist) falls back to its absolute form, and a path that
// cannot even be made absolute is returned unchanged. The result is
// normalised to forward slashes with any trailing slash trimmed, so it can be
// compared directly.
func resolvePath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return strings.TrimSuffix(filepath.ToSlash(p), "/")
}

// planText returns the worker's plan from one text message, and whether the
// message carries one (issue #284): the text from the first line that, once
// leading whitespace, list and quote markers (-, *, +, >, #) and emphasis (*, _,
// `) are stripped, starts with "PLAN ". Markdown-formatted plans (**PLAN
// files-to-read:** ...) and plans preceded by a sentence of prose are
// recognised; a message that only mentions a plan is not.
func planText(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		t := strings.TrimLeft(line, " \t-*+>#_`")
		if strings.HasPrefix(t, "PLAN ") {
			stripped := strings.TrimLeft(line, " \t-+>#")
			result := stripped
			if len(lines) > i+1 {
				result = stripped + "\n" + strings.Join(lines[i+1:], "\n")
			}
			return result, true
		}
	}
	return "", false
}

// wroteProgressLine returns the extra progress line naming the files an
// unclean attempt wrote before failing, or "" when the finish was clean
// (reason "stop") or wrote nothing (issue #163).
func wroteProgressLine(task, attempt, reason string, wrote []string) string {
	if reason == "stop" || len(wrote) == 0 {
		return ""
	}
	paths := clipNote(strings.Join(wrote, ", "))
	return fmt.Sprintf("%s %s wrote %d file(s) before failing: %s", task, attempt, len(wrote), paths)
}

// clipNote trims s and caps it at 200 characters for a finished note.
func clipNote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200]
	}
	return s
}

// firstStderrLine returns the first nonempty line of the stderr file at path,
// trimmed and capped at 200 characters, or "" when the file is missing or
// empty.
func firstStderrLine(path string) string {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			return clipNote(line)
		}
	}
	return ""
}

// workerPolicySHA writes the embedded OpenCode permission policy to
// .flywheel/opencode-worker.json when missing (never overwriting an existing
// file) and returns the policy file's SHA-256 hex.
func workerPolicySHA(dir string) (string, error) {
	path := filepath.Join(dir, ".flywheel", "opencode-worker.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(workerPermissionPolicy), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", path, err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// writeWorkerRules writes workerRules to .flywheel/worker-rules.md when
// missing, never overwriting an existing file — the same way workerPolicySHA
// writes the permission policy (issue #31).
func writeWorkerRules(dir string) error {
	path := filepath.Join(dir, ".flywheel", "worker-rules.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(workerRules), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

// workerEnv is the environment for an opencode child: the current environment
// plus OPENCODE_CONFIG pointing at the worker permission policy as an
// absolute path (the child's working directory is dir, so a relative path
// would be resolved against dir and point at the wrong file).
func workerEnv(dir string) []string {
	pol := filepath.Join(dir, ".flywheel", "opencode-worker.json")
	if abs, aerr := filepath.Abs(pol); aerr == nil {
		pol = abs
	}
	return append(os.Environ(), "OPENCODE_CONFIG="+pol)
}

// attemptNum parses the digits of an attempt id: r12 -> 12.
func attemptNum(s string) int {
	n := 0
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// tokensK renders the billed token sum (input+output+reasoning) with a k
// suffix above 1000.
func tokensK(t Tokens) string {
	n := t.Input + t.Output + t.Reasoning
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// costK renders the human-line cost with 4 decimal places: the 5th decimal
// rounds the 4th (0.0015970879999999998 -> 0.0016) but never carries past
// it (1.23456 -> 1.2345); zero stays "0".
func costK(c float64) string {
	if c == 0 {
		return "0"
	}
	return fmt.Sprintf("%.4f", math.Floor(math.Round(c*1e5)/10)/1e4)
}

// briefHasIncrement reports whether a brief defines increment n (issue #83):
// a heading naming "Increment <n>", or, inside a section whose heading
// contains "Increments", a list item numbered n ("n." or "n)"). The section
// runs until the next heading of the same or a higher level.
func briefHasIncrement(text string, n int) bool {
	num := strconv.Itoa(n)
	inSection, level := false, 0
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {

		t := strings.TrimSpace(line)
		if h := len(t) - len(strings.TrimLeft(t, "#")); h > 0 {
			title := strings.ToLower(strings.TrimSpace(t[h:]))
			if title == "increment "+num || strings.HasPrefix(title, "increment "+num+" ") || strings.HasPrefix(title, "increment "+num+":") {
				return true
			}
			if inSection && h <= level {
				inSection = false
			}
			if strings.Contains(title, "increments") {
				inSection, level = true, h
			}
			continue
		}
		if inSection {
			item := strings.TrimLeft(t, "-* ")
			if strings.HasPrefix(item, num+".") || strings.HasPrefix(item, num+")") {
				return true
			}
		}
	}
	return false
}

// breakerOpen reports whether the circuit for model is open at now: its last
// b.Errors finished attempts across the ledger (newest first, by timestamp,
// only events whose Model is model) all ended with reason "error", and the
// newest of them is less than the cooldown ago. It also returns when the
// circuit closes again. If a probed event with Reason "ok" exists for the
// model and its timestamp is after the newest error, the breaker is fully
// closed (not half-open) at once, bypassing the cooldown.
func breakerOpen(events []Event, model string, b Breaker, now time.Time) (open bool, until time.Time) {
	if b.Errors <= 0 {
		return false, time.Time{}
	}
	cooldown, err := b.CooldownDuration()
	if err != nil {
		cooldown = 10 * time.Minute
	}
	type finish struct {
		at     time.Time
		reason string
	}
	// The newest ok probe of the model is a reset boundary (issue #46, #334
	// review): the circuit it closed counts only the finishes after it, so a
	// single later error cannot reopen it on the strength of older ones.
	var probeAt time.Time
	for _, e := range events {
		if e.Kind != "probed" || e.Model != model || e.Reason != "ok" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(probeAt) {
			probeAt = t
		}
	}
	var finished []finish
	for _, e := range events {
		if e.Kind != "finished" || e.Model != model {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(probeAt) {
			finished = append(finished, finish{at: t, reason: e.Reason})
		}
	}
	if len(finished) < b.Errors {
		return false, time.Time{}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].at.After(finished[j].at) })
	for _, f := range finished[:b.Errors] {
		if f.reason != "error" {
			return false, time.Time{}
		}
	}
	newestError := finished[0].at
	until = newestError.Add(cooldown)
	if now.Before(until) {
		return true, until
	}
	// Half-open: the cooldown is over, but only ONE probe may run. A
	// dispatch of this model after the newest error whose attempt has not
	// finished yet is that probe; until it finishes, the breaker stays open
	// (#304 review). Dispatch holds the dispatch lock, so a second caller
	// always sees the first probe's dispatched event.
	done := map[string]bool{}
	for _, e := range events {
		if e.Kind == "finished" {
			done[e.Task+"/"+e.Attempt] = true
		}
	}
	for _, e := range events {
		if e.Kind != "dispatched" || e.Model != model || done[e.Task+"/"+e.Attempt] {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && t.After(newestError) {
			return true, until
		}
	}
	return false, until
}

// breakerFallback returns the first of w's approved fallbacks (config order)
// whose own breaker is closed at now, or "" when there is none (issue #46).
func breakerFallback(events []Event, w Worker, b Breaker, now time.Time) string {
	for _, f := range w.Fallbacks {
		if !f.Approved || f.Model == w.Model {
			continue
		}
		if open, _ := breakerOpen(events, f.Model, b, now); !open {
			return f.Model
		}
	}
	return ""
}

// approvedFallbackHint returns a string listing approved fallbacks from w,
// formatted as " (approved fallbacks: a, b)" or "" when there are none.
func approvedFallbackHint(w Worker) string {
	var approved []string
	for _, f := range w.Fallbacks {
		if f.Approved {
			approved = append(approved, f.Model)
		}
	}
	if len(approved) == 0 {
		return ""
	}
	return fmt.Sprintf(" (approved fallbacks: %s)", strings.Join(approved, ", "))
}

// recordedTokens sums input, output and reasoning tokens over finished events.
func recordedTokens(events []Event) int {
	total := 0
	for _, e := range events {
		if e.Kind == "finished" && e.Tokens != nil {
			total += e.Tokens.Input + e.Tokens.Output + e.Tokens.Reasoning
		}
	}
	return total
}

// rateLimited reports whether model already has limit dispatched events in
// the 60 seconds before now, and when the oldest of them leaves the window.
func rateLimited(events []Event, model string, limit int, now time.Time) (bool, time.Time) {
	if limit <= 0 {
		return false, time.Time{}
	}
	window := now.Add(-60 * time.Second)
	var dispatches []time.Time
	for _, e := range events {
		if e.Kind != "dispatched" || e.Model != model {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			continue
		}
		if t.After(window) {
			dispatches = append(dispatches, t)
		}
	}
	if len(dispatches) >= limit {
		sort.Slice(dispatches, func(i, j int) bool { return dispatches[i].Before(dispatches[j]) })
		// A slot opens when enough of them leave the window that fewer
		// than limit remain: the (len-limit)th oldest, 0-based.
		until := dispatches[len(dispatches)-limit].Add(60 * time.Second)
		return true, until
	}
	return false, time.Time{}
}

// lineHeader returns the brief header a dispatch's product line is resolved
// from (issue #69, #340 review): the header of the exact prompt this
// invocation will send — the brief on disk for a fresh attempt, the delta
// for a correction — with the base brief's line and owns filling in what a
// delta leaves out. ok is false when neither can be read (the dispatch then
// fails later with the usual error).
func lineHeader(dir string, o RunOptions) (BriefHeader, bool) {
	events, err := ReadEvents(dir)
	if err != nil {
		return BriefHeader{}, false
	}
	brief, _, _ := latestBaseBriefAndAttempt(events, o.Task)
	if brief == "" {
		return BriefHeader{}, false
	}
	baseSrc, err := promptSource(dir, brief, "", o.Task, false)
	if err != nil {
		return BriefHeader{}, false
	}
	base, baseErr := ParseBriefHeader(baseSrc)
	if o.DeltaPath == "" && !o.Resume {
		return base, baseErr == nil
	}
	src, err := promptSource(dir, brief, o.DeltaPath, o.Task, o.Resume)
	if err != nil {
		return base, baseErr == nil
	}
	h, err := ParseBriefHeader(src)
	if err != nil {
		return base, baseErr == nil
	}
	if h.Line == "" {
		h.Line = base.Line
	}
	if len(h.Owns) == 0 {
		h.Owns = base.Owns
	}
	return h, true
}
