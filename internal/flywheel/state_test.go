package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func stateJSON(st State) string {
	b, err := json.Marshal(st)
	if err != nil {
		return ""
	}
	return string(b)
}

func concat(a, b []Event) []Event {
	out := make([]Event, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func withRC(e Event, rc int) Event {
	p := new(int)
	*p = rc
	e.RC = p
	return e
}

func findTask(st State, id string) (TaskState, bool) {
	for _, ts := range st.Tasks {
		if ts.ID == id {
			return ts, true
		}
	}
	return TaskState{}, false
}

func TestDeriveStatusMapping(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "T-plan", Kind: "planned"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-disp", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-run", Kind: "started"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-fin", Kind: "finished"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-pass", Kind: "reviewed", Verdict: "pass"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-corr", Kind: "reviewed", Verdict: "correct"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-rej", Kind: "reviewed", Verdict: "reject"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-block", Kind: "blocked"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-land", Kind: "landed"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-amend", Kind: "planned", Brief: "b1"},
		{TS: "2026-09-12T00:00:01Z", Task: "T-amend", Kind: "amended", Brief: "b2"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-attr", Kind: "dispatched", Session: "s1", Model: "m1", Attempt: "r1", Reason: "first"},
		{TS: "2026-09-12T00:00:01Z", Task: "T-attr", Kind: "finished", Session: "s2", Reason: "ok"},
		{TS: "2026-09-12T00:00:00Z", Task: "T-att2", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-12T00:00:01Z", Task: "T-att2", Kind: "dispatched", Attempt: "c1"},
	}
	events[11] = withRC(events[11], 3)

	st := Derive(events)
	for id, want := range map[string]string{
		"T-plan":  "planned",
		"T-disp":  "dispatched",
		"T-run":   "running",
		"T-fin":   "finished",
		"T-pass":  "passed",
		"T-corr":  "needs-correction",
		"T-rej":   "rejected",
		"T-block": "blocked",
		"T-land":  "landed",
	} {
		ts, ok := findTask(st, id)
		if !ok {
			t.Errorf("task %s missing from derived state", id)
			continue
		}
		if ts.Status != want {
			t.Errorf("%s status = %q, want %q", id, ts.Status, want)
		}
	}

	amend, ok := findTask(st, "T-amend")
	if !ok {
		t.Fatal("T-amend missing from derived state")
	}
	if amend.Status != "planned" {
		t.Errorf("amended task status = %q, want planned (amended does not change status)", amend.Status)
	}
	if amend.Brief != "b2" {
		t.Errorf("T-amend brief = %q, want b2 (from latest planned/amended)", amend.Brief)
	}

	attr, ok := findTask(st, "T-attr")
	if !ok {
		t.Fatal("T-attr missing from derived state")
	}
	if attr.Status != "finished" || attr.Session != "s2" || attr.Model != "m1" ||
		attr.Attempt != "r1" || attr.Reason != "ok" || attr.Attempts != 1 {
		t.Errorf("T-attr fields wrong: %v", attr)
	}
	if attr.RC == nil || *attr.RC != 3 {
		t.Errorf("T-attr rc = %v, want 3", attr.RC)
	}
	if attr.UpdatedAt != "2026-09-12T00:00:01Z" {
		t.Errorf("T-attr updated = %q, want latest TS", attr.UpdatedAt)
	}

	att2, ok := findTask(st, "T-att2")
	if !ok {
		t.Fatal("T-att2 missing from derived state")
	}
	if att2.Attempts != 2 {
		t.Errorf("T-att2 attempts = %d, want 2 (two dispatched events)", att2.Attempts)
	}
	if att2.Attempt != "c1" {
		t.Errorf("T-att2 attempt = %q, want c1 (last non-empty)", att2.Attempt)
	}
}

func TestDeriveOrderIndependent(t *testing.T) {
	t.Parallel()
	a := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "A", Kind: "planned"},
		{TS: "2026-09-12T00:00:00Z", Task: "B", Kind: "started"},
		{TS: "2026-09-12T00:00:00Z", Task: "C", Kind: "finished"},
	}
	b := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "B", Kind: "finished"},
		{TS: "2026-09-12T00:00:00Z", Task: "C", Kind: "reviewed", Verdict: "pass"},
	}
	ab := stateJSON(Derive(concat(a, b)))
	ba := stateJSON(Derive(concat(b, a)))
	if ab != ba {
		t.Errorf("Derive(a+b) != Derive(b+a) with a TS tie:\n%s\nvs\n%s", ab, ba)
	}
}

