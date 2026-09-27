package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// FleetLearning is one learning across the fleet (issue #585): its stable
// Key (LearningKey), the root and ledger it was first seen in and when, its
// curation fields, and Dismissed when any ledger dismissed it.
type FleetLearning struct {
	Key       string   `json:"key"`
	Root      string   `json:"root"`
	Ledger    string   `json:"ledger"`
	TS        string   `json:"ts"`
	Task      string   `json:"task"`
	Severity  string   `json:"severity"`
	Title     string   `json:"title"`
	Observed  string   `json:"observed"`
	Evidence  string   `json:"evidence,omitempty"`
	Ask       string   `json:"ask,omitempty"`
	Signals   []string `json:"signals,omitempty"`
	Dismissed bool     `json:"dismissed"`
}

// LearningKey is a learning's identity across ledgers: the hex sha256 of its
// title and observed text, so a learning copied into another ledger, or
// recorded again word for word, is one learning.
func LearningKey(title, observed string) string {
	sum := sha256.Sum256([]byte(title + "\x00" + observed))
	return hex.EncodeToString(sum[:])
}

// learningTime parses a learning's TS; the zero time when it does not parse.
func learningTime(ts string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, ts)
	return t
}

// FleetLearnings is every learning event across FleetLedgers(f), one per
// LearningKey, oldest first. The root ledgers are read first; a worktree's
// learning events that are also in its root's ledger (eventKey, issue #608)
// are inherited and never first-seen there. Across ledgers the earliest TS
// wins, ties going to the ledger discovered first. A learning is dismissed
// when a dismissed event in any ledger targets it (by that ledger's L-NN id).
// A ledger that fails to read contributes nothing; now is unused but keeps
// the fleet functions' shape.
func FleetLearnings(f Fleet, now time.Time) []FleetLearning {
	_ = now
	ledgers := FleetLedgers(f)
	events := make([][]Event, len(ledgers))
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
	read := func(i int) { events[i], _ = ReadEvents(ledgers[i].Path) }
	fleetEach(roots, read)
	fleetEach(rest, read)
	rootKeys := map[string]map[[32]byte]bool{}
	for _, i := range roots {
		if events[i] != nil {
			keys := make(map[[32]byte]bool, len(events[i]))
			for _, e := range events[i] {
				keys[eventKey(e)] = true
			}
			rootKeys[ledgers[i].Root] = keys
		}
	}
	byKey := map[string]int{}
	dismissed := map[string]bool{}
	var out []FleetLearning
	for _, i := range append(roots, rest...) {
		var root map[[32]byte]bool
		if ledgers[i].Kind != FleetKindRoot {
			root = rootKeys[ledgers[i].Root]
		}
		views := Learnings(events[i])
		n := 0
		for _, e := range events[i] {
			if e.Kind != "learning" {
				continue
			}
			v := views[n]
			n++
			key := LearningKey(e.Title, e.Observed)
			if v.Dismissed {
				dismissed[key] = true
			}
			if root[eventKey(e)] {
				continue
			}
			l := FleetLearning{Key: key, Root: ledgers[i].Root, Ledger: ledgers[i].Name, TS: e.TS, Task: e.Task,
				Severity: e.Severity, Title: e.Title, Observed: e.Observed, Evidence: e.Evidence, Ask: e.Ask, Signals: e.Signals}
			if j, ok := byKey[key]; !ok {
				byKey[key] = len(out)
				out = append(out, l)
			} else if learningTime(l.TS).Before(learningTime(out[j].TS)) {
				out[j] = l
			}
		}
	}
	for i := range out {
		out[i].Dismissed = dismissed[out[i].Key]
	}
	slices.SortStableFunc(out, func(a, b FleetLearning) int { return learningTime(a.TS).Compare(learningTime(b.TS)) })
	return out
}

// LearningsQueueVersion is the fleet-learnings.json schema version.
const LearningsQueueVersion = 1

// FirstSyncWindow is how recent a learning must be for the first sync of an
// empty queue to make it pending; older ones are only marked seen.
const FirstSyncWindow = 7 * 24 * time.Hour

// LearningsQueue is the durable learnings queue beside the fleet registry:
// every key ever seen, and the pending learnings a lead has not handled yet.
type LearningsQueue struct {
	Version int                      `json:"version"`
	Seen    []string                 `json:"seen"`
	Pending map[string]FleetLearning `json:"pending"`
}

