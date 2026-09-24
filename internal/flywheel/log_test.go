package flywheel

import (
	"errors"
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

// dispatchBrief records a dispatched event for task's fresh attempt r1
// carrying the header of the brief at path, as flywheel run does (issue
// #259): the header a pass is measured against.
func dispatchBrief(t *testing.T, dir, task, path string) {
	t.Helper()
	h, err := ParseBriefHeader(path)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendEvent(dir, Event{Task: task, Kind: "dispatched", Attempt: "r1", Brief: path, Header: &h}); err != nil {
		t.Fatalf("dispatched error = %v", err)
	}
}

// TestRecordAmendedRefusesInertGateChangeAfterDispatch is the guard: a task
// whose attempt was already dispatched with gate A cannot be amended to
// declare gate B, because validation measures the dispatched header. The
// refusal names the correction-delta alternative, appends nothing, and
// carries the RuleRefusal shape the CLI maps to exit 6 (issue #272).
func TestRecordAmendedRefusesInertGateChangeAfterDispatch(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go test ./...\n\n# TASK: t\nv2\n")
	err := RecordAmended(dir, "t", brief, "fix gate")
	if !IsRuleRefusal(err) {
		t.Fatalf("RecordAmended() error = %v, want a RuleRefusal", err)
	}
	var r *RuleRefusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a *RuleRefusal", err)
	}
	if r.Rule == "" || r.Fix == "" {
		t.Errorf("RuleRefusal = rule %q fix %q, want both non-empty", r.Rule, r.Fix)
	}
	if !strings.Contains(r.Fix, "flywheel run t --delta") {
		t.Errorf("RuleRefusal fix = %q, want it naming the correction delta command", r.Fix)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("events = %d, want 2 (the refusal appends nothing)", len(evs))
	}
}

