package flywheel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
}

// defaultRepoLockTimings are the timings Run's dispatch lock keeps: the
// values the lock always used before timings became per-lock.
func defaultRepoLockTimings() repoLockTimings {
	return repoLockTimings{
		staleAfter: 15 * time.Second,
		wait:       5 * time.Second,
		retry:      50 * time.Millisecond,
		heartbeat:  5 * time.Second,
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
	token := repoLockToken()
	deadline := now().Add(timings.wait)
	for {
		f, oerr := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if oerr == nil {
			host, _ := os.Hostname()
			_, _ = fmt.Fprintf(f, "%s\npid %d host %s locked %s\n", token, os.Getpid(), host, now().UTC().Format(time.RFC3339))
			_ = f.Close()
			return repoLockRelease(p, token, timings.heartbeat), nil
		}
		if !os.IsExist(oerr) {
			return nil, fmt.Errorf("create %s: %w", p, oerr)
		}
		if info, serr := os.Stat(p); serr == nil && now().Sub(info.ModTime()) > timings.staleAfter {
			stale := p + ".stale-" + token
			if rerr := os.Rename(p, stale); rerr == nil {
				_ = os.Remove(stale)
				continue
			} else if !os.IsNotExist(rerr) {
				return nil, fmt.Errorf("clear stale lock %s: %w", p, rerr)
			}
		}
		if now().After(deadline) {
			return nil, fmt.Errorf("lock %s is held by another command; if none is running, remove the file and retry", p)
		}
		time.Sleep(timings.retry)
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
			if ownRepoLock(p, token) {
				_ = os.Remove(p)
			}
		})
	}
}

// ownRepoLock reports whether the lock file at p still carries token on its
// first line — that is, whether the file is still this acquisition's and not
// a successor's. A missing or unreadable file is never ours.
func ownRepoLock(p, token string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return line == token
}

// repoLockToken returns a fresh random hex token for one lock acquisition.
// crypto/rand.Read never returns an error, so none is propagated; 16 bytes
// make a collision between two processes negligible.
func repoLockToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
