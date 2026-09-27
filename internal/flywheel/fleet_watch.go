package flywheel

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// WatchStaleAfter is how old a ledger's latest health event may be before
// FleetWatch reports its health stale.
const WatchStaleAfter = 10 * time.Minute

// WatchStateVersion is the fleet-watch.json schema version.
const WatchStateVersion = 1

// WatchItem is one thing FleetWatch reports: Kind is learning, andon, state
// or stale; Ledger the ledger's name; Text what happened.
type WatchItem struct {
	Kind   string `json:"kind"`
	Ledger string `json:"ledger"`
	Text   string `json:"text"`
}

// WatchState is what FleetWatch saw last, per ledger path; Queue is the
// learnings queue it syncs (LearningsQueuePath), empty to skip learnings.
// First is set by LoadWatchState when there is no state file yet: the caller
// then records a baseline (WatchBaseline) instead of reporting every
// historical andon as new.
type WatchState struct {
	Version int                    `json:"version"`
	Ledgers map[string]WatchLedger `json:"ledgers"`
	Queue   string                 `json:"-"`
	First   bool                   `json:"-"`
}

// WatchLedger is one ledger as FleetWatch last saw it: its factory state
// (FleetRow.State), the identities of its open andons, sorted, and whether
// its health was stale.
type WatchLedger struct {
	State  string   `json:"state"`
	Andons []string `json:"andons,omitempty"`
	Stale  bool     `json:"stale,omitempty"`
}

// WatchStatePath is the watch state file beside the registry at fleetFile.
func WatchStatePath(fleetFile string) string {
	return filepath.Join(filepath.Dir(fleetFile), "fleet-watch.json")
}

// LoadWatchState reads the state at file, a missing file being empty with
// First set, and sets Queue to the learnings queue beside it. A file that
// does not parse is an error, never an empty state that would report
// everything again.
func LoadWatchState(file string) (WatchState, error) {
	s := WatchState{Version: WatchStateVersion, Ledgers: map[string]WatchLedger{}}
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		s.First = true
	} else if err != nil {
		return s, fmt.Errorf("read %s: %w", file, err)
	} else if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", file, err)
	}
	if s.Ledgers == nil {
		s.Ledgers = map[string]WatchLedger{}
	}
	s.Queue = LearningsQueuePath(file)
	return s, nil
}

// WatchBaseline is the one line a first watch prints instead of its items:
// how many ledgers and andons next records and how many learnings pending
// holds.
func WatchBaseline(next WatchState, pending int) string {
	andons := 0
	for _, l := range next.Ledgers {
		andons += len(l.Andons)
	}
	return fmt.Sprintf("baseline recorded: %d ledgers, %d andons, %d pending learnings; changes from now on are reported",
		len(next.Ledgers), andons, pending)
}

// FleetWatchAll is the whole current set as items, for --report-all: every
// pending learning of prev.Queue after a sync (oldest first), then per ledger
// its state when not running, every open andon and stale health, as
// FleetWatch reports them against an empty state. next is FleetWatch's.
func FleetWatchAll(f Fleet, prev WatchState, now time.Time) (items []WatchItem, next WatchState) {
	all, next := FleetWatch(f, WatchState{Ledgers: map[string]WatchLedger{}, Queue: prev.Queue}, now)
	if prev.Queue != "" {
		if q, err := LoadLearningsQueue(prev.Queue); err == nil {
			ls := slices.Collect(maps.Values(q.Pending))
			slices.SortFunc(ls, func(a, b FleetLearning) int {
				return cmp.Or(strings.Compare(a.TS, b.TS), strings.Compare(a.Key, b.Key))
			})
			for _, l := range ls {
				items = append(items, learningItem(l))
			}
		}
	}
	for _, it := range all {
		if it.Kind != "learning" {
			items = append(items, it)
		}
	}
	return items, next
}

// learningItem is the WatchItem of a pending learning.
func learningItem(l FleetLearning) WatchItem {
	return WatchItem{Kind: "learning", Ledger: l.Ledger, Text: fmt.Sprintf("[%s] %s (%s)", l.Severity, l.Title, l.Key[:12])}
}

// SaveWatchState writes s to file atomically.
func SaveWatchState(file string, s WatchState) error {
	s.Version = WatchStateVersion
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode watch state: %w", err)
	}
	return atomicWrite(filepath.Dir(file), filepath.Base(file), "fleet-watch-*.json", append(b, '\n'))
}

