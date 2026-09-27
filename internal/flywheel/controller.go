package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Lock is the controller's single-writer lease on the factory: at most one
// controller process may tick at a time. It is stored in
// .flywheel/controller.lock; the generation counter distinguishes a renewal
// (same generation) from a takeover (generation+1) and lets ReleaseLock
// remove the file only while it still holds our pid and generation.
type Lock struct {
	PID        int    `json:"pid"`
	Host       string `json:"host"`
	Generation int    `json:"generation"`
	StartedAt  string `json:"started_at"`
	RenewedAt  string `json:"renewed_at"`
	ExpiresAt  string `json:"expires_at"`
}

// controllerLockPath returns the controller lock file path.
func controllerLockPath(dir string) string {
	return filepath.Join(dir, ".flywheel", "controller.lock")
}

// LockLive reports whether the lock is still live at now: live while now is
// at or before expires_at. An unparseable expiry is not live.
func LockLive(l Lock, now time.Time) bool {
	exp, err := time.Parse(time.RFC3339, l.ExpiresAt)
	if err != nil {
		return false
	}
	return !now.After(exp)
}

// AcquireLock takes the controller lock for this process, with a bounded
// retry because the fresh-create path races another acquirer:
//   - no file: create it with O_EXCL, generation 1;
//   - a file whose lock has expired at now: take it over with generation+1
//     (temp file plus rename, then read it back and confirm our pid and
//     generation);
//   - a live lock of our own pid: renew it (a new tick of the same process);
//   - a live lock held by another pid: refuse with a RuleRefusal naming the
//     holder, its generation and its expiry, so the command exits 6.
func AcquireLock(dir string, now time.Time, ttl time.Duration) (Lock, error) {
	path := controllerLockPath(dir)
	for attempt := 0; attempt < 5; attempt++ {
		b, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return Lock{}, fmt.Errorf("read %s: %w", path, err)
			}
			l, err := createLockExcl(path, 1, now, ttl)
			if err != nil {
				if os.IsExist(err) {
					continue // another controller won the race; re-read
				}
				return Lock{}, fmt.Errorf("create %s: %w", path, err)
			}
			return l, nil
		}
		var cur Lock
		if uerr := json.Unmarshal(b, &cur); uerr != nil {
			return Lock{}, fmt.Errorf("parse %s: %w", path, uerr)
		}
		if !LockLive(cur, now) {
			return takeOverLock(dir, path, cur, now, ttl)
		}
		if cur.PID != os.Getpid() {
			return Lock{}, &RuleRefusal{Rule: "C1", Fix: fmt.Sprintf(
				"controller lock held by pid %d on %s (generation %d) until %s; wait for it to expire or stop that controller",
				cur.PID, cur.Host, cur.Generation, cur.ExpiresAt)}
		}
		return RenewLock(dir, cur, now, ttl)
	}
	return Lock{}, fmt.Errorf("controller lock %s kept changing while acquiring; try again", path)
}

