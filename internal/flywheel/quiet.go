package flywheel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Quiet gates (issue #411): a gate marked `gate[quiet]:` measures something
// host contention distorts — device timing, a hardware-in-the-loop probe — so
// it runs only once the host is idle. The host gate lock lives under
// .flywheel/locks/: every ordinary gate holds a SHARED marker
// (gates/<pid>-<n>, removed when the gate ends) and a quiet gate takes the
// EXCLUSIVE quiet.lock. A quiet gate waits, up to limits.quiet_wait, until no
// other task has a live lease on this host and no other process holds a
// marker; an ordinary gate waits the same budget while another process holds
// quiet.lock; and Run refuses to dispatch while quiet.lock is held. A marker
// or lock whose pid is dead on this host is stale and ignored.

// quietPoll is how often a waiting gate rechecks the host.
const quietPoll = 5 * time.Second

// quietSleep is the sleep ValidateTask's gate waits use; tests replace it.
var quietSleep = time.Sleep

// quietHolder is the record written into quiet.lock and into a gate marker.
type quietHolder struct {
	Task      string `json:"task"`
	Gate      string `json:"gate,omitempty"`
	PID       int    `json:"pid"`
	Host      string `json:"host"`
	StartedAt string `json:"started_at"`
	Token     string `json:"token,omitempty"`
}

func quietLockPath(dir string) string {
	return filepath.Join(dir, ".flywheel", "locks", "quiet.lock")
}

func gateMarkerDir(dir string) string {
	return filepath.Join(dir, ".flywheel", "locks", "gates")
}

// pidAlive reports whether pid names a running process on this host. On
// Windows FindProcess fails for a pid that does not exist; elsewhere it always
// succeeds and signal 0 probes the process (EPERM still means it exists).
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer p.Release()
	if runtime.GOOS == "windows" {
		return true
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

// holderLive reports whether a recorded holder still runs: a holder on
// another host cannot be probed and counts as live.
func holderLive(h quietHolder) bool {
	host, _ := os.Hostname()
	if h.Host != "" && h.Host != host {
		return true
	}
	return pidAlive(h.PID)
}

// readHolder reads the record at p. ok is false when the file is missing;
// a file still being written (unparseable) reports a zero record with ok.
func readHolder(p string) (h quietHolder, ok bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return quietHolder{}, false
	}
	_ = json.Unmarshal(b, &h)
	return h, true
}

// quietLockHeld returns the holder of quiet.lock when a live process other
// than this one holds it. An unparseable lock younger than ten seconds is
// still being written and counts as held; an older one is stale.
func quietLockHeld(dir string) (quietHolder, bool) {
	p := quietLockPath(dir)
	h, ok := readHolder(p)
	if !ok {
		return quietHolder{}, false
	}
	if h.PID == 0 {
		info, err := os.Stat(p)
		return h, err == nil && now().Sub(info.ModTime()) < 10*time.Second
	}
	if h.PID == os.Getpid() {
		return h, false
	}
	return h, holderLive(h)
}

// describe names a holder for a note: "<task> gate <n>".
func (h quietHolder) describe() string {
	if h.Gate == "" {
		return h.Task
	}
	return h.Task + " gate " + h.Gate
}

// tryQuietLock makes one attempt to create quiet.lock exclusively with rec.
// When a live process holds it, release is nil and held names the holder; a
// stale lock (dead holder) is renamed aside and removed, then retried.
func tryQuietLock(dir string, rec quietHolder) (release func(), held quietHolder, err error) {
	p := quietLockPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, quietHolder{}, fmt.Errorf("create %s: %w", filepath.Dir(p), err)
	}
	b, _ := json.Marshal(rec)
	for i := 0; i < 3; i++ {
		f, oerr := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if oerr == nil {
			_, _ = f.Write(b)
			_ = f.Close()
			return func() {
				if h, ok := readHolder(p); ok && h.Token == rec.Token {
					_ = os.Remove(p)
				}
			}, quietHolder{}, nil
		}
		if !os.IsExist(oerr) && !lockBusyOnWindows(oerr) {
			return nil, quietHolder{}, fmt.Errorf("create %s: %w", p, oerr)
		}
		if h, live := quietLockHeld(dir); live {
			return nil, h, nil
		}
		stale := p + ".stale-" + rec.Token
		if rerr := os.Rename(p, stale); rerr == nil {
			_ = os.Remove(stale)
		}
	}
	h, _ := readHolder(p)
	return nil, h, nil
}

