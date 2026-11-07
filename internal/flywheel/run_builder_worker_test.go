package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunCorrectionKeepsBuilderWorker checks that a correction dispatched
// without --worker runs on the worker of the task's last attempt, not on the
// default worker, and that --worker still overrides it (field feedback, epic
// #586: a --resume dropped from an opus worker to haiku).
func TestRunCorrectionKeepsBuilderWorker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	brief := "owns: docs/x.md\n\n# Brief\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	delta := filepath.Join(dir, "d.txt")
	if err := os.WriteFile(delta, []byte("Fix the nit.\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-11-07T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "cheap", Adapter: "sim", Model: fixturePath("clean.jsonl", t)},
			{Name: "strong", Adapter: "sim", Model: fixturePath("clean.jsonl", t)},
		},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	lastWorker := func() string {
		t.Helper()
		evs, err := ReadEvents(dir)
		if err != nil {
			t.Fatalf("ReadEvents() error = %v", err)
		}
		last, ok := LastDispatched(evs, "T1")
		if !ok {
			t.Fatal("no dispatched event")
		}
		return last.Worker
	}

	if _, err := Run(dir, RunOptions{Task: "T1", Worker: "strong"}); err != nil {
		t.Fatalf("Run(r1) error = %v", err)
	}
	if got := lastWorker(); got != "strong" {
		t.Fatalf("r1 worker = %q, want strong", got)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta}); err != nil {
		t.Fatalf("Run(c1) error = %v", err)
	}
	if got := lastWorker(); got != "strong" {
		t.Errorf("correction without --worker ran on %q, want the builder strong", got)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta, Worker: "cheap"}); err != nil {
		t.Fatalf("Run(c2) error = %v", err)
	}
	if got := lastWorker(); got != "cheap" {
		t.Errorf("correction with --worker cheap ran on %q, want cheap", got)
	}
}
