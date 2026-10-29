package flywheel

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// heartbeatLine is a claude stream heartbeat for an open Bash call (issue #917).
func heartbeatLine(elapsed string) string {
	return `{"type":"tool_progress","tool_use_id":"toolu_X-heartbeat-1","tool_name":"Bash","parent_tool_use_id":"toolu_X","elapsed_time_seconds":` + elapsed + `,"heartbeat":true,"session_id":"s1"}`
}

// TestToolStallParse checks toolProgress reads only tool_progress lines.
func TestToolStallParse(t *testing.T) {
	t.Parallel()
	tool, elapsed, ok := toolProgress([]byte(heartbeatLine("60")))
	if !ok || tool != "Bash" || elapsed != 60 {
		t.Errorf("toolProgress(heartbeat) = %q, %d, %v; want Bash, 60, true", tool, elapsed, ok)
	}
	for _, line := range []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]},"session_id":"s1"}`,
		`not json at all`,
		``,
	} {
		if _, _, ok := toolProgress([]byte(line)); ok {
			t.Errorf("toolProgress(%q) ok = true, want false", line)
		}
	}
	if _, elapsed, ok := toolProgress([]byte(`{"type":"tool_progress","tool_name":"Bash"}`)); ok && elapsed != 0 {
		t.Errorf("toolProgress(no elapsed) elapsed = %d, want 0", elapsed)
	}
}

// TestToolStallClassify checks a live run's open tool call drives stalled and
// long-step, and a later non-heartbeat line clears it.
func TestToolStallClassify(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	rel := ".flywheel/runs/T1.r1.jsonl"
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	adap, err := AdapterFor("claude")
	if err != nil {
		t.Fatal(err)
	}
	w := NewWatcher()
	appendRead := func(line string) string {
		t.Helper()
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		size, _, err := readRun(dir, &w, rel, adap)
		if err != nil {
			t.Fatalf("readRun() error = %v", err)
		}
		state := classifyRun(false, w.runSteps[rel], len(w.runFiles[rel]), w.runEdits[rel], w.runErr[rel], w.runReason[rel], size, 0, 600, false, false, "")
		return toolStallState(state, w.runOpen[rel], 1800)
	}
	assistant := `{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]},"session_id":"s1"}`
	if got := appendRead(assistant); got != "running" {
		t.Errorf("after an assistant line = %q, want running", got)
	}
	if got := appendRead(heartbeatLine("900")); got != "long-step" {
		t.Errorf("open call 900s of 1800 = %q, want long-step", got)
	}
	if got := appendRead(heartbeatLine("1800")); got != "stalled" {
		t.Errorf("open call 1800s of 1800 = %q, want stalled", got)
	}
	if got := appendRead(assistant); got != "running" {
		t.Errorf("after a later assistant line = %q, want running (cleared)", got)
	}
	if w.runOpen[rel] != 0 {
		t.Errorf("open call = %d after a later line, want 0", w.runOpen[rel])
	}
}

// TestToolStallRun checks flywheel run stops an attempt whose tool call stays
// open past tool_stall_timeout, though heartbeats keep the run file growing.
func TestToolStallRun(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	var b strings.Builder
	step := `{"type":"step_finish","sessionID":"ses_tool_stall","part":{"type":"step_finish","reason":"stop"}}`
	b.WriteString(step + "\n")
	for _, e := range []string{"1", "2", "3", "4"} {
		b.WriteString(heartbeatLine(e) + "\n")
	}
	b.WriteString(step + "\n")
	model := filepath.Join(t.TempDir(), "toolstall.jsonl")
	if err := os.WriteFile(model, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1, Workers: []Worker{{Name: "sim", Adapter: "sim", Model: model, ToolStallTimeout: 2}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var out bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &out})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "stalled" || ExitCode(res) != 7 {
		t.Errorf("reason = %q exit %d, want stalled exit 7", res.Reason, ExitCode(res))
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var f Event
	for _, e := range evs {
		if e.Kind == "finished" {
			f = e
		}
	}
	want := "tool call Bash open 2s (tool_stall_timeout 2s)"
	if f.Reason != "stalled" || !strings.Contains(f.Note, want) {
		t.Errorf("finished event = %+v, want reason stalled with note %q", f, want)
	}
	if !strings.Contains(out.String(), "reason=stalled") {
		t.Errorf("progress missing reason=stalled; got:\n%s", out.String())
	}
}
