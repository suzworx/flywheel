package flywheel

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

const vsViewBase = "0123456789abcdef0123456789abcdef01234567"

// vsPass is a vs-base pass of gate on tree with failing lines (base_failing
// on base).
func vsPass(task, gate, tree string, failing, baseFailing int) Event {
	rc := 0
	return Event{Task: task, Kind: "validated", Attempt: "r1", Gate: gate, Tree: tree, RC: &rc, Reason: "vs-base",
		VsBase: &VsBaseReading{Base: vsViewBase, RC: 1, BaseRC: 1, Failing: failing, BaseFailing: baseFailing}}
}

// plainReading is an ordinary reading of gate on tree with exit code rc.
func plainReading(task, gate, tree string, rc int) Event {
	return Event{Task: task, Kind: "validated", Attempt: "r1", Gate: gate, Tree: tree, RC: &rc}
}

// TestVsBasePasses checks which readings count as live vs-base passes.
func TestVsBasePasses(t *testing.T) {
	t.Parallel()
	gates := func(evs []Event) string {
		var ids []string
		for _, e := range evs {
			ids = append(ids, e.Gate)
		}
		return strings.Join(ids, ",")
	}
	for _, c := range []struct {
		name   string
		events []Event
		want   string
	}{
		{"one pass", []Event{vsPass("T", "1", "t1", 24, 30)}, "1"},
		{"superseded by a plain pass", []Event{vsPass("T", "1", "t1", 24, 30), plainReading("T", "1", "t1", 0)}, ""},
		{"superseded by a failure", []Event{vsPass("T", "1", "t1", 24, 30), plainReading("T", "1", "t1", 1)}, ""},
		{"other tree", []Event{vsPass("T", "1", "t0", 24, 30), plainReading("T", "1", "t1", 0)}, ""},
		{"other task", []Event{vsPass("U", "1", "t1", 24, 30)}, ""},
		{"two gates", []Event{vsPass("T", "1", "t1", 4, 5), plainReading("T", "2", "t1", 0), vsPass("T", "live1", "t1", 20, 25)}, "1,live1"},
	} {
		if got := gates(vsBasePasses(c.events, "T", "t1")); got != c.want {
			t.Errorf("%s: gates = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestFactoryVsBase checks the floor's VsBaseFailing, the text suffix and the
// JSON field, present only on the unit with vs-base passes.
func TestFactoryVsBase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recoverLedger(t, dir,
		Event{Task: "V", Kind: "planned", Brief: "brief.txt"},
		Event{Task: "V", Kind: "dispatched", Attempt: "r1", Model: "m"},
		Event{Task: "V", Kind: "finished", Attempt: "r1", Model: "m", Session: "ses_v", Reason: "stop"},
		vsPass("V", "1", "t1", 20, 25),
		vsPass("V", "2", "t1", 4, 5),
		Event{Task: "W", Kind: "planned", Brief: "brief.txt"},
		Event{Task: "W", Kind: "dispatched", Attempt: "r1", Model: "m"},
		Event{Task: "W", Kind: "finished", Attempt: "r1", Model: "m", Session: "ses_w", Reason: "stop"},
		plainReading("W", "1", "t2", 0),
	)
	w := NewWatcher()
	fl, err := w.Refresh(dir, recoverNow)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	units := map[string]Unit{}
	for _, u := range fl.Units {
		units[u.Task] = u
	}
	if got := units["V"].VsBaseFailing; got != 24 {
		t.Errorf("V VsBaseFailing = %d, want 24", got)
	}
	if got := units["W"].VsBaseFailing; got != 0 {
		t.Errorf("W VsBaseFailing = %d, want 0", got)
	}
	var b bytes.Buffer
	RenderText(&b, fl, 120, false)
	rows := map[string]string{}
	for _, line := range strings.Split(b.String(), "\n") {
		if f := strings.Fields(line); len(f) > 0 && (f[0] == "V" || f[0] == "W") && rows[f[0]] == "" {
			rows[f[0]] = line
		}
	}
	if !strings.HasSuffix(rows["V"], " vs-base 24") {
		t.Errorf("V row = %q, want the vs-base 24 suffix\n%s", rows["V"], b.String())
	}
	if rows["W"] == "" || strings.Contains(rows["W"], "vs-base") {
		t.Errorf("W row = %q, want a row without vs-base", rows["W"])
	}
	var j bytes.Buffer
	RenderJSON(&j, fl)
	if n := strings.Count(j.String(), `"vs_base_failing"`); n != 1 || !strings.Contains(j.String(), `"vs_base_failing": 24`) {
		t.Errorf("JSON has %d vs_base_failing keys, want one of 24:\n%s", n, j.String())
	}
}

// TestVsBaseSummary checks the lines inspect --verdict pass prints.
func TestVsBaseSummary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	recoverLedger(t, dir,
		Event{Task: "T", Kind: "planned", Brief: "brief.txt"},
		vsPass("T", "1", "t1", 24, 30),
	)
	got, err := VsBaseSummary(dir, "T")
	if err != nil || len(got) != 0 {
		t.Errorf("before inspection = %q, %v; want none", got, err)
	}
	recoverLedger(t, dir, Event{TS: recoverNow.Format(time.RFC3339), Task: "T", Kind: "inspected", Verdict: "pass", Tree: "t1"})
	got, err = VsBaseSummary(dir, "T")
	want := "gate 1 passed vs base 0123456789ab: 24 failing on base too (30 on base)"
	if err != nil || len(got) != 1 || got[0] != want {
		t.Errorf("after the pass = %q, %v; want [%q]", got, err, want)
	}
}

// TestVerifyV1 checks rule V1 on sound and inconsistent vs-base passes.
func TestVerifyV1(t *testing.T) {
	t.Parallel()
	mod := func(f func(*Event)) Event {
		e := vsPass("T", "1", "t1", 24, 30)
		f(&e)
		return e
	}
	for _, c := range []struct {
		name   string
		events []Event
		pass   bool
		reason string
	}{
		{"sound", []Event{vsPass("T", "1", "t1", 24, 30)}, true, "1 vs-base passes, 24 failing on base too"},
		{"new lines", []Event{mod(func(e *Event) { e.VsBase.New = []string{"--- FAIL: TestX"} })}, false, ""},
		{"no reading", []Event{mod(func(e *Event) { e.VsBase = nil })}, false, ""},
		{"unit passed", []Event{mod(func(e *Event) { e.VsBase.RC = 0 })}, false, ""},
		{"base passed", []Event{mod(func(e *Event) { e.VsBase.BaseRC = 0 })}, false, ""},
		{"no base", []Event{mod(func(e *Event) { e.VsBase.Base = "" })}, false, ""},
		{"rc not 0", []Event{mod(func(e *Event) { rc := 1; e.RC = &rc })}, false, ""},
		{"forged reason", []Event{mod(func(e *Event) { e.Reason = "" })}, false, ""},
		{"failed reading", []Event{mod(func(e *Event) { rc := 1; e.RC, e.Reason = &rc, "" })}, true, "no vs-base pass"},
		{"none", []Event{plainReading("T", "1", "t1", 0)}, true, "no vs-base pass"},
	} {
		items := ruleV1("T", c.events)
		if len(items) != 1 || items[0].Rule != "V1" || items[0].Pass != c.pass || (c.reason != "" && items[0].Reason != c.reason) {
			t.Errorf("%s: ruleV1 = %+v, want one V1 item pass=%v reason %q", c.name, items, c.pass, c.reason)
		}
	}
}
