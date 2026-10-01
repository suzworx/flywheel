package flywheel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Repository lock (issue #242): Run holds .flywheel/dispatch.lock across "read
// the log -> run the collision checks -> append dispatched", and the feedback
// mutations hold .flywheel/feedback.lock across read, append, reread and
// render (issue #260). The lock is created with O_EXCL, which fails atomically
// when the file exists on every OS, so at most one holder owns it at a time.
// The holder writes a random token as the file's first line and its release
// removes the file only while that token still matches, so after a takeover
// the old holder can never delete the successor's lock (issue #249). While
// the lock is held a heartbeat refreshes the file's mtime every
// timings.heartbeat, so liveness is judged from the last touch, never from
// the acquisition time: a staleAfter of three heartbeat periods means a file
// untouched that long has missed three beats and its holder is genuinely
// dead, while a live holder — however long its critical section runs — is
// never stolen. The stale takeover renames the file aside before removing it,
// so of two racers exactly one wins the rename and the other retries the
// O_EXCL create. The timings are per acquisition, not package globals: each
// caller — Run's dispatch lock, the feedback lock, a test — passes its own,
// so nothing package-level is mutable while a heartbeat goroutine reads it.
type repoLockTimings struct {
	staleAfter time.Duration
	wait       time.Duration
	retry      time.Duration
	heartbeat  time.Duration
	// poll, when non-nil, is called once at the top of every acquire-loop
	// iteration, before the O_EXCL create. It is nil in production; tests use
	// it to change holders in lockstep with the waiter's polls, so no
	// scheduling delay can decide them (issue #603).
	poll func()
	// now and sleep, when non-nil, replace the package clock and time.Sleep
	// in the acquire loop (issue #697), so a test waits without sleeping.
	now   func() time.Time
	sleep func(time.Duration)
	// holder labels the acquiring command on the lock file's second line
	// ("cmd run o12", issue #651), so a waiter that times out can name who
	// holds the lock. Empty writes no label.
	holder string
}

// RepoLockBusy is the error of a lock wait that timed out (issue #651):
// ordinary contention, not a rule refusal. Holder is the label the holder
// wrote ("run o12 (pid 4242)"), or "" when the file carried none.
type RepoLockBusy struct {
	Path      string
	Holder    string
	Waited    time.Duration
	Handovers int
}

func (e *RepoLockBusy) Error() string {
	who := "another command"
	if e.Holder != "" {
		who = e.Holder
	}
	return fmt.Sprintf("lock %s is held by %s (waited %s, %d handovers); if none is running, remove the file and retry", e.Path, who, e.Waited, e.Handovers)
}

// repoLockHolderLabel reads the lock file's second line, "pid <n> host <h>
// locked <ts> cmd <label>", and returns "<label> (pid <n>)". An older line
// without cmd, or an unreadable file, yields "".
func repoLockHolderLabel(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	_, rest, _ := strings.Cut(string(b), "\n")
	line, _, _ := strings.Cut(rest, "\n")
	pre, label, ok := strings.Cut(strings.TrimRight(line, "\r"), " cmd ")
	label = strings.TrimSpace(label)
	if !ok || label == "" {
		return ""
	}
	if f := strings.Fields(pre); len(f) >= 2 && f[0] == "pid" {
		return label + " (pid " + f[1] + ")"
	}
	return label
}

// defaultRepoLockTimings are the timings Run's dispatch lock keeps: the
// values the lock always used before timings became per-lock.
func defaultRepoLockTimings() repoLockTimings {
	return repoLockTimings{
		staleAfter: 15 * time.Second,
		// A dispatch, amendment or landing holds the lock for a few
		// milliseconds, so a caller queues rather than fails: with eight
		// concurrent dispatchers a 5s wait at 50ms polling let one starve
		// and exit "held by another command" (the #48 scale test). A crashed
		// holder is still taken over after staleAfter.
		wait:      60 * time.Second,
		retry:     10 * time.Millisecond,
		heartbeat: 5 * time.Second,
	}
}

// feedbackLockTimings are the timings the feedback lock uses (issue #260).
// Its critical section is a short file mutation — no worker run — so a
// heartbeat and stale window an order of magnitude tighter than the dispatch
// lock's are safe: a crashed feedback command is taken over quickly, while a
// live one still refreshes the file three times per staleAfter.
func feedbackLockTimings() repoLockTimings {
	return repoLockTimings{
		staleAfter: 3 * time.Second,
		wait:       2 * time.Second,
		retry:      50 * time.Millisecond,
		heartbeat:  1 * time.Second,
	}
}

// acquireRepoLock takes the repository-scoped lock stored at
// .flywheel/<name> by creating it with O_EXCL, writing a fresh random token
// and the holder's pid and an RFC3339 timestamp into it so a human can see
// who holds it and a release can prove ownership. A short bounded retry
// serialises two nearly-simultaneous holders instead of failing one of them;
// on timeout the error names the file and tells the operator to remove it
// when nothing is running — that is ordinary contention, not a rule refusal.
// A lock file whose mtime is older than timings.staleAfter and that no
// heartbeat keeps fresh is renamed aside atomically (os.Rename; only one
// racer wins it, the loser gets ErrNotExist and retries the create) and then
// removed, so a crashed holder never wedges the repository and two racers can
// never both clear the same lock. The returned release stops the heartbeat
// and removes the file only while its token still matches; the caller must
// call it once the critical section is done. The timings belong to this
// acquisition alone, so a test shortens its own without touching anyone
// else's.
func acquireRepoLock(dir, name string, timings repoLockTimings) (release func(), err error) {
	p := filepath.Join(dir, ".flywheel", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	return acquireLockFile(p, timings)
}

// acquireLockFile is acquireRepoLock's machinery for the lock file at p,
// whose directory must exist: a resource lock (issue #697) lives outside
// .flywheel. timings.now and timings.sleep, when set, replace the clock and
// the retry sleep, so a test drives the wait without sleeping for real.
func acquireLockFile(p string, timings repoLockTimings) (release func(), err error) {
	now, sleep := now, time.Sleep
	if timings.now != nil {
		now = timings.now
	}
	if timings.sleep != nil {
		sleep = timings.sleep
	}
	token := repoLockToken()
	// The lock is not fair, so a waiter can lose every race while the lock
	// changes hands many times (issue #588). Starvation is not a stuck
	// holder: each time the holder token read from the file changes, the
	// deadline restarts, so only one holder keeping the lock for the whole
	// wait — or a file unreadable that long — makes this waiter give up. An
	// empty or unreadable first line neither resets nor replaces the holder.
	start := now()
	deadline := start.Add(timings.wait)
	holder, handovers := "", 0
	var seenMod time.Time
	for {
		if timings.poll != nil {
			timings.poll()
		}
		f, oerr := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if oerr == nil {
			host, _ := os.Hostname()
			cmd := ""
			if timings.holder != "" {
				cmd = " cmd " + timings.holder
			}
			_, _ = fmt.Fprintf(f, "%s\npid %d host %s locked %s%s\n", token, os.Getpid(), host, now().UTC().Format(time.RFC3339), cmd)
			_ = f.Close()
			return repoLockRelease(p, token, timings.heartbeat), nil
		}
		if !os.IsExist(oerr) && !lockBusyOnWindows(oerr) {
			return nil, fmt.Errorf("create %s: %w", p, oerr)
		}
		info, serr := os.Stat(p)
		// Read the holder only when the file's mtime moved (a new holder or a
		// heartbeat): on Windows a reader's open handle makes the holder's
		// remove and a stale rename fail, so reading every poll would slow
		// the very handovers this waiter is waiting for.
		if serr == nil && !info.ModTime().Equal(seenMod) {
			if seen := repoLockHolder(p); seen != "" {
				seenMod = info.ModTime()
				if seen != holder {
					if holder != "" {
						handovers++
					}
					holder = seen
					deadline = now().Add(timings.wait)
				}
			}
		}
		if serr == nil && now().Sub(info.ModTime()) > timings.staleAfter {
			stale := p + ".stale-" + token
			if rerr := os.Rename(p, stale); rerr == nil {
				_ = os.Remove(stale)
				continue
			} else if !os.IsNotExist(rerr) && !lockBusyOnWindows(rerr) {
				return nil, fmt.Errorf("clear stale lock %s: %w", p, rerr)
			}
		}
		if now().After(deadline) {
			waited := now().Sub(start)
			if waited >= time.Second {
				waited = waited.Round(time.Second)
			} else {
				waited = waited.Round(time.Millisecond)
			}
			return nil, &RepoLockBusy{Path: p, Holder: repoLockHolderLabel(p), Waited: waited, Handovers: handovers}
		}
		sleep(timings.retry)
	}
}

// repoLockRelease returns the release for one acquisition: it stops the
// heartbeat, waits for it to exit (so it can never touch the file after
// release returns) and removes the lock file only while its first line still
// carries this acquisition's token. A token mismatch means a successor owns
// the path — leave it, without error. A missing file is fine too. The
// returned function is idempotent. heartbeat is the acquisition's own
// interval.
func repoLockRelease(p, token string, heartbeat time.Duration) func() {
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		ticker := time.NewTicker(heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if ownRepoLock(p, token) {
					ts := now()
					_ = os.Chtimes(p, ts, ts)
				}
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-exited
			if !ownRepoLock(p, token) {
				return
			}
			// A waiter reading the holder token can make the remove fail
			// busy on Windows for a moment; retry briefly rather than leave
			// the lock to go stale.
			for i := 0; i < 1000; i++ {
				if err := os.Remove(p); err == nil || !lockBusyOnWindows(err) {
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

// ownRepoLock reports whether the lock file at p still carries token on its
// first line — that is, whether the file is still this acquisition's and not
// a successor's. A missing or unreadable file is never ours.
func ownRepoLock(p, token string) bool {
	line := repoLockHolder(p)
	return line != "" && line == token
}

// repoLockHolder returns the holder token on the first line of the lock file
// at p, read best-effort: a missing, unreadable or not-yet-written file
// yields "".
func repoLockHolder(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return line
}

// repoLockToken returns a fresh random hex token for one lock acquisition.
// crypto/rand.Read never returns an error, so none is propagated; 16 bytes
// make a collision between two processes negligible.
func repoLockToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// lockBusyOnWindows reports whether err is Windows' answer to touching a lock
// file another holder is deleting at that moment: an exclusive create or a
// rename racing a pending delete fails with "Access is denied" instead of
// "file exists". That is contention, not a real permission problem, so the
// caller retries until its deadline; on every other OS a permission error
// stays fatal. A sharing violation — a waiter reading the holder token while
// the file is renamed or removed — is the same contention.
func lockBusyOnWindows(err error) bool {
	return runtime.GOOS == "windows" && (errors.Is(err, fs.ErrPermission) || errors.Is(err, errWindowsSharingViolation))
}

// errWindowsSharingViolation is Windows' ERROR_SHARING_VIOLATION (32).
const errWindowsSharingViolation = syscall.Errno(32)