// FleetWatch compares the fleet at now with prev and returns one WatchItem
// per change, and the state to pass next time: first the learnings
// SyncLearnings newly made pending on prev.Queue (oldest first), then, per
// ledger in FleetLedgers order, a state change (running, SUSPENDED or
// "paused: m"; a ledger not in prev counts as running before), each andon
// not open before (sorted), and health newly older than WatchStaleAfter.
// Worktree ledgers count only their events after the fork, as FleetStatus
// does. A ledger that fails to read keeps its previous state and reports
// nothing; a failed learnings sync is one item of kind error.
func FleetWatch(f Fleet, prev WatchState, now time.Time) (items []WatchItem, next WatchState) {
	next = WatchState{Version: WatchStateVersion, Ledgers: map[string]WatchLedger{}, Queue: prev.Queue}
	if prev.Queue != "" {
		newly, _, err := SyncLearnings(prev.Queue, f, now)
		if err != nil {
			items = append(items, WatchItem{Kind: "error", Ledger: "fleet", Text: "learnings: " + err.Error()})
		}
		for _, l := range newly {
			items = append(items, learningItem(l))
		}
	}
	ledgers := FleetLedgers(f)
	events := make([][]Event, len(ledgers))
	errs := make([]error, len(ledgers))
	var roots, rest []int
	for i, l := range ledgers {
		switch {
		case l.Error != "":
		case l.Kind == FleetKindRoot:
			roots = append(roots, i)
		default:
			rest = append(rest, i)
		}
	}
	read := func(i int) { events[i], errs[i] = ReadEvents(ledgers[i].Path) }
	fleetEach(roots, read)
	fleetEach(rest, read)
	rootKeys := map[string]map[[32]byte]bool{}
	for _, i := range roots {
		if errs[i] == nil {
			keys := make(map[[32]byte]bool, len(events[i]))
			for _, e := range events[i] {
				keys[eventKey(e)] = true
			}
			rootKeys[ledgers[i].Root] = keys
		}
	}
	for i, l := range ledgers {
		old, had := prev.Ledgers[l.Path]
		if l.Error != "" || errs[i] != nil {
			if had {
				next.Ledgers[l.Path] = old
			}
			continue
		}
		var root map[[32]byte]bool
		if l.Kind != FleetKindRoot {
			root = rootKeys[l.Root]
		}
		w := watchLedger(l.Path, events[i], now, root)
		next.Ledgers[l.Path] = w
		if !had {
			old.State = "running"
		}
		if w.State != old.State {
			items = append(items, WatchItem{Kind: "state", Ledger: l.Name, Text: old.State + " -> " + w.State})
		}
		for _, a := range w.Andons {
			if !slices.Contains(old.Andons, a) {
				items = append(items, WatchItem{Kind: "andon", Ledger: l.Name, Text: a})
			}
		}
		if w.Stale && !old.Stale {
			items = append(items, WatchItem{Kind: "stale", Ledger: l.Name, Text: "health older than " + WatchStaleAfter.String()})
		}
	}
	return items, next
}

// WatchLine is it as one printed line: "<time> <ledger> <kind>: <text>".
func WatchLine(it WatchItem, now time.Time) string {
	return now.Format(time.RFC3339) + " " + it.Ledger + " " + it.Kind + ": " + it.Text
}

// NotifyRunner runs argv with env added to the process environment.
type NotifyRunner func(argv, env []string) error

// ExecNotify is the NotifyRunner that really runs the command.
func ExecNotify(argv, env []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// NotifyWatch runs cmd once per line through the shell chooser (ShellArgv)
// with FLYWHEEL_FLEET_ITEM set to the line, and returns every failure; one
// failing never stops the rest.
func NotifyWatch(lines []string, cmd string, run NotifyRunner) []error {
	var errs []error
	for _, line := range lines {
		if err := run(ShellArgv(cmd), []string{"FLYWHEEL_FLEET_ITEM=" + line}); err != nil {
			errs = append(errs, fmt.Errorf("notify %q: %w", line, err))
		}
	}
	return errs
}

// watchLedger is the WatchLedger of a ledger's events at now. With root, the
// key set of its root's events, only the events not in root count, as in
// ledgerSummary. The andons are the event-derived ones summaryAndon counts,
// less the freeze, the pauses and the stale health, which the state and the
// stale flag report: each group with open blocking findings, and each unit
// with an attempt that is blocked or finished for a reason other than stop.
func watchLedger(dir string, events []Event, now time.Time, root map[[32]byte]bool) WatchLedger {
	pauseAt := Limits{}.RateLimitPauseThreshold()
	if cfg, _, err := LoadConfig(dir); err == nil {
		pauseAt = cfg.Limits.RateLimitPauseThreshold()
	}
	st := Derive(events)
	own, groups := events, st.Groups
	var mine map[string]bool
	if root != nil {
		own, mine = nil, map[string]bool{}
		for _, e := range events {
			if !root[eventKey(e)] {
				own = append(own, e)
				mine[e.Task] = true
			}
		}
		groups = Derive(own).Groups
	}
	row := FleetRow{Suspended: FactorySuspended(own, now).Suspended}
	row.Paused, _ = pausedModels(own, now, pauseAt)
	w := WatchLedger{State: row.State()}
	for _, a := range groupAndon(groups, now) {
		w.Andons = append(w.Andons, a.Task+" open findings")
	}
	for _, ts := range st.Tasks {
		if ts.Attempt == "" || mine != nil && !mine[ts.ID] {
			continue
		}
		switch {
		case ts.Status == "blocked":
			w.Andons = append(w.Andons, ts.ID+" blocked")
		case ts.Status == "finished" && ts.Reason != "" && ts.Reason != "stop":
			w.Andons = append(w.Andons, ts.ID+" finished: "+ts.Reason)
		}
	}
	slices.Sort(w.Andons)
	if _, at, ok := LatestHealth(own); ok && now.Sub(at) > WatchStaleAfter {
		w.Stale = true
	}
	return w
}
