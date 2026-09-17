package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// briefSHA returns the SHA-256 hex of the file at path.
func briefSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verifyAll runs VerifyTasks for a single task and returns every failing rule
// id as a set.
func verifyAll(t *testing.T, dir, task string) map[string]bool {
	t.Helper()
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{task}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	fails := map[string]bool{}
	for _, item := range res.Items {
		if !item.Pass {
			fails[item.Rule] = true
		}
	}
	return fails
}

// buildCleanChain builds a task whose chain passes every rule: planned brief,
// dispatched with a matching brief sha, a finished worker event, then a
// ValidateTask pass and an InspectTask pass.
func buildCleanChain(t *testing.T) string {
	t.Helper()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1", SHA256: briefSHA(t, filepath.Join(dir, "brief.txt"))}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() error = %v", err)
	}
	return dir
}

func TestVerifyCleanChainPasses(t *testing.T) {
	dir := buildCleanChain(t)
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		for _, item := range res.Items {
			if !item.Pass {
				t.Errorf("unexpected FAIL %s: %s", item.Rule, item.Reason)
			}
		}
	}
}

func TestVerifyT1TamperedBrief(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", SHA256: "deadbeef"}); err != nil {
		t.Fatalf("append dispatched: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T1"] {
		t.Error("verify did not flag a tampered dispatched brief (T1)")
	}
}

func TestVerifyT3InspectedPassWithoutReadings(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: "deadbeef"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T3"] {
		t.Error("verify did not flag an inspected pass without readings (T3)")
	}
}

func TestVerifyT4InspectedFromWorkerSession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "inspected", Verdict: "rework", Session: "w1", Persona: "inspector", Tree: "t"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T4"] {
		t.Error("verify did not flag an inspected event from a worker session (T4)")
	}
}

func TestVerifyT5LandedWithoutInspectedPass(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "landed"}); err != nil {
		t.Fatalf("append landed: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T5"] {
		t.Error("verify did not flag a landed event without an inspected pass (T5)")
	}
}

func TestVerifyT8BadPersona(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "inspected", Verdict: "rework", Session: "i1", Persona: "worker", Tree: "t"}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T8"] {
		t.Error("verify did not flag a bad persona (T8)")
	}
}

// TestVerifyT3FirstPassSurvivesLaterAttempt checks the T3 window: a legitimate
// pass recorded after its own finished event must not be invalidated by a
// later correction attempt's finished event. Each inspection uses the latest
// finished event before it.
func TestVerifyT3FirstPassSurvivesLaterAttempt(t *testing.T) {
	dir := buildCleanChain(t)
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2099-01-01T00:00:00Z", Task: "T1", Kind: "finished", Attempt: "c1", Session: "w2", RC: &rc}); err != nil {
		t.Fatalf("append later finished: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if fails["T3"] {
		t.Error("T3 flagged the first inspected pass even though a later finished event exists")
	}
}

func TestVerifyAllEmptyLogPasses(t *testing.T) {
	dir := t.TempDir()
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, All: true})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		t.Error("VerifyTasks(--all) on an empty log did not pass")
	}
	if len(res.Items) != 0 {
		t.Errorf("VerifyTasks(--all) on an empty log = %d items, want 0", len(res.Items))
	}
}

func TestVerifyNamedMissingTaskStillRunsRules(t *testing.T) {
	// An explicitly named task that does not exist must not get the passing
	// --all empty result: the rules still run, and a task with no planned
	// brief fails T3 as it did before.
	dir := t.TempDir()
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"NOPE"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if res.Passed {
		t.Error("VerifyTasks() passed a named task absent from the log")
	}
	found := false
	for _, item := range res.Items {
		if item.Rule == "T3" && !item.Pass {
			found = true
		}
	}
	if !found {
		t.Errorf("VerifyTasks() items = %v, want a failing T3 for the missing task", res.Items)
	}
}

// deltaPath writes a delta file next to the task and returns its path.
func deltaPath(t *testing.T, dir, text string) string {
	t.Helper()
	path := filepath.Join(dir, "delta.txt")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	return path
}

