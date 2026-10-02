package flywheel

import (
	"errors"
	"strings"
	"testing"
)

// groupFixture is initGroup's two members A (a.go) and B (b.go) with one
// gate each, validated as validated (a group spec) on the combined tree.
func groupFixture(t *testing.T, validated string) string {
	t.Helper()
	dir := initGroup(t, "a.go", "b.go", []string{"exit 0"}, []string{"exit 0"},
		map[string]string{"a.go": "package y\n", "b.go": "package y\n"})
	if _, err := ValidateGroup(dir, validated, ValidateOptions{Dir: dir, Workdir: dir}); err != nil {
		t.Fatalf("ValidateGroup(%s) error = %v", validated, err)
	}
	return dir
}

// statuses maps each task to its derived status.
func statuses(t *testing.T, dir string) map[string]string {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, ts := range Derive(events).Tasks {
		out[ts.ID] = ts.Status
	}
	return out
}

func TestInspectGroup(t *testing.T) {
	t.Parallel()
	group := "tasks:A,B"
	dir := groupFixture(t, group)
	got, err := InspectGroup(dir, group, InspectOptions{Verdict: "pass", Session: "i1"})
	if err != nil {
		t.Fatalf("InspectGroup() error = %v", err)
	}
	if strings.Join(got, ",") != "A,B" {
		t.Errorf("inspected = %v, want [A B]", got)
	}
	ins := kindEvents(t, dir, "inspected")
	if len(ins) != 2 {
		t.Fatalf("inspected events = %d, want 2", len(ins))
	}
	for _, e := range ins {
		if e.Group != GroupTask(group) || e.Verdict != "pass" {
			t.Errorf("inspected %s = group %q verdict %q, want %q pass", e.Task, e.Group, e.Verdict, GroupTask(group))
		}
	}
	if s := statuses(t, dir); s["A"] != "passed" || s["B"] != "passed" {
		t.Errorf("statuses = %v, want A and B passed", s)
	}
}

func TestInspectGroupRefusesAllWhenOneLacksReadings(t *testing.T) {
	t.Parallel()
	dir := groupFixture(t, "tasks:A,B")
	// B finishes again after the group pass: its readings no longer count.
	rc := 0
	if err := AppendEvent(dir, Event{Task: "B", Kind: "finished", Attempt: "r1", Session: "w3", RC: &rc}); err != nil {
		t.Fatal(err)
	}
	_, err := InspectGroup(dir, "tasks:A,B", InspectOptions{Verdict: "pass", Session: "i1"})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T3" {
		t.Fatalf("InspectGroup() error = %v, want a T3 refusal", err)
	}
	if !strings.Contains(err.Error(), "member B") {
		t.Errorf("error %q does not name member B", err)
	}
	if ins := kindEvents(t, dir, "inspected"); len(ins) != 0 {
		t.Errorf("inspected events = %+v, want none", ins)
	}
}

func TestLandGroup(t *testing.T) {
	t.Parallel()
	group := "tasks:A,B"
	dir := groupFixture(t, group)
	if _, err := InspectGroup(dir, group, InspectOptions{Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectGroup() error = %v", err)
	}
	git(t, dir, []string{"add", "a.go", "b.go"})
	git(t, dir, []string{"commit", "-m", "group"})
	commit := headCommit(dir)
	got, err := LandGroup(dir, group, commit, "merged", "")
	if err != nil {
		t.Fatalf("LandGroup() error = %v", err)
	}
	if strings.Join(got, ",") != "A,B" {
		t.Errorf("landed = %v, want [A B]", got)
	}
	for _, task := range []string{"A", "B"} {
		l := landedEvents(t, dir, task)
		if len(l) != 1 || l[0].Group != GroupTask(group) || l[0].Commit != commit {
			t.Errorf("landed %s = %+v, want one on %s with group %s", task, l, commit, GroupTask(group))
		}
	}
	again, err := LandGroup(dir, group, commit, "merged", "")
	if err != nil || len(again) != 0 {
		t.Errorf("rerun = %v, %v, want nothing landed and no error", again, err)
	}
	if n := len(kindEvents(t, dir, "landed")); n != 2 {
		t.Errorf("landed events after rerun = %d, want 2", n)
	}
}

func TestLandGroupRefusesUnpassedMember(t *testing.T) {
	t.Parallel()
	dir := groupFixture(t, "tasks:A,B")
	if _, err := InspectGroup(dir, "tasks:A", InspectOptions{Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectGroup(A) error = %v", err)
	}
	git(t, dir, []string{"add", "a.go", "b.go"})
	git(t, dir, []string{"commit", "-m", "group"})
	_, err := LandGroup(dir, "tasks:A,B", headCommit(dir), "", "")
	if got := refusalRule(t, err); got != "T5" {
		t.Errorf("rule = %q, want T5", got)
	}
	if !strings.Contains(err.Error(), "member B") {
		t.Errorf("error %q does not name member B", err)
	}
	if n := len(kindEvents(t, dir, "landed")); n != 0 {
		t.Errorf("landed events = %d, want none", n)
	}
}

func TestGroupLandedMetric(t *testing.T) {
	t.Parallel()
	var events []Event
	for _, u := range []struct{ task, group string }{{"G", "group:g1"}, {"S", ""}} {
		events = append(events,
			Event{TS: "2026-09-01T01:00:00Z", Task: u.task, Kind: "planned", Brief: "b.txt"},
			Event{TS: "2026-09-01T02:00:00Z", Task: u.task, Kind: "dispatched", Attempt: "r1", Worker: "w1"},
			Event{TS: "2026-09-01T03:00:00Z", Task: u.task, Kind: "finished", Attempt: "r1"},
			Event{TS: "2026-09-01T04:00:00Z", Task: u.task, Kind: "inspected", Verdict: "pass", Session: "i1", Group: u.group},
			Event{TS: "2026-09-01T05:00:00Z", Task: u.task, Kind: "landed", Commit: "abc1234", Group: u.group},
		)
	}
	q := Metrics(events, metricsConfig, metricsWindow(t)).Quality
	if q.Landed != 2 || q.GroupLanded != 1 || q.FirstPass != 1 {
		t.Errorf("landed %d, group landed %d, first pass %d, want 2, 1 and 1 (S only)", q.Landed, q.GroupLanded, q.FirstPass)
	}
}
