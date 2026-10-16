package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestRetroCmdExitCodes checks `flywheel retro` (issue #854): a task not
// landed exits 6, each usage mistake exits 2, a missing changed path exits 1,
// and --changed or --none on a landed task records (0), with the task id
// before or after the flags.
func TestRetroCmdExitCodes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "brief.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		rc := retroMain(append(args, "--dir", dir), &out, &errb)
		return rc, out.String() + errb.String()
	}
	if rc, out := run("T1", "--session", "lead", "--none", "--note", "n"); rc != 6 || !strings.Contains(out, "retro-not-landed") {
		t.Fatalf("not landed: rc=%d out=%s, want 6 naming retro-not-landed", rc, out)
	}
	if err := flywheel.AppendEvent(dir, flywheel.Event{Task: "T1", Kind: "landed"}); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"no task":  {"--session", "lead", "--none", "--note", "n"},
		"both":     {"T1", "--session", "lead", "--none", "--changed", "brief.md", "--note", "n"},
		"neither":  {"T1", "--session", "lead", "--note", "n"},
		"no sess":  {"T1", "--none", "--note", "n"},
		"no note":  {"T1", "--session", "lead", "--none"},
		"bad flag": {"T1", "--bogus"},
	} {
		if rc, out := run(args...); rc != 2 || !strings.Contains(out, "usage: ") {
			t.Errorf("%s: rc=%d out=%s, want 2 with usage", name, rc, out)
		}
	}
	if rc, out := run("T1", "--session", "lead", "--changed", "gone.md", "--note", "n"); rc != 1 || !strings.Contains(out, "gone.md") {
		t.Fatalf("missing path: rc=%d out=%s, want 1 naming gone.md", rc, out)
	}
	if rc, out := run("--session", "lead", "--changed", "brief.md", "--note", "tightened", "T1"); rc != 0 || !strings.Contains(out, "retro recorded for T1: brief.md") {
		t.Fatalf("--changed: rc=%d out=%s, want 0", rc, out)
	}
	if rc, out := run("T1", "--session", "lead", "--none", "--note", "fine"); rc != 0 || !strings.Contains(out, "no changes") {
		t.Fatalf("--none: rc=%d out=%s, want 0", rc, out)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == "retro" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("recorded %d retro events, want 2", n)
	}
}