// TestVerifyT1FreshAndCorrectionPass checks ruleT1 accepts a fresh attempt
// matching the planned brief and a correction matching its recorded delta.
func TestVerifyT1FreshAndCorrectionPass(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	delta := deltaPath(t, dir, "fix it\n")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1", SHA256: briefSHA(t, filepath.Join(dir, "brief.txt"))}); err != nil {
		t.Fatalf("append dispatched r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:10:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Session: "w1", Brief: "delta.txt", SHA256: briefSHA(t, delta)}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if fails["T1"] {
		t.Error("verify flagged a fresh attempt and a matching correction (T1)")
	}
}

// TestVerifyT1CorrectionTamperedDelta checks a correction whose delta file
// changed after dispatch fails T1 naming the delta.
func TestVerifyT1CorrectionTamperedDelta(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	_ = deltaPath(t, dir, "fix it\n")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt", SHA256: "deadbeef"}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T1"] {
		t.Error("verify did not flag a tampered correction delta (T1)")
	}
}

// TestVerifyT1CorrectionMissingDeltaPath checks a correction dispatched
// without a recorded delta path fails T1.
func TestVerifyT1CorrectionMissingDeltaPath(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", SHA256: "deadbeef"}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	got := false
	for _, item := range res.Items {
		if item.Rule == "T1" && !item.Pass && strings.Contains(item.Reason, "delta") {
			got = true
		}
	}
	if !got {
		t.Errorf("verify did not fail c1 for a missing delta path naming the delta (T1): %v", res.Items)
	}
}

// TestVerifyT1CorrectionMissingDeltaFile checks a correction whose delta path
// names a file that does not exist fails T1 naming the delta.
func TestVerifyT1CorrectionMissingDeltaFile(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "gone.txt", SHA256: "deadbeef"}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	got := false
	for _, item := range res.Items {
		if item.Rule == "T1" && !item.Pass && strings.Contains(item.Reason, "gone.txt") {
			got = true
		}
	}
	if !got {
		t.Errorf("verify did not fail c1 for a missing delta file naming the delta (T1): %v", res.Items)
	}
}

// TestVerifyT1AmendmentWaivesFreshTamper locks in today's amendment semantics:
// an amended event after a fresh dispatch explains a brief change.
func TestVerifyT1AmendmentWaivesFreshTamper(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", SHA256: "deadbeef"}); err != nil {
		t.Fatalf("append dispatched r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "amended", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append amended: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if fails["T1"] {
		t.Error("verify flagged a fresh dispatch an amendment already explains (T1)")
	}
}

// TestVerifyT1CRLFBriefPasses checks the field fix: a brief dispatched as LF
// that a CRLF re-checkout rewrote still passes T1, because the recorded hash
// is contentSHA and verify compares through contentSHA.
func TestVerifyT1CRLFBriefPasses(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	briefPath := filepath.Join(dir, "brief.txt")
	lfB, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1", SHA256: contentSHA(lfB)}); err != nil {
		t.Fatalf("append dispatched r1: %v", err)
	}
	crlf := strings.ReplaceAll(string(lfB), "\n", "\r\n")
	if err := os.WriteFile(briefPath, []byte(crlf), 0o644); err != nil {
		t.Fatalf("rewrite brief CRLF: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if fails["T1"] {
		t.Error("verify flagged an unchanged brief whose checkout turned CRLF (T1)")
	}
}

// TestVerifyT1LegacyRawCRLFHashPasses checks older logs, which recorded the
// raw sha256 of CRLF-checked-out content, still verify against the same
// working copy.
func TestVerifyT1LegacyRawCRLFHashPasses(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	briefPath := filepath.Join(dir, "brief.txt")
	b, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	crlf := []byte(strings.ReplaceAll(string(b), "\n", "\r\n"))
	if err := os.WriteFile(briefPath, crlf, 0o644); err != nil {
		t.Fatalf("rewrite brief CRLF: %v", err)
	}
	sum := sha256.Sum256(crlf)
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1", SHA256: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatalf("append dispatched r1: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if fails["T1"] {
		t.Error("verify rejected a legacy raw sha256 of the current CRLF brief (T1)")
	}
}

