package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLogBrief(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	return path
}

func TestRecordPlannedRecordsOwnsNeeds(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt",
		"owns: internal/a.go (new), internal/b.go,\n"+
			"      internal/c.go\n"+
			"needs: t2\n\n# TASK: t\nbody\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	e := evs[0]
	if e.Kind != "planned" || e.Task != "t" || e.Brief != brief {
		t.Fatalf("event = %+v", e)
	}
	wantOwns := []string{"internal/a.go", "internal/b.go", "internal/c.go"}
	if strings.Join(e.Owns, ",") != strings.Join(wantOwns, ",") {
		t.Errorf("owns = %v, want %v", e.Owns, wantOwns)
	}
	wantNeeds := []string{"t2"}
	if strings.Join(e.Needs, ",") != strings.Join(wantNeeds, ",") {
		t.Errorf("needs = %v, want %v", e.Needs, wantNeeds)
	}
	if e.Header == nil {
		t.Fatal("planned event carries no header")
	}
	if strings.Join(e.Header.Owns, ",") != strings.Join(wantOwns, ",") {
		t.Errorf("header owns = %v, want %v", e.Header.Owns, wantOwns)
	}
	if strings.Join(e.Header.Needs, ",") != strings.Join(wantNeeds, ",") {
		t.Errorf("header needs = %v, want %v", e.Header.Needs, wantNeeds)
	}
	if e.Header.SHA256 == "" {
		t.Error("header sha256 is empty, want the brief file's hash")
	}
}

func TestPlannerIdentity(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\nneeds: none\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	if err := RecordAmended(dir, "t", brief, "why"); err != nil {
		t.Fatalf("RecordAmended() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	if evs[0].Kind != "planned" || evs[0].Persona != "planner" {
		t.Errorf("planned event = kind %q persona %q, want persona planner", evs[0].Kind, evs[0].Persona)
	}
	if evs[1].Kind != "amended" || evs[1].Persona != "planner" {
		t.Errorf("amended event = kind %q persona %q, want persona planner", evs[1].Kind, evs[1].Persona)
	}
}

func TestRecordPlannedMissingBriefErrors(t *testing.T) {
	dir := t.TempDir()
	if err := RecordPlanned(dir, "t", filepath.Join(dir, "nope.txt")); err == nil {
		t.Fatal("RecordPlanned() error = nil, want error for missing brief")
	}
}

func TestRecordAmendedSnapshotsAndRecordsNote(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\nneeds: none\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	orig, err := os.ReadFile(brief)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	if err := RecordAmended(dir, "t", brief, "why"); err != nil {
		t.Fatalf("RecordAmended() error = %v", err)
	}
	snap := filepath.Join(dir, ".flywheel", "briefs", "t.prev.brief.txt")
	got, err := os.ReadFile(snap)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if string(got) != string(orig) {
		t.Errorf("snapshot = %q, want %q", got, orig)
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	a := evs[1]
	if a.Kind != "amended" || a.Note != "why" || a.Brief != brief {
		t.Fatalf("amended event = %+v", a)
	}
	if len(a.Owns) != 1 || a.Owns[0] != "a.go" {
		t.Errorf("owns = %v, want [a.go]", a.Owns)
	}
	if a.Header == nil || len(a.Header.Owns) != 1 || a.Header.Owns[0] != "a.go" {
		t.Errorf("amended header = %+v, want owns [a.go]", a.Header)
	}
	if a.Header == nil || a.Header.SHA256 == "" {
		t.Error("amended header sha256 is empty, want the brief file's hash")
	}

	// Editing the brief after the first amend, then amending again, snapshots
	// the now-current (v2) text and replaces the first (v1) snapshot.
	writeLogBrief(t, dir, "b.txt", "owns: a.go, b.go\nneeds: none\n\n# TASK: t\nv2\n")
	if err := RecordAmended(dir, "t", brief, "why2"); err != nil {
		t.Fatalf("second RecordAmended() error = %v", err)
	}
	got2, err := os.ReadFile(snap)
	if err != nil {
		t.Fatalf("read snapshot 2: %v", err)
	}
	if !strings.Contains(string(got2), "v2\n") || strings.Contains(string(got2), "v1\n") {
		t.Errorf("second snapshot = %q, want it replaced with the v2 content", got2)
	}
}

func TestRecordAmendedResolvesBriefFromLatestEvent(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\nneeds: none\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	if err := RecordAmended(dir, "t", "", "why"); err != nil {
		t.Fatalf("RecordAmended() with empty brief error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if evs[len(evs)-1].Brief != brief {
		t.Errorf("resolved brief = %q, want %q", evs[len(evs)-1].Brief, brief)
	}
}

func TestRecordAmendedMissingBriefFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := RecordAmended(dir, "t", filepath.Join(dir, "nope.txt"), "why"); err == nil {
		t.Fatal("RecordAmended() error = nil, want error for missing brief file")
	}
}

func TestRecordAmendedWithoutRecordedBriefErrors(t *testing.T) {
	dir := t.TempDir()
	if err := RecordAmended(dir, "t", "", "why"); err == nil {
		t.Fatal("RecordAmended() error = nil, want error when no brief path is recorded")
	}
}