// createLockExcl writes a fresh lock with generation g using O_EXCL, so two
// controllers cannot both claim the same generation.
func createLockExcl(path string, g int, now time.Time, ttl time.Duration) (Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Lock{}, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	l := freshLock(g, now, ttl)
	b, err := marshalLock(l)
	if err != nil {
		return Lock{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return Lock{}, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(path)
		return Lock{}, fmt.Errorf("write %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return Lock{}, fmt.Errorf("close %s: %w", path, err)
	}
	return l, nil
}

// takeOverLock takes over an expired lock with generation+1: a temp file plus
// rename, then the file is read back and our pid and generation confirmed.
func takeOverLock(dir, path string, cur Lock, now time.Time, ttl time.Duration) (Lock, error) {
	l := freshLock(cur.Generation+1, now, ttl)
	b, err := marshalLock(l)
	if err != nil {
		return Lock{}, err
	}
	if err := atomicWrite(filepath.Join(dir, ".flywheel"), "controller.lock", "controller-*.lock", b); err != nil {
		return Lock{}, err
	}
	back, rerr := os.ReadFile(path)
	if rerr != nil {
		return Lock{}, fmt.Errorf("read back %s: %w", path, rerr)
	}
	var got Lock
	if uerr := json.Unmarshal(back, &got); uerr != nil {
		return Lock{}, fmt.Errorf("parse back %s: %w", path, uerr)
	}
	if got.PID != l.PID || got.Generation != l.Generation {
		return Lock{}, fmt.Errorf("controller lock %s was taken over during takeover", path)
	}
	return got, nil
}

// freshLock builds a Lock for this process with generation g at now.
func freshLock(g int, now time.Time, ttl time.Duration) Lock {
	host, _ := os.Hostname()
	ts := now.UTC().Format(time.RFC3339Nano)
	return Lock{PID: os.Getpid(), Host: host, Generation: g,
		StartedAt: ts, RenewedAt: ts,
		ExpiresAt: now.UTC().Add(ttl).Format(time.RFC3339Nano)}
}

// marshalLock encodes l as one JSON line with a trailing newline.
func marshalLock(l Lock) ([]byte, error) {
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode lock: %w", err)
	}
	return append(b, '\n'), nil
}

// RenewLock moves expires_at forward to now+ttl, only while the file still
// holds lock's pid and generation; a lock that was taken over or removed is
// an error naming the file.
func RenewLock(dir string, l Lock, now time.Time, ttl time.Duration) (Lock, error) {
	path := controllerLockPath(dir)
	b, err := os.ReadFile(path)
	if err != nil {
		return Lock{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cur Lock
	if uerr := json.Unmarshal(b, &cur); uerr != nil {
		return Lock{}, fmt.Errorf("parse %s: %w", path, uerr)
	}
	if cur.PID != l.PID || cur.Generation != l.Generation {
		return Lock{}, fmt.Errorf("controller lock %s changed hands (now pid %d generation %d); cannot renew", path, cur.PID, cur.Generation)
	}
	cur.RenewedAt = now.UTC().Format(time.RFC3339Nano)
	cur.ExpiresAt = now.UTC().Add(ttl).Format(time.RFC3339Nano)
	if err := writeLock(path, cur); err != nil {
		return Lock{}, err
	}
	return cur, nil
}

// writeLock writes l to path via a temp file plus rename.
func writeLock(path string, l Lock) error {
	b, err := marshalLock(l)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Dir(path), filepath.Base(path), "controller-*.lock", b)
}

// ReleaseLock removes the controller lock only while it still holds lock's
// pid and generation; a lock that changed hands or is already gone is left
// alone.
func ReleaseLock(dir string, l Lock) error {
	path := controllerLockPath(dir)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	var cur Lock
	if uerr := json.Unmarshal(b, &cur); uerr != nil {
		return fmt.Errorf("parse %s: %w", path, uerr)
	}
	if cur.PID != l.PID || cur.Generation != l.Generation {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// TickResult is one controller tick's tally: the tick instant, the events
// appended (lost, blocked) and the actions only proposed for later phases
// (inspection requests, waits and dispatches).
type TickResult struct {
	TS       string `json:"ts"`
	Actions  int    `json:"actions"`
	Lost     int    `json:"lost"`
	Blocked  int    `json:"blocked"`
	Proposed int    `json:"proposed"`
	// Resumed are the rate-limited units the tick's auto-resume pass acted on.
	Resumed []SupervisedResume `json:"resumed,omitempty"`
	// ResumeSkipped says why the auto-resume pass did not run this tick
	// (supervise's lock was busy); the next tick tries again.
	ResumeSkipped string `json:"resume_skipped,omitempty"`
	// Warnings are controller.notify failures; they never fail the tick.
	Warnings []string `json:"warnings,omitempty"`
	// Health is true when the tick appended a health event.
	Health bool `json:"health,omitempty"`
	// Froze is set when the tick froze the factory on token exhaustion
	// (AutoFreeze, issue #572).
	Froze *TickFreeze `json:"froze,omitempty"`
	// Thawed are the units the tick started after thawing an automatic
	// suspension (AutoThaw); Thaw is true when it thawed one.
	Thaw   bool               `json:"thaw,omitempty"`
	Thawed []SupervisedResume `json:"thawed,omitempty"`
}

// TickFreeze is an automatic freeze: its thaw time, RFC 3339, and the live
// units its stop sentinel stops.
type TickFreeze struct {
	Until string `json:"until"`
	Live  int    `json:"live"`
}

// autoFreezeTick is TickWith's controller.auto_freeze pass: AutoThaw first,
// starting each returned unit through o.Start (nil thaws nothing); when
// nothing thawed, AutoFreeze. AutoThaw records each unit's auto-resume before
// the start, so the auto-resume pass that follows never starts it again.
func autoFreezeTick(dir string, now time.Time, o TickOptions, res *TickResult) error {
	if o.Start != nil {
		units, err := AutoThaw(dir, now, o.Session)
		if err != nil {
			return err
		}
		res.Thaw = units != nil // AutoThaw returns a non-nil slice when it thawed
		events, err := ReadEvents(dir)
		if err != nil {
			return fmt.Errorf("read events %s: %w", dir, err)
		}
		stopped := SuspendedAttempts(events)
		for _, u := range units {
			r := SupervisedResume{Task: u.Task, Attempt: u.Attempt}
			// A stopped unit resumes with flywheel resume's continue delta; a
			// rate-limited one gets the limit delta from run --resume itself.
			if slices.Contains(stopped, u) {
				if _, err := WriteSuspendDelta(dir, u.Task); err != nil {
					r.Reason = "delta: " + err.Error()
					res.Thawed = append(res.Thawed, r)
					continue
				}
			}
			if err := o.Start(u.Task); err != nil {
				r.Reason = "start: " + err.Error()
			} else {
				r.Started = true
			}
			res.Thawed = append(res.Thawed, r)
		}
		if res.Thaw {
			return nil
		}
	}
	acted, err := AutoFreeze(dir, now, o.Session)
	if err != nil || !acted {
		return err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return fmt.Errorf("read events %s: %w", dir, err)
	}
	res.Froze = &TickFreeze{Until: FactorySuspended(events, now).Until, Live: liveUnits(events)}
	return nil
}

// liveUnits counts the tasks whose latest dispatched event has no finished,
// lost or withdrawn event after it.
func liveUnits(events []Event) int {
	live := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case "dispatched":
			live[e.Task] = true
		case "finished", "lost", "withdrawn":
			delete(live, e.Task)
		}
	}
	return len(live)
}

// TickOptions configures TickWith. The zero value only reconciles.
type TickOptions struct {
	// Start starts flywheel run <task> --resume; nil disables auto-resume.
	// While set, controller.auto_resume (default on) decides.
	Start func(task string) error
	// Session is recorded on the auto-resume recovered event.
	Session string
	// Notify runs controller.notify with FLYWHEEL_RESUMED=resumed; nil runs
	// it through ShellArgv.
	Notify func(command, resumed string) error
	// HealthEvery is how often the tick appends a health event (issue #528):
	// one when none exists or the latest is at least this old; 0 is off.
	HealthEvery time.Duration
	// Version is the flywheel version the health event records.
	Version string
}

// resumeLockWait bounds how long a tick waits for supervise's lock: a
// supervise pass may validate for minutes, and the tick must not stall.
const resumeLockWait = time.Second

// Tick is TickWith with the zero options: it only reconciles.
func Tick(dir string, now time.Time) (TickResult, error) {
	return TickWith(dir, now, TickOptions{})
}

// autoResume runs supervise's resume pass (resumeLimited) under supervise's
// lock, so a concurrent supervise --resume-limited never double-starts a
// unit, then runs controller.notify once per unit it started.
func autoResume(dir string, now time.Time, cfg Config, o TickOptions, res *TickResult) error {
	timings := defaultRepoLockTimings()
	timings.wait = resumeLockWait
	release, err := acquireRepoLock(dir, "supervise.lock", timings)
	if err != nil {
		res.ResumeSkipped = err.Error()
		return nil
	}
	resumed, err := resumeLimited(dir, SuperviseOptions{ResumeLimited: true, Now: now, Start: o.Start, Session: o.Session})
	release()
	res.Resumed = resumed
	if err != nil {
		return err
	}
	cmd := cfg.controllerNotify()
	if cmd == "" {
		return nil
	}
	notify := o.Notify
	if notify == nil {
		notify = runResumeNotify
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return fmt.Errorf("read events %s: %w", dir, err)
	}
	for _, r := range resumed {
		if !r.Started {
			continue
		}
		line := fmt.Sprintf("%s %s model=%s", r.Task, r.Attempt, finishedModel(events, r.Task, r.Attempt))
		if nerr := notify(cmd, line); nerr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("controller.notify failed for %s: %v", r.Task, nerr))
		}
	}
	return nil
}