func TestDeriveOrderingSameSecond(t *testing.T) {
	t.Parallel()
	// dispatched, started and finished for T1 share one second-precision TS;
	// appended in reverse order they must still derive finished.
	events := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "finished"},
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "started"},
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "finished" {
		t.Errorf("T1 status = %q, want finished (started must not win over finished)", ts.Status)
	}
	if ts.Attempts != 1 {
		t.Errorf("T1 attempts = %d, want 1", ts.Attempts)
	}
}

func TestDeriveOrderingWithWorkerPlanAndReport(t *testing.T) {
	t.Parallel()
	// dispatched, started, worker_plan, report and finished share one
	// second-precision TS; appended in reverse they must still derive
	// finished, and worker_plan/report must not change status.
	events := []Event{
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "finished"},
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "report", Path: "r.md", SHA256: "x"},
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "worker_plan", Path: "p.md", SHA256: "y"},
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "started"},
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "finished" {
		t.Errorf("T1 status = %q, want finished (report must not win over finished)", ts.Status)
	}
	if ts.Attempts != 1 {
		t.Errorf("T1 attempts = %d, want 1", ts.Attempts)
	}
	if ts.Attempt != "r1" {
		t.Errorf("T1 attempt = %q, want r1 (carried from dispatched)", ts.Attempt)
	}
}

func TestDeriveOrderingNanoVsSecond(t *testing.T) {
	t.Parallel()
	// A nanosecond-precision TS in the same second sorts after the
	// second-precision one even though '.' sorts before 'Z' as text.
	a := []Event{
		{TS: "2026-09-12T00:00:00.500000000Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "finished"},
	}
	st := Derive(a)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "dispatched" {
		t.Errorf("T1 status = %q, want dispatched (nanosecond event is later)", ts.Status)
	}

	// And derivation is still order independent.
	b := []Event{
		{TS: "2026-09-12T00:00:00.250000000Z", Task: "T2", Kind: "started"},
		{TS: "2026-09-12T00:00:00.100000000Z", Task: "T2", Kind: "planned"},
	}
	ab := stateJSON(Derive(concat(a, b)))
	ba := stateJSON(Derive(concat(b, a)))
	if ab != ba {
		t.Errorf("Derive(a+b) != Derive(b+a) with nano/second mix:\n%s\nvs\n%s", ab, ba)
	}
}

func TestWriteStateReplacesOnlyMarkedBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "flywheel.md")
	md := "before\n<!-- flywheel:status:start -->\nOLD TABLE\n<!-- flywheel:status:end -->\nafter\n"
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Model: "deepseek"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:01Z", Task: "T1", Kind: "started"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	st, err := WriteState(dir)
	if err != nil {
		t.Fatalf("WriteState() error = %v", err)
	}
	if st.Version != 2 {
		t.Errorf("state version = %d, want 2", st.Version)
	}

	b, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	content := string(b)
	if !strings.HasPrefix(content, "before\n<!-- flywheel:status:start -->\n") {
		t.Errorf("text before the marked block changed:\n%q", content)
	}
	if !strings.HasSuffix(content, "\n<!-- flywheel:status:end -->\nafter\n") {
		t.Errorf("text after the marked block changed:\n%q", content)
	}
	if strings.Contains(content, "OLD TABLE") {
		t.Error("old table body was not replaced")
	}
	if !strings.Contains(content, "| T1 | running | 0 |") {
		t.Errorf("status block missing T1 row:\n%q", content)
	}
	if !strings.Contains(content, "deepseek") {
		t.Errorf("status block missing model:\n%q", content)
	}

	// Same input gives byte-identical output on the second run.
	if _, err := WriteState(dir); err != nil {
		t.Fatalf("WriteState() second run error = %v", err)
	}
	b2, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("re-read flywheel.md: %v", err)
	}
	if string(b2) != string(b) {
		t.Error("WriteState() output changed between runs")
	}
}

