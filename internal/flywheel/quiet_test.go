package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeQuietLock writes quiet.lock under dir with the given record.
func writeQuietLock(t *testing.T, dir, record string) {
	t.Helper()
	p := quietLockPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(record), 0o644); err != nil {
		t.Fatal(err)
	}
}

// quietClock returns a now that advances only through the returned sleep.
func quietClock(polls *int) (func() time.Time, func(time.Duration)) {
	clock := time.Now()
	return func() time.Time { return clock }, func(d time.Duration) { *polls++; clock = clock.Add(d) }
}

// TestGateTurnWaitsForQuietGate checks an ordinary gate waits the whole
// budget while another process holds quiet.lock, then runs anyway noting it
// ran during a quiet gate, and holds a shared marker until released (#411).
func TestGateTurnWaitsForQuietGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQuietLock(t, dir, `{"task":"T9","gate":"2","pid":4242,"host":"another-host"}`)
	polls := 0
	nowFn, sleep := quietClock(&polls)
	release, note, err := gateTurn(dir, "T1", "1", time.Minute, nowFn, sleep)
	if err != nil {
		t.Fatalf("gateTurn() error = %v", err)
	}
	if polls != 12 || note != "ran during a quiet gate (T9 gate 2)" {
		t.Errorf("polls/note = %d/%q, want 12/ran during a quiet gate (T9 gate 2)", polls, note)
	}
	entries, _ := os.ReadDir(gateMarkerDir(dir))
	if len(entries) != 1 {
		t.Errorf("markers = %d, want 1 while the gate runs", len(entries))
	}
	release()
	if entries, _ = os.ReadDir(gateMarkerDir(dir)); len(entries) != 0 {
		t.Errorf("markers = %d, want 0 after release", len(entries))
	}
}

// TestQuietLockStaleIgnored checks a quiet.lock whose holder pid is dead on
// this host neither blocks an ordinary gate nor a new quiet gate, which takes
// it over; a dead process's gate marker never keeps the host busy (#411).
func TestQuietLockStaleIgnored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	host, _ := os.Hostname()
	writeQuietLock(t, dir, `{"task":"T9","gate":"2","pid":999999999,"host":"`+host+`"}`)
	if _, held := quietLockHeld(dir); held {
		t.Fatal("quietLockHeld() = true for a dead holder, want false")
	}
	if err := os.MkdirAll(gateMarkerDir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := `{"task":"T8","gate":"1","pid":999999999,"host":"` + host + `"}`
	if err := os.WriteFile(filepath.Join(gateMarkerDir(dir), "999999999-1"), []byte(marker), 0o644); err != nil {
		t.Fatal(err)
	}
	polls := 0
	nowFn, sleep := quietClock(&polls)
	release, busy, err := waitQuiet(dir, "T1", time.Minute, nowFn, sleep)
	if err != nil || len(busy) != 0 || polls != 0 {
		t.Fatalf("waitQuiet() = busy %v, err %v, polls %d; want idle at once", busy, err, polls)
	}
	b, _ := os.ReadFile(quietLockPath(dir))
	if !strings.Contains(string(b), `"task":"T1"`) {
		t.Errorf("quiet.lock = %s, want the new holder T1", b)
	}
	release()
	if _, err := os.Stat(quietLockPath(dir)); !os.IsNotExist(err) {
		t.Errorf("quiet.lock present after release (stat err %v)", err)
	}
}
