package flywheel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnableShardsSealsLegacy(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	// Initialize and add 3 legacy events
	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	for i := 1; i <= 3; i++ {
		err := AppendEvent(dir, Event{
			Task: "T1",
			Kind: "planned",
			Note: fmt.Sprintf("event %d", i),
		})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	// Enable shards
	sealed, legacyLines, err := EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}
	if !sealed {
		t.Fatal("expected sealed=true on first migration")
	}
	if legacyLines != 3 {
		t.Fatalf("expected 3 legacy lines, got %d", legacyLines)
	}

	// Verify events/ exists with @floor.jsonl
	eventsDir := filepath.Join(dir, ".flywheel", "events")
	if info, err := os.Stat(eventsDir); err != nil || !info.IsDir() {
		t.Fatal("events/ directory not created")
	}

	floorPath := filepath.Join(eventsDir, "@floor.jsonl")
	floorData, err := os.ReadFile(floorPath)
	if err != nil {
		t.Fatalf("read @floor.jsonl: %v", err)
	}

	// Parse the seal event
	lines := strings.Split(strings.TrimSpace(string(floorData)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line in @floor.jsonl, got %d", len(lines))
	}

	var seal Event
	if err := json.Unmarshal([]byte(lines[0]), &seal); err != nil {
		t.Fatalf("parse seal: %v", err)
	}

	if seal.Kind != "sharded" {
		t.Fatalf("expected kind=sharded, got %q", seal.Kind)
	}
	if seal.Path != ".flywheel/events.jsonl" {
		t.Fatalf("expected path=.flywheel/events.jsonl, got %q", seal.Path)
	}
	if seal.Prev != shardGenesis {
		t.Fatalf("expected prev=shardGenesis, got %q", seal.Prev)
	}
	if seal.SHA256 == "" {
		t.Fatal("expected non-empty SHA256")
	}

	// Verify legacy file is unchanged
	legacyPath := filepath.Join(dir, ".flywheel", "events.jsonl")
	legacyData, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	legacyLineCount := countCompleteLines(legacyData)
	if legacyLineCount != 3 {
		t.Fatalf("expected 3 legacy lines, got %d", legacyLineCount)
	}

	// Second EnableShards should not reseal
	sealed2, legacyLines2, err := EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards second: %v", err)
	}
	if sealed2 {
		t.Fatal("expected sealed=false on idempotent call")
	}
	if legacyLines2 != 3 {
		t.Fatalf("expected 3 legacy lines on second call, got %d", legacyLines2)
	}
}

func TestEnableShardsEmptyLegacy(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sealed, legacyLines, err := EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}
	if !sealed {
		t.Fatal("expected sealed=true")
	}
	if legacyLines != 0 {
		t.Fatalf("expected 0 legacy lines, got %d", legacyLines)
	}

	floorPath := filepath.Join(dir, ".flywheel", "events", "@floor.jsonl")
	floorData, err := os.ReadFile(floorPath)
	if err != nil {
		t.Fatalf("read @floor.jsonl: %v", err)
	}

	var seal Event
	if err := json.Unmarshal(bytes.TrimSpace(floorData), &seal); err != nil {
		t.Fatalf("parse seal: %v", err)
	}
	if seal.SHA256 != "" {
		t.Fatalf("expected empty SHA256 for empty legacy, got %q", seal.SHA256)
	}
}