func TestWriteStateAppendsBlockWhenMarkersMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "flywheel.md")
	md := "custom content\nsecond line\n"
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	if _, err := WriteState(dir); err != nil {
		t.Fatalf("WriteState() error = %v", err)
	}
	b, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	content := string(b)
	if !strings.HasPrefix(content, "custom content\nsecond line\n") {
		t.Errorf("existing markdown text changed:\n%q", content)
	}
	if !strings.Contains(content, statusStartMarker) || !strings.Contains(content, statusEndMarker) {
		t.Error("status markers were not appended")
	}
	if !strings.Contains(content, "| T1 | planned | 0 |") {
		t.Errorf("status block missing T1 row:\n%q", content)
	}
}

func TestWriteStateCreatesMarkdownWhenMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "landed"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	if _, err := WriteState(dir); err != nil {
		t.Fatalf("WriteState() error = %v", err)
	}
	mdPath := filepath.Join(dir, "flywheel.md")
	b, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	content := string(b)
	if !strings.HasPrefix(content, statusStartMarker) {
		t.Errorf("flywheel.md does not start with the status block:\n%q", content)
	}
	if !strings.Contains(content, "| T1 | landed | 0 |") {
		t.Errorf("status block missing T1 row:\n%q", content)
	}
}

func TestDeriveInspectedStatusMapping(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Task: "T-pass", Kind: "inspected", Verdict: "pass"},
		{TS: "2026-09-13T00:00:00Z", Task: "T-rework", Kind: "inspected", Verdict: "rework"},
		{TS: "2026-09-13T00:00:00Z", Task: "T-scrap", Kind: "inspected", Verdict: "scrap"},
		{TS: "2026-09-13T00:00:00Z", Task: "T-escalate", Kind: "inspected", Verdict: "escalate"},
	}
	st := Derive(events)
	for id, want := range map[string]string{
		"T-pass":     "passed",
		"T-rework":   "needs-correction",
		"T-scrap":    "rejected",
		"T-escalate": "blocked",
	} {
		ts, ok := findTask(st, id)
		if !ok {
			t.Errorf("task %s missing from derived state", id)
			continue
		}
		if ts.Status != want {
			t.Errorf("%s status = %q, want %q", id, ts.Status, want)
		}
	}
}

func TestDeriveValidatedOwnsCheckedKeepStatus(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "finished"},
		{TS: "2026-09-13T00:00:01Z", Task: "T1", Kind: "validated", Gate: "1", Tree: "abc123"},
		{TS: "2026-09-13T00:00:02Z", Task: "T1", Kind: "owns_checked", Tree: "abc123"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "finished" {
		t.Errorf("T1 status = %q, want finished (validated/owns_checked must not change status)", ts.Status)
	}
}

func TestDeriveOrderingGaugeKinds(t *testing.T) {
	t.Parallel()
	// finished, validated, owns_checked and inspected share one second-precision
	// TS; appended in reverse order they must still derive passed.
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "inspected", Verdict: "pass"},
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "owns_checked", Tree: "abc123"},
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: "abc123"},
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "finished"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "passed" {
		t.Errorf("T1 status = %q, want passed (inspected must win over finished)", ts.Status)
	}
	if ts.Attempts != 0 {
		t.Errorf("T1 attempts = %d, want 0", ts.Attempts)
	}
}

func TestDeriveSkipsEmptyTaskEvents(t *testing.T) {
	t.Parallel()
	// A staffed event carries no task; Derive must not create an "" task row
	// and the staffed fields must not leak into any task's state.
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-13T00:00:01Z", Kind: "staffed", Session: "s1", Persona: "lead", Model: "m1"},
	}
	st := Derive(events)
	if len(st.Tasks) != 1 {
		t.Errorf("Derive() = %d tasks, want 1 (the empty-task event skipped)", len(st.Tasks))
	}
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Session != "" || ts.Model != "" {
		t.Errorf("T1 session/model = %q/%q, want empty (staffed fields leaked into a task)", ts.Session, ts.Model)
	}
}