// LearningsQueuePath is the queue file beside the registry at fleetFile.
func LearningsQueuePath(fleetFile string) string {
	return filepath.Join(filepath.Dir(fleetFile), "fleet-learnings.json")
}

// LoadLearningsQueue reads the queue at file; a missing file is empty. A
// file that does not parse is an error, never an empty queue: resetting it
// would flood pending with every learning again.
func LoadLearningsQueue(file string) (LearningsQueue, error) {
	q := LearningsQueue{Version: LearningsQueueVersion, Pending: map[string]FleetLearning{}}
	b, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return q, nil
	} else if err != nil {
		return q, fmt.Errorf("read %s: %w", file, err)
	}
	if err := json.Unmarshal(b, &q); err != nil {
		return q, fmt.Errorf("parse %s: %w", file, err)
	}
	if q.Pending == nil {
		q.Pending = map[string]FleetLearning{}
	}
	return q, nil
}

// SaveLearningsQueue writes q to file atomically, seen keys sorted.
func SaveLearningsQueue(file string, q LearningsQueue) error {
	q.Version = LearningsQueueVersion
	if q.Seen == nil {
		q.Seen = []string{}
	}
	slices.Sort(q.Seen)
	b, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return fmt.Errorf("encode learnings queue: %w", err)
	}
	return atomicWrite(filepath.Dir(file), filepath.Base(file), "fleet-learnings-*.json", append(b, '\n'))
}

// SyncLearnings adds every FleetLearnings key the queue at file has not seen
// to its seen set and, unless dismissed, to pending, and returns those it
// made pending in age order. first reports the first sync of an empty queue:
// it marks everything seen but makes pending only the learnings newer than
// FirstSyncWindow, so history does not flood the queue.
func SyncLearnings(file string, f Fleet, now time.Time) (newly []FleetLearning, first bool, err error) {
	q, err := LoadLearningsQueue(file)
	if err != nil {
		return nil, false, err
	}
	first = len(q.Seen) == 0 && len(q.Pending) == 0
	seen := map[string]bool{}
	for _, k := range q.Seen {
		seen[k] = true
	}
	for _, l := range FleetLearnings(f, now) {
		if seen[l.Key] {
			continue
		}
		seen[l.Key] = true
		q.Seen = append(q.Seen, l.Key)
		if l.Dismissed || first && now.Sub(learningTime(l.TS)) > FirstSyncWindow {
			continue
		}
		q.Pending[l.Key] = l
		newly = append(newly, l)
	}
	return newly, first, SaveLearningsQueue(file, q)
}

// DoneLearning removes from the queue's pending every learning whose key
// starts with ref (at least 6 characters, so a short title prefix is never
// read as a key) or, when none does, whose title starts with ref (case
// folded); the seen set keeps them. It returns how many it removed. An empty
// ref removes nothing.
func DoneLearning(file, ref string) (int, error) {
	return doneLearnings(file, func(q LearningsQueue) []string {
		if ref == "" {
			return nil
		}
		var byKey, byTitle []string
		for k, l := range q.Pending {
			if len(ref) >= 6 && strings.HasPrefix(k, strings.ToLower(ref)) {
				byKey = append(byKey, k)
			} else if strings.HasPrefix(strings.ToLower(l.Title), strings.ToLower(ref)) {
				byTitle = append(byTitle, k)
			}
		}
		if len(byKey) > 0 {
			return byKey
		}
		return byTitle
	})
}

// DoneAllLearnings empties the queue's pending; it returns how many it removed.
func DoneAllLearnings(file string) (int, error) {
	return doneLearnings(file, func(q LearningsQueue) []string { return slices.Collect(maps.Keys(q.Pending)) })
}

// doneLearnings removes the keys pick chooses from pending and saves the
// queue when any were removed.
func doneLearnings(file string, pick func(LearningsQueue) []string) (int, error) {
	q, err := LoadLearningsQueue(file)
	if err != nil {
		return 0, err
	}
	keys := pick(q)
	if len(keys) == 0 {
		return 0, nil
	}
	for _, k := range keys {
		delete(q.Pending, k)
	}
	return len(keys), SaveLearningsQueue(file, q)
}
