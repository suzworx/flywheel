package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaimTaskFreshClaim(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	c, tookOver, err := ClaimTask(dir, "T1", "leadA", "starting", 30*time.Minute, false, now)
	if err != nil {
		t.Fatalf("ClaimTask() error = %v", err)
	}
	if tookOver != "" {
		t.Errorf("tookOver = %q, want empty for a fresh claim", tookOver)
	}
	wantExpires := now.Add(30 * time.Minute).Format(time.RFC3339)
	if c.Task != "T1" || c.Session != "leadA" || c.Note != "starting" || c.ExpiresAt != wantExpires {
		t.Errorf("ClaimTask() = %+v, want task T1 session leadA note starting expires %s", c, wantExpires)
	}
	got, ok, err := ReadClaim(dir, "T1")
	if err != nil || !ok {
		t.Fatalf("ReadClaim() = %v, %v, %v", got, ok, err)
	}
	if got != c {
		t.Errorf("ReadClaim() = %+v, want %+v", got, c)
	}
}

func TestClaimTaskRenewedBySameSession(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, _, err := ClaimTask(dir, "T1", "leadA", "", 30*time.Minute, false, now); err != nil {
		t.Fatalf("first ClaimTask() error = %v", err)
	}
	later := now.Add(10 * time.Minute)
	c, tookOver, err := ClaimTask(dir, "T1", "leadA", "still going", 30*time.Minute, false, later)
	if err != nil {
		t.Fatalf("renewal ClaimTask() error = %v", err)
	}
	if tookOver != "" {
		t.Errorf("tookOver = %q, want empty for a same-session renewal", tookOver)
	}
	wantExpires := later.Add(30 * time.Minute).Format(time.RFC3339)
	if c.ExpiresAt != wantExpires || c.Note != "still going" {
		t.Errorf("renewed claim = %+v, want expires %s and the new note", c, wantExpires)
	}
}

func TestClaimTaskRefusedForDifferentSessionWhileLive(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, _, err := ClaimTask(dir, "T1", "leadA", "", 30*time.Minute, false, now); err != nil {
		t.Fatalf("first ClaimTask() error = %v", err)
	}
	_, _, err := ClaimTask(dir, "T1", "leadB", "", 30*time.Minute, false, now.Add(time.Minute))
	var held *ErrClaimHeld
	if !errors.As(err, &held) {
		t.Fatalf("ClaimTask() error = %v, want *ErrClaimHeld", err)
	}
	if held.Task != "T1" || held.Session != "leadA" {
		t.Errorf("ErrClaimHeld = %+v, want task T1 session leadA", held)
	}
	got, _, _ := ReadClaim(dir, "T1")
	if got.Session != "leadA" {
		t.Errorf("claim holder = %q after a refused takeover, want leadA unchanged", got.Session)
	}
}

func TestClaimTaskTakenOutrightAfterExpiry(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, _, err := ClaimTask(dir, "T1", "leadA", "", time.Millisecond, false, now); err != nil {
		t.Fatalf("first ClaimTask() error = %v", err)
	}
	c, tookOver, err := ClaimTask(dir, "T1", "leadB", "", 30*time.Minute, false, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ClaimTask() over an expired claim: error = %v", err)
	}
	if tookOver != "" {
		t.Errorf("tookOver = %q, want empty when the prior claim had merely expired", tookOver)
	}
	if c.Session != "leadB" {
		t.Errorf("claim holder = %q, want leadB", c.Session)
	}
}

func TestClaimTaskForceTakeoverWhileLive(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, _, err := ClaimTask(dir, "T1", "leadA", "", 30*time.Minute, false, now); err != nil {
		t.Fatalf("first ClaimTask() error = %v", err)
	}
	c, tookOver, err := ClaimTask(dir, "T1", "leadB", "", 30*time.Minute, true, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("forced ClaimTask() error = %v", err)
	}
	if tookOver != "leadA" {
		t.Errorf("tookOver = %q, want leadA", tookOver)
	}
	if c.Session != "leadB" {
		t.Errorf("claim holder = %q, want leadB", c.Session)
	}
}

func TestReleaseTaskByHolder(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, _, err := ClaimTask(dir, "T1", "leadA", "", 30*time.Minute, false, now); err != nil {
		t.Fatalf("ClaimTask() error = %v", err)
	}
	found, err := ReleaseTask(dir, "T1", "leadA", false, now)
	if err != nil || !found {
		t.Fatalf("ReleaseTask() = %v, %v, want found=true err=nil", found, err)
	}
	if _, ok, _ := ReadClaim(dir, "T1"); ok {
		t.Error("claim file still present after ReleaseTask() by its holder")
	}
}

func TestReleaseTaskRefusedForDifferentSessionWhileLive(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if _, _, err := ClaimTask(dir, "T1", "leadA", "", 30*time.Minute, false, now); err != nil {
		t.Fatalf("ClaimTask() error = %v", err)
	}
	found, err := ReleaseTask(dir, "T1", "leadB", false, now)
	var held *ErrClaimHeld
	if !found || !errors.As(err, &held) {
		t.Fatalf("ReleaseTask() = %v, %v, want found=true and *ErrClaimHeld", found, err)
	}
	if _, ok, _ := ReadClaim(dir, "T1"); !ok {
		t.Error("claim file removed despite the refused release")
	}
}

func TestReleaseTaskNoClaimIsNoOp(t *testing.T) {
	dir := t.TempDir()
	found, err := ReleaseTask(dir, "T1", "leadA", false, time.Now())
	if found || err != nil {
		t.Errorf("ReleaseTask() on an unclaimed task = %v, %v, want found=false err=nil", found, err)
	}
}

func TestReadClaimsMixedLiveAndExpired(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	live := Claim{Task: "T1", Session: "leadA", ExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}
	expired := Claim{Task: "T2", Session: "leadB", ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)}
	if err := WriteClaim(dir, live); err != nil {
		t.Fatalf("WriteClaim(live) error = %v", err)
	}
	if err := WriteClaim(dir, expired); err != nil {
		t.Fatalf("WriteClaim(expired) error = %v", err)
	}
	claims, err := ReadClaims(dir)
	if err != nil {
		t.Fatalf("ReadClaims() error = %v", err)
	}
	if len(claims) != 2 || claims[0].Task != "T1" || claims[1].Task != "T2" {
		t.Fatalf("ReadClaims() = %+v, want [T1 T2] sorted by task", claims)
	}
	if !ClaimLive(claims[0], now) {
		t.Error("T1 reported expired, want live")
	}
	if ClaimLive(claims[1], now) {
		t.Error("T2 reported live, want expired")
	}
}

func TestReadClaimsSkipsMalformedFile(t *testing.T) {
	dir := t.TempDir()
	good := Claim{Task: "T1", Session: "leadA", ExpiresAt: "2026-09-16T12:00:00Z"}
	if err := WriteClaim(dir, good); err != nil {
		t.Fatalf("WriteClaim() error = %v", err)
	}
	bad := filepath.Join(dir, ".flywheel", "claims", "T2.json")
	if err := os.WriteFile(bad, []byte("not json\n"), 0o644); err != nil {
		t.Fatalf("write malformed claim: %v", err)
	}
	claims, err := ReadClaims(dir)
	if err == nil {
		t.Fatal("ReadClaims() = nil error, want the malformed file named (skipped, not fatal)")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("ReadClaims() error = %v, want it to name %s", err, bad)
	}
	if len(claims) != 1 || claims[0] != good {
		t.Errorf("ReadClaims() = %+v, want only the well-formed claim %+v", claims, good)
	}
}
