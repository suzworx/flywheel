package flywheel

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runScratchpadWrite runs T1 with one write inside the worktree and one write
// to a scratchpad file of the given session segment, and returns the finished
// event's note and the attempt's off-course signal count (issue #906).
func runScratchpadWrite(t *testing.T, sessionSeg string) (string, string, int) {
	t.Helper()
	dir := setupTask(t)
	pad := filepath.Join(t.TempDir(), "claude", "D--proj", sessionSeg, "scratchpad", "notes.md")
	calls := [][2]string{
		{"write", filepath.Join(dir, "a.go")},
		{"write", pad},
	}
	if err := WriteConfig(dir, simConfig(wroteFixture(t, calls, "stop"))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var fin Event
	signals := 0
	for _, e := range evs {
		if e.Kind == "finished" {
			fin = e
		}
		if e.Kind == "signal" && e.Signal == "off-course" && e.Attempt == "r1" {
			signals++
		}
	}
	return fin.Note, pad, signals
}

// TestRunScratchpadWriteNotOffCourse checks a write to the worker session's
// own scratchpad adds no outside-write note and no off-course signal
// (issue #906).
func TestRunScratchpadWriteNotOffCourse(t *testing.T) {
	t.Parallel()
	note, _, signals := runScratchpadWrite(t, "ses_test_wrote_001")
	if strings.Contains(note, "wrote outside the worktree") || signals != 0 {
		t.Errorf("note = %q, off-course signals = %d; want neither for the session scratchpad", note, signals)
	}
}

// TestRunScratchpadOtherSessionIsOffCourse checks a write to another
// session's scratchpad still counts as outside the worktree (issue #906).
func TestRunScratchpadOtherSessionIsOffCourse(t *testing.T) {
	t.Parallel()
	note, pad, signals := runScratchpadWrite(t, "ses_other")
	if !strings.Contains(note, "wrote outside the worktree:") || !strings.Contains(note, pad) {
		t.Errorf("note = %q, want it to name %s", note, pad)
	}
	if signals != 1 {
		t.Errorf("off-course signals = %d, want exactly 1", signals)
	}
}

// TestRunScratchpadHelper table-tests isSessionScratchpad (issue #906).
func TestRunScratchpadHelper(t *testing.T) {
	t.Parallel()
	const ses = "ses_abc"
	for _, tc := range []struct {
		name    string
		p       string
		session string
		want    bool
	}{
		{"match", "/tmp/claude/D--proj/ses_abc/scratchpad/notes.md", ses, true},
		{"nested match", "/tmp/claude/D--proj/ses_abc/scratchpad/sub/notes.md", ses, true},
		{"wrong session", "/tmp/claude/D--proj/ses_other/scratchpad/notes.md", ses, false},
		{"empty session", "/tmp/claude/D--proj/ses_abc/scratchpad/notes.md", "", false},
		{"no scratchpad segment", "/tmp/claude/D--proj/ses_abc/notes.md", ses, false},
		{"scratchpad is last", "/tmp/claude/D--proj/ses_abc/scratchpad", ses, false},
		{"scratchpad trailing slash", "/tmp/claude/D--proj/ses_abc/scratchpad/", ses, false},
		{"no project segment", "/tmp/claude/ses_abc/scratchpad/notes.md", ses, false},
		{"backslash windows path", `C:\Users\u\AppData\Local\Temp\claude\D--x--y\ses_abc\scratchpad\voice-turn-header`, ses, runtime.GOOS == "windows"},
	} {
		if got := isSessionScratchpad(tc.p, tc.session); got != tc.want {
			t.Errorf("%s: isSessionScratchpad(%q, %q) = %v, want %v", tc.name, tc.p, tc.session, got, tc.want)
		}
	}
	if runtime.GOOS == "windows" {
		if !isSessionScratchpad(`C:\Temp\Claude\P\SES_ABC\Scratchpad\n.md`, ses) {
			t.Errorf("isSessionScratchpad is case-sensitive on Windows, want case-insensitive")
		}
	}
}
