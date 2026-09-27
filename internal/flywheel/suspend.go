package flywheel

import (
	"fmt"
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
}

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
	return SuspendState{Suspended: true, Since: e.TS, By: e.Session, Reason: e.Note, Until: e.Until}
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

// Suspend appends a suspended event: session froze the factory for reason,
// until the thaw time when until is not zero. It refuses when the factory is
// already suspended, naming since and by, and when session is empty.
func Suspend(dir, session, reason string, until time.Time) error {
	if session == "" {
		return &RuleRefusal{Rule: "T4", Fix: "a --session is required to suspend the factory"}
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	now := time.Now()
	if s := FactorySuspended(events, now); s.Suspended {
		return &RuleRefusal{Rule: "suspended", Fix: fmt.Sprintf("the factory is already suspended since %s by %s: %s", s.Since, s.By, s.Reason)}
	}
	e := Event{Kind: "suspended", Session: session, Note: reason}
	if !until.IsZero() {
		if !until.After(now) {
			return fmt.Errorf("--until %s is not in the future", until.Format(time.RFC3339))
		}
		e.Until = until.UTC().Format(time.RFC3339)
	}
	if err := AppendEvent(dir, e); err != nil {
		return err
	}
	_, _ = WriteState(dir)
	return nil
}

// Unsuspend appends an unsuspended event: session thawed the factory, note
// why. It refuses when the factory is not suspended and when session is empty.
func Unsuspend(dir, session, note string) error {
	if session == "" {
		return &RuleRefusal{Rule: "T4", Fix: "a --session is required to resume the factory"}
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	if !FactorySuspended(events, time.Now()).Suspended {
		return &RuleRefusal{Rule: "suspended", Fix: "the factory is not suspended; nothing to resume"}
	}
	if err := AppendEvent(dir, Event{Kind: "unsuspended", Session: session, Note: note}); err != nil {
		return err
	}
	_, _ = WriteState(dir)
	return nil
}
