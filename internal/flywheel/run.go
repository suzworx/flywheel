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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RunOptions configures one dispatch. StartTimeout bounds the wait for the
// first stdout line; zero means the default 60s.
type RunOptions struct {
	Task         string
	Worker       string // worker name; empty selects the default worker
	Model        string // override; empty uses the worker's model
	Resume       bool
	DeltaPath    string // correction prompt; on a resume the default is .flywheel/briefs/<task>.delta.txt
	StartTimeout time.Duration
	Progress     io.Writer
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
// .flywheel/opencode-worker.json when missing. It must stay byte-identical to
// skills/flywheel/references/worker-permissions.json: the catch-all allow for
// bash comes FIRST and the git denies follow, and OpenCode applies the last
// matching rule. The user's own opencode.json is never touched.
var workerPermissionPolicy = `{
  "$schema": "https://opencode.ai/config.json",
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
    }
  }
}`

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
// dispatched, started, worker_plan, report and finished events. Every path
// after the dispatched event records a finished event.
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
	model := o.Model
	if model == "" {
		model = worker.Model
	}
	adap, err := AdapterFor(worker.Adapter)
	if err != nil {
		return Result{}, err
	}

	// T1: dispatched needs a planned event carrying the brief path.
	events, err := ReadEvents(dir)
	if err != nil {
		return Result{}, err
	}
	brief, _ := latestBaseBriefAndAttempt(events, o.Task)
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
	promptSrc, err := promptSource(dir, brief, o.DeltaPath, o.Task, o.Resume)
	if err != nil {
		return Result{}, err
	}
	promptBriefField := promptBrief(dir, promptSrc)
	promptB, err := os.ReadFile(promptSrc)
	if err != nil {
		return Result{}, fmt.Errorf("read prompt %s: %w", promptSrc, err)
	}
	prompt := string(promptB)

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

	// Baseline: hash every path dirty at dispatch (the same read-only git
	// commands the owns check uses) so validate can tell this unit's edits
	// from the lead's or another worker's pre-existing ones. Not a git repo:
	// no baseline.
	baseline := computeBaseline(dir)

	if err := AppendEvent(dir, Event{
		TS: "", Task: o.Task, Kind: "dispatched", Attempt: attempt,
		Adapter: worker.Adapter, Model: model, Path: runRel, SHA256: promptSHA,
		Brief: promptBriefField, Note: "policy sha256=" + policySHA,
		Baseline: baseline,
	}); err != nil {
		return Result{}, err
	}
	progress(o.Progress, o.Task+" "+attempt+" dispatched "+worker.Adapter+" "+model)

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
		if aerr := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "finished", Attempt: attempt, Reason: reason, Note: note, SHA256: runSHA}); aerr == nil {
			progress(o.Progress, o.Task+" "+attempt+" finished rc=1 reason="+reason+" note="+note)
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

	// The command request. The session is passed only on a resume: a fresh
	// run must start a new conversation, never continue the previous one.
	sessionArg := ""
	if o.Resume {
		sessionArg = lastSession
	}
	req := RunRequest{
		Task: o.Task, Attempt: attempt, PromptFile: promptSrc, Model: model,
		Variant: worker.Variant, Session: sessionArg, Title: o.Task + "-" + attempt,
		Resume: o.Resume,
	}
	if commandHook != nil {
		commandHook(req)
	}

	if worker.Adapter == "opencode" {
		bin, args := adap.Command(req)
		cmd = exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = workerEnv(dir)
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
	lastText := ""
	lastReason := ""
	seenError := false
	steps := 0
	session := ""
	var tok Tokens
	cost := 0.0

	firstLine := true
	for sc.Scan() {
		if silent.Load() {
			break
		}
		line := sc.Bytes()
		if firstLine {
			// The start check ends when the first line arrives. If Stop
			// reports the timer already fired, the run is silent.
			firstLine = false
			if !watchdog.Stop() {
				silent.Store(true)
				break
			}
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
			if !planRecorded && strings.HasPrefix(obs.Text, "PLAN ") {
				planRecorded = true
				planPath := filepath.Join(runsDir, o.Task+"."+attempt+".plan.md")
				if err := os.WriteFile(planPath, []byte(obs.Text), 0o644); err != nil {
					return Result{}, err
				}
				planSum := sha256.Sum256([]byte(obs.Text))
				if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "worker_plan", Path: ".flywheel/runs/" + o.Task + "." + attempt + ".plan.md", SHA256: hex.EncodeToString(planSum[:])}); err != nil {
					return Result{}, err
				}
				progress(o.Progress, o.Task+" "+attempt+" plan recorded")
			}
			lastText = obs.Text
		case "step":
			steps++
			if obs.Reason != "" {
				lastReason = obs.Reason
			}
			if obs.Tokens != nil {
				tok.Input += obs.Tokens.Input
				tok.Output += obs.Tokens.Output
				tok.Reasoning += obs.Tokens.Reasoning
				tok.CacheRead += obs.Tokens.CacheRead
				tok.CacheWrite += obs.Tokens.CacheWrite
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
	stopRenewer()

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
		runSHA := hex.EncodeToString(hasher.Sum(nil))
		if err := AppendEvent(dir, Event{TS: "", Task: o.Task, Kind: "finished", Attempt: attempt, Reason: "silent", SHA256: runSHA}); err != nil {
			return Result{}, err
		}
		progress(o.Progress, o.Task+" "+attempt+" finished rc=-1 reason=silent")
		_ = RemoveLease(dir, o.Task, attempt)
		_, _ = WriteState(dir)
		return Result{Attempt: attempt, RC: -1, Reason: "silent"}, nil
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

	// A worker that exits before any completed step is start-failed: record
	// the first nonempty stderr line (trimmed, at most 200 characters) as the
	// note. Provider errors keep the "error" reason and a silent run already
	// returned above.
	note := ""
	reason := lastReason
	if seenError {
		reason = "error"
	} else if steps == 0 {
		reason = "start-failed"
		note = firstStderrLine(filepath.Join(runsDir, o.Task+"."+attempt+".err"))
	}
	rcPtr := new(int)
	*rcPtr = rc
	tokPtr := new(Tokens)
	*tokPtr = tok
	runSHA := hex.EncodeToString(hasher.Sum(nil))
	if err := AppendEvent(dir, Event{
		TS: "", Task: o.Task, Kind: "finished", Session: session, Attempt: attempt,
		RC: rcPtr, Reason: reason, Note: note, Steps: steps, Tokens: tokPtr, Cost: cost, SHA256: runSHA,
	}); err != nil {
		return Result{}, err
	}
	progress(o.Progress, o.Task+" "+attempt+fmt.Sprintf(" finished rc=%d reason=%s steps=%d tokens=%s cost=$%s", rc, reason, steps, tokensK(tok), costK(cost)))
	_ = RemoveLease(dir, o.Task, attempt)
	_, _ = WriteState(dir)
	return Result{Attempt: attempt, Session: session, RC: rc, Reason: reason, Steps: steps, Tokens: tokPtr, Cost: cost}, nil
}

// progress writes a run transition line to w, when w is set.
func progress(w io.Writer, line string) {
	if w != nil {
		fmt.Fprintln(w, line)
	}
}

// ExitCode maps a result to the CLI exit code: 0 when the worker exited 0
// with reason stop, 4 when it exited nonzero or ended capped or with an
// error, 3 on a start timeout.
func ExitCode(r Result) int {
	if r.Reason == "silent" {
		return 3
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
