package flywheel

import "strings"

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