func TestEnableShardsSealTimeAfterLegacy(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	legacyTime := fixedTime.Add(1 * time.Hour)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Append event with explicit TS one hour after now
	err = AppendEvent(dir, Event{
		Task: "T1",
		Kind: "planned",
		TS:   legacyTime.Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	sealed, _, err := EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}
	if !sealed {
		t.Fatal("expected sealed=true")
	}

	floorPath := filepath.Join(dir, ".flywheel", "events", "@floor.jsonl")
	floorData, err := os.ReadFile(floorPath)
	if err != nil {
		t.Fatalf("read @floor.jsonl: %v", err)
	}

	var seal Event
	if err := json.Unmarshal(bytes.TrimSpace(floorData), &seal); err != nil {
		t.Fatalf("parse seal: %v", err)
	}

	sealTime, err := time.Parse(time.RFC3339Nano, seal.TS)
	if err != nil {
		t.Fatalf("parse seal TS: %v", err)
	}

	expectedTime := legacyTime.Add(time.Nanosecond)
	if !sealTime.Equal(expectedTime) {
		t.Fatalf("expected seal TS=%v, got %v", expectedTime, sealTime)
	}
}

func TestEnableShardsResealsAfterLegacyGrowth(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// First enable shards
	sealed, _, err := EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards first: %v", err)
	}
	if !sealed {
		t.Fatal("expected sealed=true on first migration")
	}

	// Manually append a line to legacy file (simulating a binary without shard support)
	legacyPath := filepath.Join(dir, ".flywheel", "events.jsonl")
	f, err := os.OpenFile(legacyPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	e := Event{Task: "T1", Kind: "planned", Note: "appended manually"}
	b, _ := marshalEvent(e)
	f.Write(append(b, '\n'))
	f.Close()

	// Verify should break
	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}
	if chain.OK() {
		t.Fatal("expected chain to break after legacy growth")
	}
	if !strings.Contains(chain.BreakReason, "appended after it was sealed") {
		t.Fatalf("expected break reason about appending, got %q", chain.BreakReason)
	}

	// Enable shards again
	sealed2, _, err := EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards second: %v", err)
	}
	if !sealed2 {
		t.Fatal("expected sealed=true on reseal")
	}

	// Verify should now pass
	chain2, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain after reseal: %v", err)
	}
	if !chain2.OK() {
		t.Fatalf("expected chain to be OK after reseal, break reason: %q", chain2.BreakReason)
	}
}

func TestLegacyAppendDivertsAfterSeal(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	sealed, _, err := EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}
	if !sealed {
		t.Fatal("expected sealed=true")
	}

	// Append event with planned T9 (should go to events/T9.jsonl)
	err = AppendEvent(dir, Event{
		Task: "T9",
		Kind: "planned",
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// events.jsonl should be unchanged
	legacyPath := filepath.Join(dir, ".flywheel", "events.jsonl")
	legacyData1, _ := os.ReadFile(legacyPath)
	beforeAppend := countCompleteLines(legacyData1)

	// Append another event to T9
	err = AppendEvent(dir, Event{
		Task: "T9",
		Kind: "dispatched",
	})
	if err != nil {
		t.Fatalf("AppendEvent second: %v", err)
	}

	legacyData2, _ := os.ReadFile(legacyPath)
	afterAppend := countCompleteLines(legacyData2)

	if beforeAppend != afterAppend {
		t.Fatalf("legacy file changed after append: before=%d, after=%d", beforeAppend, afterAppend)
	}

	// Verify T9 shard exists with 2 lines
	t9Path := filepath.Join(dir, ".flywheel", "events", "T9.jsonl")
	t9Data, err := os.ReadFile(t9Path)
	if err != nil {
		t.Fatalf("read T9.jsonl: %v", err)
	}
	t9Lines := countCompleteLines(t9Data)
	if t9Lines != 2 {
		t.Fatalf("expected 2 lines in T9.jsonl, got %d", t9Lines)
	}
}

func TestVerifyLogChainShardedIntact(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, _, err = EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}

	// Append events for T1, T2, and a staffed event
	err = AppendEvent(dir, Event{Task: "T1", Kind: "planned"})
	if err != nil {
		t.Fatalf("AppendEvent T1: %v", err)
	}
	err = AppendEvent(dir, Event{Task: "T2", Kind: "planned"})
	if err != nil {
		t.Fatalf("AppendEvent T2: %v", err)
	}
	err = AppendEvent(dir, Event{Kind: "staffed", Session: "s1"})
	if err != nil {
		t.Fatalf("AppendEvent staffed: %v", err)
	}

	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}

	if !chain.OK() {
		t.Fatalf("expected chain OK, break reason: %q", chain.BreakReason)
	}
	if chain.Files != 4 {
		t.Fatalf("expected 4 files (legacy + @floor + T1 + T2), got %d", chain.Files)
	}
	if chain.Chained == 0 {
		t.Fatal("expected chained > 0")
	}
}