// finishedModel is the model of task's latest finish of attempt, "" if none.
func finishedModel(events []Event, task, attempt string) string {
	m := ""
	for _, e := range events {
		if e.Task == task && e.Kind == "finished" && e.Attempt == attempt {
			m = e.Model
		}
	}
	return m
}

// runResumeNotify runs command through the gates' shell (ShellArgv) with
// FLYWHEEL_RESUMED=resumed; a failure carries the command's output.
func runResumeNotify(command, resumed string) error {
	argv := ShellArgv(command)
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), "FLYWHEEL_RESUMED="+resumed)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TickWith runs one controller tick at now: it acquires (or renews) the
// controller lock, reads the events, leases, run files and config, calls Reconcile and
// executes the durable actions — MARK_LOST appends a lost event (reason
// lease-expired or idle, the note carrying the evidence; see MarkLost) and BLOCK appends a
// blocked event whose reason names the needs target. REQUEST_INSPECTION,
// WAIT and DISPATCH are reported in the result only and append nothing. The
// tick is idempotent: the same inputs append nothing the second time. With
// o.Start set and controller.auto_resume on, it then resumes the rate-limited
// units whose model's reset has passed (autoResume); before that, with
// controller.auto_freeze on, it thaws or freezes the factory on token
// exhaustion (autoFreezeTick). With o.HealthEvery set it
// last appends a health event (HealthAt) when none is at most that old.
func TickWith(dir string, now time.Time, o TickOptions) (TickResult, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return TickResult{}, fmt.Errorf("read config %s: %w", dir, err)
	}
	_, ttl, _ := cfg.controllerTimings()
	lock, err := AcquireLock(dir, now, ttl)
	if err != nil {
		return TickResult{}, err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return TickResult{}, fmt.Errorf("read events %s: %w", dir, err)
	}
	obs, err := observe(dir)
	if err != nil {
		return TickResult{}, err
	}
	actions := Reconcile(Derive(events), events, obs, PolicyFromConfig(cfg), now)
	var res TickResult
	res.TS = now.UTC().Format(time.RFC3339Nano)
	for _, a := range actions {
		switch a.Kind {
		case "MARK_LOST":
			if err := appendLost(dir, res.TS, a); err != nil {
				return TickResult{}, err
			}
			res.Lost++
			res.Actions++
		case "BLOCK":
			if err := AppendEvent(dir, Event{TS: res.TS, Task: a.Task, Kind: "blocked",
				Reason: a.Reason}); err != nil {
				return TickResult{}, fmt.Errorf("append blocked for %s: %w", a.Task, err)
			}
			res.Blocked++
			res.Actions++
		default:
			res.Proposed++
		}
	}
	if cfg.controllerAutoFreeze() {
		if err := autoFreezeTick(dir, now, o, &res); err != nil {
			return res, err
		}
	}
	if o.Start != nil && cfg.controllerAutoResume() {
		if err := autoResume(dir, now, cfg, o, &res); err != nil {
			return res, err
		}
	}
	if o.HealthEvery > 0 && healthDue(events, now, o.HealthEvery) {
		snap, err := HealthAt(dir, now, lock.Generation, o.Version)
		if err != nil {
			return res, err
		}
		snap.StaleAfter = cfg.controllerHealthStale(o.HealthEvery).String()
		if err := AppendEvent(dir, Event{TS: res.TS, Kind: "health", Health: &snap}); err != nil {
			return res, fmt.Errorf("append health: %w", err)
		}
		res.Health = true
	}
	return res, nil
}

