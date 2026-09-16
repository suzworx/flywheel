package flywheel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Claim records that one session is driving a task: which lead wrote the
// brief and dispatched the run, so a second lead sharing the same tree and
// event log knows the task is already spoken for. Claim is deliberately a
// sibling of Lease, not an extension of it: the same atomic write-and-rename
// discipline and the same expiry style, in a separate directory with its own
// type. Claims are advisory here — nothing refuses to run because of one.
type Claim struct {
	Task      string `json:"task"`
	Session   string `json:"session"`
	ClaimedAt string `json:"claimed_at"`
	TTL       string `json:"ttl"`
	ExpiresAt string `json:"expires_at"`
	Note      string `json:"note,omitempty"`
}

// ErrClaimHeld reports a live claim held by a different session; the CLI
// exits 6 on it unless --force overrides the takeover.
type ErrClaimHeld struct {
	Task      string
	Session   string
	ExpiresAt string
}

func (e *ErrClaimHeld) Error() string {
	return fmt.Sprintf("%s is held by %s until %s", e.Task, e.Session, e.ExpiresAt)
}

// claimPath returns the claim file path for one task.
func claimPath(dir, task string) string {
	return filepath.Join(dir, ".flywheel", "claims", task+".json")
}

// WriteClaim writes c to .flywheel/claims/<task>.json via a temp file plus
// rename, creating the directory.
func WriteClaim(dir string, c Claim) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode claim: %w", err)
	}
	b = append(b, '\n')
	return atomicWrite(filepath.Join(dir, ".flywheel", "claims"), c.Task+".json", "claim-*.json", b)
}

// RemoveClaim deletes the claim file for task; a missing file is not an
// error.
func RemoveClaim(dir, task string) error {
	path := claimPath(dir, task)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove claim %s: %w", path, err)
	}
	return nil
}

// ReadClaim reads one task's claim file. ok is false when no claim file
// exists; a malformed file is reported as an error.
func ReadClaim(dir, task string) (Claim, bool, error) {
	b, err := os.ReadFile(claimPath(dir, task))
	if err != nil {
		if os.IsNotExist(err) {
			return Claim{}, false, nil
		}
		return Claim{}, false, fmt.Errorf("read claim %s: %w", task, err)
	}
	var c Claim
	if err := json.Unmarshal(b, &c); err != nil {
		return Claim{}, false, fmt.Errorf("parse claim %s: %w", task, err)
	}
	return c, true, nil
}

// ReadClaims returns every claim in .flywheel/claims, sorted by task. A
// malformed file is skipped and named in the returned error; the other
// claims are still returned, so a caller such as `flywheel claims` can list
// what parsed and treat the rest as skipped rather than fatal.
func ReadClaims(dir string) ([]Claim, error) {
	claimsDir := filepath.Join(dir, ".flywheel", "claims")
	entries, err := os.ReadDir(claimsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Claim{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", claimsDir, err)
	}
	var claims []Claim
	var problems []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(claimsDir, e.Name())
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			problems = append(problems, fmt.Errorf("read %s: %w", path, rerr))
			continue
		}
		var c Claim
		if uerr := json.Unmarshal(b, &c); uerr != nil {
			problems = append(problems, fmt.Errorf("parse %s: %w", path, uerr))
			continue
		}
		claims = append(claims, c)
	}
	sort.Slice(claims, func(i, j int) bool { return claims[i].Task < claims[j].Task })
	if len(problems) > 0 {
		return claims, errors.Join(problems...)
	}
	return claims, nil
}

// ClaimLive reports whether the claim is still live at now: live while now
// is at or before expires_at. An unparseable expiry is not live.
func ClaimLive(c Claim, now time.Time) bool {
	exp, err := time.Parse(time.RFC3339, c.ExpiresAt)
	if err != nil {
		return false
	}
	return !now.After(exp)
}

// ClaimTask claims task for session. With no existing claim, or one that has
// expired (held by anyone), the claim is taken outright. A live claim held
// by the same session is renewed with a fresh expiry. A live claim held by a
// different session is refused with *ErrClaimHeld unless force is set, which
// takes it over; tookOver then names the session it was taken from (""
// otherwise). now is the caller's clock; ttl is the caller's already-parsed
// duration.
func ClaimTask(dir, task, session, note string, ttl time.Duration, force bool, now time.Time) (c Claim, tookOver string, err error) {
	if task == "" {
		return Claim{}, "", errors.New("a task id is required")
	}
	if session == "" {
		return Claim{}, "", errors.New("a --session is required")
	}
	existing, ok, err := ReadClaim(dir, task)
	if err != nil {
		return Claim{}, "", err
	}
	if ok && existing.Session != session && ClaimLive(existing, now) {
		if !force {
			return Claim{}, "", &ErrClaimHeld{Task: task, Session: existing.Session, ExpiresAt: existing.ExpiresAt}
		}
		tookOver = existing.Session
	}
	c = Claim{
		Task:      task,
		Session:   session,
		ClaimedAt: now.Format(time.RFC3339),
		TTL:       ttl.String(),
		ExpiresAt: now.Add(ttl).Format(time.RFC3339),
		Note:      note,
	}
	if err := WriteClaim(dir, c); err != nil {
		return Claim{}, "", err
	}
	return c, tookOver, nil
}

// ReleaseTask removes task's claim when session holds it, the claim has
// expired, or force is set. found reports whether a claim existed at all: a
// task with no claim is a no-op (found=false, err=nil). A live claim held by
// a different session without force is refused with *ErrClaimHeld.
func ReleaseTask(dir, task, session string, force bool, now time.Time) (found bool, err error) {
	existing, ok, err := ReadClaim(dir, task)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if existing.Session != session && ClaimLive(existing, now) && !force {
		return true, &ErrClaimHeld{Task: task, Session: existing.Session, ExpiresAt: existing.ExpiresAt}
	}
	if err := RemoveClaim(dir, task); err != nil {
		return true, err
	}
	return true, nil
}