// TestDeriveSkipsGroupTask: a group:<id> task's records (issue #420) list no
// status-less unit among Tasks; the group is in Groups with its members, its
// latest verdict and its open blocking integration findings, and a later
// round's second group_reviewed closes a finding it no longer re-reports.
func TestDeriveSkipsGroupTask(t *testing.T) {
	t.Parallel()
	rc := 0
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Task: "A", Kind: "planned"},
		{TS: "2026-09-13T00:00:00Z", Task: "B", Kind: "planned"},
		{TS: "2026-09-13T00:00:01Z", Task: "group:g1", Kind: "validated", Gate: "g1", RC: &rc},
		{TS: "2026-09-13T00:00:01Z", Task: "A", Kind: "review_finding", Category: IntegrationPersona, Severity: "blocker",
			Reason: "group:g1", Finding: "group:g1-r1-1", Path: "a.go", Title: "clash"},
		{TS: "2026-09-13T00:00:01Z", Task: "group:g1", Kind: "review_finding", Category: IntegrationPersona, Severity: "minor",
			Reason: "group:g1", Finding: "group:g1-r1-2", Path: "x.go", Title: "nit"},
		{TS: "2026-09-13T00:00:01Z", Task: "group:g1", Kind: "group_reviewed", Verdict: "correct",
			Note: "members A,B; missing -; conflicts -; gates g1=0; 2 finding(s)"},
	}
	st := Derive(events)
	if len(st.Tasks) != 2 {
		t.Fatalf("Derive() tasks = %+v, want A and B only", st.Tasks)
	}
	if _, ok := findTask(st, "group:g1"); ok {
		t.Error("group:g1 listed among Tasks")
	}
	if st.Counts[""] != 0 {
		t.Errorf("counts = %v, want no status-less entry", st.Counts)
	}
	if len(st.Groups) != 1 {
		t.Fatalf("Groups = %+v, want one", st.Groups)
	}
	g := st.Groups[0]
	if g.ID != "g1" || g.Task != "group:g1" || strings.Join(g.Members, ",") != "A,B" || g.Verdict != "correct" || g.Rounds != 1 || g.Open != 1 {
		t.Errorf("group = %+v, want g1 A,B correct 1 round 1 open (the minor never blocks)", g)
	}
	later := append(slices.Clone(events), Event{TS: "2026-09-13T00:00:02Z", Task: "group:g1", Kind: "group_reviewed",
		Verdict: "pass", Note: "members A,B; missing -; conflicts -; gates g1=0; 0 finding(s)"})
	if g := Derive(later).Groups[0]; g.Verdict != "pass" || g.Rounds != 2 || g.Open != 0 {
		t.Errorf("after a clean round group = %+v, want pass, 2 rounds, 0 open", g)
	}
	if st := Derive(events[:2]); st.Groups != nil {
		t.Errorf("no group records: Groups = %+v, want nil (omitted from state.json)", st.Groups)
	}
}

func TestDeriveKeepsWorkerSessionAfterInspected(t *testing.T) {
	t.Parallel()
	// inspected events carry the inspector's session; they must never
	// overwrite the task's worker session, so a later resume keeps the
	// worker's conversation.
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-13T00:00:01Z", Task: "T1", Kind: "started", Session: "w1"},
		{TS: "2026-09-13T00:00:02Z", Task: "T1", Kind: "finished", Session: "w1"},
		{TS: "2026-09-13T00:00:03Z", Task: "T1", Kind: "inspected", Verdict: "rework", Session: "i1"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Session != "w1" {
		t.Errorf("T1 session = %q, want w1 (inspector sessions must not overwrite the worker session)", ts.Session)
	}
}

func TestDeriveStaleLateFinished(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r2"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "finished", Attempt: "r1"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "dispatched" {
		t.Errorf("T1 status = %q, want dispatched (late finished r1 must not end the task)", ts.Status)
	}
	if ts.Attempt != "r2" {
		t.Errorf("T1 attempt = %q, want r2 (current attempt is the latest dispatched)", ts.Attempt)
	}
	if want := []string{"finished r1"}; !slices.Equal(ts.Stale, want) {
		t.Errorf("T1 stale = %q, want %q", ts.Stale, want)
	}
}

