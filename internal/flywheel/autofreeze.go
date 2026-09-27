package flywheel

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

// TokensExhausted reports whether the factory has no tokens left at now
// (issue #572): at least one worker is configured and every configured
// worker's model and fallback models is paused by a rate limit (pausedModels
// at limits.rate_limit_pause_at). until is the earliest reset among them and
// models the paused models, in configuration order.
func TokensExhausted(events []Event, cfg Config, now time.Time) (exhausted bool, until time.Time, models []string) {
	var want []string
	for _, w := range cfg.Workers {
		for _, m := range append([]string{w.Model}, fallbackModels(w)...) {
			if m != "" && !slices.Contains(want, m) {
				want = append(want, m)
			}
		}
	}
	if len(want) == 0 {
		return false, time.Time{}, nil
	}
	_, pauses := pausedModels(events, now, cfg.Limits.RateLimitPauseThreshold())
	for _, m := range want {
		p, ok := pauses[m]
		if !ok {
			return false, time.Time{}, nil
		}
		if until.IsZero() || p.Until.Before(until) {
			until = p.Until
		}
	}
	return true, until, want
}

// fallbackModels is w's fallback models, in order.
func fallbackModels(w Worker) []string {
	out := make([]string, 0, len(w.Fallbacks))
	for _, f := range w.Fallbacks {
		out = append(out, f.Model)
	}
	return out
}

// AutoFreeze suspends the factory with stop until the earliest reset when
// TokensExhausted holds at now and the factory is not already suspended. The
// suspension is automatic (reason autoFreezeReason), so AutoThaw may thaw it.
// session is the suspending session, "controller" when empty.
func AutoFreeze(dir string, now time.Time, session string) (acted bool, err error) {
	if session == "" {
		session = "controller"
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return false, err
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return false, err
	}
	if FactorySuspended(events, now).Suspended {
		return false, nil
	}
	exhausted, until, models := TokensExhausted(events, cfg, now)
	if !exhausted {
		return false, nil
	}
	reason := fmt.Sprintf("tokens exhausted: %s paused until %s UTC", strings.Join(models, ", "), until.UTC().Format("15:04"))
	if err := SuspendWith(dir, session, SuspendOptions{Reason: reason, Until: until, Stop: true, Auto: true, Now: now}); err != nil {
		return false, err
	}
	return true, nil
}

// AutoThaw thaws an automatic suspension whose until has passed at now: it
// appends unsuspended (note "tokens returned"), removes the stop sentinel and
// returns the units to resume with run --resume: SuspendedAttempts plus every
// task whose latest finish is rate-limited with no dispatched event and no
// auto-resume since, once each. Before returning it appends one auto-resume
// recovered event per unit, the same record supervise's resume pass skips on,
// so no pass starts a unit twice. A thaw returns a non-nil slice, empty when
// no unit waits. A manual suspension, a live one and an already thawed one
// return nil.
func AutoThaw(dir string, now time.Time, session string) ([]TaskAttempt, error) {
	if session == "" {
		session = "controller"
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	last := -1
	for i, e := range events {
		switch e.Kind {
		case "suspended":
			last = i
		case "unsuspended":
			last = -1
		}
	}
	if last < 0 || events[last].Reason != autoFreezeReason || FactorySuspended(events, now).Suspended {
		return nil, nil
	}
	if err := AppendEvent(dir, Event{Kind: "unsuspended", Session: session, Note: "tokens returned"}); err != nil {
		return nil, err
	}
	if err := os.Remove(suspendStopPath(dir)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	out := thawAttempts(events)
	for _, ta := range out {
		note := fmt.Sprintf("auto-resume %s %s after tokens returned", ta.Task, ta.Attempt)
		if err := AppendEvent(dir, Event{Kind: "recovered", Note: note, Paths: []string{ta.Task}, Session: session}); err != nil {
			return out, err
		}
	}
	_, _ = WriteState(dir)
	return out, nil
}

// thawAttempts is SuspendedAttempts plus the rate-limited units: every task
// whose latest finished event is rate-limited with no dispatched event and no
// auto-resume recovered event after it; by task, each once.
func thawAttempts(events []Event) []TaskAttempt {
	open := map[string]TaskAttempt{}
	for _, e := range events {
		switch {
		case e.Kind == "dispatched":
			delete(open, e.Task)
		case e.Kind == "finished" && (e.Reason == "suspended" || e.Reason == "rate-limited"):
			open[e.Task] = TaskAttempt{Task: e.Task, Attempt: e.Attempt, Session: e.Session}
		case e.Kind == "finished":
			delete(open, e.Task)
		case e.Kind == "recovered" && strings.HasPrefix(e.Note, "auto-resume "):
			for _, t := range e.Paths {
				delete(open, t)
			}
		}
	}
	out := make([]TaskAttempt, 0, len(open))
	for _, ta := range open {
		out = append(out, ta)
	}
	slices.SortFunc(out, func(a, b TaskAttempt) int { return strings.Compare(a.Task, b.Task) })
	return out
}