func TestVerifyLogChainShardedShardEditedLineBreaks(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, _, err = EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}

	// Append 2 events to T1
	err = AppendEvent(dir, Event{Task: "T1", Kind: "planned"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	err = AppendEvent(dir, Event{Task: "T1", Kind: "dispatched"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Edit a byte in the first line
	t1Path := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	data, _ := os.ReadFile(t1Path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 1 {
		t.Fatal("no lines in T1.jsonl")
	}

	// Flip one byte in the first line
	firstLine := lines[0]
	b := []byte(firstLine)
	if len(b) > 10 {
		b[10] ^= 0xFF
	}
	lines[0] = string(b)

	// Rewrite the file
	newData := strings.Join(lines, "\n") + "\n"
	os.WriteFile(t1Path, []byte(newData), 0o644)

	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}

	if chain.OK() {
		t.Fatal("expected chain to break after edit")
	}
	if chain.File != "events/T1.jsonl" {
		t.Fatalf("expected File=events/T1.jsonl, got %q", chain.File)
	}
	if chain.BreakLine != 2 {
		t.Fatalf("expected BreakLine=2, got %d", chain.BreakLine)
	}
}

func TestVerifyLogChainShardedLineWithoutPrevBreaks(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, _, err = EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}

	// Append one event to create the shard
	err = AppendEvent(dir, Event{Task: "T1", Kind: "planned"})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	// Manually append a line without prev
	t1Path := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	f, err := os.OpenFile(t1Path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open T1.jsonl: %v", err)
	}
	// Write a line without prev
	e := Event{Task: "T1", Kind: "planned", Note: "no prev"}
	e.Prev = ""
	b, _ := marshalEvent(e)
	f.Write(append(b, '\n'))
	f.Close()

	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}

	if chain.OK() {
		t.Fatal("expected chain to break")
	}
	if !strings.Contains(chain.BreakReason, "line has no prev") {
		t.Fatalf("expected break reason about no prev, got %q", chain.BreakReason)
	}
}

func TestVerifyLogChainShardedWrongGenesisBreaks(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	_, _, err = EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}

	// Create a shard file with wrong genesis
	t1Path := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	e := Event{Task: "T1", Kind: "planned", Prev: "abc123"}
	b, _ := marshalEvent(e)
	os.WriteFile(t1Path, append(b, '\n'), 0o644)

	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}

	if chain.OK() {
		t.Fatal("expected chain to break")
	}
	if !strings.Contains(chain.BreakReason, "prev matches no earlier line") {
		t.Fatalf("expected break reason about prev, got %q", chain.BreakReason)
	}
}

func TestVerifyLogChainShardedLegacyTruncatedBreaks(t *testing.T) {
	dir := t.TempDir()
	fixedTime := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Add multiple events to legacy before sealing
	err = AppendEvent(dir, Event{Task: "T1", Kind: "planned"})
	if err != nil {
		t.Fatalf("AppendEvent 1: %v", err)
	}
	err = AppendEvent(dir, Event{Task: "T1", Kind: "dispatched"})
	if err != nil {
		t.Fatalf("AppendEvent 2: %v", err)
	}

	_, _, err = EnableShards(dir, fixedTime)
	if err != nil {
		t.Fatalf("EnableShards: %v", err)
	}

	// Truncate legacy file to its first line
	legacyPath := filepath.Join(dir, ".flywheel", "events.jsonl")
	data, _ := os.ReadFile(legacyPath)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 0 {
		os.WriteFile(legacyPath, []byte(lines[0]+"\n"), 0o644)
	}

	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}

	if chain.OK() {
		t.Fatal("expected chain to break after truncation")
	}
	if !strings.Contains(chain.BreakReason, "edited or truncated") {
		t.Fatalf("expected break reason about truncation, got %q", chain.BreakReason)
	}
}

