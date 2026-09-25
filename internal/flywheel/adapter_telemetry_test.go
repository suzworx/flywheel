package flywheel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTelemetryRunAdapterFromDispatch(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Task: "T1", Attempt: "r1", Kind: "dispatched", Adapter: "claude"},
		{Task: "T1", Attempt: "r1", Kind: "started"},
	}
	adap := runAdapter(events, "T1", "r1")
	if adap.Name() != "claude" {
		t.Errorf("adapter for dispatched claude: got %q, want %q", adap.Name(), "claude")
	}

	events = []Event{
		{Task: "T1", Attempt: "r1", Kind: "dispatched", Adapter: "codex"},
	}
	adap = runAdapter(events, "T1", "r1")
	if adap.Name() != "codex" {
		t.Errorf("adapter for dispatched codex: got %q, want %q", adap.Name(), "codex")
	}

	events = []Event{
		{Task: "T1", Attempt: "r1", Kind: "started"},
	}
	adap = runAdapter(events, "T1", "r1")
	if adap.Name() != "opencode" {
		t.Errorf("adapter for no dispatched event: got %q, want %q", adap.Name(), "opencode")
	}

	events = []Event{
		{Task: "T1", Attempt: "r1", Kind: "dispatched", Adapter: ""},
	}
	adap = runAdapter(events, "T1", "r1")
	if adap.Name() != "opencode" {
		t.Errorf("adapter for empty Adapter: got %q, want %q", adap.Name(), "opencode")
	}
}

func TestTelemetryClaudeRunCountsLiveSteps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := AppendEvent(dir, Event{
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Task: "T1", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent planned: %v", err)
	}

	if err := AppendEvent(dir, Event{
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Task: "T1", Attempt: "r1", Kind: "dispatched",
		Adapter: "claude", Model: "m",
	}); err != nil {
		t.Fatalf("AppendEvent dispatched: %v", err)
	}

	fixture := filepath.Join("testdata", "claude-tool.jsonl")
	b, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	runsDir := filepath.Join(dir, ".flywheel", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	runPath := filepath.Join(runsDir, "T1.r1.jsonl")
	if err := os.WriteFile(runPath, b, 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}

	w := NewWatcher()
	fl, err := w.Refresh(dir, time.Now())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	for _, u := range fl.Units {
		if u.Task == "T1" {
			if u.Steps <= 0 {
				t.Errorf("claude run steps: got %d, want > 0", u.Steps)
			}
			return
		}
	}
	t.Error("T1 unit not found in floor")
}

func TestTelemetryCodexRunCountsLiveSteps(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := Init(dir, false)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if err := AppendEvent(dir, Event{
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Task: "T1", Kind: "planned", Brief: "b.txt",
	}); err != nil {
		t.Fatalf("AppendEvent planned: %v", err)
	}

	if err := AppendEvent(dir, Event{
		TS:   time.Now().UTC().Format(time.RFC3339Nano),
		Task: "T1", Attempt: "r1", Kind: "dispatched",
		Adapter: "codex", Model: "m",
	}); err != nil {
		t.Fatalf("AppendEvent dispatched: %v", err)
	}

	fixture := filepath.Join("testdata", "codex-clean.jsonl")
	b, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	runsDir := filepath.Join(dir, ".flywheel", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	runPath := filepath.Join(runsDir, "T1.r1.jsonl")
	if err := os.WriteFile(runPath, b, 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}

	w := NewWatcher()
	fl, err := w.Refresh(dir, time.Now())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	for _, u := range fl.Units {
		if u.Task == "T1" {
			if u.Steps != 4 {
				t.Errorf("codex run steps: got %d, want 4", u.Steps)
			}
			return
		}
	}
	t.Error("T1 unit not found in floor")
}

func TestTelemetryCodexFileChangeAllPaths(t *testing.T) {
	t.Parallel()
	jsonLine := []byte(`{"type":"item.completed","item":{"id":"i","type":"file_change","changes":[{"path":"a.go","kind":"delete"},{"path":"b.go","kind":"add"}],"status":"completed"}}`)
	codex := codexAdapter{}
	obs, ok := codex.Parse(jsonLine)
	if !ok {
		t.Fatal("Parse failed")
	}

	if obs.Tool != "delete" {
		t.Errorf("tool: got %q, want %q", obs.Tool, "delete")
	}
	if obs.Path != "a.go" {
		t.Errorf("Path: got %q, want %q", obs.Path, "a.go")
	}
	if len(obs.Paths) != 2 {
		t.Errorf("Paths length: got %d, want 2", len(obs.Paths))
	} else if obs.Paths[0] != "a.go" || obs.Paths[1] != "b.go" {
		t.Errorf("Paths: got %v, want [a.go b.go]", obs.Paths)
	}

	paths := obsPaths(obs)
	if len(paths) != 2 || paths[0] != "a.go" || paths[1] != "b.go" {
		t.Errorf("obsPaths: got %v, want [a.go b.go]", paths)
	}

	obsWithPath := Observation{Path: "x"}
	paths = obsPaths(obsWithPath)
	if len(paths) != 1 || paths[0] != "x" {
		t.Errorf("obsPaths with Path: got %v, want [x]", paths)
	}

	obsEmpty := Observation{}
	paths = obsPaths(obsEmpty)
	if len(paths) != 0 {
		t.Errorf("obsPaths empty: got %v, want empty", paths)
	}
}
