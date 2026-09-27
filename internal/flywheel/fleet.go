package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// FleetVersion is the fleet.json schema version this binary writes.
const FleetVersion = 1

// FleetRoot is one registered factory root (issue #585): a checkout or
// repository whose ledgers the fleet reads.
type FleetRoot struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// Fleet is the user-level registry of factory roots, stored as JSON at
// FleetPath.
type Fleet struct {
	Version int         `json:"version"`
	Roots   []FleetRoot `json:"roots"`
}

// FleetPath is the registry file: $FLYWHEEL_FLEET when set, otherwise
// fleet.json under the user config directory's flywheel folder.
func FleetPath() (string, error) {
	if p := os.Getenv("FLYWHEEL_FLEET"); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("fleet registry: %w", err)
	}
	return filepath.Join(dir, "flywheel", "fleet.json"), nil
}

// LoadFleet reads the registry at file; a missing file is an empty fleet.
func LoadFleet(file string) (Fleet, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return Fleet{Version: FleetVersion}, nil
		}
		return Fleet{}, fmt.Errorf("read %s: %w", file, err)
	}
	var f Fleet
	if err := json.Unmarshal(b, &f); err != nil {
		return Fleet{}, fmt.Errorf("parse %s: %w", file, err)
	}
	if f.Version == 0 {
		f.Version = FleetVersion
	}
	return f, nil
}

// SaveFleet writes f to file atomically.
func SaveFleet(file string, f Fleet) error {
	f.Version = FleetVersion
	if f.Roots == nil {
		f.Roots = []FleetRoot{}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fleet: %w", err)
	}
	b = append(b, '\n')
	return atomicWrite(filepath.Dir(file), filepath.Base(file), "fleet-*.json", b)
}

