package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zones resolve on hosts without a zoneinfo database (Windows)
)

// resetClause matches a rate-limit reset clause: "10:20am
// (America/Los_Angeles)", "10am (Europe/London)", "3:05pm".
var resetClause = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*(?:\(([^()]+)\))?$`)

// parseResetTime turns a rate-limit reset clause into the next occurrence of
// that clock time at or after now, in the clause's zone (now's location when
// it names none); tomorrow when the time has already passed today. An
// unknown zone or an unparsable clause is false (issue #380).
func parseResetTime(text string, now time.Time) (time.Time, bool) {
	m := resetClause.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return time.Time{}, false
	}
	hour, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	if minute > 59 {
		return time.Time{}, false
	}
	switch strings.ToLower(m[3]) {
	case "am", "pm":
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		hour %= 12
		if strings.EqualFold(m[3], "pm") {
			hour += 12
		}
	default:
		if hour > 23 {
			return time.Time{}, false
		}
	}
	loc := now.Location()
	if zone := strings.TrimSpace(m[4]); zone != "" {
		l, err := time.LoadLocation(zone)
		if err != nil {
			return time.Time{}, false
		}
		loc = l
	}
	local := now.In(loc)
	at := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if at.Before(now) {
		at = time.Date(local.Year(), local.Month(), local.Day()+1, hour, minute, 0, 0, loc)
	}
	return at, true
}

// rateLimitPaused reports whether model is paused by a rate limit at now
// (issue #383): a rate limit belongs to the subscription, so every unit on the
// model waits for the reset. The latest finished event for model carrying a
// reset_at decides it, unless a later finished event on that model stopped
// cleanly (the window has reopened). It pauses before the wall at the default
// limits.rate_limit_pause_at; callers holding the config use
// rateLimitPausedAt.
func rateLimitPaused(events []Event, model string, now time.Time) (until time.Time, paused bool) {
	p, ok := rateLimitPausedAt(events, model, now, Limits{}.RateLimitPauseThreshold())
	return p.Until, ok
}

// ratePause is why and until when a model is paused. Utilization and Window
// are set only when the pause is the utilization threshold, not a hit limit.
type ratePause struct {
	Until       time.Time
	Utilization float64
	Window      string
}

// reason is " (<n>% of the <window> window used)" for a utilization pause, ""
// for a hit limit.
func (p ratePause) reason() string {
	if p.Utilization == 0 {
		return ""
	}
	window := p.Window
	if window == "" {
		window = "rate-limit"
	}
	return fmt.Sprintf(" (%.0f%% of the %s window used)", p.Utilization*100, window)
}

// rateLimitPausedAt is rateLimitPaused with the pause threshold (issue #417):
// besides a hit limit, the model is paused when its latest finished event's
// rate_limit_event utilization is at least pauseAt (0 disables) and its
// limit_reset_at is still ahead. A later finish below the threshold, or the
// reset passing, releases it.
func rateLimitPausedAt(events []Event, model string, now time.Time, pauseAt float64) (ratePause, bool) {
	latest := true
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind != "finished" || e.Model != model {
			continue
		}
		if latest {
			latest = false
			if e.ResetAt == "" && pauseAt > 0 && e.LimitUtilization >= pauseAt && e.LimitResetAt != "" {
				if at, err := time.Parse(time.RFC3339, e.LimitResetAt); err == nil && now.Before(at) {
					return ratePause{Until: at, Utilization: e.LimitUtilization, Window: e.LimitWindow}, true
				}
			}
		}
		if e.Reason == "stop" {
			return ratePause{}, false
		}
		if e.ResetAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.ResetAt)
		if err != nil || !now.Before(at) {
			return ratePause{}, false
		}
		return ratePause{Until: at}, true
	}
	return ratePause{}, false
}

// pausedModels lists every model in events that rateLimitPausedAt holds at
// now, with its pause, in first-seen order.
func pausedModels(events []Event, now time.Time, pauseAt float64) (models []string, pauses map[string]ratePause) {
	pauses = map[string]ratePause{}
	seen := map[string]bool{}
	for _, e := range events {
		if e.Kind != "finished" || (e.ResetAt == "" && e.LimitResetAt == "") || e.Model == "" || seen[e.Model] {
			continue
		}
		seen[e.Model] = true
		if p, ok := rateLimitPausedAt(events, e.Model, now, pauseAt); ok {
			models = append(models, e.Model)
			pauses[e.Model] = p
		}
	}
	return models, pauses
}

// pauseClock is a reset time as the viewer's local HH:MM with the RFC 3339
// instant in parentheses.
func pauseClock(t time.Time) string {
	return t.Local().Format("15:04") + " (" + t.UTC().Format(time.RFC3339) + ")"
}

// limitContinue is the task text of the delta a rate-limit resume attaches.
const limitContinue = "# TASK: continue\n\nYour run was cut off by a rate limit. Continue the same task from where you stopped; do not redo finished work. Re-run the gates at the end and report their exit codes.\n"

// jobContinue is the task text of the delta an abandoned-job resume attaches;
// %s is the killed command(s) (issue #390).
const jobContinue = "# TASK: continue\n\nYour background job `%s` was killed when your session ended. Run it in the foreground (with a timeout long enough for it to finish) and wait for it, then finish the task and report its result and every gate's exit code.\n"

// RunResumingLimits calls Run and, while the attempt ends rate-limited and
// limits.rate_limit_retries allows, waits for the limit to reset and resumes
// the same worker session with a continue delta (issue #380). The wait is the
// parsed reset time plus a minute, else a backoff of 2m, 4m, 8m... capped at
// 30m; a wait beyond limits.rate_limit_max_wait stops and returns the
// rate-limited result. An attempt that ends abandoned-job is resumed once,
// immediately, on the same session with a delta naming the killed job; a
// second abandoned-job is returned as is (issue #390). A --resume on a model
// still paused by a rate limit first waits for the reset (resumeWait, issue
// #472). sleep and now are injected so tests never sleep.
func RunResumingLimits(dir string, o RunOptions, sleep func(time.Duration), now func() time.Time) (Result, error) {
	o, err := resumeDelta(dir, o)
	if err != nil {
		return Result{}, err
	}
	wait, err := resumeWait(dir, o, now())
	if err != nil {
		return Result{}, err
	}
	if wait > 0 {
		sleep(wait)
	}
	res, err := resumeLimits(dir, o, sleep, now)
	if err != nil || res.Reason != "abandoned-job" {
		return res, err
	}
	delta, err := writeResumeDelta(dir, o.Task, fmt.Sprintf("%s.job-1.txt", o.Task), fmt.Sprintf(jobContinue, strings.Join(res.Jobs, "`, `")))
	if err != nil {
		return res, err
	}
	progress(o.Progress, fmt.Sprintf("%s abandoned-job (%s); resuming once in the same session", o.Task, strings.Join(res.Jobs, ", ")))
	next := o
	next.Resume, next.DeltaPath, next.Increment = true, delta, 0
	return resumeLimits(dir, next, sleep, now)
}

// resumeDelta gives a bare --resume after a rate-limited or abandoned-job
// finish the continue delta the automatic path uses (issue #472): with no
// --delta and no .flywheel/briefs/<task>.delta.txt, it writes the next unused
// <task>.limit-<n>.txt and sets o.DeltaPath to it. The finished event records
// no jobs, so an abandoned-job gets the limit continue text too. Any other
// case returns o unchanged.
func resumeDelta(dir string, o RunOptions) (RunOptions, error) {
	if !o.Resume || o.DeltaPath != "" {
		return o, nil
	}
	briefs := filepath.Join(dir, ".flywheel", "briefs")
	if _, err := os.Stat(filepath.Join(briefs, o.Task+".delta.txt")); err == nil {
		return o, nil
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return o, err
	}
	reason := ""
	for i := len(events) - 1; i >= 0; i-- {
		if e := events[i]; e.Kind == "finished" && e.Task == o.Task {
			reason = e.Reason
			break
		}
	}
	if reason != "rate-limited" && reason != "abandoned-job" {
		return o, nil
	}
	n := 1
	for {
		if _, err := os.Stat(filepath.Join(briefs, fmt.Sprintf("%s.limit-%d.txt", o.Task, n))); err != nil {
			break
		}
		n++
	}
	delta, err := writeLimitDelta(dir, o.Task, n)
	if err != nil {
		return o, err
	}
	progress(o.Progress, fmt.Sprintf("%s: no delta given; resuming with %s", o.Task, delta))
	o.DeltaPath = delta
	return o, nil
}

// resumeWait is how long a --resume waits before its first dispatch (issue
// #472): while the task's model is paused by a rate limit at now, until the
// pause ends plus a minute. The model is --model, else the one on the task's
// latest finished event (the session being resumed). A wait beyond
// limits.rate_limit_max_wait is a rate-limit RuleRefusal naming the reset; no
// resume, or no pause, is no wait.
func resumeWait(dir string, o RunOptions, now time.Time) (time.Duration, error) {
	if !o.Resume {
		return 0, nil
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return 0, err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return 0, err
	}
	model := o.Model
	for i := len(events) - 1; i >= 0 && model == ""; i-- {
		if e := events[i]; e.Kind == "finished" && e.Task == o.Task {
			model = e.Model
		}
	}
	if model == "" {
		return 0, nil
	}
	p, paused := rateLimitPausedAt(events, model, now, cfg.Limits.RateLimitPauseThreshold())
	if !paused {
		return 0, nil
	}
	maxWait, err := cfg.Limits.RateLimitMaxWaitDuration()
	if err != nil {
		maxWait = 5 * time.Hour
	}
	wait := p.Until.Sub(now) + time.Minute
	if wait > maxWait {
		return 0, &RuleRefusal{
			Rule: "rate-limit",
			Fix: fmt.Sprintf("%s is paused by a rate limit until %s%s; the wait %s is beyond limits.rate_limit_max_wait %s — rerun flywheel run %s --resume after the reset",
				model, pauseClock(p.Until), p.reason(), wait.Round(time.Second), maxWait, o.Task),
		}
	}
	progress(o.Progress, fmt.Sprintf("%s %s is paused by a rate limit until %s; resuming at %s (in %s)",
		o.Task, model, pauseClock(p.Until), now.Add(wait).Format("2006-01-02 15:04 MST"), wait.Round(time.Second)))
	return wait, nil
}

// resumeLimits is RunResumingLimits' rate-limit loop: Run, then the bounded
// waits and resumes while the attempt ends rate-limited.
func resumeLimits(dir string, o RunOptions, sleep func(time.Duration), now func() time.Time) (Result, error) {
	res, err := Run(dir, o)
	if err != nil || res.Reason != "rate-limited" {
		return res, err
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return res, nil
	}
	maxWait, err := cfg.Limits.RateLimitMaxWaitDuration()
	if err != nil {
		maxWait = 5 * time.Hour
	}
	backoff := 2 * time.Minute
	for n := 1; res.Reason == "rate-limited" && n <= cfg.Limits.RateLimitRetryCount(); n++ {
		t := now()
		var wait time.Duration
		if at, ok := parseResetTime(res.ResetText, t); ok {
			wait = at.Sub(t) + time.Minute
		} else if at, err := time.Parse(time.RFC3339, res.ResetAt); err == nil && at.After(t) {
			// No clause, but the stream's rate_limit_event gave the exact
			// reset (issue #417).
			wait = at.Sub(t) + time.Minute
		} else {
			wait = backoff
			backoff = min(2*backoff, 30*time.Minute)
		}
		if wait > maxWait {
			progress(o.Progress, fmt.Sprintf("%s rate-limited; the reset is in %s, beyond limits.rate_limit_max_wait %s; not resuming", o.Task, wait.Round(time.Second), maxWait))
			return res, nil
		}
		progress(o.Progress, fmt.Sprintf("%s rate-limited; resuming at %s (in %s)", o.Task, t.Add(wait).Format("2006-01-02 15:04 MST"), wait.Round(time.Second)))
		sleep(wait)
		delta, err := writeLimitDelta(dir, o.Task, n)
		if err != nil {
			return res, err
		}
		next := o
		next.Resume, next.DeltaPath, next.Increment = true, delta, 0
		if res, err = Run(dir, next); err != nil {
			return res, err
		}
	}
	return res, nil
}

// writeLimitDelta writes .flywheel/briefs/<task>.limit-<n>.txt: the owns,
// needs and gate lines of the task's effective brief, then limitContinue. It
// returns the repo-relative path.
func writeLimitDelta(dir, task string, n int) (string, error) {
	return writeResumeDelta(dir, task, fmt.Sprintf("%s.limit-%d.txt", task, n), limitContinue)
}

// writeResumeDelta writes .flywheel/briefs/<name>: the owns, needs and gate
// lines of the task's effective brief, then text. It returns the
// repo-relative path.
func writeResumeDelta(dir, task, name, text string) (string, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return "", err
	}
	header, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "owns: %s\n", strings.Join(header.Owns, ", "))
	needs := strings.Join(header.Needs, ", ")
	if needs == "" {
		needs = "none"
	}
	fmt.Fprintf(&b, "needs: %s\n", needs)
	for _, g := range header.Gates {
		fmt.Fprintf(&b, "gate: %s\n", g)
	}
	b.WriteString("\n" + text)
	rel := filepath.Join(".flywheel", "briefs", name)
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "briefs"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return rel, nil
}
