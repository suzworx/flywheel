package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// fixDir returns an initialised flywheel dir with a cheap default worker and
// a strong one, and T1 last dispatched with the given worker, adapter and
// model.
func fixDir(t *testing.T, worker, adapter, model string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := flywheel.Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	cfg := flywheel.Config{Version: 1, Workers: []flywheel.Worker{
		{Name: "cheap", Adapter: "opencode", Model: "small"},
		{Name: "strong", Adapter: "claude", Model: "big"},
	}}
	if err := flywheel.WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	for _, e := range []flywheel.Event{
		{TS: "2026-09-25T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-25T00:00:01Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Worker: worker, Adapter: adapter, Model: model},
	} {
		if err := flywheel.AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent(%s) error = %v", e.Kind, err)
		}
	}
	return dir
}

// TestReviewFixWorkerDefaultsToBuilder checks review --fix without
// --fix-worker corrects with the worker that built the unit, and says so when
// that builder is not configured (issue #469).
func TestReviewFixWorkerDefaultsToBuilder(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	got, err := fixWorker(fixDir(t, "strong", "claude", "big"), "T1", "", &buf)
	if err != nil || got != "strong" || buf.Len() != 0 {
		t.Errorf("fixWorker() = %q, %v, stderr %q; want strong, nil, silent", got, err, buf.String())
	}
	if got, _ := fixWorker(fixDir(t, "strong", "claude", "big"), "T1", "cheap", &buf); got != "cheap" {
		t.Errorf("fixWorker() with --fix-worker cheap = %q, want cheap", got)
	}
	got, err = fixWorker(fixDir(t, "gone", "codex", "x"), "T1", "", &buf)
	if err != nil || got != "" {
		t.Errorf("fixWorker() for an unconfigured builder = %q, %v; want \"\" (the default worker)", got, err)
	}
	if want := "the unit's builder (codex/x) is not a configured worker; corrections use the default worker cheap"; !strings.Contains(buf.String(), want) {
		t.Errorf("stderr = %q, want it containing %q", buf.String(), want)
	}
}

// TestReviewFixResumesOnlyOnSameAdapter checks a correction resumes the
// session only when the fix worker is on the last attempt's adapter (#469).
func TestReviewFixResumesOnlyOnSameAdapter(t *testing.T) {
	t.Parallel()
	dir := fixDir(t, "strong", "claude", "big")
	var buf bytes.Buffer
	if resume, err := fixResumes(dir, "T1", "strong", &buf); err != nil || !resume || buf.Len() != 0 {
		t.Errorf("fixResumes(strong) = %v, %v, stderr %q; want true, nil, silent", resume, err, buf.String())
	}
	resume, err := fixResumes(dir, "T1", "", &buf)
	if err != nil || resume {
		t.Errorf("fixResumes(default cheap) = %v, %v; want false (opencode cannot resume a claude session)", resume, err)
	}
	if !strings.Contains(buf.String(), "fresh session that reads the delta") {
		t.Errorf("stderr = %q, want it saying the correction is a fresh session", buf.String())
	}
}
