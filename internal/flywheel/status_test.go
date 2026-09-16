package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func statusNow(t *testing.T) time.Time {
	now, err := time.Parse(time.RFC3339Nano, "2026-09-14T01:00:00Z")
	if err != nil {
		t.Fatalf("parse test now: %v", err)
	}
	return now
}

// statusFixture builds an event log with one task per derived status, one
// stale attempt, one andon unit, and a staffed event as the latest of all.
func statusFixture(t *testing.T) string {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t-planned", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-run", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-run", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-run", Kind: "started"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-stale", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-stale", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:01:00Z", Task: "t-stale", Kind: "dispatched", Attempt: "r2"},
		{TS: "2026-09-14T00:02:00Z", Task: "t-stale", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-silent", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-silent", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-pass", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-pass", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-pass", Kind: "started"},
		{TS: "2026-09-14T00:03:00Z", Task: "t-pass", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-14T00:05:00Z", Task: "t-pass", Kind: "inspected", Attempt: "r1", Verdict: "pass"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-land", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-land", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-land", Kind: "started"},
		{TS: "2026-09-14T00:04:00Z", Task: "t-land", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-14T00:06:00Z", Task: "t-land", Kind: "landed", Commit: "abc"},
		{TS: "2026-09-14T00:06:30Z", Kind: "goal", Goal: &GoalSpec{ID: "g1", Title: "Ship status", Required: []string{"t-pass", "t-planned"}, Status: "active"}},
		{TS: "2026-09-14T00:06:31Z", Kind: "goal", Goal: &GoalSpec{ID: "g2", Title: "Ship goals", Status: "met"}},
		{TS: "2026-09-14T00:07:00Z", Kind: "staffed", Persona: "lead", Session: "s1"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	// A live run file each for t-run and t-stale keeps their units off the
	// andon; t-silent has no run file, so its unit is silent.
	runLine := `{"type":"step_finish","sessionID":"s1","part":{"type":"step_finish","reason":"stop"}}` + "\n"
	for _, name := range []string{"t-run.r1.jsonl", "t-stale.r2.jsonl"} {
		run := filepath.Join(dir, ".flywheel", "runs", name)
		if err := os.MkdirAll(filepath.Dir(run), 0o755); err != nil {
			t.Fatalf("mkdir runs: %v", err)
		}
		if err := os.WriteFile(run, []byte(runLine), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestStatusFixture(t *testing.T) {
	dir := statusFixture(t)
	now := statusNow(t)
	rep, err := Status(dir, now)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if rep.Factory != filepath.Base(dir) {
		t.Errorf("factory = %q, want %q", rep.Factory, filepath.Base(dir))
	}
	if rep.Tasks.Total != 6 {
		t.Errorf("tasks total = %d, want 6", rep.Tasks.Total)
	}
	if rep.Tasks.Planned != 1 || rep.Tasks.Dispatched != 2 || rep.Tasks.Running != 1 {
		t.Errorf("planned/dispatched/running = %d/%d/%d, want 1/2/1", rep.Tasks.Planned, rep.Tasks.Dispatched, rep.Tasks.Running)
	}
	if rep.Tasks.Finished != 0 || rep.Tasks.Passed != 1 || rep.Tasks.NeedsCorrection != 0 {
		t.Errorf("finished/passed/needs-correction = %d/%d/%d, want 0/1/0", rep.Tasks.Finished, rep.Tasks.Passed, rep.Tasks.NeedsCorrection)
	}
	if rep.Tasks.Rejected != 0 || rep.Tasks.Blocked != 0 || rep.Tasks.Landed != 1 {
		t.Errorf("rejected/blocked/landed = %d/%d/%d, want 0/0/1", rep.Tasks.Rejected, rep.Tasks.Blocked, rep.Tasks.Landed)
	}
	if rep.Attempts.Live != 3 {
		t.Errorf("attempts live = %d, want 3", rep.Attempts.Live)
	}
	if rep.Attempts.Lost != 0 || len(rep.Attempts.LostList) != 0 {
		t.Errorf("attempts lost = %d, want 0", rep.Attempts.Lost)
	}
	if rep.Leases.Live != 0 || rep.Leases.Expired != 0 || rep.Leases.Skipped != 0 {
		t.Errorf("leases = %+v, want none", rep.Leases)
	}
	if rep.Attempts.Stale != 1 {
		t.Errorf("attempts stale = %d, want 1", rep.Attempts.Stale)
	}
	if rep.LastEventAt == nil || rep.LastEventAt.TS != "2026-09-14T00:07:00Z" || rep.LastEventAt.Age != 3180 {
		t.Errorf("last_event_at = %+v, want 00:07:00Z age 3180", rep.LastEventAt)
	}
	if rep.LastProgressAt == nil || rep.LastProgressAt.TS != "2026-09-14T00:06:00Z" || rep.LastProgressAt.Age != 3240 {
		t.Errorf("last_progress_at = %+v, want 00:06:00Z age 3240", rep.LastProgressAt)
	}
	if rep.Andon != 1 {
		t.Errorf("andon = %d, want 1 (only the silent unit)", rep.Andon)
	}
	if rep.Goals.Active != 1 || rep.Goals.Met != 1 || rep.Goals.Failed != 0 || rep.Goals.Abandoned != 0 {
		t.Errorf("goals = active %d, met %d, failed %d, abandoned %d; want 1/1/0/0",
			rep.Goals.Active, rep.Goals.Met, rep.Goals.Failed, rep.Goals.Abandoned)
	}
	if len(rep.Goals.List) != 2 {
		t.Fatalf("goals list = %d entries, want 2", len(rep.Goals.List))
	}
	g1, g2 := rep.Goals.List[0], rep.Goals.List[1]
	if g1.ID != "g1" || g1.Status != "active" || g1.Title != "Ship status" {
		t.Errorf("goals[0] = %+v, want g1 active Ship status", g1)
	}
	if g1.Progress != "1/2 required tasks accepted" {
		t.Errorf("g1 progress = %q, want 1/2 required tasks accepted", g1.Progress)
	}
	if g2.ID != "g2" || g2.Status != "met" || g2.Title != "Ship goals" {
		t.Errorf("goals[1] = %+v, want g2 met Ship goals", g2)
	}
}

// TestStatusAttentionListsCappedNotClean checks attention lists a task whose
// current attempt ended capped (reason length) and omits a task that finished
// cleanly (reason stop) (issue #131).
func TestStatusAttentionListsCappedNotClean(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t-capped", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-capped", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:01Z", Task: "t-capped", Kind: "finished", Attempt: "r1", Reason: "length"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-clean", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-clean", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:01Z", Task: "t-clean", Kind: "finished", Attempt: "r1", Reason: "stop"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	rep, err := Status(dir, statusNow(t))
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if len(rep.Attention) != 1 {
		t.Fatalf("attention = %v, want exactly 1 entry", rep.Attention)
	}
	a := rep.Attention[0]
	if a.Task != "t-capped" || a.Attempt != "r1" || a.Reason != "length" {
		t.Errorf("attention[0] = %+v, want t-capped r1 length", a)
	}
}

// TestStatusCountsLost: a lost event derives status lost and flywheel status
// counts it in its task counts.
func TestStatusCountsLost(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t-lost", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:01:00Z", Task: "t-lost", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:02:00Z", Task: "t-lost", Kind: "lost", Attempt: "r1", Reason: "lease-expired"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	rep, err := Status(dir, statusNow(t))
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if rep.Tasks.Total != 1 || rep.Tasks.Lost != 1 {
		t.Errorf("tasks total/lost = %d/%d, want 1/1", rep.Tasks.Total, rep.Tasks.Lost)
	}
}

func TestStatusJSONRoundTrip(t *testing.T) {
	dir := statusFixture(t)
	rep, err := Status(dir, statusNow(t))
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got StatusReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Factory != rep.Factory || !reflect.DeepEqual(got.Tasks, rep.Tasks) ||
		!reflect.DeepEqual(got.Attempts, rep.Attempts) || got.Andon != rep.Andon {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, rep)
	}
	if !reflect.DeepEqual(got.Goals, rep.Goals) {
		t.Errorf("goals round-trip = %+v, want %+v", got.Goals, rep.Goals)
	}
	if got.LastEventAt == nil || *got.LastEventAt != *rep.LastEventAt {
		t.Errorf("last_event_at round-trip = %+v, want %+v", got.LastEventAt, rep.LastEventAt)
	}
	if got.LastProgressAt == nil || *got.LastProgressAt != *rep.LastProgressAt {
		t.Errorf("last_progress_at round-trip = %+v, want %+v", got.LastProgressAt, rep.LastProgressAt)
	}
}

func TestStatusEmptyFactory(t *testing.T) {
	rep, err := Status(t.TempDir(), statusNow(t))
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if rep.Tasks.Total != 0 {
		t.Errorf("tasks total = %d, want 0", rep.Tasks.Total)
	}
	if rep.LastEventAt != nil {
		t.Errorf("last_event_at = %+v, want none", rep.LastEventAt)
	}
	if rep.LastProgressAt != nil {
		t.Errorf("last_progress_at = %+v, want none", rep.LastProgressAt)
	}
	if rep.Attempts.Live != 0 || rep.Attempts.Stale != 0 {
		t.Errorf("attempts = %+v, want 0/0", rep.Attempts)
	}
	if rep.Leases.Live != 0 || rep.Leases.Expired != 0 || rep.Leases.Skipped != 0 {
		t.Errorf("leases = %+v, want none", rep.Leases)
	}
	if rep.Andon != 0 {
		t.Errorf("andon = %d, want 0", rep.Andon)
	}
	if !reflect.DeepEqual(rep.Goals, StatusGoals{}) {
		t.Errorf("goals = %+v, want none (prints Goals: none)", rep.Goals)
	}
}

// TestStatusAgeUnits pins the text and JSON ages of the last-event lines: the
// text output renders an 845-second age as "14m ago" (HumanAge), while --json
// keeps age in whole seconds.
func TestStatusAgeUnits(t *testing.T) {
	rep := StatusReport{
		LastEventAt:    &LastEvent{TS: "2026-09-14T10:00:00Z", Age: 845},
		LastProgressAt: &LastEvent{TS: "2026-09-14T10:01:00Z", Age: 845},
	}
	for name, l := range map[string]*LastEvent{"last event": rep.LastEventAt, "last progress": rep.LastProgressAt} {
		if got := HumanAge(l.Age); got != "14m" {
			t.Errorf("%s text age = %q, want 14m", name, got)
		}
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got StatusReport
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LastEventAt.Age != 845 || got.LastProgressAt.Age != 845 {
		t.Errorf("json ages = %d/%d, want 845/845 (whole seconds)", got.LastEventAt.Age, got.LastProgressAt.Age)
	}
}

// writeLease writes one lease file for the leaseStatusFixture.
func writeLease(t *testing.T, dir, task, attempt, expiresAt string) {
	t.Helper()
	if err := WriteLease(dir, Lease{
		Task: task, Attempt: attempt, PID: 1, Host: "h",
		StartedAt: "2026-09-14T00:00:00Z", RenewedAt: "2026-09-14T00:05:00Z",
		ExpiresAt: expiresAt, RunFile: ".flywheel/runs/" + task + "." + attempt + ".jsonl",
	}); err != nil {
		t.Fatalf("write lease %s.%s: %v", task, attempt, err)
	}
}

// leaseStatusFixture builds an event log plus lease files: t-live is running
// with a live lease, t-lost and t-other are dispatched with expired leases
// (both lost), t-done finished with an expired lease (expired, not lost),
// t-nolease is dispatched with no lease file, and one lease file is malformed.
func leaseStatusFixture(t *testing.T) string {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T00:00:00Z", Task: "t-live", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-live", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-live", Kind: "started"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-lost", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-lost", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-other", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-other", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-done", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-done", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-done", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-nolease", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T00:00:00Z", Task: "t-nolease", Kind: "dispatched", Attempt: "r1"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	writeLease(t, dir, "t-live", "r1", "2026-09-14T02:00:00Z")
	writeLease(t, dir, "t-lost", "r1", "2026-09-14T00:10:00Z")
	writeLease(t, dir, "t-other", "r1", "2026-09-14T00:20:00Z")
	writeLease(t, dir, "t-done", "r1", "2026-09-14T00:15:00Z")
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "leases", "t-bad.r1.json"), []byte("not a lease"), 0o644); err != nil {
		t.Fatalf("write malformed lease: %v", err)
	}
	return dir
}

// TestStatusLeases covers the lease report: one live lease; expired leases on
// a dispatched current attempt (lost, with evidence), on a finished task
// (expired, not lost) and none for a task with no lease file (neither); and a
// malformed lease file (skipped and reported). All at the injected now.
func TestStatusLeases(t *testing.T) {
	dir := leaseStatusFixture(t)
	now := statusNow(t)
	rep, err := Status(dir, now)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if rep.Leases.Live != 1 || rep.Leases.Expired != 3 || rep.Leases.Skipped != 1 {
		t.Errorf("leases = live %d, expired %d, skipped %d; want 1/3/1",
			rep.Leases.Live, rep.Leases.Expired, rep.Leases.Skipped)
	}
	if rep.Attempts.Live != 4 || rep.Attempts.Lost != 2 || rep.Attempts.Stale != 0 {
		t.Errorf("attempts = live %d, lost %d, stale %d; want 4/2/0",
			rep.Attempts.Live, rep.Attempts.Lost, rep.Attempts.Stale)
	}
	if len(rep.Attempts.LostList) != 2 {
		t.Fatalf("lost list = %d entries, want 2", len(rep.Attempts.LostList))
	}
	first, second := rep.Attempts.LostList[0], rep.Attempts.LostList[1]
	if first.Task != "t-lost" || first.Attempt != "r1" ||
		first.ExpiresAt != "2026-09-14T00:10:00Z" || first.RunFile != ".flywheel/runs/t-lost.r1.jsonl" {
		t.Errorf("lost[0] = %+v, want t-lost r1 with the expired-at evidence", first)
	}
	if second.Task != "t-other" || second.Attempt != "r1" {
		t.Errorf("lost[1] = %+v, want t-other r1 (sorted by task)", second)
	}
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got StatusReport
	if uerr := json.Unmarshal(b, &got); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if !reflect.DeepEqual(got.Attempts, rep.Attempts) || !reflect.DeepEqual(got.Leases, rep.Leases) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, rep)
	}
}
