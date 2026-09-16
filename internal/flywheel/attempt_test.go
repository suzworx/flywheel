package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAttemptBrief writes a brief file under dir/name and returns name, the
// repo-relative path callers put on planned/amended/dispatched events.
func writeAttemptBrief(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return name
}

func TestAttemptBriefFreshUsesPlannedBrief(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "brief.txt"},
	}
	header, paths, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "brief.txt" {
		t.Errorf("paths = %v, want [brief.txt]", paths)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "exit 0" {
		t.Errorf("gates = %v, want [exit 0]", header.Gates)
	}
}

func TestAttemptBriefAmendedReplacesPlanned(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "brief2.txt", "owns: b.go\nneeds: none\ngate: exit 1\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "amended", Brief: "brief2.txt"},
	}
	header, paths, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "brief2.txt" {
		t.Errorf("paths = %v, want [brief2.txt]", paths)
	}
	if len(header.Owns) != 1 || header.Owns[0] != "b.go" {
		t.Errorf("owns = %v, want [b.go]", header.Owns)
	}
}

func TestAttemptBriefCorrectionDeltaWithGatesOverrides(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "delta.txt", "owns: b.go\ngate: exit 2\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt"},
	}
	header, paths, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(paths) != 2 || paths[0] != "brief.txt" || paths[1] != "delta.txt" {
		t.Errorf("paths = %v, want [brief.txt delta.txt]", paths)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "exit 2" {
		t.Errorf("gates = %v, want [exit 2] (delta's own)", header.Gates)
	}
}

func TestAttemptBriefCorrectionDeltaNoGatesFallsBack(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "delta.txt", "owns: b.go\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt"},
	}
	header, _, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "exit 0" {
		t.Errorf("gates = %v, want [exit 0] (fallback to the brief's)", header.Gates)
	}
}

func TestAttemptBriefOwnsUnionNoDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go, b.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "delta.txt", "owns: b.go, c.go\ngate: exit 0\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt"},
	}
	header, _, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	want := []string{"a.go", "b.go", "c.go"}
	if len(header.Owns) != len(want) {
		t.Fatalf("owns = %v, want %v", header.Owns, want)
	}
	for i, w := range want {
		if header.Owns[i] != w {
			t.Errorf("owns[%d] = %q, want %q", i, header.Owns[i], w)
		}
	}
}

func TestAttemptBriefMissingDeltaIsError(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "missing-delta.txt"},
	}
	if _, _, err := AttemptBrief(dir, events, "T1"); err == nil {
		t.Fatal("AttemptBrief() error = nil, want an error naming the missing delta")
	} else if !strings.Contains(err.Error(), "missing-delta.txt") {
		t.Errorf("error = %v, want it to name missing-delta.txt", err)
	}
}

func TestAttemptBriefNoPlannedEventIsError(t *testing.T) {
	dir := t.TempDir()
	_, _, err := AttemptBrief(dir, nil, "T1")
	if err == nil {
		t.Fatal("AttemptBrief() error = nil, want the no-planned-event error")
	}
	if !errors.Is(err, errNoPlannedBrief) {
		t.Errorf("error = %v, want it to wrap errNoPlannedBrief", err)
	}
	if !strings.Contains(err.Error(), "has no planned event") {
		t.Errorf("error = %v, want the message gauges.go gave before AttemptBrief existed", err)
	}
}
