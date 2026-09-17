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

// TestLedgerHeaderBriefEditedAfterPassKeepsPass is the issue #259 regression
// guard: a pass is measured against the header recorded on its planned event,
// not against the brief file as it is now. A planned brief with 2 gates, a
// full pass, then an in-place edit adding a third gate: verify's T3 still
// passes, because the pass is measured against the recorded header. This is
// the test that decides whether the unit is correct.
func TestLedgerHeaderBriefEditedAfterPassKeepsPass(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	briefPath := filepath.Join(dir, "brief.txt")
	twoGates := "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\n\n# TASK: w259\n"
	if err := os.WriteFile(briefPath, []byte(twoGates), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	planned, err := ParseBriefHeader(briefPath)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt", Header: &planned}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	logFinished(t, dir, "T1", "w1")
	tree, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	for _, g := range []string{"1", "2"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-17T01:00:00Z", Task: "T1", Kind: "validated", Gate: g, Tree: tree, RC: &rc, Persona: "supervisor"}); err != nil {
			t.Fatalf("append validated gate %s: %v", g, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	// The lead edits the brief file on disk to add a third gate, recording no
	// new event. The pass above was measured against the recorded 2-gate
	// header: T3 still passes.
	threeGates := "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\ngate: exit 0\n\n# TASK: w259\n"
	if err := os.WriteFile(briefPath, []byte(threeGates), 0o644); err != nil {
		t.Fatalf("rewrite brief: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	for _, item := range res.Items {
		if item.Rule == "T3" && !item.Pass {
			t.Errorf("T3 failed after the in-place edit: %s", item.Reason)
		}
	}
}

// TestLedgerHeaderAmendedInPlaceBeforePassApplies is the other half of the
// regression: when the edit happens before the pass is recorded and the
// amendment is logged (flywheel log --kind amended --brief <same path>), the
// new gate set is in force for passes recorded after it. The pass with only
// two readings must fail T3 naming the third gate.
func TestLedgerHeaderAmendedInPlaceBeforePassApplies(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte("owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\n\n# TASK: w259\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	planned, err := ParseBriefHeader(briefPath)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt", Header: &planned}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	// The lead edits the brief in place and logs the amendment before the
	// pass: the recorded amended header carries the third gate.
	if err := os.WriteFile(briefPath, []byte("owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\ngate: exit 0\n\n# TASK: w259\n"), 0o644); err != nil {
		t.Fatalf("rewrite brief: %v", err)
	}
	amended, err := ParseBriefHeader(briefPath)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:30:00Z", Task: "T1", Kind: "amended", Brief: "brief.txt", Header: &amended}); err != nil {
		t.Fatalf("append amended: %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	for _, g := range []string{"1", "2"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-17T01:00:00Z", Task: "T1", Kind: "validated", Gate: g, Tree: tree, RC: &rc, Persona: "supervisor"}); err != nil {
			t.Fatalf("append validated gate %s: %v", g, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	found := false
	for _, item := range res.Items {
		if item.Rule == "T3" && !item.Pass {
			found = true
			if !strings.Contains(item.Reason, "gate 3") {
				t.Errorf("T3 reason = %q, want it naming the third gate", item.Reason)
			}
		}
	}
	if !found {
		t.Errorf("verify did not apply the recorded amendment's third gate: %v", res.Items)
	}
}

// TestLedgerHeaderAbsentFallsBackToFile pins the backward-compatibility
// guarantee: a ledger whose events carry no header still resolves by reading
// the brief file, exactly as before the header field existed. The edit after
// the planned event is picked up by the file read.
func TestLedgerHeaderAbsentFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\n\n# TASK: w259\n")
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
	}
	header, _, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(header.Gates) != 2 {
		t.Fatalf("gates = %v, want the 2 gates on disk", header.Gates)
	}
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\ngate: exit 0\n\n# TASK: w259\n")
	header, _, err = AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() after edit error = %v", err)
	}
	if len(header.Gates) != 3 {
		t.Errorf("gates = %v, want 3 after the file edit (no recorded header to prefer)", header.Gates)
	}
}

// TestLedgerHeaderCorrectionDeltaRecordedAndUsed checks a correction delta's
// recorded header rides on its dispatched event and drives that attempt, with
// gates/owns/exclusive merging exactly as today — and without the delta file
// ever existing, proving the ledger header, not the file, is what counts.
func TestLedgerHeaderCorrectionDeltaRecordedAndUsed(t *testing.T) {
	dir := t.TempDir()
	writeAttemptBrief(t, dir, "brief.txt", "owns: a.go\nexclusive: cache\nneeds: none\ngate: exit 0\n\n# TASK\n")
	delta := BriefHeader{
		Owns:      []string{"b.go"},
		Gates:     []string{"exit 2"},
		Exclusive: []string{"db"},
		SHA256:    "delta-sha-differs-from-brief",
	}
	events := []Event{
		{Task: "T1", Kind: "planned", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "brief.txt"},
		{Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt", Header: &delta},
	}
	header, paths, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(paths) != 2 || paths[0] != "brief.txt" || paths[1] != "delta.txt" {
		t.Errorf("paths = %v, want [brief.txt delta.txt]", paths)
	}
	if len(header.Gates) != 1 || header.Gates[0] != "exit 2" {
		t.Errorf("gates = %v, want [exit 2] (the recorded delta's)", header.Gates)
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
