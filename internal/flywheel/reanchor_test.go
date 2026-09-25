package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reanchorLog appends one note event per text to dir's legacy log and returns
// the log's lines.
func reanchorLog(t *testing.T, dir string, notes ...string) []string {
	t.Helper()
	for _, n := range notes {
		if err := AppendEvent(dir, Event{Kind: "note", Note: n}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	return readLines(t, filepath.Join(dir, ".flywheel", "events.jsonl"))
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustChain(t *testing.T, dir string) LogChain {
	t.Helper()
	c, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("VerifyLogChain: %v", err)
	}
	return c
}

// TestReanchorReordered: a swapped pair breaks as reordered; Reanchor appends
// an acknowledgement; the chain is then OK with one Acknowledged entry, and
// stays OK after a further append.
func TestReanchorReordered(t *testing.T) {
	dir := t.TempDir()
	lines := reanchorLog(t, dir, "one", "two", "three")
	if _, err := Reanchor(dir, "why", "S1", false); !IsRuleRefusal(err) || !strings.Contains(err.Error(), "intact") {
		t.Fatalf("Reanchor on an intact log = %v, want an intact refusal", err)
	}
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	lines[1], lines[2] = lines[2], lines[1]
	writeLines(t, path, lines)
	if c := mustChain(t, dir); c.OK() || !strings.HasPrefix(c.BreakReason, "reordered:") {
		t.Fatalf("chain = %+v, want a reordered break", c)
	}
	ack, err := Reanchor(dir, "a merge reordered one batch", "S1", false)
	if err != nil || ack.Reason != "reordered" || ack.Line != 2 || ack.File != "events.jsonl" {
		t.Fatalf("Reanchor = %+v, %v", ack, err)
	}
	c := mustChain(t, dir)
	if !c.OK() || len(c.Acknowledged) != 1 || c.Acknowledged[0].Session != "S1" {
		t.Fatalf("chain after reanchor = %+v, want OK with one acknowledgement", c)
	}
	if !strings.Contains(c.AckText(), "acknowledged break at events.jsonl line 2 (reordered), by S1: a merge reordered one batch") {
		t.Errorf("AckText = %q", c.AckText())
	}
	reanchorLog(t, dir, "four")
	if c := mustChain(t, dir); !c.OK() || len(c.Acknowledged) != 1 {
		t.Errorf("chain after a further append = %+v, want OK", c)
	}
}

// TestReanchorRemoved: a removed line needs --force and is then acknowledged
// as removed.
func TestReanchorRemoved(t *testing.T) {
	dir := t.TempDir()
	lines := reanchorLog(t, dir, "one", "two", "three")
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	writeLines(t, path, []string{lines[0], lines[2]})
	if _, err := Reanchor(dir, "why", "S1", false); !IsRuleRefusal(err) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Reanchor without force = %v, want a refusal naming --force", err)
	}
	if c := mustChain(t, dir); c.OK() {
		t.Fatal("a refused reanchor acknowledged the break")
	}
	ack, err := Reanchor(dir, "record two was lost in a rebase", "S1", true)
	if err != nil || ack.Reason != "removed" {
		t.Fatalf("Reanchor --force = %+v, %v", ack, err)
	}
	if c := mustChain(t, dir); !c.OK() || len(c.Acknowledged) != 1 || c.Acknowledged[0].Reason != "removed" {
		t.Errorf("chain = %+v, want OK with a removed acknowledgement", c)
	}
}

// TestReanchorDoesNotCoverAnotherBreak: an acknowledgement covers its own
// break only; an edit elsewhere still breaks the chain.
func TestReanchorDoesNotCoverAnotherBreak(t *testing.T) {
	dir := t.TempDir()
	lines := reanchorLog(t, dir, "one", "two", "three", "four", "five")
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	lines[1], lines[2] = lines[2], lines[1]
	writeLines(t, path, lines)
	if _, err := Reanchor(dir, "merge", "S1", false); err != nil {
		t.Fatal(err)
	}
	lines = readLines(t, path)
	lines[3] = strings.Replace(lines[3], `"note":"four"`, `"note":"FOUR"`, 1)
	writeLines(t, path, lines)
	if c := mustChain(t, dir); c.OK() || c.BreakLine != 5 || len(c.Acknowledged) != 1 {
		t.Errorf("chain = %+v, want a break at line 5 past the acknowledged one", c)
	}
}

// TestReanchorReorderedAckDoesNotCoverRemoved: a reordered acknowledgement
// does not cover the same line once it classifies as removed.
func TestReanchorReorderedAckDoesNotCoverRemoved(t *testing.T) {
	dir := t.TempDir()
	lines := reanchorLog(t, dir, "one", "two", "three")
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	lines[1], lines[2] = lines[2], lines[1]
	writeLines(t, path, lines)
	if _, err := Reanchor(dir, "merge", "S1", false); err != nil {
		t.Fatal(err)
	}
	lines = readLines(t, path) // one, three, two, reanchored
	writeLines(t, path, []string{lines[0], lines[1], lines[3]})
	c := mustChain(t, dir)
	if c.OK() || c.BreakLine != 2 || strings.HasPrefix(c.BreakReason, "reordered:") || len(c.Acknowledged) != 0 {
		t.Errorf("chain = %+v, want an unacknowledged removed break at line 2", c)
	}
}

// TestReanchorValidate: a reanchored event must carry note, file, line,
// break_prev, sha256 and a reason of reordered or removed, and no task.
func TestReanchorValidate(t *testing.T) {
	good := Event{Kind: "reanchored", Note: "why", File: "events.jsonl", LineNo: 2, BreakPrev: "ab", SHA256: "cd", Reason: "reordered"}
	if err := Validate(good); err != nil {
		t.Fatalf("Validate(good) = %v", err)
	}
	for name, mut := range map[string]func(*Event){
		"no note":    func(e *Event) { e.Note = "" },
		"no prev":    func(e *Event) { e.BreakPrev = "" },
		"no sha256":  func(e *Event) { e.SHA256 = "" },
		"no file":    func(e *Event) { e.File = "" },
		"no line":    func(e *Event) { e.LineNo = 0 },
		"bad reason": func(e *Event) { e.Reason = "edited" },
		"a task":     func(e *Event) { e.Task = "T1" },
	} {
		e := good
		mut(&e)
		if err := Validate(e); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, e)
		}
	}
	if err := Validate(Event{Kind: "note", Note: "x", BreakPrev: "ab"}); err == nil {
		t.Error("a note event carrying break_prev was accepted")
	}
}

// TestReanchorSharded: an acknowledged break in a shard file is honoured.
func TestReanchorSharded(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, _, err := EnableShards(dir, time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("EnableShards: %v", err)
	}
	for _, n := range []string{"one", "two", "three"} {
		if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned", Note: n}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	path := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	lines := readLines(t, path)
	lines[1], lines[2] = lines[2], lines[1]
	writeLines(t, path, lines)
	if c := mustChain(t, dir); c.OK() || c.File != "events/T1.jsonl" || !strings.HasPrefix(c.BreakReason, "reordered:") {
		t.Fatalf("chain = %+v, want a reordered break in events/T1.jsonl", c)
	}
	if _, err := Reanchor(dir, "merge", "S1", false); err != nil {
		t.Fatalf("Reanchor: %v", err)
	}
	c := mustChain(t, dir)
	if !c.OK() || len(c.Acknowledged) != 1 || c.Acknowledged[0].File != "events/T1.jsonl" {
		t.Errorf("chain = %+v, want OK with the shard break acknowledged", c)
	}
}
