package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteLeaseReadRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := Lease{
		Task: "T1", Attempt: "r1", PID: 4242, Host: "host-a",
		StartedAt: "2026-09-12T00:00:00Z", RenewedAt: "2026-09-12T00:00:15Z",
		ExpiresAt: "2026-09-12T00:00:45Z", RunFile: ".flywheel/runs/T1.r1.jsonl",
	}
	if err := WriteLease(dir, want); err != nil {
		t.Fatalf("WriteLease() error = %v", err)
	}
	path := filepath.Join(dir, ".flywheel", "leases", "T1.r1.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lease file: %v", err)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Error("lease file does not end with a trailing newline")
	}
	got, err := ReadLeases(dir)
	if err != nil {
		t.Fatalf("ReadLeases() error = %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Errorf("ReadLeases() = %v, want [%+v]", got, want)
	}
}

func TestReadLeasesSortOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	leases := []Lease{
		{Task: "T2", Attempt: "r1"},
		{Task: "T1", Attempt: "r10"},
		{Task: "T1", Attempt: "c2"},
		{Task: "T1", Attempt: "r2"},
		{Task: "T1", Attempt: "c1"},
	}
	for _, l := range leases {
		if err := WriteLease(dir, l); err != nil {
			t.Fatalf("WriteLease(%s.%s) error = %v", l.Task, l.Attempt, err)
		}
	}
	got, err := ReadLeases(dir)
	if err != nil {
		t.Fatalf("ReadLeases() error = %v", err)
	}
	var ids []string
	for _, l := range got {
		ids = append(ids, l.Task+"."+l.Attempt)
	}
	want := "T1.c1 T1.c2 T1.r2 T1.r10 T2.r1"
	if strings.Join(ids, " ") != want {
		t.Errorf("ReadLeases() order = %v, want %s", ids, want)
	}
}

func TestReadLeasesSkipsMalformedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := Lease{Task: "T1", Attempt: "r1", ExpiresAt: "2026-09-12T00:00:45Z"}
	if err := WriteLease(dir, good); err != nil {
		t.Fatalf("WriteLease() error = %v", err)
	}
	bad := filepath.Join(dir, ".flywheel", "leases", "T1.r2.json")
	if err := os.WriteFile(bad, []byte("not json\n"), 0o644); err != nil {
		t.Fatalf("write malformed lease: %v", err)
	}
	got, err := ReadLeases(dir)
	if err == nil {
		t.Fatal("ReadLeases() = nil error, want the malformed file named")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("ReadLeases() error = %v, want it to name %s", err, bad)
	}
	if len(got) != 1 || got[0] != good {
		t.Errorf("ReadLeases() = %v, want only the well-formed lease %+v", got, good)
	}
}

func TestLeaseLiveBoundary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	l := Lease{ExpiresAt: now.Format(time.RFC3339)}
	if !LeaseLive(l, now.Add(-time.Second)) {
		t.Error("LeaseLive() = false before expiry, want true")
	}
	if !LeaseLive(l, now) {
		t.Error("LeaseLive() = false exactly at expiry, want true (live while now <= expires_at)")
	}
	if LeaseLive(l, now.Add(time.Second)) {
		t.Error("LeaseLive() = true after expiry, want false")
	}
	if LeaseLive(Lease{ExpiresAt: "not a time"}, now) {
		t.Error("LeaseLive() = true for an unparseable expiry, want false")
	}
}

func TestRemoveLeaseMissingFileNotError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := RemoveLease(dir, "T1", "r99"); err != nil {
		t.Errorf("RemoveLease() on a missing lease: error = %v, want nil", err)
	}
	if err := WriteLease(dir, Lease{Task: "T1", Attempt: "r1"}); err != nil {
		t.Fatalf("WriteLease() error = %v", err)
	}
	if err := RemoveLease(dir, "T1", "r1"); err != nil {
		t.Fatalf("RemoveLease() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "leases", "T1.r1.json")); !os.IsNotExist(err) {
		t.Errorf("lease file still present after RemoveLease: %v", err)
	}
}
