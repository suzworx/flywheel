package flywheel

import (
	"errors"
	"fmt"
	"io/fs"
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

// progressLockTimings are short timings for the lock-progress tests (issue
// #588): a waiter gives up after 150ms of one unchanged holder.
func progressLockTimings() repoLockTimings {
	return repoLockTimings{staleAfter: 15 * time.Second, wait: 150 * time.Millisecond, retry: time.Millisecond, heartbeat: 40 * time.Millisecond}
}

// churnRepoLock hands the lock at p from holder to holder in place — a new
// token rewritten every few milliseconds, the file never absent — for d, so a
// waiter never gets a chance at it, then calls last with the file still held.
func churnRepoLock(p string, d time.Duration, last func()) {
	end := time.Now().Add(d)
	for i := 0; time.Now().Before(end); i++ {
		_ = os.WriteFile(p, []byte(fmt.Sprintf("churn-%d\npid 1 host test\n", i)), 0o644)
		time.Sleep(5 * time.Millisecond)
	}
	last()
}

// acquireWithGuard runs acquireRepoLock and fails the test if it hangs.
func acquireWithGuard(t *testing.T, dir string, timings repoLockTimings) (func(), error, time.Duration) {
	t.Helper()
	type result struct {
		release func()
		err     error
	}
	start := time.Now()
	done := make(chan result, 1)
	go func() {
		r, err := acquireRepoLock(dir, "dispatch.lock", timings)
		done <- result{r, err}
	}()
	select {
	case r := <-done:
		return r.release, r.err, time.Since(start)
	case <-time.After(20 * time.Second):
		t.Fatal("acquireRepoLock hung")
		return nil, nil, 0
	}
}

// seedRepoLock creates dir/.flywheel/dispatch.lock held by a first holder.
func seedRepoLock(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, ".flywheel", "dispatch.lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	if err := os.WriteFile(p, []byte("first\npid 1 host test\n"), 0o644); err != nil {
		t.Fatalf("write dispatch.lock: %v", err)
	}
	return p
}

// TestRepoLockProgressWaitsWhileLockChangesHands: other holders take the lock
// in turn for 600ms, four times the wait; the waiter keeps waiting and gets
// the lock once they stop. On a fixed deadline it gave up at 150ms.
func TestRepoLockProgressWaitsWhileLockChangesHands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := seedRepoLock(t, dir)
	go churnRepoLock(p, 600*time.Millisecond, func() {
		for err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist); err = os.Remove(p) {
			time.Sleep(time.Millisecond)
		}
	})
	release, err, waited := acquireWithGuard(t, dir, progressLockTimings())
	if err != nil {
		t.Fatalf("acquire while the lock changes hands error = %v, want it to wait and acquire", err)
	}
	defer release()
	if waited < 600*time.Millisecond {
		t.Errorf("acquired after %s, want after the churn ended (600ms)", waited)
	}
}

// TestRepoLockProgressSingleHolderFails: one live holder keeps the lock past
// the wait; the waiter fails naming the wait and the handovers.
func TestRepoLockProgressSingleHolderFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	timings := progressLockTimings()
	release, err := acquireRepoLock(dir, "dispatch.lock", timings)
	if err != nil {
		t.Fatalf("acquire holder error = %v", err)
	}
	defer release()
	_, err, _ = acquireWithGuard(t, dir, timings)
	if err == nil {
		t.Fatal("second acquire succeeded, want it refused while one holder keeps the lock")
	}
	for _, want := range []string{"held by another command (waited ", ", 0 handovers)", "remove the file and retry"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

// TestRepoLockProgressStaleTakeover: after the lock changes hands past the
// wait, the last holder dies (its mtime ages past staleAfter); the waiter,
// still waiting, takes the stale lock over and leaves no residue.
func TestRepoLockProgressStaleTakeover(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := seedRepoLock(t, dir)
	go churnRepoLock(p, 400*time.Millisecond, func() {
		past := time.Now().Add(-10 * time.Minute)
		for os.WriteFile(p, []byte("dead\npid 1 host test\n"), 0o644) != nil || os.Chtimes(p, past, past) != nil {
			time.Sleep(time.Millisecond)
		}
	})
	release, err, _ := acquireWithGuard(t, dir, progressLockTimings())
	if err != nil {
		t.Fatalf("acquire past a stale lock after handovers error = %v, want a takeover", err)
	}
	defer release()
	if h := repoLockHolder(p); h == "dead" || strings.HasPrefix(h, "churn-") {
		t.Errorf("lock holder = %q, want the waiter's own token", h)
	}
	if stale, _ := filepath.Glob(p + ".stale-*"); len(stale) != 0 {
		t.Errorf("stale lock residue = %v, want none", stale)
	}
}
