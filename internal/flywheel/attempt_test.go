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

func TestAttemptBriefFreshAttemptIgnoresDifferentPromptPath(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "copy.txt", "owns: a.go\nneeds: none\ngate: exit 9\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "copy.txt"},
	}
	header, paths, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "brief.txt" {
		t.Errorf("paths = %v, want [brief.txt]", paths)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "exit 0" {
		t.Errorf("gates = %v, want [exit 0] (copy.txt's own gate must not appear)", header.Gates)
	}
}

func TestAttemptBriefCorrectionIdenticalContentUsesBriefAlone(t *testing.T) {
	dir := t.TempDir()
	body := "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n"
	writeAttemptBrief(t, dir, "brief.txt", body)
	writeAttemptBrief(t, dir, "copy.txt", body)
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "copy.txt"},
	}
	header, paths, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "brief.txt" {
		t.Errorf("paths = %v, want [brief.txt]", paths)
	}
	if len(header.Owns) != 1 || header.Owns[0] != "a.go" {
		t.Errorf("owns = %v, want [a.go]", header.Owns)
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

// TestAttemptBriefCorrectionDeltaExclusiveAdded checks a correction delta
// declaring exclusive: on a base with no exclusive: yields a merged header
// carrying the delta's resource (issue #242).
func TestAttemptBriefCorrectionDeltaExclusiveAdded(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "delta.txt", "owns: b.go\nexclusive: db\ngate: exit 0\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt"},
	}
	header, _, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(header.Exclusive) != 1 || header.Exclusive[0] != "db" {
		t.Errorf("exclusive = %v, want [db] (the correction's resource must reach the merged header)", header.Exclusive)
	}
}

// TestAttemptBriefCorrectionDeltaExclusiveUnionNoDuplicates checks a base
// with exclusive: a plus a delta with exclusive: b yields both resources,
// without duplicates — the correction adds to the claim, it does not replace
// it (issue #242). One exclusive: line is one resource name, so the delta
// repeats a on its own line to exercise the dedup.
func TestAttemptBriefCorrectionDeltaExclusiveUnionNoDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nexclusive: a\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "delta.txt", "owns: b.go\nexclusive: b\nexclusive: a\ngate: exit 0\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt"},
	}
	header, _, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	want := []string{"a", "b"}
	if len(header.Exclusive) != len(want) {
		t.Fatalf("exclusive = %v, want %v", header.Exclusive, want)
	}
	for i, w := range want {
		if header.Exclusive[i] != w {
			t.Errorf("exclusive[%d] = %q, want %q (base first, no duplicates)", i, header.Exclusive[i], w)
		}
	}
}

// TestAttemptBriefCorrectionDeltaMergesGatesOwnsExclusiveTogether checks the
// exclusive union joins the existing merges without disturbing them: a delta
// with its own gate still overrides, owns still unions base-first without
// duplicates, and the delta's exclusive adds to the base's (issue #242).
func TestAttemptBriefCorrectionDeltaMergesGatesOwnsExclusiveTogether(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nexclusive: cache\ngate: exit 0\n\n# TASK\n")
	writeAttemptBrief(t, dir, "delta.txt", "owns: b.go, a.go\nexclusive: db\ngate: exit 2\n\n# TASK\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt"},
	}
	header, _, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "exit 2" {
		t.Errorf("gates = %v, want [exit 2] (delta's own, as today)", header.Gates)
	}
	wantOwns := []string{"a.go", "b.go"}
	if len(header.Owns) != len(wantOwns) {
		t.Fatalf("owns = %v, want %v", header.Owns, wantOwns)
	}
	for i, w := range wantOwns {
		if header.Owns[i] != w {
			t.Errorf("owns[%d] = %q, want %q (base first, no duplicates, as today)", i, header.Owns[i], w)
		}
	}
	wantExcl := []string{"cache", "db"}
	if len(header.Exclusive) != len(wantExcl) {
		t.Fatalf("exclusive = %v, want %v", header.Exclusive, wantExcl)
	}
	for i, w := range wantExcl {
		if header.Exclusive[i] != w {
			t.Errorf("exclusive[%d] = %q, want %q", i, header.Exclusive[i], w)
		}
	}
}
