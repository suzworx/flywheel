package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ctlNow parses an injected clock instant for the lock and tick tests.
func ctlNow(t *testing.T, s string) time.Time {
	now, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return now
}

// lockFile returns the controller lock path in dir.
func lockFile(dir string) string {
	return filepath.Join(dir, ".flywheel", "controller.lock")
}

// TestAcquireLockFresh acquires into an empty factory: generation 1, our pid
// and an expiry one ttl ahead.
func TestAcquireLockFresh(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := ctlNow(t, "2026-09-14T10:00:00Z")
	l, err := AcquireLock(dir, now, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	if l.Generation != 1 {
		t.Errorf("generation = %d, want 1", l.Generation)
	}
	if l.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d (our own pid)", l.PID, os.Getpid())
	}
	if !LockLive(l, now) {
		t.Error("fresh lock not live at now")
	}
	exp, perr := time.Parse(time.RFC3339Nano, l.ExpiresAt)
	want := now.Add(30 * time.Second)
	if perr != nil || exp.Before(want) || exp.After(want) {
		t.Errorf("expires_at = %q, want now+30s", l.ExpiresAt)
	}
	if _, err := os.Stat(lockFile(dir)); err != nil {
		t.Errorf("lock file missing after acquire: %v", err)
	}
}

// TestAcquireLockRefusesLiveForeignLock writes a live lock held by another
// pid: AcquireLock refuses with a RuleRefusal naming the holder, its
// generation and its expiry, and leaves the file untouched.
func TestAcquireLockRefusesLiveForeignLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := ctlNow(t, "2026-09-14T10:12:00Z")
	body := `{"pid":999999,"host":"other","generation":3,"started_at":"2026-09-14T10:00:00Z","renewed_at":"2026-09-14T10:11:30Z","expires_at":"2026-09-14T10:30:00Z"}`
	if err := os.MkdirAll(filepath.Dir(lockFile(dir)), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	if err := os.WriteFile(lockFile(dir), []byte(body), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	_, err := AcquireLock(dir, now, 30*time.Second)
	if err == nil {
		t.Fatal("AcquireLock() accepted a live lock held by another pid")
	}
	if !IsRuleRefusal(err) {
		t.Errorf("AcquireLock() error = %v, want a RuleRefusal", err)
	}
	msg := err.Error()
	for _, want := range []string{"999999", "generation 3", "10:30:00Z"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal missing %q: %q", want, msg)
		}
	}
	b, rerr := os.ReadFile(lockFile(dir))
	if rerr != nil || string(b) != body {
		t.Errorf("refusal changed the lock file: %v %q", rerr, string(b))
	}
}

