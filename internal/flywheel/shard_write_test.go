package flywheel

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLegacyAppendUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	e := Event{Task: "t1", Kind: "planned"}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	// Legacy repo: no events directory should be created
	eventsDir := filepath.Join(dir, ".flywheel", "events")
	if _, err := os.Stat(eventsDir); err == nil {
		t.Errorf("events directory should not exist in legacy layout")
	}
	// Verify event was written to events.jsonl
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 1 || events[0].Task != "t1" {
		t.Errorf("expected one event in events.jsonl, got %d", len(events))
	}
}

func TestAppendShardedRoutesByTaskFloorSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Task-specific event: should go to T1.jsonl
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent T1: %v", err)
	}

	// Floor-level event: should go to @floor.jsonl
	if err := AppendEvent(dir, Event{Kind: "staffed", Session: "s1"}); err != nil {
		t.Fatalf("AppendEvent staffed: %v", err)
	}

	// Session event: should go to @session-s1.jsonl
	if err := AppendEvent(dir, Event{Kind: "session_start", Session: "s1"}); err != nil {
		t.Fatalf("AppendEvent session_start: %v", err)
	}

	// Verify files exist
	t1Shard := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	floorShard := filepath.Join(dir, ".flywheel", "events", "@floor.jsonl")
	sessionShard := filepath.Join(dir, ".flywheel", "events", "@session-s1.jsonl")

	if _, err := os.Stat(t1Shard); err != nil {
		t.Errorf("T1.jsonl not found: %v", err)
	}
	if _, err := os.Stat(floorShard); err != nil {
		t.Errorf("@floor.jsonl not found: %v", err)
	}
	if _, err := os.Stat(sessionShard); err != nil {
		t.Errorf("@session-s1.jsonl not found: %v", err)
	}
}

func TestAppendShardedFirstLineCarriesGenesis(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Append two events to same shard
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent 1: %v", err)
	}
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched"}); err != nil {
		t.Fatalf("AppendEvent 2: %v", err)
	}

	// Read events and check prevs
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}
	if events[0].Prev != shardGenesis {
		t.Errorf("first event prev should be shardGenesis, got %q", events[0].Prev)
	}
	expectedPrev := lineHash([]byte(mustMarshalEvent(events[0])))
	if events[1].Prev != expectedPrev {
		t.Errorf("second event prev should be hash of first, got %q", events[1].Prev)
	}
}

func TestAppendShardedRefusesCrossShardBatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Try to append batch with two different tasks (cross-shard)
	err := AppendEvents(dir, []Event{
		{Task: "T1", Kind: "planned"},
		{Task: "T2", Kind: "planned"},
	})
	if err == nil {
		t.Errorf("expected error for cross-shard batch, got none")
	}

	// Verify no files were written
	t1Shard := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	if _, err := os.Stat(t1Shard); err == nil {
		t.Errorf("T1.jsonl should not be created on cross-shard batch error")
	}
}

func TestAppendRefusesShardedKind(t *testing.T) {
	t.Parallel()
	// Test in both legacy and sharded layouts
	for _, setupSharded := range []bool{false, true} {
		dir := t.TempDir()
		if _, err := Init(dir, false); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if setupSharded {
			if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
		}

		err := AppendEvent(dir, Event{Kind: "sharded", Path: ".flywheel/events.jsonl"})
		if err == nil {
			t.Errorf("sharded kind should be refused by AppendEvent (sharded=%v)", setupSharded)
		}
	}
}

func TestNextStampTable(t *testing.T) {
	t.Parallel()
	now := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		shardLst time.Time
		observed time.Time
		want     time.Time
	}{
		{
			name:     "zero observed and last",
			shardLst: time.Time{},
			observed: time.Time{},
			want:     now,
		},
		{
			name:     "last in past",
			shardLst: now.Add(-1 * time.Hour),
			observed: time.Time{},
			want:     now,
		},
		{
			name:     "last ahead within skew",
			shardLst: now.Add(30 * time.Second),
			observed: time.Time{},
			want:     now.Add(30*time.Second + time.Nanosecond),
		},
		{
			name:     "last beyond skew",
			shardLst: now.Add(2 * time.Minute),
			observed: time.Time{},
			want:     now,
		},
		{
			// Review: an observed time beyond the skew must not discard the
			// shard's own sane last stamp (stamps would go backwards).
			name:     "last within skew kept when observed beyond skew",
			shardLst: now.Add(30 * time.Second),
			observed: now.Add(5 * time.Minute),
			want:     now.Add(30*time.Second + time.Nanosecond),
		},
		{
			name:     "observed ahead within skew wins",
			shardLst: now.Add(30 * time.Second),
			observed: now.Add(50 * time.Second),
			want:     now.Add(50*time.Second + time.Nanosecond),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextStamp(now, tt.shardLst, tt.observed)
			if !got.Equal(tt.want) {
				t.Errorf("nextStamp got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAppendShardedMonotonicPerShard(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Append 20 events to same shard
	for i := 0; i < 20; i++ {
		if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned"}); err != nil {
			t.Fatalf("AppendEvent %d: %v", i, err)
		}
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Verify timestamps are strictly increasing
	for i := 1; i < len(events); i++ {
		ts1, _ := time.Parse(time.RFC3339Nano, events[i-1].TS)
		ts2, _ := time.Parse(time.RFC3339Nano, events[i].TS)
		if !ts2.After(ts1) {
			t.Errorf("event %d TS not strictly after event %d", i, i-1)
		}
	}
}

func TestAppendShardedBatchSharesOneInstant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Append 3-event batch without TS
	if err := AppendEvents(dir, []Event{
		{Task: "T1", Kind: "planned"},
		{Task: "T1", Kind: "worker_plan"},
		{Task: "T1", Kind: "dispatched"},
	}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// All should have same TS
	if events[0].TS != events[1].TS || events[1].TS != events[2].TS {
		t.Errorf("batch events should share same TS: %q, %q, %q", events[0].TS, events[1].TS, events[2].TS)
	}
}

func TestAppendShardedExplicitTSUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	explicitTS := "2020-01-01T00:00:00Z"
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned", TS: explicitTS}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].TS != explicitTS {
		t.Errorf("explicit TS should be kept: got %q, want %q", events[0].TS, explicitTS)
	}
}

func TestAppendShardedCausalStampAfterRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	now := time.Now().UTC()
	futureTS := now.Add(30 * time.Second).Format(time.RFC3339Nano)

	// Append to T2 with explicit TS 30s ahead
	if err := AppendEvent(dir, Event{Task: "T2", Kind: "planned", TS: futureTS}); err != nil {
		t.Fatalf("AppendEvent T2: %v", err)
	}

	// Read to raise logical clock
	if _, err := ReadEvents(dir); err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Append to T1 without TS
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent T1: %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Find the T1 event
	var t1Event Event
	for _, e := range events {
		if e.Task == "T1" {
			t1Event = e
			break
		}
	}

	t2TS, _ := time.Parse(time.RFC3339Nano, futureTS)
	t1TS, _ := time.Parse(time.RFC3339Nano, t1Event.TS)
	if !t1TS.After(t2TS) {
		t.Errorf("T1 TS should be after 30s future TS: got %q, want after %q", t1Event.TS, futureTS)
	}
}

