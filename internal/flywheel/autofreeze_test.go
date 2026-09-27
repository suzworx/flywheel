package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// freezeFixture writes a two-worker config (models a and b; cc as the
// controller block when set) and a ledger where T1 on a is rate-limited until
// recoverNow+1h and T2 on b finished with reasonB, reset recoverNow+2h.
func freezeFixture(t *testing.T, reasonB string, cc *ControllerConfig) string {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{Version: 1, Controller: cc, Workers: []Worker{
		{Name: "wa", Adapter: "claude", Model: "a"}, {Name: "wb", Adapter: "claude", Model: "b"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	recoverLedger(t, dir, append(limitedUnit("T1", "a", "rate-limited", recoverNow.Add(time.Hour)),
		limitedUnit("T2", "b", reasonB, recoverNow.Add(2*time.Hour))...)...)
	return dir
}

// kindEvents returns the ledger's events of kind.
func kindEvents(t *testing.T, dir, kind string) []Event {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// stopUnit appends a unit a stopping suspension stopped: finished suspended,
// its brief <task>.brief.txt owning a.go.
func stopUnit(t *testing.T, dir, task string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, task+".brief.txt"), []byte("owns: a.go\nneeds: none\ngate: go test ./...\n\n# TASK: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recoverLedger(t, dir, Event{Task: task, Kind: "planned", Brief: task + ".brief.txt"},
		Event{Task: task, Kind: "dispatched", Attempt: "r1", Model: "a"},
		Event{Task: task, Kind: "finished", Attempt: "r1", Model: "a", Session: "ses_" + task, Reason: "suspended"})
}

// TestAutoFreezeAllPaused: with both worker models paused, AutoFreeze records
// one automatic stopping suspension until the earlier reset; again, nothing.
func TestAutoFreezeAllPaused(t *testing.T) {
	t.Parallel()
	dir := freezeFixture(t, "rate-limited", nil)
	for i, want := range []bool{true, false} {
		acted, err := AutoFreeze(dir, recoverNow, "")
		if err != nil || acted != want {
			t.Fatalf("AutoFreeze #%d = %v, %v; want %v", i+1, acted, err, want)
		}
	}
	sus := kindEvents(t, dir, "suspended")
	wantUntil := recoverNow.Add(time.Hour).UTC().Format(time.RFC3339)
	if len(sus) != 1 || !sus[0].Stop || sus[0].Until != wantUntil || sus[0].Reason != autoFreezeReason || sus[0].Session != "controller" {
		t.Fatalf("suspended events = %+v; want one auto stop until %s by controller", sus, wantUntil)
	}
	if sus[0].Note != "tokens exhausted: a, b paused until 01:00 UTC" {
		t.Errorf("note = %q", sus[0].Note)
	}
	if _, err := os.Stat(suspendStopPath(dir)); err != nil {
		t.Errorf("stop sentinel: %v", err)
	}
}

// TestAutoFreezeOneModelFree: a worker whose model is not paused keeps the
// factory running.
func TestAutoFreezeOneModelFree(t *testing.T) {
	t.Parallel()
	dir := freezeFixture(t, "stop", nil)
	if acted, err := AutoFreeze(dir, recoverNow, ""); err != nil || acted {
		t.Fatalf("AutoFreeze = %v, %v; want no freeze", acted, err)
	}
	if sus := kindEvents(t, dir, "suspended"); len(sus) != 0 {
		t.Errorf("suspended events = %+v; want none", sus)
	}
}

// TestAutoFreezeThawReturnsUnitsOnce: before until AutoThaw does nothing;
// after it records unsuspended and returns the suspended and rate-limited
// units once each, and a second call returns nothing.
func TestAutoFreezeThawReturnsUnitsOnce(t *testing.T) {
	t.Parallel()
	dir := freezeFixture(t, "rate-limited", nil)
	if _, err := AutoFreeze(dir, recoverNow, ""); err != nil {
		t.Fatal(err)
	}
	stopUnit(t, dir, "L")
	if got, err := AutoThaw(dir, recoverNow.Add(30*time.Minute), ""); err != nil || got != nil {
		t.Fatalf("AutoThaw before until = %+v, %v; want nil", got, err)
	}
	got, err := AutoThaw(dir, recoverNow.Add(61*time.Minute), "")
	var tasks []string
	for _, ta := range got {
		tasks = append(tasks, ta.Task)
	}
	if err != nil || !slices.Equal(tasks, []string{"L", "T1", "T2"}) {
		t.Fatalf("AutoThaw after until = %+v, %v; want L, T1, T2", got, err)
	}
	if un := kindEvents(t, dir, "unsuspended"); len(un) != 1 || un[0].Note != "tokens returned" {
		t.Errorf("unsuspended events = %+v; want one, tokens returned", un)
	}
	if _, err := os.Stat(suspendStopPath(dir)); !os.IsNotExist(err) {
		t.Errorf("stop sentinel left behind: %v", err)
	}
	if again, err := AutoThaw(dir, recoverNow.Add(62*time.Minute), ""); err != nil || again != nil {
		t.Errorf("second AutoThaw = %+v, %v; want nil", again, err)
	}
}

// freezeTick runs TickWith at at with a starter appending to calls.
func freezeTick(t *testing.T, dir string, at time.Time, calls *[]string) TickResult {
	t.Helper()
	res, err := TickWith(dir, at, TickOptions{Session: "controller",
		Start: func(task string) error { *calls = append(*calls, task); return nil }})
	if err != nil {
		t.Fatalf("TickWith at %s: %v", at, err)
	}
	return res
}

// TestAutoFreezeManualNeverThawed: a manual suspension past its until is
// neither thawed by AutoThaw nor re-dispatched by the controller.
func TestAutoFreezeManualNeverThawed(t *testing.T) {
	t.Parallel()
	dir := freezeFixture(t, "stop", nil)
	stopUnit(t, dir, "L")
	if err := SuspendWith(dir, "lead", SuspendOptions{Reason: "freeze", Stop: true, Until: recoverNow.Add(time.Hour), Now: recoverNow}); err != nil {
		t.Fatal(err)
	}
	if got, err := AutoThaw(dir, recoverNow.Add(2*time.Hour), ""); err != nil || got != nil {
		t.Fatalf("AutoThaw = %+v, %v; want nil", got, err)
	}
	var calls []string
	res := freezeTick(t, dir, recoverNow.Add(2*time.Hour), &calls)
	if slices.Contains(calls, "L") || res.Thaw || len(res.Thawed) != 0 {
		t.Errorf("tick started %q, thawed %v %+v; want L never started", calls, res.Thaw, res.Thawed)
	}
	if un := kindEvents(t, dir, "unsuspended"); len(un) != 0 {
		t.Errorf("unsuspended events = %+v; want none", un)
	}
}

// TestAutoFreezeTick: a tick while exhausted freezes; a tick after the reset
// thaws and starts each unit once; later ticks and auto-resume start none again.
func TestAutoFreezeTick(t *testing.T) {
	t.Parallel()
	dir := freezeFixture(t, "rate-limited", nil)
	var calls []string
	res := freezeTick(t, dir, recoverNow, &calls)
	if res.Froze == nil || res.Froze.Until != recoverNow.Add(time.Hour).UTC().Format(time.RFC3339) || len(calls) != 0 {
		t.Fatalf("freeze tick: Froze = %+v, calls = %q; want a freeze until +1h, no start", res.Froze, calls)
	}
	stopUnit(t, dir, "L")
	if res = freezeTick(t, dir, recoverNow.Add(30*time.Minute), &calls); res.Thaw || res.Froze != nil || len(calls) != 0 {
		t.Fatalf("tick while frozen: %+v, calls = %q; want nothing", res, calls)
	}
	res = freezeTick(t, dir, recoverNow.Add(61*time.Minute), &calls)
	if !res.Thaw || len(res.Thawed) != 3 || !slices.Equal(calls, []string{"L", "T1", "T2"}) {
		t.Fatalf("thaw tick: %+v, calls = %q; want L, T1, T2 started once", res, calls)
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "briefs", "L.delta.txt")); err != nil || !strings.Contains(string(b), "stopped by a factory suspension") {
		t.Errorf("L continue delta = %q, %v; want the suspension continue text", b, err)
	}
	for _, at := range []time.Duration{62 * time.Minute, 3 * time.Hour} {
		if res = freezeTick(t, dir, recoverNow.Add(at), &calls); len(calls) != 3 || res.Froze != nil {
			t.Fatalf("tick at +%s: %+v, calls = %q; want no second start", at, res, calls)
		}
	}
}

// TestAutoFreezeConfigOff: controller.auto_freeze false neither freezes nor
// thaws.
func TestAutoFreezeConfigOff(t *testing.T) {
	t.Parallel()
	off := false
	dir := freezeFixture(t, "rate-limited", &ControllerConfig{AutoFreeze: &off})
	var calls []string
	if res := freezeTick(t, dir, recoverNow, &calls); res.Froze != nil {
		t.Fatalf("tick froze with auto_freeze false: %+v", res.Froze)
	}
	if sus := kindEvents(t, dir, "suspended"); len(sus) != 0 {
		t.Fatalf("suspended events = %+v; want none", sus)
	}
	if err := SuspendWith(dir, "controller", SuspendOptions{Reason: "x", Stop: true, Auto: true, Until: recoverNow.Add(time.Hour), Now: recoverNow}); err != nil {
		t.Fatal(err)
	}
	if res := freezeTick(t, dir, recoverNow.Add(61*time.Minute), &calls); res.Thaw || len(res.Thawed) != 0 {
		t.Errorf("tick thawed with auto_freeze false: %+v", res)
	}
	if un := kindEvents(t, dir, "unsuspended"); len(un) != 0 {
		t.Errorf("unsuspended events = %+v; want none", un)
	}
}