func TestVerifyLogChainShardedWithoutSealBreaks(t *testing.T) {
	dir := t.TempDir()

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Create events/ manually (no seal)
	eventsDir := filepath.Join(dir, ".flywheel", "events")
	os.MkdirAll(eventsDir, 0o755)

	// Create a valid shard file without a seal
	t1Path := filepath.Join(eventsDir, "T1.jsonl")
	e := Event{Task: "T1", Kind: "planned", Prev: shardGenesis}
	b, _ := marshalEvent(e)
	os.WriteFile(t1Path, append(b, '\n'), 0o644)

	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}

	if chain.OK() {
		t.Fatal("expected chain to break without seal")
	}
	if !strings.Contains(chain.BreakReason, "no seal") {
		t.Fatalf("expected break reason about seal, got %q", chain.BreakReason)
	}
}

func TestVerifyLogChainLegacyUnchanged(t *testing.T) {
	dir := t.TempDir()

	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Add some events
	for i := 0; i < 3; i++ {
		err = AppendEvent(dir, Event{Task: "T1", Kind: "planned"})
		if err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	chain1, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}

	// In legacy layout, Files should be 0
	if chain1.Files != 0 {
		t.Fatalf("expected Files=0 in legacy layout, got %d", chain1.Files)
	}
	if chain1.File != "" {
		t.Fatalf("expected File=empty in legacy layout, got %q", chain1.File)
	}
	if chain1.Lines == 0 {
		t.Fatal("expected Lines > 0")
	}
	if chain1.Chained == 0 {
		t.Fatal("expected Chained > 0")
	}

	// Call again to verify consistency
	chain2, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain second: %v", err)
	}

	if chain1.Lines != chain2.Lines {
		t.Fatalf("Lines changed: %d -> %d", chain1.Lines, chain2.Lines)
	}
	if chain1.Chained != chain2.Chained {
		t.Fatalf("Chained changed: %d -> %d", chain1.Chained, chain2.Chained)
	}
}

// TestEnableShardsSealsHandMadeLayout checks that a sharded layout created
// without a seal is repaired rather than refused: log --shard is the only
// command that writes a seal, and verification requires one.
func TestEnableShardsSealsHandMadeLayout(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:01Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "events"), 0o755); err != nil {
		t.Fatalf("mkdir events: %v", err)
	}
	if chain, err := VerifyLogChain(dir); err != nil || chain.OK() {
		t.Fatalf("VerifyLogChain() = %+v, %v; want a break for the missing seal", chain, err)
	}
	sealed, lines, err := EnableShards(dir, time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC))
	if err != nil || !sealed || lines != 1 {
		t.Fatalf("EnableShards() = %v, %d, %v; want true, 1, nil", sealed, lines, err)
	}
	chain, err := VerifyLogChain(dir)
	if err != nil || !chain.OK() {
		t.Errorf("VerifyLogChain() after sealing = %+v, %v; want intact", chain, err)
	}
}

// TestEnableShardsSealTimeUsesLatestLegacyTS checks that the seal sorts
// after EVERY legacy event, not only the last line's.
func TestEnableShardsSealTimeUsesLatestLegacyTS(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	now := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	ahead := now.Add(2 * time.Hour)
	for _, e := range []Event{
		{TS: ahead.Format(time.RFC3339Nano), Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: now.Add(-time.Hour).Format(time.RFC3339Nano), Task: "T1", Kind: "started", Attempt: "r1", Session: "s1"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	if _, _, err := EnableShards(dir, now); err != nil {
		t.Fatalf("EnableShards() error = %v", err)
	}
	seal, err := lastSealEvent(filepath.Join(dir, ".flywheel", "events", floorShard))
	if err != nil || seal == nil {
		t.Fatalf("lastSealEvent() = %v, %v", seal, err)
	}
	got, err := time.Parse(time.RFC3339Nano, seal.TS)
	if err != nil {
		t.Fatalf("seal ts %q: %v", seal.TS, err)
	}
	if want := ahead.Add(time.Nanosecond); !got.Equal(want) {
		t.Errorf("seal ts = %v, want %v (after the latest legacy event, which is not the last line)", got, want)
	}
}
