package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendEventsChainsPrev(t *testing.T) {
	dir := t.TempDir()
	e1 := Event{Task: "T1", Kind: "planned"}
	if err := AppendEvent(dir, e1); err != nil {
		t.Fatalf("append event 1: %v", err)
	}
	e2 := Event{Task: "T2", Kind: "planned"}
	if err := AppendEvent(dir, e2); err != nil {
		t.Fatalf("append event 2: %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
	if events[0].Prev != "" {
		t.Errorf("first event Prev should be empty, got %q", events[0].Prev)
	}
	if events[1].Prev == "" {
		t.Errorf("second event Prev should not be empty")
	}
}

func TestAppendEventsBatchChains(t *testing.T) {
	dir := t.TempDir()
	e1 := Event{Task: "T1", Kind: "planned"}
	if err := AppendEvent(dir, e1); err != nil {
		t.Fatalf("append event 1: %v", err)
	}
	e2 := Event{Task: "T2", Kind: "planned"}
	e3 := Event{Task: "T3", Kind: "planned"}
	if err := AppendEvents(dir, []Event{e2, e3}); err != nil {
		t.Fatalf("append events batch: %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events, got %d", len(events))
	}
	if events[1].Prev == "" {
		t.Errorf("second event Prev should not be empty")
	}
	if events[2].Prev == "" {
		t.Errorf("third event Prev should not be empty")
	}
}

func TestAppendEventsOverwritesSuppliedPrevChain(t *testing.T) {
	dir := t.TempDir()
	e1 := Event{Task: "T1", Kind: "planned", Prev: "forged"}
	if err := AppendEvent(dir, e1); err != nil {
		t.Fatalf("append event: %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if events[0].Prev != "" {
		t.Errorf("first event Prev should be overwritten to empty, got %q", events[0].Prev)
	}
}

func TestVerifyLogChainIntact(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		e := Event{Task: "T" + string(rune('0'+i)), Kind: "planned"}
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	data, _ := os.ReadFile(path)
	t.Logf("File:\n%s", string(data))
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		h := lineHash([]byte(line))
		var prev string
		var rec struct{ Prev string }
		if err := json.Unmarshal([]byte(line), &rec); err == nil {
			prev = rec.Prev
		}
		t.Logf("  Line %d: hash=%s, prev=%s", i+1, h[:16], prev)
	}

	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	t.Logf("BreakLine=%d, BreakPrev=%s", chain.BreakLine, chain.BreakPrev)
	if !chain.OK() {
		t.Errorf("chain should be OK, but BreakLine=%d", chain.BreakLine)
	}
	if chain.Lines != 3 {
		t.Errorf("expected 3 lines, got %d", chain.Lines)
	}
	if chain.Chained != 2 {
		t.Errorf("expected 2 chained, got %d", chain.Chained)
	}
	if chain.FirstChained != 2 {
		t.Errorf("expected FirstChained=2, got %d", chain.FirstChained)
	}
}

func TestVerifyLogChainEditedLineBreaks(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		e := Event{Task: "T" + string(rune('0'+i)), Kind: "planned"}
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := string(data)
	lines = replaceNthLine(lines, 2, `{"task":"TX","kind":"planned"}`)
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if chain.OK() {
		t.Errorf("chain should be broken, but OK() returned true")
	}
	if chain.BreakLine != 3 {
		t.Errorf("expected BreakLine=3, got %d", chain.BreakLine)
	}
}

func TestVerifyLogChainDeletedLineBreaks(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 3; i++ {
		e := Event{Task: "T" + string(rune('0'+i)), Kind: "planned"}
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := removeNthLine(string(data), 2)
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if chain.OK() {
		t.Errorf("chain should be broken after deletion")
	}
	if chain.BreakLine != 2 {
		t.Errorf("expected BreakLine=2 (old line 3 is now line 2), got %d", chain.BreakLine)
	}
}

func TestVerifyLogChainInterleavedOK(t *testing.T) {
	dir := t.TempDir()
	dot := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dot, "events.jsonl")
	l1 := `{"task":"T1","kind":"planned"}`
	h1 := lineHash([]byte(l1))
	l2 := `{"task":"T2","kind":"planned","prev":"` + h1 + `"}`
	l3 := `{"task":"T3","kind":"planned","prev":"` + h1 + `"}`
	content := l1 + "\n" + l2 + "\n" + l3 + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if !chain.OK() {
		t.Errorf("interleaved chain should be OK, but BreakLine=%d", chain.BreakLine)
	}
}

func TestVerifyLogChainLegacyLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	dot := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dot, "events.jsonl")
	l1 := `{"task":"T1","kind":"planned"}`
	l2 := `{"task":"T2","kind":"planned"}`
	if err := os.WriteFile(path, []byte(l1+"\n"+l2+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	e3 := Event{Task: "T3", Kind: "planned"}
	if err := AppendEvent(dir, e3); err != nil {
		t.Fatalf("append: %v", err)
	}
	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if !chain.OK() {
		t.Errorf("chain with legacy lines should be OK")
	}
	if chain.Chained != 1 {
		t.Errorf("expected Chained=1, got %d", chain.Chained)
	}
	if chain.FirstChained != 3 {
		t.Errorf("expected FirstChained=3, got %d", chain.FirstChained)
	}
}

func TestLastLineHashSkipsUnterminatedTail(t *testing.T) {
	dir := t.TempDir()
	dot := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dot, "events.jsonl")
	content := "a\nb\npartial"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hash, err := lastLineHash(path)
	if err != nil {
		t.Fatalf("lastLineHash: %v", err)
	}
	expected := lineHash([]byte("b"))
	if hash != expected {
		t.Errorf("expected hash of 'b', got different hash")
	}
}

// Helper functions for test manipulation
func replaceNthLine(s string, n int, replacement string) string {
	lines := splitLines(s)
	if n > 0 && n <= len(lines) {
		lines[n-1] = replacement
	}
	return joinLines(lines)
}

func removeNthLine(s string, n int) string {
	lines := splitLines(s)
	if n > 0 && n <= len(lines) {
		lines = append(lines[:n-1], lines[n:]...)
	}
	return joinLines(lines)
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	var lines []string
	var current string
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(s[i])
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		result += line
		if i < len(lines)-1 {
			result += "\n"
		}
	}
	if len(lines) > 0 {
		result += "\n"
	}
	return result
}

// TestLastLineHashAcrossChunksChain checks the backward chunked read finds a
// last line longer than one 64 KiB chunk, with and without an unterminated
// tail after it, and a file whose only complete line is that long line.
func TestLastLineHashAcrossChunksChain(t *testing.T) {
	big := strings.Repeat("x", 150000)
	for name, content := range map[string]string{
		"after a short line":      "a\n" + big + "\n",
		"before a partial record": "a\n" + big + "\npartial",
		"only line":               big + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := lastLineHash(path)
			if err != nil {
				t.Fatalf("lastLineHash() error = %v", err)
			}
			if want := lineHash([]byte(big)); got != want {
				t.Errorf("lastLineHash() = %s, want the long line's hash %s", got, want)
			}
		})
	}
}