// hostBusy lists, sorted and once each, the other tasks keeping this host
// busy at t: a live lease of another task on this host, or a gate marker of
// another live process. task's own leases are ignored.
func hostBusy(dir, task string, t time.Time) ([]string, error) {
	host, _ := os.Hostname()
	leases, err := ReadLeases(dir)
	if err != nil && leases == nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, l := range leases {
		if l.Task != task && l.Host == host && LeaseLive(l, t) {
			seen[l.Task] = true
		}
	}
	entries, _ := os.ReadDir(gateMarkerDir(dir))
	for _, e := range entries {
		h, ok := readHolder(filepath.Join(gateMarkerDir(dir), e.Name()))
		if !ok || h.PID == os.Getpid() || h.PID == 0 || !holderLive(h) {
			continue
		}
		seen[h.describe()] = true
	}
	busy := make([]string, 0, len(seen))
	for k := range seen {
		busy = append(busy, k)
	}
	sort.Strings(busy)
	return busy, nil
}

// waitQuiet is waitQuietGate with no gate id recorded.
func waitQuiet(dir string, task string, wait time.Duration, now func() time.Time, sleep func(time.Duration)) (release func(), busy []string, err error) {
	return waitQuietGate(dir, task, "", wait, now, sleep)
}

// waitQuietGate takes quiet.lock for task's gate, then polls every quietPoll
// until the host is idle (hostBusy is empty). Within wait it returns the
// lock's release; on timeout it releases the lock and returns the busy tasks
// (or the other quiet gate holding the lock) with a no-op release.
func waitQuietGate(dir, task, gate string, wait time.Duration, now func() time.Time, sleep func(time.Duration)) (release func(), busy []string, err error) {
	host, _ := os.Hostname()
	rec := quietHolder{Task: task, Gate: gate, PID: os.Getpid(), Host: host,
		StartedAt: now().UTC().Format(time.RFC3339), Token: repoLockToken()}
	deadline := now().Add(wait)
	for {
		rel, held, err := tryQuietLock(dir, rec)
		if err != nil {
			return func() {}, nil, err
		}
		if rel != nil {
			release = rel
			break
		}
		if !now().Before(deadline) {
			return func() {}, []string{held.describe()}, nil
		}
		sleep(quietPoll)
	}
	for {
		busy, err := hostBusy(dir, task, now())
		if err != nil {
			release()
			return func() {}, nil, err
		}
		if len(busy) == 0 {
			return release, nil, nil
		}
		if !now().Before(deadline) {
			release()
			return func() {}, busy, nil
		}
		sleep(quietPoll)
	}
}

// gateMarkerSeq numbers this process's gate markers.
var gateMarkerSeq atomic.Int64

// writeGateMarker creates this process's shared marker for task's gate and
// returns its removal.
func writeGateMarker(dir, task, gate string) (func(), error) {
	d := gateMarkerDir(dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", d, err)
	}
	host, _ := os.Hostname()
	p := filepath.Join(d, strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(gateMarkerSeq.Add(1), 10))
	b, _ := json.Marshal(quietHolder{Task: task, Gate: gate, PID: os.Getpid(), Host: host,
		StartedAt: now().UTC().Format(time.RFC3339)})
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", p, err)
	}
	return func() { _ = os.Remove(p) }, nil
}

// gateTurn is an ordinary gate's side of the host gate lock: it waits, up to
// wait, while another process holds quiet.lock, then holds a shared marker
// until the returned release. After the budget it runs anyway and note says
// so. The lock is rechecked after the marker is written, so a quiet gate
// that won the race is never run alongside.
func gateTurn(dir, task, gate string, wait time.Duration, now func() time.Time, sleep func(time.Duration)) (release func(), note string, err error) {
	deadline := now().Add(wait)
	for {
		holder, held := quietLockHeld(dir)
		if held && now().Before(deadline) {
			sleep(quietPoll)
			continue
		}
		rel, err := writeGateMarker(dir, task, gate)
		if err != nil {
			return func() {}, "", err
		}
		if held {
			return rel, "ran during a quiet gate (" + holder.describe() + ")", nil
		}
		again, heldAgain := quietLockHeld(dir)
		if !heldAgain {
			return rel, "", nil
		}
		if !now().Before(deadline) {
			return rel, "ran during a quiet gate (" + again.describe() + ")", nil
		}
		rel()
	}
}

// quietBusyNote is the inconclusive note of a quiet gate whose host never
// went idle.
func quietBusyNote(busy []string) string {
	return "host busy: " + strings.Join(busy, ", ")
}
