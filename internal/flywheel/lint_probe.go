package flywheel

import (
	"fmt"
	"strconv"
	"strings"
)

// GateProbe is one gate's result on the base tree, before any worker runs
// (issue #544): its 1-based index, exit code, duration and first output line.
// CannotStart is true when the gate could not start at all: the spawn failed
// (Err is set), or the shell reported 127 (command not found) or 126 (not
// executable).
type GateProbe struct {
	N           int
	RC          int
	DurMS       int64
	FirstLine   string
	Err         error
	CannotStart bool
}

// probeFirstLineMax caps a probe's FirstLine in bytes.
const probeFirstLineMax = 160

// ProbeGates runs every gate once, in order, in dir, with the runner validate
// uses, and FLYWHEEL_BASE set to dir's HEAD commit (unset when dir is not a
// git repo), so a `git diff --check "$FLYWHEEL_BASE"` gate probes cleanly.
func ProbeGates(dir string, gates []string) []GateProbe {
	base := headCommit(dir)
	probes := make([]GateProbe, 0, len(gates))
	for i, g := range gates {
		rc, dur, out, err := runGateBase(dir, g, base)
		p := GateProbe{N: i + 1, RC: rc, DurMS: dur, Err: err, FirstLine: firstLine(out)}
		p.CannotStart = err != nil || rc == 127 || rc == 126
		probes = append(probes, p)
	}
	return probes
}

// TaskIDOK reports whether s is a valid task id, ^[A-Za-z0-9._-]+$, the
// pattern every event's task must match.
func TaskIDOK(s string) bool { return taskOK(s) }

// RecordGateProbes appends one gate_probed event per probe for task to dir's
// ledger in a single write (issue #544). gates are the probed commands in
// probe order; Commit is dir's HEAD when dir is a git repo.
func RecordGateProbes(dir, task string, gates []string, probes []GateProbe) error {
	if !taskOK(task) {
		return fmt.Errorf("task %q does not match ^[A-Za-z0-9._-]+$", task)
	}
	commit := headCommit(dir)
	events := make([]Event, 0, len(probes))
	for _, p := range probes {
		rc := p.RC
		reason := p.FirstLine
		if p.Err != nil {
			reason = p.Err.Error()
		}
		events = append(events, Event{
			Task: task, Kind: "gate_probed", Gate: strconv.Itoa(p.N), Command: gates[p.N-1],
			RC: &rc, DurationMS: p.DurMS, Reason: reason, Commit: commit,
		})
	}
	return AppendEvents(dir, events)
}

// BaseProbeFailures returns task's newest gate_probed event per gate command
// in dir's ledger whose rc is non-zero (issue #544): a gate that already
// failed on the base tree before dispatch. A command whose newest probe passed
// is absent.
func BaseProbeFailures(dir, task string) (map[string]Event, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	return baseProbeFailures(events, task), nil
}

// baseProbeFailures is BaseProbeFailures over events already read.
func baseProbeFailures(events []Event, task string) map[string]Event {
	newest := map[string]Event{}
	for _, e := range derivationOrder(events) {
		if e.Kind == "gate_probed" && e.Task == task && e.Command != "" {
			newest[e.Command] = e
		}
	}
	out := map[string]Event{}
	for cmd, e := range newest {
		if e.RC != nil && *e.RC != 0 {
			out[cmd] = e
		}
	}
	return out
}

// firstLine returns out's first non-blank line, trimmed and cut to
// probeFirstLineMax bytes.
func firstLine(out []byte) string {
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if len(l) > probeFirstLineMax {
				l = l[:probeFirstLineMax]
			}
			return l
		}
	}
	return ""
}