// TestVerifyT1RealContentChangeFails checks a genuine content change still
// fails T1, even when dispatched through contentSHA.
func TestVerifyT1RealContentChangeFails(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	briefPath := filepath.Join(dir, "brief.txt")
	lfB, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("read brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1", SHA256: contentSHA(lfB)}); err != nil {
		t.Fatalf("append dispatched r1: %v", err)
	}
	if err := os.WriteFile(briefPath, []byte("a genuinely different brief\n"), 0o644); err != nil {
		t.Fatalf("rewrite brief: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T1"] {
		t.Error("verify did not flag a genuinely changed brief (T1)")
	}
}

// TestVerifyT1AmendmentDoesNotWaiveCorrectionTamper checks an amendment never
// hides a tampered correction delta.
func TestVerifyT1AmendmentDoesNotWaiveCorrectionTamper(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	_ = deltaPath(t, dir, "fix it\n")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt", SHA256: "deadbeef"}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "amended", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append amended: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T1"] {
		t.Error("verify let an amendment waive a tampered correction delta (T1)")
	}
}

// TestVerifyT3PassRelaxedOutsideOwns checks the issue #218 relaxation on the
// audit side: an inspected pass whose readings are on another tree, with the
// diff entirely outside owns, reports T3 passing. verify re-checks the diff
// itself, never trusting the note.
func TestVerifyT3PassRelaxedOutsideOwns(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	measured, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	passed, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: measured, RC: &rc}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: measured}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: passed}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if fails["T3"] {
		t.Error("T3 flagged a relaxed pass whose diff from the measured tree is outside owns")
	}
}

// TestVerifyT3PassRefusedWhenOwnedFileChanged is the regression guard for the
// issue #218 relaxation on the audit side: a relaxed pass whose diff touches an
// owned file must report T3 failing with today's reason string.
func TestVerifyT3PassRefusedWhenOwnedFileChanged(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	measured, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\nchanged\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	passed, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: measured, RC: &rc}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: measured}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: passed}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	want := fmt.Sprintf("inspected pass on tree %s lacks gate 1 after the latest finished event before it", short(passed))
	found := false
	for _, item := range res.Items {
		if item.Rule == "T3" {
			found = true
			if item.Pass {
				t.Errorf("T3 passed a relaxed pass whose diff touches an owned file")
			}
			if item.Reason != want {
				t.Errorf("T3 reason = %q, want today's string %q", item.Reason, want)
			}
		}
	}
	if !found {
		t.Errorf("VerifyTasks() items = %v, want a failing T3", res.Items)
	}
}

// TestVerifyT3PassOnOwnTree checks that a pass with readings on its own tree
// still passes exactly as before the issue #218 relaxation.
func TestVerifyT3PassOnOwnTree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree, RC: &rc}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if fails["T3"] {
		t.Error("T3 flagged a pass whose readings are on its own tree")
	}
}

// TestVerifyT3HistoricalGateSetAfterCorrection is the issue #252 regression
// guard: a pass granted under a 2-gate brief stays valid after a correction
// delta declares a third gate, because it is judged by the gate set in force
// when it was recorded. The second pass, recorded after the delta with
// readings for all three gates, is judged against the 3-gate header exactly
// as today.
func TestVerifyT3HistoricalGateSetAfterCorrection(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree1, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	for _, g := range []string{"1", "2"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: g, Tree: tree1, RC: &rc, Persona: "supervisor"}); err != nil {
			t.Fatalf("append validated gate %s: %v", g, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree1, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree1}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	delta := deltaPath(t, dir, "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\ngate: exit 0\n\n# TASK: delta\n")
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Session: "w2", Brief: "delta.txt", SHA256: briefSHA(t, delta)}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	// tree2 changes an owned file so the relaxed outside-owns reading can
	// never excuse pass 1: it must stand on its own 2-gate reading.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\nchanged\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	tree2, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	for _, g := range []string{"1", "2", "3"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-16T02:10:00Z", Task: "T1", Kind: "validated", Gate: g, Tree: tree2, RC: &rc, Persona: "supervisor"}); err != nil {
			t.Fatalf("append validated gate %s: %v", g, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:10:01Z", Task: "T1", Kind: "owns_checked", Tree: tree2, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:10:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i2", Persona: "inspector", Tree: tree2}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		for _, item := range res.Items {
			if !item.Pass {
				t.Errorf("unexpected FAIL %s: %s", item.Rule, item.Reason)
			}
		}
	}
}

