package flywheel

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// WaitTimeout is WaitFor's error when the timeout passes before every named
// task has finished (issue #393); Pending names the tasks still waited on.
type WaitTimeout struct {
	Timeout time.Duration
	Pending []string
}

func (e *WaitTimeout) Error() string {
	return fmt.Sprintf("timed out after %s waiting for %s", e.Timeout, strings.Join(e.Pending, " "))
}

// WaitFor blocks until every named task has a finished event for the attempt
// it was on when the wait began, or for any attempt dispatched after that
// (issue #393): a finished unit reaches the lead deterministically instead of
// through a backgrounded shell job nobody watches. A task with no attempt yet
// waits for its first finish. It prints one line per finish as it lands
// ("<task> <attempt> finished reason=<r>"); clean is true when every reason is
// stop. The log is re-read each tick (complete lines only, via ReadEvents)
// and sleep is called between ticks. A zero timeout waits forever; otherwise
// passing it returns a *WaitTimeout.
func WaitFor(dir string, tasks []string, timeout time.Duration, poll time.Duration, now func() time.Time, sleep func(time.Duration), out io.Writer) (clean bool, err error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return false, err
	}
	start := map[string]string{}
	for _, ts := range Derive(events).Tasks {
		start[ts.ID] = ts.Attempt
	}
	base := len(events)
	deadline := now().Add(timeout)
	done := map[string]bool{}
	clean = true
	for {
		// Attempts dispatched after the wait began also count as "at or after"
		// the starting attempt; attempt ids are not ordered, the log is.
		later := map[string]bool{}
		for i, e := range events {
			if e.Kind == "dispatched" && i >= base && e.Attempt != "" {
				later[e.Task+"\x00"+e.Attempt] = true
			}
		}
		for _, task := range tasks {
			if done[task] {
				continue
			}
			for i, e := range events {
				if e.Task != task || e.Kind != "finished" {
					continue
				}
				a := start[task]
				if !(a == "" || e.Attempt == a || i >= base || later[task+"\x00"+e.Attempt]) {
					continue
				}
				done[task] = true
				if e.Reason != "stop" {
					clean = false
				}
				attempt := e.Attempt
				if attempt == "" {
					attempt = "-"
				}
				fmt.Fprintf(out, "%s %s finished reason=%s\n", task, attempt, e.Reason)
				break
			}
		}
		var pending []string
		for _, task := range tasks {
			if !done[task] {
				pending = append(pending, task)
			}
		}
		if len(pending) == 0 {
			return clean, nil
		}
		if timeout > 0 && !now().Before(deadline) {
			return false, &WaitTimeout{Timeout: timeout, Pending: pending}
		}
		sleep(poll)
		if events, err = ReadEvents(dir); err != nil {
			return false, err
		}
	}
}