func TestAppendShardedConcurrent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				task := []string{"T1", "T2", "T3", "T4"}[goroutineID%4]
				if err := AppendEvent(dir, Event{Task: task, Kind: "planned"}); err != nil {
					t.Errorf("AppendEvent: %v", err)
				}
			}
		}(g)
	}
	wg.Wait()

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Verify total count: 8 goroutines × 25 appends = 200
	if len(events) != 200 {
		t.Errorf("expected 200 events, got %d", len(events))
	}

	// Verify per-shard timestamp monotonicity
	shardTS := make(map[string]time.Time)
	for _, e := range events {
		ts, _ := time.Parse(time.RFC3339Nano, e.TS)
		if prev, ok := shardTS[e.Task]; ok {
			if !ts.After(prev) {
				t.Errorf("shard %q not monotonic: %v not after %v", e.Task, ts, prev)
			}
		}
		shardTS[e.Task] = ts
	}

	// Verify chain integrity per shard
	shardLines := make(map[string][][]byte)
	shardPath := filepath.Join(dir, ".flywheel", "events")
	entries, err := os.ReadDir(shardPath)
	if err != nil {
		t.Fatalf("ReadDir events: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(shardPath, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		lines := bytes.Split(data, []byte("\n"))
		for _, line := range lines {
			if len(line) > 0 {
				shardLines[entry.Name()] = append(shardLines[entry.Name()], line)
			}
		}
	}
	for shard, lines := range shardLines {
		var prev string
		for _, line := range lines {
			var e Event
			if err := json.Unmarshal(line, &e); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if prev == "" {
				if e.Prev != shardGenesis {
					t.Errorf("shard %q first line prev not genesis", shard)
				}
			} else if e.Prev != prev {
				t.Errorf("shard %q chain broken", shard)
			}
			prev = lineHash(line)
		}
	}
}

func TestValidateSharded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		event   Event
		wantErr bool
	}{
		{
			name:    "sharded without path",
			event:   Event{Kind: "sharded"},
			wantErr: true,
		},
		{
			name:    "sharded with wrong path",
			event:   Event{Kind: "sharded", Path: ".flywheel/events/T1.jsonl"},
			wantErr: true,
		},
		{
			name:    "sharded with correct path",
			event:   Event{Kind: "sharded", Path: ".flywheel/events.jsonl"},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.event)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate got err=%v, wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestAppendLearningEventsSortsAfterSignal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	now := time.Now().UTC()
	futureTS := now.Add(20 * time.Second).Format(time.RFC3339Nano)

	// Append signal with explicit future TS
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "signal", Signal: "stalled", TS: futureTS}); err != nil {
		t.Fatalf("AppendEvent signal: %v", err)
	}

	// Append learning (via AppendLearningEvents to update logical clock)
	if err := AppendLearningEvents(dir, []Event{
		{Task: "T1", Kind: "learning", Severity: "P1", Title: "test", Observed: "obs", Evidence: "ev", Ask: "ask", Signals: []string{"stalled"}},
	}); err != nil {
		t.Fatalf("AppendLearningEvents: %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Find signal and learning
	var sigIdx, learnIdx int
	for i, e := range events {
		if e.Kind == "signal" {
			sigIdx = i
		}
		if e.Kind == "learning" {
			learnIdx = i
		}
	}

	if sigIdx >= learnIdx {
		t.Errorf("signal should come before learning in log order")
	}

	// Check that signal is now triaged
	untriaged := UntriagedSignals(events)
	for _, sig := range untriaged {
		if sig.Task == "T1" && sig.Signal == "stalled" {
			t.Errorf("stalled signal on T1 should be triaged after learning")
		}
	}
}

func mustMarshalEvent(e Event) string {
	b, err := marshalEvent(e)
	if err != nil {
		panic(err)
	}
	return string(b)
}