func TestDeriveStaleValidatedIgnored(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r2"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "abc123"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "dispatched" || ts.Attempt != "r2" {
		t.Errorf("T1 status/attempt = %q/%q, want dispatched/r2 (stale validated changes nothing)", ts.Status, ts.Attempt)
	}
	if want := []string{"validated r1"}; !slices.Equal(ts.Stale, want) {
		t.Errorf("T1 stale = %q, want %q", ts.Stale, want)
	}
	if ts.UpdatedAt != "2026-09-14T10:01:00Z" {
		t.Errorf("T1 updated = %q, want the latest non-stale TS", ts.UpdatedAt)
	}
}

func TestDeriveStaleLegacyNoAttempt(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "started"},
		{TS: "2026-09-14T10:03:00Z", Task: "T1", Kind: "finished"},
		{TS: "2026-09-14T10:04:00Z", Task: "T2", Kind: "planned"},
		{TS: "2026-09-14T10:05:00Z", Task: "T2", Kind: "finished", Attempt: "r1"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "finished" {
		t.Errorf("T1 status = %q, want finished (empty attempts apply as today)", ts.Status)
	}
	if ts.Attempt != "r1" || len(ts.Stale) != 0 {
		t.Errorf("T1 attempt/stale = %q/%q, want r1/[] (no stale events)", ts.Attempt, ts.Stale)
	}
	ts2, ok := findTask(st, "T2")
	if !ok {
		t.Fatal("T2 missing from derived state")
	}
	if ts2.Status != "finished" || ts2.Attempt != "r1" || len(ts2.Stale) != 0 {
		t.Errorf("T2 status/attempt/stale = %q/%q/%q, want finished/r1/[] (no dispatched means no current attempt)", ts2.Status, ts2.Attempt, ts2.Stale)
	}
}

func TestDeriveStaleLegacyEmptyDispatch(t *testing.T) {
	t.Parallel()
	// A legacy log dispatched by hand carries no attempt: the task gets
	// status dispatched and counts the attempt, but no current attempt is
	// set, so a later attempt-bearing finished applies as today.
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "finished", Attempt: "r1"},
	}
	events[2] = withRC(events[2], 0)
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "finished" {
		t.Errorf("T1 status = %q, want finished (empty dispatched attempt must not set a current attempt)", ts.Status)
	}
	if ts.Attempt != "r1" {
		t.Errorf("T1 attempt = %q, want r1 (carried from finished)", ts.Attempt)
	}
	if len(ts.Stale) != 0 {
		t.Errorf("T1 stale = %q, want [] (no stale entries)", ts.Stale)
	}
	if ts.Attempts != 1 {
		t.Errorf("T1 attempts = %d, want 1 (empty dispatched still counts an attempt)", ts.Attempts)
	}
}

func TestDeriveLostStatusAndStaleRule(t *testing.T) {
	t.Parallel()
	// A lost event for the task's current attempt sets status lost and is
	// counted; one for any other attempt is stale, changes nothing and is
	// recorded in Stale.
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "lost", Attempt: "r1", Reason: "lease-expired"},
	}
	st := Derive(events)
	ts, ok := findTask(st, "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "lost" {
		t.Errorf("T1 status = %q, want lost", ts.Status)
	}
	if ts.Attempt != "r1" || ts.Reason != "lease-expired" {
		t.Errorf("T1 attempt/reason = %q/%q, want r1/lease-expired", ts.Attempt, ts.Reason)
	}
	if st.Counts["lost"] != 1 {
		t.Errorf("lost count = %d, want 1", st.Counts["lost"])
	}

	stale := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T2", Kind: "planned"},
		{TS: "2026-09-14T10:01:00Z", Task: "T2", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:01:30Z", Task: "T2", Kind: "dispatched", Attempt: "r2"},
		{TS: "2026-09-14T10:02:00Z", Task: "T2", Kind: "lost", Attempt: "r1", Reason: "lease-expired"},
	}
	st2 := Derive(stale)
	ts2, ok := findTask(st2, "T2")
	if !ok {
		t.Fatal("T2 missing from derived state")
	}
	if ts2.Status != "dispatched" || ts2.Attempt != "r2" {
		t.Errorf("T2 status/attempt = %q/%q, want dispatched/r2 (stale lost changes nothing)",
			ts2.Status, ts2.Attempt)
	}
	if want := []string{"lost r1"}; !slices.Equal(ts2.Stale, want) {
		t.Errorf("T2 stale = %q, want %q", ts2.Stale, want)
	}
	if st2.Counts["lost"] != 0 {
		t.Errorf("lost count = %d, want 0 (stale lost not counted)", st2.Counts["lost"])
	}
}