// TestRecordAmendedUndispatchedStillRecordsNewGates is the common case: a
// brief that has not been dispatched yet is amended to declare new gates
// exactly as before.
func TestRecordAmendedUndispatchedStillRecordsNewGates(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go test ./...\n\n# TASK: t\nv2\n")
	if err := RecordAmended(dir, "t", brief, "fix gate"); err != nil {
		t.Fatalf("RecordAmended() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 || evs[1].Kind != "amended" {
		t.Fatalf("events = %+v, want the amended event appended", evs)
	}
	a := evs[1]
	if a.Header == nil || len(a.Header.Gates) != 1 || a.Header.Gates[0] != "go test ./..." {
		t.Errorf("amended header gates = %+v, want [go test ./...]", a.Header)
	}
}

// TestRecordAmendedDispatchedWithoutGateChangeSucceeds is the legitimate
// amendment the refusal must keep working: the attempt is dispatched but the
// gates are unchanged — only owns: is widened — so the amendment is read from
// the base brief and is not inert.
func TestRecordAmendedDispatchedWithoutGateChangeSucceeds(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: a.go, b.go\ngate: go build ./...\n\n# TASK: t\nv2\n")
	if err := RecordAmended(dir, "t", brief, "widen owns"); err != nil {
		t.Fatalf("RecordAmended() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 3 || evs[2].Kind != "amended" {
		t.Fatalf("events = %+v, want the amended event appended", evs)
	}
	a := evs[2]
	if strings.Join(a.Owns, ",") != "a.go,b.go" {
		t.Errorf("amended owns = %v, want [a.go b.go]", a.Owns)
	}
	if a.Header == nil || len(a.Header.Gates) != 1 || a.Header.Gates[0] != "go build ./..." {
		t.Errorf("amended header gates = %+v, want the unchanged gate", a.Header)
	}
}

// TestRecordAmendedInheritedGatesChangeSucceeds is the false-refusal guard:
// a correction whose delta declares no gate: lines inherits the base brief's
// gates, so amending the base gates DOES change what validation would run
// for that attempt and must be allowed (issue #272 correction).
func TestRecordAmendedInheritedGatesChangeSucceeds(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	delta := writeLogBrief(t, dir, "delta.txt", "owns: b.go\n\n# TASK: t\ncorrection\n")
	dh, err := ParseBriefHeader(delta)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendEvent(dir, Event{Task: "t", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt", Header: &dh}); err != nil {
		t.Fatalf("dispatched error = %v", err)
	}
	writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go test ./...\n\n# TASK: t\nv2\n")
	if err := RecordAmended(dir, "t", brief, "fix gate"); err != nil {
		t.Fatalf("RecordAmended() error = %v, want the inherited-gate amendment to succeed", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 3 || evs[2].Kind != "amended" {
		t.Fatalf("events = %+v, want the amended event appended", evs)
	}
	if evs[2].Header == nil || len(evs[2].Header.Gates) != 1 || evs[2].Header.Gates[0] != "go test ./..." {
		t.Errorf("amended header gates = %+v, want [go test ./...]", evs[2].Header)
	}
	header, _, err := AttemptBrief(dir, evs, "t")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "go test ./..." {
		t.Errorf("AttemptBrief gates = %v, want the amended [go test ./...] inherited by the correction", header.Gates)
	}
}

// TestRecordAmendedEffectiveGatesLiveChangeSucceeds pins the live-gate
// rule: the base's live-gate: lines are always inherited — a correction's
// delta never replaces them — so an amendment touching only live-gate: lines
// takes effect on a correction attempt and is allowed, and AttemptBrief
// returns the amended live gate while the delta's own gates still override.
func TestRecordAmendedEffectiveGatesLiveChangeSucceeds(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\nlive-gate: go run ./cmd/real\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	delta := writeLogBrief(t, dir, "delta.txt", "owns: b.go\ngate: go test ./...\n\n# TASK: t\ncorrection\n")
	dh, err := ParseBriefHeader(delta)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendEvent(dir, Event{Task: "t", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt", Header: &dh}); err != nil {
		t.Fatalf("dispatched error = %v", err)
	}
	writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\nlive-gate: go run ./cmd/real2\n\n# TASK: t\nv2\n")
	if err := RecordAmended(dir, "t", brief, "fix live gate"); err != nil {
		t.Fatalf("RecordAmended() error = %v, want the live-gate amendment to succeed", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	header, _, err := AttemptBrief(dir, evs, "t")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(header.LiveGates) != 1 || header.LiveGates[0] != "go run ./cmd/real2" {
		t.Errorf("AttemptBrief live gates = %v, want the amended [go run ./cmd/real2] (always inherited from the base)", header.LiveGates)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "go test ./..." {
		t.Errorf("AttemptBrief gates = %v, want the delta's [go test ./...] still overriding", header.Gates)
	}
}

// TestRecordAmendedLiveGatesInertAfterDispatchRefused is the fresh-attempt
// half of the live-gate rule: a dispatched attempt measures its own recorded
// header, so a live-gate-only amendment is inert there and refused, exactly
// as an ordinary gate change is.
func TestRecordAmendedLiveGatesInertAfterDispatchRefused(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\nlive-gate: go run ./cmd/real\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\nlive-gate: go run ./cmd/real2\n\n# TASK: t\nv2\n")
	err := RecordAmended(dir, "t", brief, "fix live gate")
	if !IsRuleRefusal(err) {
		t.Fatalf("RecordAmended() error = %v, want a RuleRefusal", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("events = %d, want 2 (the refusal appends nothing)", len(evs))
	}
}

// TestAppendAmendedEventRefusesInert is the JSON-path guard: a
// JSON-ingested amended event that would be inert is refused through the
// same check as --kind amended, before anything is written (issue #272).
func TestAppendAmendedEventRefusesInert(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	newH, err := ParseBriefHeader(writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go test ./...\n\n# TASK: t\nv2\n"))
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	err = AppendAmendedEvent(dir, Event{Task: "t", Kind: "amended", Brief: brief, Header: &newH, Note: "via json"})
	if !IsRuleRefusal(err) {
		t.Fatalf("AppendAmendedEvent() error = %v, want a RuleRefusal", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("events = %d, want 2 (the refusal appends nothing)", len(evs))
	}
}

// TestAppendAmendedEventBenignAppends is the other half of the JSON path: a
// JSON-ingested amendment that does not try to change the gates (owns
// widened) lands exactly as the flag path would.
func TestAppendAmendedEventBenignAppends(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	newH, err := ParseBriefHeader(writeLogBrief(t, dir, "b.txt", "owns: a.go, b.go\ngate: go build ./...\n\n# TASK: t\nv2\n"))
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendAmendedEvent(dir, Event{Task: "t", Kind: "amended", Brief: brief, Header: &newH, Note: "widen owns"}); err != nil {
		t.Fatalf("AppendAmendedEvent() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 3 || evs[2].Kind != "amended" {
		t.Fatalf("events = %+v, want the amended event appended", evs)
	}
	if evs[2].Header == nil || len(evs[2].Header.Gates) != 1 || evs[2].Header.Gates[0] != "go build ./..." {
		t.Errorf("amended header gates = %+v, want the unchanged gate", evs[2].Header)
	}
}

// TestRecordPlannedByIdentity records the planner's identity and goal link.
func TestRecordPlannedByIdentity(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.txt\nneeds: none\n\n# TASK: t\n")
	if err := RecordPlannedBy(dir, "t", brief, PlanMeta{Session: "lead-1", Model: "m", GoalID: "G1", Note: "n"}); err != nil {
		t.Fatalf("RecordPlannedBy() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	e := evs[0]
	if e.Session != "lead-1" || e.Model != "m" || e.GoalID != "G1" || e.Note != "n" || e.Persona != "planner" {
		t.Errorf("event = %+v, want Session=lead-1, Model=m, GoalID=G1, Note=n, Persona=planner", e)
	}
	if len(e.Owns) != 1 || e.Owns[0] != "a.txt" {
		t.Errorf("owns = %v, want [a.txt]", e.Owns)
	}
}

// TestRecordPlannedIdentityEmptyByDefault verifies RecordPlanned leaves Session, Model, GoalID, Note empty.
func TestRecordPlannedIdentityEmptyByDefault(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.txt\nneeds: none\n\n# TASK: t\n")
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
	if e.Session != "" || e.Model != "" || e.GoalID != "" || e.Note != "" {
		t.Errorf("event = %+v, want empty Session, Model, GoalID, Note", e)
	}
}

// TestRecordAmendedByIdentity records the planner's identity on an amended event.
func TestRecordAmendedByIdentity(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.txt\nneeds: none\n\n# TASK: t\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	if err := RecordAmendedBy(dir, "t", brief, "why", PlanMeta{Session: "lead-2", Model: "m"}); err != nil {
		t.Fatalf("RecordAmendedBy() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %d, want 2", len(evs))
	}
	a := evs[1]
	if a.Kind != "amended" || a.Session != "lead-2" || a.Model != "m" || a.Note != "why" {
		t.Errorf("amended event = %+v, want Kind=amended, Session=lead-2, Model=m, Note=why", a)
	}
}

// TestRecordAmendedWidenOwnsAfterDispatchTakesEffect checks that when an
// amendment widens owns after dispatch, the amendment takes effect on
// AttemptBrief (issue #281).
func TestRecordAmendedWidenOwnsAfterDispatchTakesEffect(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: a.go, b.go\ngate: go build ./...\n\n# TASK: t\nv2\n")
	if err := RecordAmended(dir, "t", brief, "widen owns"); err != nil {
		t.Fatalf("RecordAmended() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	header, _, err := AttemptBrief(dir, evs, "t")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	wantOwns := []string{"a.go", "b.go"}
	if len(header.Owns) != len(wantOwns) {
		t.Fatalf("owns = %v, want %v", header.Owns, wantOwns)
	}
	for i, w := range wantOwns {
		if header.Owns[i] != w {
			t.Errorf("owns[%d] = %q, want %q", i, header.Owns[i], w)
		}
	}
}

// TestRecordAmendedRefusesInertOwnsNarrowingAfterDispatch checks that an
// amendment that would narrow owns after dispatch is refused with a
// RuleRefusal (issue #281).
func TestRecordAmendedRefusesInertOwnsNarrowingAfterDispatch(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go, b.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv2\n")
	err := RecordAmended(dir, "t", brief, "narrow owns")
	if !IsRuleRefusal(err) {
		t.Fatalf("RecordAmended() error = %v, want a RuleRefusal", err)
	}
	var r *RuleRefusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a *RuleRefusal", err)
	}
	if r.Rule == "" || r.Fix == "" {
		t.Errorf("RuleRefusal = rule %q fix %q, want both non-empty", r.Rule, r.Fix)
	}
	if !strings.Contains(r.Fix, "cannot narrow") {
		t.Errorf("RuleRefusal fix = %q, want it to mention 'cannot narrow'", r.Fix)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Errorf("events = %d, want 2 (the refusal appends nothing)", len(evs))
	}
}

// TestRecordAmendedRefusesPatternNarrowingAfterDispatch checks narrowing a
// directory to one file is seen as a narrowing: the union would keep src/, so
// the replacement cannot take effect and is refused (issue #281 review).
func TestRecordAmendedRefusesPatternNarrowingAfterDispatch(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: src/\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: src/main.go\ngate: go build ./...\n\n# TASK: t\nv2\n")
	err := RecordAmended(dir, "t", brief, "narrow to one file")
	if !IsRuleRefusal(err) {
		t.Fatalf("RecordAmended() error = %v, want a RuleRefusal for a pattern narrowing", err)
	}
}

// TestRecordAmendedRefusesExclusiveRemovalAfterDispatch checks dropping an
// exclusive resource after dispatch is refused like an owns narrowing: the
// union keeps it held (issue #281 review).
func TestRecordAmendedRefusesExclusiveRemovalAfterDispatch(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\nexclusive: db\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv2\n")
	err := RecordAmended(dir, "t", brief, "drop the db lock")
	var r *RuleRefusal
	if !errors.As(err, &r) || !strings.Contains(r.Fix, "exclusive") {
		t.Fatalf("RecordAmended() error = %v, want a RuleRefusal naming exclusive", err)
	}
}

// TestAppendAmendedEventHeaderlessWidensOwns checks a JSON amendment without a
// header is stored with the header parsed from its brief, so its widened owns
// take effect on the dispatched attempt (issue #281 review).
func TestAppendAmendedEventHeaderlessWidensOwns(t *testing.T) {
	dir := t.TempDir()
	brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
	if err := RecordPlanned(dir, "t", brief); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	dispatchBrief(t, dir, "t", brief)
	writeLogBrief(t, dir, "b.txt", "owns: a.go, b.go\ngate: go build ./...\n\n# TASK: t\nv2\n")
	if err := AppendAmendedEvent(dir, Event{Task: "t", Kind: "amended", Brief: brief, Note: "widen"}); err != nil {
		t.Fatalf("AppendAmendedEvent() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	h, _, err := AttemptBrief(dir, evs, "t")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if !ownsContains(h.Owns, "b.go") {
		t.Errorf("effective owns = %v, want b.go included", h.Owns)
	}
	last := evs[len(evs)-1]
	if last.Header == nil || strings.Join(last.Owns, ",") != "a.go,b.go" {
		t.Errorf("amended event header = %v owns = %v, want the parsed header and its owns", last.Header, last.Owns)
	}
}

// TestPlanDriftWarning is issue #366: re-planning a task whose attempt r1 was
// already dispatched with different gates warns that validate keeps measuring
// the dispatched header; before any dispatch, or when only prose changes,
// there is nothing to warn about.
func TestPlanDriftWarning(t *testing.T) {
	t.Run("no dispatch", func(t *testing.T) {
		dir := t.TempDir()
		brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
		if err := RecordPlanned(dir, "t", brief); err != nil {
			t.Fatalf("RecordPlanned() error = %v", err)
		}
		writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go test ./...\n\n# TASK: t\nv2\n")
		if w := PlanDriftWarning(dir, "t", brief); w != "" {
			t.Errorf("PlanDriftWarning() = %q, want empty before any dispatch", w)
		}
	})
	t.Run("changed gate after dispatch", func(t *testing.T) {
		dir := t.TempDir()
		brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
		if err := RecordPlanned(dir, "t", brief); err != nil {
			t.Fatalf("RecordPlanned() error = %v", err)
		}
		dispatchBrief(t, dir, "t", brief)
		writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go test ./...\n\n# TASK: t\nv2\n")
		w := PlanDriftWarning(dir, "t", brief)
		for _, s := range []string{"attempt r1", "--delta"} {
			if !strings.Contains(w, s) {
				t.Errorf("PlanDriftWarning() = %q, want it containing %q", w, s)
			}
		}
	})
	t.Run("prose change after dispatch", func(t *testing.T) {
		dir := t.TempDir()
		brief := writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv1\n")
		if err := RecordPlanned(dir, "t", brief); err != nil {
			t.Fatalf("RecordPlanned() error = %v", err)
		}
		dispatchBrief(t, dir, "t", brief)
		writeLogBrief(t, dir, "b.txt", "owns: a.go\ngate: go build ./...\n\n# TASK: t\nv2, clearer prose\n")
		if w := PlanDriftWarning(dir, "t", brief); w != "" {
			t.Errorf("PlanDriftWarning() = %q, want empty for a prose-only change", w)
		}
	})
}

// TestCoversNegatedNarrowing checks a new negated owns entry that stops
// covering a path the attempt covered is a narrowing (issue #388), while
// dropping a negation widens.
func TestCoversNegatedNarrowing(t *testing.T) {
	have := []string{"apps/inc/**"}
	if covers([]string{"apps/inc/**", "!apps/inc/wake.h"}, have) {
		t.Errorf("covers with a new negation = true, want false (a narrowing)")
	}
	if !covers(have, []string{"apps/inc/**", "!apps/inc/wake.h"}) {
		t.Errorf("covers after dropping a negation = false, want true (a widening)")
	}
}