// TestVerifyT3HistoricalPassWithoutReadingsStillFails is the issue #252 guard
// that the fix did not stop checking old passes: a pass recorded under a
// 2-gate brief with a reading for only one of those two gates still fails T3,
// even after a correction delta, against the gate set in force when it was
// granted.
func TestVerifyT3HistoricalPassWithoutReadingsStillFails(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree1, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree1, RC: &rc, Persona: "supervisor"}); err != nil {
		t.Fatalf("append validated gate 1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree1, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree1}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	delta := deltaPath(t, dir, "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\ngate: exit 0\n\n# TASK: delta\n")
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Session: "w2", Brief: "delta.txt", SHA256: briefSHA(t, delta)}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	found := false
	for _, item := range res.Items {
		if item.Rule == "T3" && !item.Pass {
			found = true
			if !strings.Contains(item.Reason, short(tree1)) || !strings.Contains(item.Reason, "gate 2") {
				t.Errorf("T3 reason = %q, want the pass on %s failing its own historical gate 2", item.Reason, short(tree1))
			}
		}
	}
	if !found {
		t.Errorf("verify did not flag a historical pass missing a reading under its own gate set: %v", res.Items)
	}
}

// TestVerifyT3CurrentPassMissingGateStillFails checks the current pass's check
// is unchanged: with no corrections, a pass missing a reading for one of the
// brief's gates still fails T3 naming that gate.
func TestVerifyT3CurrentPassMissingGateStillFails(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree, RC: &rc, Persona: "supervisor"}); err != nil {
		t.Fatalf("append validated gate 1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
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
			if !strings.Contains(item.Reason, "gate 2") {
				t.Errorf("T3 reason = %q, want the pass failing its current gate 2", item.Reason)
			}
		}
	}
	if !found {
		t.Errorf("verify did not flag a current pass missing a current gate: %v", res.Items)
	}
}

// TestVerifyT3PassSplitAcrossTrees checks that readings split across two
// different trees do not qualify on the audit side either.
func TestVerifyT3PassSplitAcrossTrees(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\none\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	ta, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	tb, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package x\nthree\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	passed, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: ta, RC: &rc}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tb}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: passed}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	fails := verifyAll(t, dir, "T1")
	if !fails["T3"] {
		t.Error("T3 accepted readings split across two different trees")
	}
}

// TestVerifyT3SameTimestampCorrectionAfterPass is the tie-break guard for the
// issue #252 fix: the ledger is append-only, so a correction dispatched after
// a pass with the identical TS must not rewrite that pass's header. The pass
// at index i is judged by events[:i+1]; the later c1 with the same timestamp
// never enters its prefix, so its third gate does not apply.
func TestVerifyT3SameTimestampCorrectionAfterPass(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree1, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	for _, g := range []string{"1", "2"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: g, Tree: tree1, RC: &rc, Persona: "supervisor"}); err != nil {
			t.Fatalf("append validated gate %s: %v", g, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree1, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree1}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	// The correction carries the identical TS as the pass above but is
	// appended after it in the ledger, and adds a third gate.
	delta := deltaPath(t, dir, "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\ngate: exit 0\n\n# TASK: delta\n")
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Session: "w2", Brief: "delta.txt", SHA256: briefSHA(t, delta)}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		for _, item := range res.Items {
			if !item.Pass {
				t.Errorf("unexpected FAIL %s: %s", item.Rule, item.Reason)
			}
		}
	}
}