func TestDeriveReplayDeterminism(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r2"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "started", Attempt: "r1"},
		{TS: "2026-09-14T10:03:00Z", Task: "T1", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-14T10:00:00Z", Task: "T2", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-14T10:00:00Z", Task: "T2", Kind: "validated", Attempt: "r2", Gate: "1", Tree: "abc123"},
		{TS: "2026-09-14T10:00:00Z", Task: "T2", Kind: "dispatched", Attempt: "r2"},
		{TS: "2026-09-14T10:04:00Z", Task: "T3", Kind: "planned"},
		{TS: "2026-09-14T10:05:00Z", Task: "T3", Kind: "finished"},
	}
	shuffled := make([]Event, len(events))
	for i := range events {
		shuffled[len(events)-1-i] = events[i]
	}
	a := stateJSON(Derive(events))
	if b := stateJSON(Derive(events)); a != b {
		t.Errorf("Derive twice on the same input differs:\n%s\nvs\n%s", a, b)
	}
	if b := stateJSON(Derive(shuffled)); a != b {
		t.Errorf("Derive(shuffled) differs from Derive(events):\n%s\nvs\n%s", a, b)
	}
}

// TestAgentReviewNeverPasses: a reviewed event the review agent wrote (persona
// reviewer with an adapter) never makes a unit passed; its correct still
// counts, and a hand-recorded pass still passes (issue #389).
func TestAgentReviewNeverPasses(t *testing.T) {
	t.Parallel()
	fin := Event{TS: "2026-09-24T00:00:00Z", Task: "T1", Kind: "finished"}
	agent := func(verdict string) Event {
		return Event{TS: "2026-09-24T00:00:01Z", Task: "T1", Kind: "reviewed", Verdict: verdict, Persona: "reviewer", Adapter: "claude"}
	}
	hand := Event{TS: "2026-09-24T00:00:01Z", Task: "T1", Kind: "reviewed", Verdict: "pass", Persona: "reviewer"}
	for name, c := range map[string]struct {
		events []Event
		want   string
	}{
		"agent pass":    {[]Event{fin, agent("pass")}, "finished"},
		"agent correct": {[]Event{fin, agent("correct")}, "needs-correction"},
		"hand pass":     {[]Event{fin, hand}, "passed"},
	} {
		ts, ok := findTask(Derive(c.events), "T1")
		if !ok {
			t.Fatalf("%s: T1 missing", name)
		}
		if ts.Status != c.want {
			t.Errorf("%s: status %q, want %q", name, ts.Status, c.want)
		}
	}
}

