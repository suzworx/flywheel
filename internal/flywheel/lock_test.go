package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDispatchLockLiveHolderNotStolen is the regression guard for the stolen
// live lock (issue #249): a holder whose lock mtime is forced far into the
// past — simulating a critical section far longer than the stale window —
// keeps the lock because its heartbeat refreshes the mtime, so a second
// acquire waits out its deadline and fails rather than taking over. The
// heartbeat interval and the acquire wait are shortened so the guard stays
// fast; the relationship they test (heartbeat < staleAfter) is unchanged.
// The timings are this test's own, passed per acquisition: no package global
// is reassigned (issue #260).
func TestDispatchLockLiveHolderNotStolen(t *testing.T) {
	t.Parallel()
	timings := repoLockTimings{
		staleAfter: 15 * time.Second,
		wait:       400 * time.Millisecond,
		retry:      10 * time.Millisecond,
		heartbeat:  40 * time.Millisecond,
	}

	dir := t.TempDir()
	release, err := acquireRepoLock(dir, "dispatch.lock", timings)
	if err != nil {
		t.Fatalf("acquire dispatch.lock error = %v", err)
	}
	defer release()
	p := filepath.Join(dir, ".flywheel", "dispatch.lock")
	past := now().Add(-10 * time.Minute)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	// Wait until the heartbeat has refreshed the mtime: a live holder.
	deadline := now().Add(5 * time.Second)
	for {
		info, serr := os.Stat(p)
		if serr == nil && info.ModTime().After(now().Add(-3*timings.heartbeat)) {
			break
		}
		if now().After(deadline) {
			t.Fatal("heartbeat never refreshed the lock mtime; a live holder would read as stale")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := acquireRepoLock(dir, "dispatch.lock", timings); err == nil {
		t.Fatal("second acquire succeeded, want it refused: a live holder must not be stolen")
	}
}

// TestDispatchLockDeadHolderTakenOver checks the stale path (issue #249): a
// lock file whose mtime is far past the stale window and that no heartbeat
// keeps fresh — a crashed holder — is renamed aside atomically and the
// acquire succeeds, leaving no .stale- residue behind. The dead file is aged
// past the default dispatch timings, so this acquisition runs on the same
// defaults Run uses.
func TestDispatchLockDeadHolderTakenOver(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, ".flywheel", "dispatch.lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	if err := os.WriteFile(p, []byte("deadbeef\npid 999 host dead locked 2026-09-01T00:00:00Z\n"), 0o644); err != nil {
		t.Fatalf("write dispatch.lock: %v", err)
	}
	past := now().Add(-10 * time.Minute)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	release, err := acquireDispatchLock(dir)
	if err != nil {
		t.Fatalf("acquireDispatchLock() past a dead lock error = %v, want a takeover", err)
	}
	defer release()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read dispatch.lock: %v", err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	if line == "deadbeef" {
		t.Error("dispatch.lock still carries the dead holder's token, want the new holder's")
	}
	stale, gerr := filepath.Glob(filepath.Join(dir, ".flywheel", "dispatch.lock.stale-*"))
	if gerr != nil {
		t.Fatalf("Glob() error = %v", gerr)
	}
	if len(stale) != 0 {
		t.Errorf("stale lock residue = %v, want none (the rename target is removed)", stale)
	}
}

// TestDispatchLockReleaseDoesNotDeleteSuccessor checks the ABA fix (issue
// #249): after B takes the lock over from A, A's release reads the file,
// sees B's token and leaves B's lock in place — A must never delete a lock
// it no longer owns. A's heartbeat is parked so the aged mtime cannot be
// refreshed between the Chtimes and B's takeover. Each holder carries its
// own timings: A parks its heartbeat, B runs the defaults.
func TestDispatchLockReleaseDoesNotDeleteSuccessor(t *testing.T) {
	t.Parallel()
	aTimings := repoLockTimings{
		staleAfter: 15 * time.Second,
		wait:       5 * time.Second,
		retry:      50 * time.Millisecond,
		heartbeat:  time.Hour,
	}

	dir := t.TempDir()
	releaseA, err := acquireRepoLock(dir, "dispatch.lock", aTimings)
	if err != nil {
		t.Fatalf("acquire A error = %v", err)
	}
	p := filepath.Join(dir, ".flywheel", "dispatch.lock")
	past := now().Add(-10 * time.Minute)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	releaseB, err := acquireDispatchLock(dir)
	if err != nil {
		t.Fatalf("acquire B (takeover) error = %v", err)
	}
	defer releaseB()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read B's lock: %v", err)
	}
	tokenB, _, _ := strings.Cut(string(b), "\n")

	releaseA()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("A's release removed B's lock file: %v", err)
	}
	b, err = os.ReadFile(p)
	if err != nil {
		t.Fatalf("read B's lock after A's release: %v", err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	if line != tokenB {
		t.Errorf("lock first line = %q after A's release, want B's token %q", line, tokenB)
	}
}