// TestVerifyT3SameTimestampCorrectionBeforePassApplies is the reverse of the
// tie-break guard: a correction that precedes a pass in the ledger with the
// same TS does apply to it. Without this, the guard above could be satisfied
// by ignoring corrections altogether.
func TestVerifyT3SameTimestampCorrectionBeforePassApplies(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree1, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	for _, g := range []string{"1", "2"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: g, Tree: tree1, RC: &rc, Persona: "supervisor"}); err != nil {
			t.Fatalf("append validated gate %s: %v", g, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree1, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	// The correction precedes the pass with the same TS and adds a third gate.
	delta := deltaPath(t, dir, "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\ngate: exit 0\n\n# TASK: delta\n")
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Session: "w2", Brief: "delta.txt", SHA256: briefSHA(t, delta)}); err != nil {
		t.Fatalf("append dispatched c1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree1}); err != nil {
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
				t.Errorf("T3 reason = %q, want the pass failing the correction's gate 3", item.Reason)
			}
		}
	}
	if !found {
		t.Errorf("verify did not apply a preceding correction to a same-timestamp pass: %v", res.Items)
	}
}

// TestVerifyT3HistoricalPassIgnoresLaterFreshDispatch is the issue #259
// correction's interaction guard: AttemptBrief now prefers a fresh attempt's
// dispatched header, so ruleT3's historical pass must still be measured
// against the header available when the pass was recorded — the prefix
// events[:i+1] resolves only the dispatch that existed before that
// inspection, never a later one. Pass 1 has a reading for gate 1 only; if it
// were measured against the later r2's 2-gate header, T3 would fail it
// missing gate 2. The later fresh dispatch must not drag the earlier pass
// up to its own gate set.
func TestVerifyT3HistoricalPassIgnoresLaterFreshDispatch(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	briefPath := filepath.Join(dir, "brief.txt")
	header1, err := ParseBriefHeader(briefPath)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if len(header1.Gates) != 1 {
		t.Fatalf("brief gates = %v, want 1", header1.Gates)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:30:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1", Brief: "brief.txt", SHA256: briefSHA(t, briefPath), Header: &header1}); err != nil {
		t.Fatalf("append dispatched r1: %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	tree1, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree1, RC: &rc, Persona: "supervisor"}); err != nil {
		t.Fatalf("append validated gate 1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree1, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree1}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	// The brief is edited to two gates; the lead re-records it (so T1's hash
	// check stays explained) and dispatches a fresh r2 carrying the 2-gate
	// header — after the first pass, and irrelevant to it.
	twoGates := "owns: a.go\nneeds: none\ngate: exit 0\ngate: exit 0\n\n# TASK: w259\n"
	if err := os.WriteFile(briefPath, []byte(twoGates), 0o644); err != nil {
		t.Fatalf("edit brief: %v", err)
	}
	header2, err := ParseBriefHeader(briefPath)
	if err != nil {
		t.Fatalf("ParseBriefHeader() after edit error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:30:00Z", Task: "T1", Kind: "amended", Brief: "brief.txt", Header: &header2}); err != nil {
		t.Fatalf("append amended: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r2", Session: "w2", Brief: "brief.txt", SHA256: briefSHA(t, briefPath), Header: &header2}); err != nil {
		t.Fatalf("append dispatched r2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:00:01Z", Task: "T1", Kind: "finished", Attempt: "r2", Session: "w2", RC: &rc}); err != nil {
		t.Fatalf("append finished r2: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\nchanged\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	tree2, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	for _, g := range []string{"1", "2"} {
		if err := AppendEvent(dir, Event{TS: "2026-09-16T02:10:00Z", Task: "T1", Kind: "validated", Gate: g, Tree: tree2, RC: &rc, Persona: "supervisor"}); err != nil {
			t.Fatalf("append validated gate %s: %v", g, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:10:01Z", Task: "T1", Kind: "owns_checked", Tree: tree2, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T02:10:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i2", Persona: "inspector", Tree: tree2}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		for _, item := range res.Items {
			if !item.Pass {
				t.Errorf("unexpected FAIL %s: %s", item.Rule, item.Reason)
			}
		}
	}
}
