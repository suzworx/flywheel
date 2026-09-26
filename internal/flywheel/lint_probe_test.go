package flywheel

import (
	"os/exec"
	"strings"
	"testing"
)

// TestProbeGates runs a passing, a failing and a cannot-start gate in a temp
// dir and checks each probe's index, exit code, first line and CannotStart.
func TestProbeGates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	probes := ProbeGates(dir, []string{"exit 0", "echo oops; exit 3", "definitely-not-a-command-xyz"})
	if len(probes) != 3 {
		t.Fatalf("ProbeGates returned %d probes, want 3", len(probes))
	}
	for i, p := range probes {
		if p.N != i+1 {
			t.Errorf("probe %d: N = %d, want %d", i, p.N, i+1)
		}
	}
	if p := probes[0]; p.Err != nil || p.RC != 0 || p.CannotStart {
		t.Errorf("pass gate: %+v, want rc 0 and startable", p)
	}
	if p := probes[1]; p.Err != nil || p.RC != 3 || p.FirstLine != "oops" || p.CannotStart {
		t.Errorf("fail gate: %+v, want rc 3, first line oops, startable", p)
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH: the cannot-start exit code is shell-specific")
	}
	if p := probes[2]; !p.CannotStart || p.RC != 127 {
		t.Errorf("missing command gate: %+v, want CannotStart with rc 127", p)
	}
}

// TestProbeFirstLine checks the first non-blank line is trimmed and cut to
// 160 bytes.
func TestProbeFirstLine(t *testing.T) {
	t.Parallel()
	if got := firstLine([]byte("\n  \r\n  hello \r\nworld\n")); got != "hello" {
		t.Errorf("firstLine = %q, want hello", got)
	}
	long := strings.Repeat("x", 300)
	if got := firstLine([]byte(long)); len(got) != probeFirstLineMax {
		t.Errorf("firstLine length = %d, want %d", len(got), probeFirstLineMax)
	}
	if got := firstLine(nil); got != "" {
		t.Errorf("firstLine(nil) = %q, want empty", got)
	}
}