// TestAcquireLockTakeoverAfterExpiry writes an expired lock from another pid
// (generation 2): AcquireLock takes it over with generation 3, our pid, and
// the file is rewritten and read back confirmed.
func TestAcquireLockTakeoverAfterExpiry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := ctlNow(t, "2026-09-14T10:10:00Z")
	body := `{"pid":999999,"host":"other","generation":2,"started_at":"2026-09-14T09:00:00Z","renewed_at":"2026-09-14T09:59:00Z","expires_at":"2026-09-14T10:00:00Z"}`
	if err := os.MkdirAll(filepath.Dir(lockFile(dir)), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	if err := os.WriteFile(lockFile(dir), []byte(body), 0o644); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	l, err := AcquireLock(dir, now, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	if l.Generation != 3 {
		t.Errorf("generation = %d, want 3 (old generation + 1)", l.Generation)
	}
	if l.PID != os.Getpid() {
		t.Errorf("pid = %d, want %d", l.PID, os.Getpid())
	}
	b, rerr := os.ReadFile(lockFile(dir))
	if rerr != nil {
		t.Fatalf("read back lock: %v", rerr)
	}
	var got Lock
	if uerr := json.Unmarshal(b, &got); uerr != nil {
		t.Fatalf("parse lock: %v", uerr)
	}
	if got.PID != l.PID || got.Generation != l.Generation {
		t.Errorf("lock file holds pid %d generation %d, want %d/%d",
			got.PID, got.Generation, l.PID, l.Generation)
	}
}

// TestRenewLockMovesExpiryForward renews an owned lock and asserts the
// expiry moved forward one ttl from the new instant; a lock whose generation
// changed hands cannot be renewed.
func TestRenewLockMovesExpiryForward(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := ctlNow(t, "2026-09-14T10:00:00Z")
	l, err := AcquireLock(dir, now, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	l2, err := RenewLock(dir, l, now.Add(10*time.Second), 60*time.Second)
	if err != nil {
		t.Fatalf("RenewLock() error = %v", err)
	}
	exp, perr := time.Parse(time.RFC3339Nano, l2.ExpiresAt)
	want := now.Add(10 * time.Second).Add(60 * time.Second)
	if perr != nil || exp.Before(want) || exp.After(want) {
		t.Errorf("renewed expires_at = %q, want now+10s+60s", l2.ExpiresAt)
	}
	if l2.Generation != l.Generation || l2.PID != l.PID {
		t.Errorf("renewal changed identity: %+v", l2)
	}
	stale := l
	stale.Generation = l.Generation + 1
	if _, err := RenewLock(dir, stale, now, 60*time.Second); err == nil {
		t.Error("RenewLock() renewed a lock with a stale generation")
	}
}

// TestReleaseLockOnlyByHolder releases a lock with a foreign identity (must
// not remove the file) and with the holder's identity (must remove it); a
// second release of an already-gone lock is not an error.
func TestReleaseLockOnlyByHolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := ctlNow(t, "2026-09-14T10:00:00Z")
	l, err := AcquireLock(dir, now, 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	other := l
	other.PID = 999999
	if err := ReleaseLock(dir, other); err != nil {
		t.Fatalf("ReleaseLock() foreign error = %v", err)
	}
	if _, err := os.Stat(lockFile(dir)); err != nil {
		t.Error("ReleaseLock() removed a lock it did not hold")
	}
	if err := ReleaseLock(dir, l); err != nil {
		t.Fatalf("ReleaseLock() error = %v", err)
	}
	if _, err := os.Stat(lockFile(dir)); err == nil {
		t.Error("ReleaseLock() left the holder's lock file behind")
	}
	if err := ReleaseLock(dir, l); err != nil {
		t.Errorf("ReleaseLock() second release error = %v, want nil", err)
	}
}

// tickFixture appends the given events and writes one expired lease for
// m.r1, the shape of the gate's controller scenario.
func tickFixture(t *testing.T) string {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "m", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "m", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:01:30Z", Task: "m", Kind: "started"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	if err := WriteLease(dir, Lease{
		Task: "m", Attempt: "r1", PID: 1, Host: "h",
		StartedAt: "2026-09-14T10:01:00Z", RenewedAt: "2026-09-14T10:01:15Z",
		ExpiresAt: "2026-09-14T10:02:00Z", RunFile: ".flywheel/runs/m.r1.jsonl",
	}); err != nil {
		t.Fatalf("write lease: %v", err)
	}
	return dir
}

// TestTickAppendsLostOnce: one expired current lease appends exactly one
// lost event with the lease-expired reason and the lease evidence as its
// note; the derived status becomes lost and a second tick appends nothing.
func TestTickAppendsLostOnce(t *testing.T) {
	t.Parallel()
	dir := tickFixture(t)
	now := ctlNow(t, "2026-09-14T10:10:00Z")
	res, err := Tick(dir, now)
	if err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if res.Actions != 1 || res.Lost != 1 || res.Blocked != 0 || res.Proposed != 0 {
		t.Errorf("tick = %+v, want 1 action (1 lost)", res)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 4 {
		t.Fatalf("events = %d, want 4", len(evs))
	}
	last := evs[len(evs)-1]
	if last.Kind != "lost" || last.Task != "m" || last.Attempt != "r1" {
		t.Errorf("appended event = %+v, want lost m r1", last)
	}
	if last.Reason != "lease-expired" {
		t.Errorf("reason = %q, want lease-expired", last.Reason)
	}
	if last.Note != "lease expired at 2026-09-14T10:02:00Z" {
		t.Errorf("note = %q, want the lease evidence", last.Note)
	}
	ts, ok := findTask(Derive(evs), "m")
	if !ok {
		t.Fatal("m missing from derived state")
	}
	if ts.Status != "lost" {
		t.Errorf("m status = %q, want lost", ts.Status)
	}

	// A second tick with the same inputs appends nothing.
	res2, err := Tick(dir, ctlNow(t, "2026-09-14T10:11:00Z"))
	if err != nil {
		t.Fatalf("second Tick() error = %v", err)
	}
	if res2.Actions != 0 || res2.Lost != 0 {
		t.Errorf("second tick = %+v, want 0 actions (already lost)", res2)
	}
	evs2, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("re-read events: %v", err)
	}
	if len(evs2) != len(evs) {
		t.Errorf("events grew from %d to %d on the second tick", len(evs), len(evs2))
	}
}

