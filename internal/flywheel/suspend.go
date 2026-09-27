package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SuspendState is the factory's freeze as the ledger records it (issue #572):
// Since is the suspended event's ts, By its session, Reason its note and
// Until its thaw time, RFC 3339 ("" when it lasts until flywheel resume).
type SuspendState struct {
	Suspended bool   `json:"suspended"`
	Since     string `json:"since,omitempty"`
	By        string `json:"by,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Until     string `json:"until,omitempty"`
	// Stop is set when the suspension also stops every live worker.
	Stop bool `json:"stop,omitempty"`
	// Auto is set when the controller froze the factory on token exhaustion
	// (AutoFreeze): the event's reason is autoFreezeReason.
	Auto bool `json:"auto,omitempty"`
}

// autoFreezeReason is the reason field of a suspended event AutoFreeze
// appended: it marks the suspension automatic, so AutoThaw may thaw it.
const autoFreezeReason = "tokens-exhausted"

// FactorySuspended derives the freeze at now: the factory is suspended when
// its latest suspended event comes after its latest unsuspended event and
// that event's Until, when set, is still after now. An expired Until thaws
// the factory without an event.
func FactorySuspended(events []Event, now time.Time) SuspendState {
	last := -1
	for i, e := range events {
		switch e.Kind {
		case "suspended":
			last = i
		case "unsuspended":
			last = -1
		}
	}
	if last < 0 {
		return SuspendState{}
	}
	e := events[last]
	if e.Until != "" {
		if u, err := time.Parse(time.RFC3339, e.Until); err == nil && !u.After(now) {
			return SuspendState{}
		}
	}
	return SuspendState{Suspended: true, Since: e.TS, By: e.Session, Reason: e.Note, Until: e.Until, Stop: e.Stop,
		Auto: e.Reason == autoFreezeReason}
}

// suspendedRefusal is the refusal every dispatch path returns while s holds.
func suspendedRefusal(s SuspendState) *RuleRefusal {
	return &RuleRefusal{Rule: "suspended", Fix: fmt.Sprintf(
		"the factory is suspended since %s by %s: %s; flywheel resume --session <s> to thaw", s.Since, s.By, s.Reason)}
}

// refuseIfSuspended reads the ledger and returns the suspended refusal when
// the factory is suspended at now, nil otherwise.
func refuseIfSuspended(dir string, now time.Time) error {
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	if s := FactorySuspended(events, now); s.Suspended {
		return suspendedRefusal(s)
	}
	return nil
}

// suspendStopPath is the sentinel a stopping suspension writes (issue #572):
// a running flywheel run stats it on every lease tick and reads the ledger
// only when it exists.
func suspendStopPath(dir string) string {
	return filepath.Join(dir, ".flywheel", "suspend.stop")
}

// suspendStopSince reports whether a live worker must stop now: the sentinel
// exists and the ledger's suspension at now carries Stop. since is that
// suspended event's ts.
func suspendStopSince(dir string, now time.Time) (since string, stop bool) {
	if _, err := os.Stat(suspendStopPath(dir)); err != nil {
		return "", false
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return "", false
	}
	s := FactorySuspended(events, now)
	return s.Since, s.Suspended && s.Stop
}

// SuspendOptions are a suspension's reason, its optional thaw time and
// whether it also stops every live worker (Stop, issue #572).
type SuspendOptions struct {
	Reason string
	Until  time.Time
	Stop   bool
	// Auto marks the suspension automatic (AutoFreeze): the event carries
	// reason autoFreezeReason.
	Auto bool
	// Now is the instant the suspension is judged at; zero is time.Now().
	Now time.Time
}

// Suspend appends a suspended event: session froze the factory for reason,
// until the thaw time when until is not zero. It refuses when the factory is
// already suspended, naming since and by, and when session is empty.
func Suspend(dir, session, reason string, until time.Time) error {
	return SuspendWith(dir, session, SuspendOptions{Reason: reason, Until: until})
}

// SuspendWith is Suspend with options: with Stop the event carries stop and
// the sentinel .flywheel/suspend.stop is written (content: the event's ts),
// so every running flywheel run stops its worker at its next lease tick.
func SuspendWith(dir, session string, o SuspendOptions) error {
	reason, until := o.Reason, o.Until
	if session == "" {
		return &RuleRefusal{Rule: "T4", Fix: "a --session is required to suspend the factory"}
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if s := FactorySuspended(events, now); s.Suspended {
		return &RuleRefusal{Rule: "suspended", Fix: fmt.Sprintf("the factory is already suspended since %s by %s: %s", s.Since, s.By, s.Reason)}
	}
	e := Event{Kind: "suspended", Session: session, Note: reason, Stop: o.Stop}
	if o.Auto {
		e.Reason = autoFreezeReason
	}
	if !until.IsZero() {
		if !until.After(now) {
			return fmt.Errorf("--until %s is not in the future", until.Format(time.RFC3339))
		}
		e.Until = until.UTC().Format(time.RFC3339)
	}
	if err := AppendEvent(dir, e); err != nil {
		return err
	}
	if o.Stop {
		if events, err = ReadEvents(dir); err != nil {
			return err
		}
		ts := FactorySuspended(events, now).Since
		if err := atomicWrite(filepath.Join(dir, ".flywheel"), "suspend.stop", "suspend.stop-*", []byte(ts+"\n")); err != nil {
			return err
		}
	}
	_, _ = WriteState(dir)
	return nil
}

// TaskAttempt is one attempt a suspension stopped: its task, attempt and the
// worker session its finished event kept.
type TaskAttempt struct {
	Task    string `json:"task"`
	Attempt string `json:"attempt"`
	Session string `json:"session,omitempty"`
}

// SuspendedAttempts lists, by task, every task whose latest finished event
// has reason suspended and no dispatched event after it: the units flywheel
// resume continues.
func SuspendedAttempts(events []Event) []TaskAttempt {
	open := map[string]TaskAttempt{}
	for _, e := range events {
		switch {
		case e.Kind == "dispatched":
			delete(open, e.Task)
		case e.Kind == "finished" && e.Reason == "suspended":
			open[e.Task] = TaskAttempt{Task: e.Task, Attempt: e.Attempt, Session: e.Session}
		case e.Kind == "finished":
			delete(open, e.Task)
		}
	}
	out := make([]TaskAttempt, 0, len(open))
	for _, ta := range open {
		out = append(out, ta)
	}
	slices.SortFunc(out, func(a, b TaskAttempt) int { return strings.Compare(a.Task, b.Task) })
	return out
}

// suspendContinue is the task text of the delta a suspended unit resumes with.
const suspendContinue = "# TASK: continue\n\nYou were stopped by a factory suspension; your owned files are as you left them; continue from where you stopped and finish the brief.\n"

// WriteSuspendDelta writes .flywheel/briefs/<task>.delta.txt, the default
// prompt of flywheel run <task> --resume: the task's owns, needs and gates,
// then suspendContinue. It returns the repo-relative path.
func WriteSuspendDelta(dir, task string) (string, error) {
	return writeResumeDelta(dir, task, task+".delta.txt", suspendContinue)
}

// Unsuspend appends an unsuspended event: session thawed the factory, note
// why, and removes the stop sentinel. It returns the attempts the suspension
// stopped (SuspendedAttempts). It refuses when the factory is not suspended
// and when session is empty.
func Unsuspend(dir, session, note string) ([]TaskAttempt, error) {
	if session == "" {
		return nil, &RuleRefusal{Rule: "T4", Fix: "a --session is required to resume the factory"}
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	if !FactorySuspended(events, time.Now()).Suspended {
		return nil, &RuleRefusal{Rule: "suspended", Fix: "the factory is not suspended; nothing to resume"}
	}
	if err := AppendEvent(dir, Event{Kind: "unsuspended", Session: session, Note: note}); err != nil {
		return nil, err
	}
	if err := os.Remove(suspendStopPath(dir)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	_, _ = WriteState(dir)
	return SuspendedAttempts(events), nil
}
