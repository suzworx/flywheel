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
// cleanly (the window has reopened).
func rateLimitPaused(events []Event, model string, now time.Time) (until time.Time, paused bool) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Kind != "finished" || e.Model != model {
			continue
		}
		if e.Reason == "stop" {
			return time.Time{}, false
		}
		if e.ResetAt == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, e.ResetAt)
		if err != nil || !now.Before(at) {
			return time.Time{}, false
		}
		return at, true
	}
	return time.Time{}, false
}

// pausedModels lists every model in events that rateLimitPaused holds at now,
// with its reset time, in first-seen order.
func pausedModels(events []Event, now time.Time) (models []string, until map[string]time.Time) {
	until = map[string]time.Time{}
	seen := map[string]bool{}
	for _, e := range events {
		if e.Kind != "finished" || e.ResetAt == "" || e.Model == "" || seen[e.Model] {
			continue
		}
		seen[e.Model] = true
		if at, ok := rateLimitPaused(events, e.Model, now); ok {
			models = append(models, e.Model)
			until[e.Model] = at
		}
	}
	return models, until
}

// pauseClock is a reset time as the viewer's local HH:MM with the RFC 3339
// instant in parentheses.
func pauseClock(t time.Time) string {
	return t.Local().Format("15:04") + " (" + t.UTC().Format(time.RFC3339) + ")"
}

// limitContinue is the task text of the delta a rate-limit resume attaches.
const limitContinue = "# TASK: continue\n\nYour run was cut off by a rate limit. Continue the same task from where you stopped; do not redo finished work. Re-run the gates at the end and report their exit codes.\n"

// RunResumingLimits calls Run and, while the attempt ends rate-limited and
// limits.rate_limit_retries allows, waits for the limit to reset and resumes
// the same worker session with a continue delta (issue #380). The wait is the
// parsed reset time plus a minute, else a backoff of 2m, 4m, 8m... capped at
// 30m; a wait beyond limits.rate_limit_max_wait stops and returns the
// rate-limited result. sleep and now are injected so tests never sleep.
func RunResumingLimits(dir string, o RunOptions, sleep func(time.Duration), now func() time.Time) (Result, error) {
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
	b.WriteString("\n" + limitContinue)
	rel := filepath.Join(".flywheel", "briefs", fmt.Sprintf("%s.limit-%d.txt", task, n))
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "briefs"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return rel, nil
}