// TestTickAppendsBlockedForRejectedNeed: a planned task whose needs target
// was scrapped gets one blocked event naming the target; a second tick
// appends nothing.
func TestTickAppendsBlockedForRejectedNeed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "a", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "a", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:02:00Z", Task: "a", Kind: "finished", Attempt: "r1"},
		{TS: "2026-09-14T10:03:00Z", Task: "a", Kind: "inspected", Verdict: "scrap"},
		{TS: "2026-09-14T10:04:00Z", Task: "x", Kind: "planned", Brief: "b.txt", Needs: []string{"a"}},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	res, err := Tick(dir, ctlNow(t, "2026-09-14T10:10:00Z"))
	if err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if res.Actions != 1 || res.Blocked != 1 || res.Lost != 0 {
		t.Errorf("tick = %+v, want 1 action (1 blocked)", res)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	last := evs[len(evs)-1]
	if last.Kind != "blocked" || last.Task != "x" || last.Reason != "needs a" {
		t.Errorf("appended event = %+v, want blocked x needs a", last)
	}

	res2, err := Tick(dir, ctlNow(t, "2026-09-14T10:11:00Z"))
	if err != nil {
		t.Fatalf("second Tick() error = %v", err)
	}
	if res2.Actions != 0 {
		t.Errorf("second tick = %+v, want 0 actions (already blocked)", res2)
	}
	evs2, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("re-read events: %v", err)
	}
	if len(evs2) != len(evs) {
		t.Errorf("events grew from %d to %d on the second tick", len(evs), len(evs2))
	}
}

// TestTickNoLostForLiveLease: a live lease keeps the task dispatched; the
// tick proposes nothing and appends nothing.
func TestTickNoLostForLiveLease(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "m", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "m", Kind: "dispatched", Attempt: "r1"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	if err := WriteLease(dir, Lease{
		Task: "m", Attempt: "r1", PID: 1, Host: "h",
		StartedAt: "2026-09-14T10:01:00Z", RenewedAt: "2026-09-14T10:09:00Z",
		ExpiresAt: "2026-09-14T10:30:00Z", RunFile: ".flywheel/runs/m.r1.jsonl",
	}); err != nil {
		t.Fatalf("write lease: %v", err)
	}
	res, err := Tick(dir, ctlNow(t, "2026-09-14T10:10:00Z"))
	if err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if res.Actions != 0 || res.Lost != 0 {
		t.Errorf("tick = %+v, want 0 lost for a live lease", res)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("events = %d, want 2 (nothing appended)", len(evs))
	}
}

// TestTickNoLostForFinishedTask: an expired lease on a finished task is not
// lost; the tick appends nothing.
func TestTickNoLostForFinishedTask(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "m", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "m", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:02:00Z", Task: "m", Kind: "finished", Attempt: "r1"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	if err := WriteLease(dir, Lease{
		Task: "m", Attempt: "r1", PID: 1, Host: "h",
		StartedAt: "2026-09-14T10:01:00Z", RenewedAt: "2026-09-14T10:01:30Z",
		ExpiresAt: "2026-09-14T10:02:00Z", RunFile: ".flywheel/runs/m.r1.jsonl",
	}); err != nil {
		t.Fatalf("write lease: %v", err)
	}
	res, err := Tick(dir, ctlNow(t, "2026-09-14T10:10:00Z"))
	if err != nil {
		t.Fatalf("Tick() error = %v", err)
	}
	if res.Actions != 0 || res.Lost != 0 {
		t.Errorf("tick = %+v, want 0 lost for a finished task", res)
	}
}
