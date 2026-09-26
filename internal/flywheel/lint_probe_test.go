package flywheel

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// probeRC returns a pointer to n, for an event's RC.
func probeRC(n int) *int { return &n }

// TestGateProbedValidate checks a gate_probed event needs a task, gate,
// command and rc, and is accepted with them (issue #544).
func TestGateProbedValidate(t *testing.T) {
	t.Parallel()
	ok := Event{Task: "T1", Kind: "gate_probed", Gate: "1", Command: "go test ./...", RC: probeRC(1)}
	if err := Validate(ok); err != nil {
		t.Fatalf("Validate(%+v) = %v, want nil", ok, err)
	}
	const want = "gate_probed event must carry a task, gate, command and rc"
	for name, mut := range map[string]func(*Event){
		"no command": func(e *Event) { e.Command = "" },
		"no gate":    func(e *Event) { e.Gate = "" },
		"no rc":      func(e *Event) { e.RC = nil },
		"no task":    func(e *Event) { e.Task = "" },
	} {
		e := ok
		mut(&e)
		if err := Validate(e); err == nil || !strings.Contains(err.Error(), want) && name != "no task" {
			t.Errorf("%s: Validate = %v, want %q", name, err, want)
		}
	}
}

// TestBaseProbeFailures checks the lookup keys by command, keeps the newest
// probe per command, and omits a command whose newest probe passed.
func TestBaseProbeFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	evs := []Event{
		{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "gate_probed", Gate: "1", Command: "a", RC: probeRC(0)},
		{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "gate_probed", Gate: "2", Command: "b", RC: probeRC(3), Reason: "old"},
		{TS: "2026-09-26T00:00:01Z", Task: "T1", Kind: "gate_probed", Gate: "1", Command: "a", RC: probeRC(2), Reason: "now broken"},
		{TS: "2026-09-26T00:00:01Z", Task: "T1", Kind: "gate_probed", Gate: "2", Command: "b", RC: probeRC(0)},
		{TS: "2026-09-26T00:00:01Z", Task: "T2", Kind: "gate_probed", Gate: "1", Command: "c", RC: probeRC(1)},
	}
	if err := AppendEvents(dir, evs); err != nil {
		t.Fatal(err)
	}
	got, err := BaseProbeFailures(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["a"].Reason != "now broken" || *got["a"].RC != 2 {
		t.Errorf("BaseProbeFailures(T1) = %+v, want only a (rc 2, now broken)", got)
	}
}

// TestGateProbedChangesNoState checks probe events recorded before planned
// derive the same state and verify result as the same run without them, and
// the log chain still passes (issue #544).
func TestGateProbedChangesNoState(t *testing.T) {
	t.Parallel()
	run := []Event{
		{TS: "2026-09-26T00:00:02Z", Task: "T1", Kind: "planned", Brief: "b.md"},
		{TS: "2026-09-26T00:00:03Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Model: "m"},
		{TS: "2026-09-26T00:00:04Z", Task: "T1", Kind: "started", Attempt: "r1", Session: "s1"},
		{TS: "2026-09-26T00:00:05Z", Task: "T1", Kind: "finished", Attempt: "r1", RC: probeRC(0), Reason: "stop"},
		{TS: "2026-09-26T00:00:06Z", Task: "T1", Kind: "reviewed", Attempt: "r1", Verdict: "pass", Session: "lead"},
	}
	probes := []Event{
		{TS: "2026-09-26T00:00:01Z", Task: "T1", Kind: "gate_probed", Gate: "1", Command: "exit 1", RC: probeRC(1), Reason: "boom"},
		{TS: "2026-09-26T00:00:01Z", Task: "T9", Kind: "gate_probed", Gate: "1", Command: "exit 0", RC: probeRC(0)},
	}
	plain, probed := t.TempDir(), t.TempDir()
	if err := AppendEvents(plain, run); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvents(probed, append(append([]Event(nil), probes...), run...)); err != nil {
		t.Fatal(err)
	}
	a, err := ReadEvents(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReadEvents(probed)
	if err != nil {
		t.Fatal(err)
	}
	if sa, sb := Derive(a), Derive(b); !reflect.DeepEqual(sa, sb) {
		t.Errorf("Derive with probes = %+v\nwant %+v", sb, sa)
	}
	va, err := VerifyTasks(plain, VerifyOptions{Dir: plain, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatal(err)
	}
	vb, err := VerifyTasks(probed, VerifyOptions{Dir: probed, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range va.Items {
		va.Items[i].Reason = strings.ReplaceAll(va.Items[i].Reason, plain, "<dir>")
	}
	for i := range vb.Items {
		vb.Items[i].Reason = strings.ReplaceAll(vb.Items[i].Reason, probed, "<dir>")
	}
	if !reflect.DeepEqual(va, vb) {
		t.Errorf("VerifyTasks with probes = %+v\nwant %+v", vb, va)
	}
	if c, err := VerifyLogChain(probed); err != nil || !c.OK() {
		t.Errorf("VerifyLogChain = %+v, %v; want intact", c, err)
	}
	x, err := Explain(b, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := x.Timeline[0].Line, "gate 1 probed on the base tree: fail rc=1: boom"; got != want {
		t.Errorf("explain probe line = %q, want %q", got, want)
	}
}

// TestGateProbedOnlyIsNoTask checks a ledger holding only gate_probed events
// (linted with --probe --task, never planned) gives allTasks nothing and
// verify --all passes with nothing to check (issue #544).
func TestGateProbedOnlyIsNoTask(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	evs := []Event{
		{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "gate_probed", Gate: "1", Command: "exit 1", RC: probeRC(1), Reason: "boom"},
		{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "gate_probed", Gate: "2", Command: "exit 0", RC: probeRC(0)},
	}
	if err := AppendEvents(dir, evs); err != nil {
		t.Fatal(err)
	}
	read, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := allTasks(read); len(got) != 0 {
		t.Errorf("allTasks = %v, want none", got)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, All: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed || len(res.Items) != 0 {
		t.Errorf("VerifyTasks(--all) = %+v, want passed with no items", res)
	}
}

// TestGateProbedEmptyReasonLine checks a failed probe with no reason ends the
// explain line after the exit code, with no dangling ": " (issue #544).
func TestGateProbedEmptyReasonLine(t *testing.T) {
	t.Parallel()
	evs := []Event{
		{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "gate_probed", Gate: "2", Command: "exit 1", RC: probeRC(1)},
		{TS: "2026-09-26T00:00:01Z", Task: "T1", Kind: "planned", Brief: "b.md"},
	}
	x, err := Explain(evs, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := x.Timeline[0].Line, "gate 2 probed on the base tree: fail rc=1"; got != want {
		t.Errorf("explain probe line = %q, want %q", got, want)
	}
}

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
