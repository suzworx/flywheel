package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// observe reads what Reconcile sees beyond the event log: the lease files and
// each run file's modification time, keyed "<task>.<attempt>" from
// .flywheel/runs/<task>.<attempt>.jsonl (issue #402). A missing runs
// directory is no run files.
func observe(dir string) (Observed, error) {
	leases, err := ReadLeases(dir)
	if err != nil {
		return Observed{}, fmt.Errorf("read leases %s: %w", dir, err)
	}
	obs := Observed{Leases: leases, RunFileMTime: map[string]time.Time{}}
	runsDir := filepath.Join(dir, ".flywheel", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return obs, nil
		}
		return Observed{}, fmt.Errorf("read %s: %w", runsDir, err)
	}
	for _, e := range entries {
		key, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if e.IsDir() || !ok {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue // removed since the listing
		}
		obs.RunFileMTime[key] = info.ModTime()
	}
	return obs, nil
}

// appendLost records one MARK_LOST action as a lost event at ts: the reason
// is the action's (lease-expired or idle), the note its evidence.
func appendLost(dir, ts string, a Action) error {
	reason := a.Reason
	if reason == "" {
		reason = "lease-expired"
	}
	if err := AppendEvent(dir, Event{TS: ts, Task: a.Task, Kind: "lost",
		Attempt: a.Attempt, Reason: reason, Note: a.Evidence}); err != nil {
		return fmt.Errorf("append lost for %s: %w", a.Task, err)
	}
	return nil
}

// MarkLost appends a lost event for every attempt Reconcile marks lost at now
// — an expired lease, or no live lease and a run file (else a dispatch) idle
// past limits.lost_after — exactly as a controller tick does, and returns how
// many it appended. It is idempotent: a lost task is never marked again.
// flywheel run and flywheel next call it so an abandoned attempt never holds
// its owns or exclusive resources (issue #402).
func MarkLost(dir string, now time.Time) (int, error) {
	marked, err := markLost(dir, now)
	return len(marked), err
}

// markLost is MarkLost returning the MARK_LOST actions it recorded.
func markLost(dir string, now time.Time) ([]Action, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", dir, err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, fmt.Errorf("read events %s: %w", dir, err)
	}
	obs, err := observe(dir)
	if err != nil {
		return nil, err
	}
	ts := now.UTC().Format(time.RFC3339Nano)
	var marked []Action
	for _, a := range Reconcile(Derive(events), events, obs, PolicyFromConfig(cfg), now) {
		if a.Kind != "MARK_LOST" {
			continue
		}
		if err := appendLost(dir, ts, a); err != nil {
			return marked, err
		}
		marked = append(marked, a)
	}
	return marked, nil
}
