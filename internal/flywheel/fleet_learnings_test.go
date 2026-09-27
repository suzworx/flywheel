package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// learnEvent is a learning event titled title recorded age before now.
func learnEvent(now time.Time, age time.Duration, title string) Event {
	return Event{TS: now.Add(-age).Format(time.RFC3339Nano), Task: "t-" + title, Kind: "learning",
		Severity: "P2", Title: title, Observed: "saw " + title, Evidence: "e", Ask: "a"}
}

// writeLedger writes events as dir's .flywheel/events.jsonl.
func writeLedger(t *testing.T, dir string, events ...Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range events {
		line, _ := json.Marshal(e)
		b.Write(append(line, '\n'))
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// learnFleet is root "a" holding old (10 days) and root (1h), and its unit
// worktree u1 holding a copy of both plus fresh (10m) after the fork.
func learnFleet(t *testing.T, now time.Time) (Fleet, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "a")
	inherited := []Event{learnEvent(now, 240*time.Hour, "old"), learnEvent(now, time.Hour, "root")}
	writeLedger(t, root, inherited...)
	writeLedger(t, filepath.Join(root, ".flywheel", "worktrees", "u1"), append(inherited, learnEvent(now, 10*time.Minute, "fresh"))...)
	return Fleet{Roots: []FleetRoot{{Name: "a", Path: root}}}, root
}

func titles(ls []FleetLearning) string {
	var s []string
	for _, l := range ls {
		s = append(s, l.Title+"@"+l.Ledger)
	}
	return strings.Join(s, " ")
}

// TestFleetLearningsDedupe: learnings a worktree copied from its root count
// once, first seen in the root; the worktree's own learning after the fork
// appears; a word-for-word learning in a second root counts once, earliest
// wins; the key is stable.
func TestFleetLearningsDedupe(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	f, _ := learnFleet(t, now)
	b := filepath.Join(t.TempDir(), "b")
	writeLedger(t, b, learnEvent(now, 30*time.Minute, "root"), learnEvent(now, 5*time.Minute, "own"))
	f.Roots = append(f.Roots, FleetRoot{Name: "b", Path: b})
	got := FleetLearnings(f, now)
	if want := "old@a root@a fresh@a/u1 own@b"; titles(got) != want {
		t.Fatalf("FleetLearnings = %q, want %q", titles(got), want)
	}
	if got[1].Key != LearningKey("root", "saw root") || len(got[1].Key) != 64 || got[1].Root != "a" || got[1].Task != "t-root" {
		t.Errorf("root learning = %+v, want the stable sha256 key of title+observed", got[1])
	}
}

// TestFleetLearningsSync: the first sync of an empty queue marks all seen
// and makes pending only the learnings newer than 7 days, and says so;
// repeated syncs add nothing; a learning appended in the worktree later is
// added exactly once; the queue survives a reload.
func TestFleetLearningsSync(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	f, root := learnFleet(t, now)
	queue := LearningsQueuePath(filepath.Join(t.TempDir(), "fleet.json"))
	newly, first, err := SyncLearnings(queue, f, now)
	if err != nil || !first || titles(newly) != "root@a fresh@a/u1" {
		t.Fatalf("first sync = %q, first %v, err %v; want root and fresh (old is history)", titles(newly), first, err)
	}
	for range 2 {
		if newly, first, err := SyncLearnings(queue, f, now); err != nil || first || len(newly) != 0 {
			t.Fatalf("repeat sync = %q, first %v, err %v; want nothing new", titles(newly), first, err)
		}
	}
	u1 := filepath.Join(root, ".flywheel", "worktrees", "u1")
	if err := AppendEvent(u1, learnEvent(now, 0, "later")); err != nil {
		t.Fatal(err)
	}
	if newly, _, err := SyncLearnings(queue, f, now); err != nil || titles(newly) != "later@a/u1" {
		t.Fatalf("sync after append = %q, err %v; want later once", titles(newly), err)
	}
	if newly, _, _ := SyncLearnings(queue, f, now); len(newly) != 0 {
		t.Errorf("sync again = %q, want nothing", titles(newly))
	}
	q, err := LoadLearningsQueue(queue)
	if err != nil || len(q.Seen) != 4 || len(q.Pending) != 3 || q.Version != LearningsQueueVersion {
		t.Errorf("reloaded queue: seen %d pending %d version %d err %v; want 4, 3, %d", len(q.Seen), len(q.Pending), q.Version, err, LearningsQueueVersion)
	}
}

// TestFleetLearningsSinceFirstSync: a first sync with a 30m window makes only
// the learning newer than that pending and still marks every learning seen.
func TestFleetLearningsSinceFirstSync(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	f, _ := learnFleet(t, now)
	queue := filepath.Join(t.TempDir(), "fleet-learnings.json")
	newly, first, err := SyncLearningsSince(queue, f, now, 30*time.Minute)
	if err != nil || !first || titles(newly) != "fresh@a/u1" {
		t.Fatalf("first sync since 30m = %q, first %v, err %v; want only fresh", titles(newly), first, err)
	}
	if q, _ := LoadLearningsQueue(queue); len(q.Seen) != 3 || len(q.Pending) != 1 {
		t.Errorf("queue seen %d pending %d; want 3, 1", len(q.Seen), len(q.Pending))
	}
}

// TestFleetLearningsImportSeen: importing titles, as strings or {title}
// objects, into an empty queue marks the matching learnings seen, removes
// them from pending and reports how many matched; an unknown title matches
// nothing, a later sync brings none back, and a file of the wrong shape is
// an error.
func TestFleetLearningsImportSeen(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	f, _ := learnFleet(t, now)
	queue := filepath.Join(t.TempDir(), "fleet-learnings.json")
	n, err := ImportSeenLearnings(queue, f, now, 0, []byte(`[{"title":"root","seen_at":"x"},"old","nope"]`))
	if err != nil || n != 2 {
		t.Fatalf("import = %d, %v; want 2 matched (root, old)", n, err)
	}
	q, _ := LoadLearningsQueue(queue)
	if len(q.Seen) != 3 || len(q.Pending) != 1 || q.Pending[LearningKey("fresh", "saw fresh")].Title != "fresh" {
		t.Errorf("queue seen %d pending %v; want 3 seen, only fresh pending", len(q.Seen), q.Pending)
	}
	if newly, first, _ := SyncLearnings(queue, f, now); first || len(newly) != 0 {
		t.Errorf("sync after import = %q, first %v; want nothing new", titles(newly), first)
	}
	if n, err := ImportSeenLearnings(queue, f, now, 0, []byte(`["fresh"]`)); err != nil || n != 1 {
		t.Errorf("import fresh = %d, %v; want 1", n, err)
	}
	if q, _ := LoadLearningsQueue(queue); len(q.Pending) != 0 {
		t.Errorf("pending after importing fresh = %v, want empty", q.Pending)
	}
	for _, bad := range []string{`{"title":"root"}`, `[42]`} {
		if _, err := ImportSeenLearnings(queue, f, now, 0, []byte(bad)); err == nil {
			t.Errorf("import %s succeeded, want an error", bad)
		}
	}
}

// TestFleetLearningsDismissedDone: a learning dismissed in its worktree (by
// that ledger's L-NN id) is dismissed fleet-wide and never pending, only
// seen; --done drains by key prefix and by title prefix, never from seen, so
// a later sync does not bring it back; done-all empties pending.
func TestFleetLearningsDismissedDone(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	f, root := learnFleet(t, now)
	queue := filepath.Join(t.TempDir(), "fleet-learnings.json")
	if err := SaveLearningsQueue(queue, LearningsQueue{Seen: []string{"prior"}}); err != nil {
		t.Fatal(err)
	}
	u1 := filepath.Join(root, ".flywheel", "worktrees", "u1")
	if err := AppendEvent(u1, Event{Task: "t-root", Kind: "dismissed", ID: "L-02", Note: "handled"}); err != nil {
		t.Fatal(err)
	}
	newly, first, err := SyncLearnings(queue, f, now)
	if err != nil || first || titles(newly) != "old@a fresh@a/u1" {
		t.Fatalf("sync = %q, first %v, err %v; want old and fresh (root dismissed)", titles(newly), first, err)
	}
	if q, _ := LoadLearningsQueue(queue); len(q.Seen) != 4 || q.Pending[LearningKey("root", "saw root")].Title != "" {
		t.Errorf("queue seen %v pending %v; want the dismissed key seen, not pending", q.Seen, q.Pending)
	}
	if n, err := DoneLearning(queue, LearningKey("fresh", "saw fresh")[:8]); n != 1 || err != nil {
		t.Errorf("done by key prefix = %d, %v; want 1", n, err)
	}
	if n, err := DoneLearning(queue, "OL"); n != 1 || err != nil {
		t.Errorf("done by title prefix = %d, %v; want 1", n, err)
	}
	if n, _ := DoneLearning(queue, ""); n != 0 {
		t.Errorf("done with an empty ref removed %d", n)
	}
	if newly, _, _ := SyncLearnings(queue, f, now); len(newly) != 0 {
		t.Errorf("sync after done = %q, want nothing back", titles(newly))
	}
	if err := AppendEvent(u1, learnEvent(now, 0, "next")); err != nil {
		t.Fatal(err)
	}
	SyncLearnings(queue, f, now)
	if n, err := DoneAllLearnings(queue); n != 1 || err != nil {
		t.Errorf("done-all = %d, %v; want 1", n, err)
	}
}

// TestFleetLearningsQueueAtomic: a crash mid-write leaves a temp file beside
// the queue, which load ignores; a queue that does not parse is an error,
// never an empty queue that would re-flood pending.
func TestFleetLearningsQueueAtomic(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	f, _ := learnFleet(t, now)
	dir := t.TempDir()
	queue := filepath.Join(dir, "fleet-learnings.json")
	if _, _, err := SyncLearnings(queue, f, now); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fleet-learnings-123.json"), []byte(`{"version":1,"seen":[`), 0o644); err != nil {
		t.Fatal(err)
	}
	if q, err := LoadLearningsQueue(queue); err != nil || len(q.Seen) != 3 || len(q.Pending) != 2 {
		t.Errorf("load beside a torn temp file: seen %d pending %d err %v; want 3, 2", len(q.Seen), len(q.Pending), err)
	}
	if err := os.WriteFile(queue, []byte(`{"version":1,"seen":[`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SyncLearnings(queue, f, now); err == nil {
		t.Error("sync over a torn queue succeeded, want a parse error")
	}
}