// TestDeriveReplanClearsAttemptFields checks a planned event on a task with
// attempts resets the floor row (issue #476): no session, attempt or reason
// survive from the old run, and the Attempts counter stays.
func TestDeriveReplanClearsAttemptFields(t *testing.T) {
	t.Parallel()
	rc := 0
	events := []Event{
		{TS: "2026-09-25T00:00:00Z", Task: "T1", Kind: "planned", Brief: "a.md"},
		{TS: "2026-09-25T00:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: "2026-09-25T00:00:02Z", Task: "T1", Kind: "started", Attempt: "r1", Session: "s1"},
		{TS: "2026-09-25T00:00:03Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "s1", Reason: "silent", RC: &rc},
		{TS: "2026-09-25T00:00:04Z", Task: "T1", Kind: "planned", Brief: "b.md"},
	}
	ts, ok := findTask(Derive(events), "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Status != "planned" || ts.Brief != "b.md" {
		t.Errorf("status, brief = %q, %q, want planned, b.md", ts.Status, ts.Brief)
	}
	if ts.Session != "" || ts.Attempt != "" || ts.Reason != "" || ts.RC != nil || ts.Model != "" || ts.Verdict != "" {
		t.Errorf("re-planned row keeps attempt fields: %+v", ts)
	}
	if ts.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", ts.Attempts)
	}
}

// TestDeriveAmendedKeepsAttempt checks amended corrects a live plan and
// clears nothing (issue #476).
func TestDeriveAmendedKeepsAttempt(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-25T00:00:00Z", Task: "T1", Kind: "planned", Brief: "a.md"},
		{TS: "2026-09-25T00:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-25T00:00:02Z", Task: "T1", Kind: "started", Attempt: "r1", Session: "s1"},
		{TS: "2026-09-25T00:00:03Z", Task: "T1", Kind: "amended", Brief: "b.md", Note: "fix"},
	}
	ts, ok := findTask(Derive(events), "T1")
	if !ok {
		t.Fatal("T1 missing from derived state")
	}
	if ts.Attempt != "r1" || ts.Session != "s1" || ts.Status != "running" {
		t.Errorf("amended row = attempt %q session %q status %q, want r1, s1, running", ts.Attempt, ts.Session, ts.Status)
	}
}

// TestReplanNextAttemptFollowsOldOnes checks the first dispatch after a
// re-plan numbers after the earlier attempts, never reusing r1 (issue #476).
func TestReplanNextAttemptFollowsOldOnes(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf strings.Builder
	res1, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() 1 error = %v", err)
	}
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ts, _ := findTask(Derive(events), "T1"); ts.Status != "planned" || ts.Attempt != "" {
		t.Fatalf("re-planned row = status %q attempt %q, want planned and no attempt", ts.Status, ts.Attempt)
	}
	res2, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() 2 error = %v", err)
	}
	if res1.Attempt != "r1" || res2.Attempt != "r2" {
		t.Errorf("attempts = %q, %q, want r1, r2", res1.Attempt, res2.Attempt)
	}
}

// TestWithdrawnDerive checks a withdrawn event (issue #479) derives status
// withdrawn after a plan, and a later planned event revives the unit.
func TestWithdrawnDerive(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-25T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.md"},
		{TS: "2026-09-25T00:01:00Z", Task: "T1", Kind: "withdrawn", Note: "another root owns T1"},
	}
	st := Derive(events)
	if len(st.Tasks) != 1 || st.Tasks[0].Status != "withdrawn" {
		t.Fatalf("planned -> withdrawn: tasks = %+v, want status withdrawn", st.Tasks)
	}
	if st.Counts["withdrawn"] != 1 {
		t.Errorf("withdrawn count = %d, want 1", st.Counts["withdrawn"])
	}
	events = append(events, Event{TS: "2026-09-25T00:02:00Z", Task: "T1", Kind: "planned", Brief: "b2.md"})
	st = Derive(events)
	if st.Tasks[0].Status != "planned" || st.Tasks[0].Brief != "b2.md" {
		t.Errorf("withdrawn -> planned: task = %+v, want planned with brief b2.md", st.Tasks[0])
	}
	// Same instant: a withdrawn sorts after the planned it takes back.
	same := []Event{
		{TS: "2026-09-25T00:00:00Z", Task: "T2", Kind: "withdrawn", Note: "dup"},
		{TS: "2026-09-25T00:00:00Z", Task: "T2", Kind: "planned", Brief: "b.md"},
	}
	if got := Derive(same).Tasks[0].Status; got != "withdrawn" {
		t.Errorf("same-instant planned, withdrawn: status = %q, want withdrawn", got)
	}
	if err := Validate(Event{Task: "T1", Kind: "withdrawn"}); err == nil || !strings.Contains(err.Error(), "note") {
		t.Errorf("Validate(withdrawn without note) = %v, want an error naming the note", err)
	}
	if err := Validate(Event{Kind: "withdrawn", Note: "why"}); err == nil {
		t.Error("Validate(withdrawn without task) = nil, want an error")
	}
}
