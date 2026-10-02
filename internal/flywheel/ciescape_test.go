package flywheel

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// ciFailedEvents returns dir's ci_failed events, in log order.
func ciFailedEvents(t *testing.T, dir string) []Event {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	return slices.DeleteFunc(events, func(e Event) bool { return e.Kind != "ci_failed" })
}

// TestCIEscapeRefusal (issue #776): the newest ci_failed event refuses a pass
// until the brief carries a gate that event's Gates lack.
func TestCIEscapeRefusal(t *testing.T) {
	t.Parallel()
	failed := func(ts, note string, gates ...string) Event {
		return Event{TS: ts, Task: "T1", Kind: "ci_failed", Attempt: "r1", Note: note, Gates: gates}
	}
	cases := []struct {
		name   string
		events []Event
		gates  []string
		want   string // "" = no refusal, else the note the Fix names
	}{
		{"no ci_failed", []Event{{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned"}}, []string{"a", "b"}, ""},
		{"same gates", []Event{failed("2026-09-12T01:00:00Z", "#7: lint", "a", "b")}, []string{"a", "b"}, "#7: lint"},
		{"gained a gate", []Event{failed("2026-09-12T01:00:00Z", "#7: lint", "a", "b")}, []string{"a", "b", "c"}, ""},
		{"newest has the gate", []Event{
			failed("2026-09-12T01:00:00Z", "#7: lint", "a", "b"),
			failed("2026-09-12T02:00:00Z", "#8: e2e", "a", "b", "c"),
		}, []string{"a", "b", "c"}, "#8: e2e"},
		{"another task", []Event{{TS: "2026-09-12T01:00:00Z", Task: "T2", Kind: "ci_failed", Note: "#9: x", Gates: []string{"a"}}}, []string{"a"}, ""},
	}
	for _, c := range cases {
		r := ciEscapeRefusal(c.events, "T1", BriefHeader{Gates: c.gates})
		if c.want == "" {
			if r != nil {
				t.Errorf("%s: ciEscapeRefusal() = %v, want nil", c.name, r)
			}
			continue
		}
		if r == nil || r.Rule != "ci-escape" || !strings.Contains(r.Fix, "CI failed on "+c.want) || !strings.Contains(r.Fix, "flywheel run T1 --delta") {
			t.Errorf("%s: ciEscapeRefusal() = %v, want rule ci-escape naming %q", c.name, r, c.want)
		}
	}
}

// TestShipRecordsCIFailed (issue #776): a failed check with none pending
// appends a ci_failed event naming the PR and the check, its gates the
// brief's; a timeout is not an escape and appends none.
func TestShipRecordsCIFailed(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}, Failed: []string{"lint"}}}}
	if _, err := ciShip(t, f, ff, ShipOptions{}); !errors.Is(err, ErrShipCI) {
		t.Fatalf("ship error = %v, want ErrShipCI", err)
	}
	got := ciFailedEvents(t, f.dir)
	if len(got) != 1 || got[0].Task != "T" || got[0].Note != "#7: lint" || !slices.Equal(got[0].Gates, []string{"exit 0"}) {
		t.Fatalf("ci_failed events = %+v, want one with note #7: lint and gates [exit 0]", got)
	}

	f = newShipFixture(t, "exit 0", true)
	ff = &fakeForge{checks: []ChecksState{{Pending: []string{"lint"}}}}
	if _, err := ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond}); !errors.Is(err, ErrShipCI) {
		t.Fatalf("timed-out ship error = %v, want ErrShipCI", err)
	}
	if got := ciFailedEvents(t, f.dir); len(got) != 0 {
		t.Errorf("a ci timeout appended %+v, want no ci_failed", got)
	}
}

// TestCIEscapeMetric (issue #776): a landed unit with a ci_failed event before
// its landing is one CI escape; a landed unit without one is none.
func TestCIEscapeMetric(t *testing.T) {
	t.Parallel()
	var events []Event
	for _, id := range []string{"esc", "clean"} {
		events = append(events,
			Event{TS: "2026-09-01T01:00:00Z", Task: id, Kind: "planned", Brief: "brief.txt"},
			Event{TS: "2026-09-01T01:30:00Z", Task: id, Kind: "dispatched", Attempt: "r1", Model: "m1", Worker: "w1"},
			Event{TS: "2026-09-01T02:00:00Z", Task: id, Kind: "finished", Attempt: "r1", Reason: "stop"},
			Event{TS: "2026-09-01T03:00:00Z", Task: id, Kind: "landed"})
	}
	events = append(events, Event{TS: "2026-09-01T02:30:00Z", Task: "esc", Kind: "ci_failed", Attempt: "r1", Note: "#7: lint", Gates: []string{"exit 0"}})
	rep := Metrics(events, metricsConfig, metricsWindow(t))
	if rep.Quality.Landed != 2 || rep.Quality.CIEscapes != 1 {
		t.Fatalf("landed %d, ci escapes %d, want 2 and 1", rep.Quality.Landed, rep.Quality.CIEscapes)
	}
	ev := rep.Evidence["quality.ci_escapes"]
	if len(ev) != 1 || ev[0].Task != "esc" || ev[0].Group != "ci-escaped" || !strings.Contains(ev[0].Value, "#7: lint") {
		t.Errorf("quality.ci_escapes evidence = %+v, want esc ci-escaped naming #7: lint", ev)
	}
}

// TestLogCIFailed (issue #776): the event flywheel log --kind ci_failed
// records carries the brief's gates.
func TestLogCIFailed(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0", "exit 0 && true"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	e, err := CIFailedEvent(dir, "T1", "", "#12: e2e", time.Now())
	if err != nil {
		t.Fatalf("CIFailedEvent() error = %v", err)
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	got := ciFailedEvents(t, dir)
	if len(got) != 1 || got[0].Task != "T1" || got[0].Note != "#12: e2e" || !slices.Equal(got[0].Gates, []string{"exit 0", "exit 0 && true"}) {
		t.Fatalf("ci_failed events = %+v, want one with the brief's two gates", got)
	}
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "ci_failed"}); err == nil {
		t.Error("AppendEvent() accepted a ci_failed event with no note")
	}
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "note", Note: "x", Gates: []string{"a"}}); err == nil {
		t.Error("AppendEvent() accepted gates on a note event")
	}
}

// TestInspectRefusesCIEscape (issue #776): a validated unit whose CI failed
// after its gates passed cannot pass inspect until the brief gains a gate.
func TestInspectRefusesCIEscape(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	e, err := CIFailedEvent(dir, "T1", "r1", "#7: lint", time.Now())
	if err != nil {
		t.Fatalf("CIFailedEvent() error = %v", err)
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass after a CI escape")
	}
	if got := refusalRule(t, err); got != "ci-escape" || !strings.Contains(err.Error(), "CI failed on #7: lint") {
		t.Errorf("refusal = %v, want rule ci-escape naming #7: lint", err)
	}
}