// fleetKey is p's identity for de-duplication: absolute, symlinks resolved
// when it exists (macOS temp dirs reach git as /private/...), cleaned, and
// case-folded on Windows, whose file system ignores case.
func fleetKey(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// AddRoot registers path under name (the path's base when empty, made
// unique with -2, -3, ...). It refuses a path already registered, naming the
// entry, and a path that holds no .flywheel/ and is not a git repository.
func (f *Fleet) AddRoot(path, name string) (FleetRoot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return FleetRoot{}, fmt.Errorf("fleet add %s: %w", path, err)
	}
	abs = filepath.Clean(abs)
	for _, r := range f.Roots {
		if fleetKey(r.Path) == fleetKey(abs) {
			return FleetRoot{}, fmt.Errorf("fleet add %s: already registered as %q", abs, r.Name)
		}
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return FleetRoot{}, fmt.Errorf("fleet add %s: not a directory", abs)
	}
	if !isDir(filepath.Join(abs, ".flywheel")) && exec.Command("git", "-C", abs, "rev-parse", "--git-dir").Run() != nil {
		return FleetRoot{}, fmt.Errorf("fleet add %s: neither a flywheel factory (.flywheel/) nor a git repository", abs)
	}
	base := name
	if base == "" {
		base = filepath.Base(abs)
	} else if f.hasName(base) {
		return FleetRoot{}, fmt.Errorf("fleet add %s: name %q is taken", abs, base)
	}
	name = base
	for i := 2; f.hasName(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	r := FleetRoot{Name: name, Path: abs}
	f.Roots = append(f.Roots, r)
	return r, nil
}

// hasName reports whether a root is registered under name.
func (f *Fleet) hasName(name string) bool {
	for _, r := range f.Roots {
		if r.Name == name {
			return true
		}
	}
	return false
}

// RemoveRoot unregisters the root named name; an unknown name is an error.
func (f *Fleet) RemoveRoot(name string) error {
	for i, r := range f.Roots {
		if r.Name == name {
			f.Roots = append(f.Roots[:i:i], f.Roots[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("fleet remove: no root named %q", name)
}

// Fleet ledger kinds: how discovery reached a ledger.
const (
	FleetKindRoot             = "root"
	FleetKindGitWorktree      = "git-worktree"
	FleetKindFlywheelWorktree = "flywheel-worktree"
	FleetKindClaudeWorktree   = "claude-worktree"
)

// FleetLedger is one ledger discovery found under a root: the root's name,
// the ledger's display name and directory, how it was reached (Kind), and
// Error when the root itself is missing.
type FleetLedger struct {
	Root  string `json:"root"`
	Name  string `json:"name"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Error string `json:"error,omitempty"`
}

// FleetLedgers discovers every ledger under f's roots: the root itself, its
// git worktrees (read-only `git worktree list --porcelain`, skipped when git
// fails), and the directories under .flywheel/worktrees and .claude/worktrees.
// Only a directory holding .flywheel/events.jsonl or a shard directory counts.
// Ledgers are de-duplicated by path (the first root reaching one keeps it)
// and ordered by root, the root's own ledger first, then by path. A missing
// root is one entry carrying Error, never a failure.
func FleetLedgers(f Fleet) []FleetLedger {
	var out []FleetLedger
	seen := map[string]bool{}
	for _, r := range f.Roots {
		if !isDir(r.Path) {
			out = append(out, FleetLedger{Root: r.Name, Name: r.Name, Path: r.Path, Kind: FleetKindRoot, Error: "root missing"})
			continue
		}
		var found []FleetLedger
		add := func(p, kind string) {
			p = filepath.Clean(p)
			if seen[fleetKey(p)] || !hasLedger(p) {
				return
			}
			seen[fleetKey(p)] = true
			name := r.Name
			if kind != FleetKindRoot {
				name = r.Name + "/" + filepath.Base(p)
			}
			found = append(found, FleetLedger{Root: r.Name, Name: name, Path: p, Kind: kind})
		}
		add(r.Path, FleetKindRoot)
		var rest []FleetLedger
		for _, p := range gitWorktreePaths(r.Path) {
			add(p, FleetKindGitWorktree)
		}
		for _, sub := range []struct{ dir, kind string }{
			{filepath.Join(r.Path, ".flywheel", "worktrees"), FleetKindFlywheelWorktree},
			{filepath.Join(r.Path, ".claude", "worktrees"), FleetKindClaudeWorktree},
		} {
			entries, _ := os.ReadDir(sub.dir)
			for _, e := range entries {
				if e.IsDir() {
					add(filepath.Join(sub.dir, e.Name()), sub.kind)
				}
			}
		}
		if len(found) > 0 && found[0].Kind == FleetKindRoot {
			out = append(out, found[0])
			found = found[1:]
		}
		rest = append(rest, found...)
		slices.SortStableFunc(rest, func(a, b FleetLedger) int { return strings.Compare(fleetKey(a.Path), fleetKey(b.Path)) })
		out = append(out, rest...)
	}
	return out
}

// hasLedger reports whether dir holds a flywheel ledger: an events.jsonl
// file or a shard directory under .flywheel.
func hasLedger(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, ".flywheel", "events.jsonl")); err == nil && !info.IsDir() {
		return true
	}
	return isDir(filepath.Join(dir, ".flywheel", shardDirName))
}

// gitWorktreePaths lists dir's git worktrees from `git worktree list
// --porcelain`; nil when git fails (not a repository, no git).
func gitWorktreePaths(dir string) []string {
	b, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			out = append(out, filepath.FromSlash(p))
		}
	}
	return out
}

// isDir reports whether p is an existing directory.
func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// FleetRow is one ledger's line in the fleet status (issue #585): its
// discovery entry, its units by stage and event-derived andon count
// (ledgerSummary), whether it is suspended, the models a rate limit pauses,
// and the ages in whole seconds of its latest health and latest events (nil
// when none). Error is set when the ledger fails to read; the other fields
// are then zero. A FoldIdle row has Kind FleetKindIdle, Idle the number of
// ledgers it stands for and LastAge the oldest of their latest-event ages.
type FleetRow struct {
	FleetLedger
	Tasks     StatusTasks `json:"tasks"`
	Andon     int         `json:"andon"`
	Suspended bool        `json:"suspended"`
	Paused    []string    `json:"paused,omitempty"`
	HealthAge *int        `json:"health_age,omitempty"`
	LastAge   *int        `json:"last_age,omitempty"`
	Idle      int         `json:"idle,omitempty"`
}

// FleetKindIdle is the kind of a FoldIdle row.
const FleetKindIdle = "idle"

// DefaultIdleAfter is how long a worktree ledger's latest event may be old
// before FoldIdle folds it away (issue #605).
const DefaultIdleAfter = 72 * time.Hour

// FoldIdle drops each non-root ledger whose latest event is older than
// idleAfter and appends, after the last row of each root that lost any, one
// FleetKindIdle row counting them with the oldest age. Root ledgers, rows
// carrying Error and ledgers with no event are always kept.
func FoldIdle(rows []FleetRow, idleAfter time.Duration) []FleetRow {
	idle := func(r FleetRow) bool {
		return r.Kind != FleetKindRoot && r.Error == "" && r.LastAge != nil &&
			time.Duration(*r.LastAge)*time.Second > idleAfter
	}
	var out []FleetRow
	for i := 0; i < len(rows); {
		root := rows[i].Root
		fold := FleetRow{FleetLedger: FleetLedger{Root: root, Kind: FleetKindIdle}}
		for ; i < len(rows) && rows[i].Root == root; i++ {
			if !idle(rows[i]) {
				out = append(out, rows[i])
				continue
			}
			fold.Idle++
			if fold.LastAge == nil || *rows[i].LastAge > *fold.LastAge {
				age := *rows[i].LastAge
				fold.LastAge = &age
			}
		}
		if fold.Idle > 0 {
			fold.Name = fmt.Sprintf("+%d idle worktree ledgers", fold.Idle)
			out = append(out, fold)
		}
	}
	return out
}

// State is the row's factory state: SUSPENDED, "paused: m1,m2" or running.
func (r FleetRow) State() string {
	switch {
	case r.Suspended:
		return "SUSPENDED"
	case len(r.Paused) > 0:
		return "paused: " + strings.Join(r.Paused, ",")
	}
	return "running"
}

// FleetStatus summarises every ledger FleetLedgers finds in f at now with
// ledgerSummary, runtime.GOMAXPROCS(0) ledgers at a time, returning the rows
// in discovery order. A ledger that fails to read is a row carrying Error; it
// never fails the rest.
func FleetStatus(f Fleet, now time.Time) []FleetRow {
	ledgers := FleetLedgers(f)
	rows := make([]FleetRow, len(ledgers))
	next := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(ledgers)) {
		wg.Go(func() {
			for i := range next {
				rows[i] = FleetRow{FleetLedger: ledgers[i]}
				if ledgers[i].Error != "" {
					continue
				}
				row, err := ledgerSummary(ledgers[i].Path, now)
				row.FleetLedger = ledgers[i]
				if err != nil {
					row = FleetRow{FleetLedger: ledgers[i]}
					row.Error = err.Error()
				}
				rows[i] = row
			}
		})
	}
	for i := range ledgers {
		next <- i
	}
	close(next)
	wg.Wait()
	return rows
}

// ledgerSummary is dir's fleet row from one read of its events (issue #605):
// stage counts from Derive, the event-derivable andon count (summaryAndon),
// the freeze, the paused models, and the ages of the latest health and the
// latest event. It builds no floor, runs no git and reads no run file; the
// returned row's FleetLedger is left zero for the caller to fill.
func ledgerSummary(dir string, now time.Time) (FleetRow, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return FleetRow{}, fmt.Errorf("read events %s: %w", dir, err)
	}
	pauseAt := Limits{}.RateLimitPauseThreshold()
	if cfg, _, err := LoadConfig(dir); err == nil {
		pauseAt = cfg.Limits.RateLimitPauseThreshold()
	}
	st := Derive(events)
	var row FleetRow
	for _, ts := range st.Tasks {
		countStage(&row.Tasks, ts.Status)
	}
	row.Suspended = FactorySuspended(events, now).Suspended
	row.Paused, _ = pausedModels(events, now, pauseAt)
	row.Andon = summaryAndon(events, st, now, len(row.Paused), row.Suspended)
	if last := latestEvent(events, now, func(Event) bool { return true }); last != nil {
		age := last.Age
		row.LastAge = &age
	}
	if _, at, ok := LatestHealth(events); ok {
		age := ageOfTime(at, now)
		row.HealthAge = &age
	}
	return row, nil
}

// countStage adds one task in status to t, as Status counts it.
func countStage(t *StatusTasks, status string) {
	t.Total++
	switch status {
	case "planned":
		t.Planned++
	case "dispatched":
		t.Dispatched++
	case "running":
		t.Running++
	case "finished":
		t.Finished++
	case "passed":
		t.Passed++
	case "needs-correction":
		t.NeedsCorrection++
	case "rejected":
		t.Rejected++
	case "blocked":
		t.Blocked++
	case "lost":
		t.Lost++
	case "withdrawn":
		t.Withdrawn++
	case "landed":
		t.Landed++
	}
}

// summaryAndon counts the floor's andon kinds that the events alone decide:
// the freeze, each paused model, each group with open blocking findings, a
// stale health event, and each task with an attempt that is blocked or
// finished for a reason other than a clean stop (the floor, too, judges only
// tasks with an attempt). Run-file states (silent, stalled, no-writes),
// review findings and staffing mismatches need the floor and are left out.
func summaryAndon(events []Event, st State, now time.Time, paused int, suspended bool) int {
	n := paused + len(groupAndon(st.Groups, now)) + len(healthAndon(events, now))
	if suspended {
		n++
	}
	for _, ts := range st.Tasks {
		if ts.Attempt == "" {
			continue
		}
		if ts.Status == "blocked" || ts.Status == "finished" && ts.Reason != "" && ts.Reason != "stop" {
			n++
		}
	}
	return n
}
