package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// seedWrite writes s at the repo-relative path p under wd.
func seedWrite(t *testing.T, wd, p, s string) {
	t.Helper()
	fp := filepath.Join(wd, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fp, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

var seedMaps = []StageMap{{From: "staging/claude/", To: ".claude/"}}

// TestSeedStageCopiesExistingDestination checks an owned staging file whose
// copy is missing gets its existing destination's bytes (issue #898).
func TestSeedStageCopiesExistingDestination(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	seedWrite(t, wd, ".claude/domains.json", `{"a":1}`+"\n")
	files, err := seedStage(wd, []string{"staging/claude/domains.json"}, seedMaps)
	if err != nil {
		t.Fatalf("seedStage() error = %v", err)
	}
	sum := sha256.Sum256([]byte(`{"a":1}` + "\n"))
	want := []StagedFile{{From: ".claude/domains.json", To: "staging/claude/domains.json", SHA256: hex.EncodeToString(sum[:]), Copied: true}}
	if !slices.Equal(files, want) {
		t.Errorf("seedStage() = %+v, want %+v", files, want)
	}
	if b, err := os.ReadFile(filepath.Join(wd, "staging", "claude", "domains.json")); err != nil || string(b) != `{"a":1}`+"\n" {
		t.Errorf("staging copy = %q, %v; want the destination's bytes", b, err)
	}
}

// TestSeedStageLeavesExistingStagingAlone checks an existing staging file is
// neither overwritten nor recorded (issue #898).
func TestSeedStageLeavesExistingStagingAlone(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	seedWrite(t, wd, ".claude/domains.json", "dest\n")
	seedWrite(t, wd, "staging/claude/domains.json", "mine\n")
	files, err := seedStage(wd, []string{"staging/claude/domains.json"}, seedMaps)
	if err != nil || len(files) != 0 {
		t.Errorf("seedStage() = %+v, %v; want nothing", files, err)
	}
	if b, err := os.ReadFile(filepath.Join(wd, "staging", "claude", "domains.json")); err != nil || string(b) != "mine\n" {
		t.Errorf("staging file = %q, %v; want it unchanged", b, err)
	}
}

// TestSeedStageDirectoryOwnsSeedNothing checks directory, glob and negated
// owns entries seed nothing (issue #898).
func TestSeedStageDirectoryOwnsSeedNothing(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	seedWrite(t, wd, ".claude/domains.json", "dest\n")
	files, err := seedStage(wd, []string{"staging/claude/", "staging/claude/*.json", "!staging/claude/domains.json"}, seedMaps)
	if err != nil || len(files) != 0 {
		t.Errorf("seedStage() = %+v, %v; want nothing", files, err)
	}
	if _, err := os.Stat(filepath.Join(wd, "staging")); !os.IsNotExist(err) {
		t.Errorf("staging exists (%v); want nothing written", err)
	}
}

// TestSeedStageMissingDestination checks a missing destination seeds nothing
// and is no error (issue #898).
func TestSeedStageMissingDestination(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	files, err := seedStage(wd, []string{"staging/claude/domains.json"}, seedMaps)
	if err != nil || len(files) != 0 {
		t.Errorf("seedStage() = %+v, %v; want nothing", files, err)
	}
	if _, err := os.Stat(filepath.Join(wd, "staging")); !os.IsNotExist(err) {
		t.Errorf("staging exists (%v); want nothing written", err)
	}
}

// TestSeedStageDispatchRecordsSeeded checks flywheel run seeds the staging
// file before the worker starts and records it on the dispatched event, with
// a progress line (issue #898).
func TestSeedStageDispatchRecordsSeeded(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	brief := "owns: staging/claude/domains.json\nneeds: none\nstage: staging/claude/ -> .claude/\ngate: exit 0\n\n# TASK: seed\n"
	seedWrite(t, dir, "b.txt", brief)
	seedWrite(t, dir, ".claude/domains.json", "dest\n")
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "staging", "claude", "domains.json")); err != nil || string(b) != "dest\n" {
		t.Errorf("staging copy = %q, %v; want the destination's bytes", b, err)
	}
	sum := sha256.Sum256([]byte("dest\n"))
	want := []StagedFile{{From: ".claude/domains.json", To: "staging/claude/domains.json", SHA256: hex.EncodeToString(sum[:]), Copied: true}}
	if ev := kindEvents(t, dir, "dispatched"); len(ev) != 1 || !slices.Equal(ev[0].Seeded, want) {
		t.Errorf("dispatched events = %+v, want one with Seeded %+v", ev, want)
	}
	if !strings.Contains(buf.String(), "T1 seeded staging/claude/domains.json from .claude/domains.json") {
		t.Errorf("progress = %q, want the seeded line", buf.String())
	}
}