// LatestHealth is the newest health event in events, or false when none.
func LatestHealth(events []Event) (Event, time.Time, bool) {
	var best Event
	var at time.Time
	have := false
	for _, e := range events {
		if e.Kind != "health" || e.Health == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil {
			continue
		}
		if !have || t.After(at) {
			best, at, have = e, t, true
		}
	}
	return best, at, have
}

// healthDue reports whether a health event is due at now: none recorded yet,
// or the latest is at least every old. Within the interval a tick appends
// nothing, so the recorder is idempotent.
func healthDue(events []Event, now time.Time, every time.Duration) bool {
	_, at, ok := LatestHealth(events)
	return !ok || now.Sub(at) >= every
}

// HealthAt computes the factory's health snapshot at now from the same views
// the factory floor draws: Derive's statuses (running, finished less the
// rate-limited units), the units'
// run states (stalled, rate-limited), the floor's andon count and the models
// a rate limit pauses (pausedModels).
func HealthAt(dir string, now time.Time, generation int, version string) (HealthSnapshot, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return HealthSnapshot{}, fmt.Errorf("read config %s: %w", dir, err)
	}
	w := NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		return HealthSnapshot{}, fmt.Errorf("health %s: %w", dir, err)
	}
	snap := HealthSnapshot{Andon: len(fl.Andon), ControllerGeneration: generation, Version: version}
	runState := map[string]string{}
	for _, u := range fl.Units {
		runState[u.Task] = u.RunState
		switch u.RunState {
		case "stalled":
			snap.Stalled++
		case "rate-limited":
			snap.RateLimited++
		}
	}
	oldest, oldestAge := "", -1
	for _, ts := range Derive(w.events).Tasks {
		switch ts.Status {
		case "dispatched", "running":
			snap.Running++
			if age := dispatchAge(w.events, ts.ID, ts.Attempt, now); age > oldestAge || age == oldestAge && ts.ID < oldest {
				oldest, oldestAge = ts.ID, age
			}
		case "finished":
			// A rate-limited attempt also derives to finished; RateLimited
			// counts it (issue #552).
			if runState[ts.ID] != "rate-limited" {
				snap.Finished++
			}
		}
	}
	if oldest != "" {
		snap.OldestInFlight = oldest + " " + HumanAge(oldestAge)
	}
	models, pauses := pausedModels(w.events, now, cfg.Limits.RateLimitPauseThreshold())
	for _, m := range models {
		snap.PausedModels = append(snap.PausedModels, PausedModel{Model: m, ResetAt: pauses[m].Until.UTC().Format(time.RFC3339)})
	}
	return snap, nil
}

// dispatchAge is the whole seconds since task's attempt was dispatched, 0
// when no dispatched event for it parses.
func dispatchAge(events []Event, task, attempt string, now time.Time) int {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind == "dispatched" && e.Task == task && e.Attempt == attempt {
			if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
				return ageOfTime(t, now)
			}
		}
	}
	return 0
}
