package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestVerifyExternalWorkdirPassUnresolvableTree is the issue #244 guard: a
// ledger whose pass was measured on a tree this repository does not have (an
// external --workdir clone), with complete readings for every gate, reports
// T3 passing. allReadings is pure event matching and needs no git, so an
// unresolvable tree must never short-circuit a genuine pass.
func TestVerifyExternalWorkdirPassUnresolvableTree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	external, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() external error = %v", err)
	}
	// A file only the external clone has, so its tree object is genuinely
	// absent from dir's object database.
	if err := os.WriteFile(filepath.Join(external, "extra.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write extra.go: %v", err)
	}
	tree, err := treeHash(external)
	if err != nil {
		t.Fatalf("treeHash(external) error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree, RC: &rc, Persona: "supervisor"}); err != nil {
		t.Fatalf("append validated: %v", err)
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
	if !res.Passed {
		for _, item := range res.Items {
			if !item.Pass {
				t.Errorf("unexpected %s %s: %s", item.Rule, map[bool]string{true: "INCONCLUSIVE", false: "FAIL"}[item.Inconclusive], item.Reason)
			}
		}
	}
}

// TestVerifyExternalWorkdirMissingGateInconclusive is the issue #244 flip
// side: the same unresolvable tree but with a gate reading missing cannot be
// established as a violation — T3 reports inconclusive (Pass false,
// Inconclusive true, the exit-7 path), never a violation (exit 6).
func TestVerifyExternalWorkdirMissingGateInconclusive(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	external, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() external error = %v", err)
	}
	// A file only the external clone has, so its tree object is genuinely
	// absent from dir's object database.
	if err := os.WriteFile(filepath.Join(external, "extra.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write extra.go: %v", err)
	}
	tree, err := treeHash(external)
	if err != nil {
		t.Fatalf("treeHash(external) error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree, RC: &rc, Persona: "supervisor"}); err != nil {
		t.Fatalf("append validated: %v", err)
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
		if item.Rule == "T3" {
			found = true
			if item.Pass {
				t.Errorf("T3 passed an unverifiable pass")
			}
			if !item.Inconclusive {
				t.Errorf("T3 = %+v, want inconclusive (unresolvable tree must not be a violation)", item)
			}
		}
	}
	if !found {
		t.Errorf("VerifyTasks() items = %v, want an inconclusive T3", res.Items)
	}
	if res.Passed {
		t.Error("VerifyTasks() passed a result containing an inconclusive check")
	}
}

// TestVerifyExternalWorkdirViolationStillFails is the guard that inconclusive
// never swallowed real failures: a genuine violation with a resolvable tree
// still fails (Pass false, Inconclusive false — the exit-6 path).
func TestVerifyExternalWorkdirViolationStillFails(t *testing.T) {
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
	found := false
	for _, item := range res.Items {
		if item.Rule == "T3" && !item.Pass {
			found = true
			if item.Inconclusive {
				t.Errorf("T3 = %+v, want an established violation, not inconclusive", item)
			}
		}
	}
	if !found {
		t.Errorf("VerifyTasks() items = %v, want a failing T3", res.Items)
	}
}

// TestVerifyExternalWorkdirResolvedByFlag checks the --workdir path: a
// verifier pointed at the repository that does have the tree resolves it and
// verifies normally — a missing gate there is an established violation, not
// inconclusive.
func TestVerifyExternalWorkdirResolvedByFlag(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	external, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() external error = %v", err)
	}
	// A file only the external clone has, so its tree object is genuinely
	// absent from dir's object database.
	if err := os.WriteFile(filepath.Join(external, "extra.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write extra.go: %v", err)
	}
	tree, err := treeHash(external)
	if err != nil {
		t.Fatalf("treeHash(external) error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree, RC: &rc, Persona: "supervisor"}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree, Persona: "supervisor"}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}, Workdir: external})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	found := false
	for _, item := range res.Items {
		if item.Rule == "T3" && !item.Pass {
			found = true
			if item.Inconclusive {
				t.Errorf("T3 = %+v, want a resolvable violation, not inconclusive", item)
			}
		}
	}
	if !found {
		t.Errorf("VerifyTasks() items = %v, want a failing T3 missing gate 2", res.Items)
	}
}

// TestVerifyExternalWorkdirResolvedFromRecordedWorkdir checks the fallback:
// when no --workdir is given, a workdir recorded on the task's own reading
// events (validated, owns_checked, inspected) resolves the tree when it still
// exists, and the pass verifies normally.
func TestVerifyExternalWorkdirResolvedFromRecordedWorkdir(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	external, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() external error = %v", err)
	}
	// A file only the external clone has, so its tree object is genuinely
	// absent from dir's object database.
	if err := os.WriteFile(filepath.Join(external, "extra.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write extra.go: %v", err)
	}
	tree, err := treeHash(external)
	if err != nil {
		t.Fatalf("treeHash(external) error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree, RC: &rc, Persona: "supervisor", Workdir: external}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "owns_checked", Tree: tree, Persona: "supervisor", Workdir: external}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		for _, item := range res.Items {
			if !item.Pass {
				t.Errorf("unexpected %s %s: %s", item.Rule, map[bool]string{true: "INCONCLUSIVE", false: "FAIL"}[item.Inconclusive], item.Reason)
			}
		}
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

// TestVerifyT3EmptyTreeIsViolation is the correction guard: an inspected pass
// whose tree is the empty string cannot name an object in any repository, so
// it is a malformed record, not a check that could not be established. It is
// reported a violation (exit 6), never inconclusive (exit 8) — a verifier that
// under-reports a violation is worse than one that cannot run.
func TestVerifyT3EmptyTreeIsViolation(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector"}); err != nil {
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
			if item.Inconclusive {
				t.Errorf("T3 = %+v, want an established violation, not inconclusive, for an empty tree", item)
			}
		}
	}
	if !found {
		t.Errorf("VerifyTasks() items = %v, want a failing T3 for an empty inspected tree", res.Items)
	}
}

// TestVerifyPerPassWorkdirResolvesEachPass is the correction guard: a task can
// accumulate readings from several workdirs across attempts, so each inspected
// pass must resolve its repository from that pass's own recorded workdirs,
// preferring the one that actually contains the inspected tree. An early
// rework inspection recorded in a different workdir must not pin this pass to
// the wrong object database and degrade an established violation into
// inconclusive.
func TestVerifyPerPassWorkdirResolvesEachPass(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	external1, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() external1 error = %v", err)
	}
	external2, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() external2 error = %v", err)
	}
	// Distinct trees: a file only each clone has, so its tree object is
	// genuinely absent from dir and from the other clone's database.
	if err := os.WriteFile(filepath.Join(external1, "one.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write one.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(external2, "two.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write two.go: %v", err)
	}
	tree1, err := treeHash(external1)
	if err != nil {
		t.Fatalf("treeHash(external1) error = %v", err)
	}
	tree2, err := treeHash(external2)
	if err != nil {
		t.Fatalf("treeHash(external2) error = %v", err)
	}
	// An early rework inspection measured in external1: it carries no T3
	// readings, but its recorded workdir must not pin the later pass to
	// external1's object database.
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "inspected", Verdict: "rework", Session: "i1", Persona: "inspector", Tree: tree1, Workdir: external1}); err != nil {
		t.Fatalf("append inspected rework: %v", err)
	}
	rc := 0
	// The second pass's readings live in external2 and are incomplete: gate 1
	// validated only, gate 2 and owns_checked missing, so T3 must fail it.
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree2, RC: &rc, Persona: "supervisor", Workdir: external2}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:03Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree2}); err != nil {
		t.Fatalf("append inspected pass: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	found := false
	for _, item := range res.Items {
		if item.Rule == "T3" && !item.Pass {
			found = true
			if item.Inconclusive {
				t.Errorf("T3 = %+v, want an established violation resolved against external2, not inconclusive", item)
			}
		}
	}
	if !found {
		t.Errorf("VerifyTasks() items = %v, want a failing T3 for the external2 pass", res.Items)
	}
}

// TestVerifyRelativeWorkdirRecordedAbsoluteAndResolves is the correction
// guard: a relative --workdir names a place against the process's current
// directory, so a reading recorded with it must persist as an absolute,
// canonical path — otherwise the same string later names a different place,
// or nothing. The ledger read from another directory still resolves the
// recorded repository.
func TestVerifyRelativeWorkdirRecordedAbsoluteAndResolves(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ledger")
	ext := filepath.Join(base, "external")
	for _, p := range []string{dir, ext} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if _, err := Init(p, false); err != nil {
			t.Fatalf("Init(%s) error = %v", p, err)
		}
		initRepo(t, p)
		if err := os.WriteFile(filepath.Join(p, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
			t.Fatalf("write .gitignore: %v", err)
		}
		brief := "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK: relative\n"
		if err := os.WriteFile(filepath.Join(p, "brief.txt"), []byte(brief), 0o644); err != nil {
			t.Fatalf("write brief: %v", err)
		}
		if err := AppendEvent(p, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
			t.Fatalf("AppendEvent planned: %v", err)
		}
		git(t, p, []string{"add", "-A"})
		git(t, p, []string{"commit", "-m", "brief"})
	}
	// A file only the external clone has, so its tree object is genuinely
	// absent from dir's object database.
	if err := os.WriteFile(filepath.Join(ext, "extra.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write extra.go: %v", err)
	}
	// Record a reading from base with relative paths; the recorded workdir
	// must come out absolute.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(base); err != nil {
		t.Fatalf("Chdir(%s) error = %v", base, err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()
	if err := AppendEvent("ledger", Event{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "w1"}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	if err := InspectTask("ledger", "T1", InspectOptions{Dir: "ledger", Workdir: "external", Verdict: "rework", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() relative error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	recorded := ""
	for _, e := range evs {
		if e.Kind == "inspected" {
			recorded = e.Workdir
		}
	}
	// The recording normalises the workdir with absPath to canonical form:
	// filepath.Abs, then filepath.EvalSymlinks (which follows symlinks on
	// Unix and expands DOS 8.3 short names on Windows), then Clean. The
	// expected path is canonicalised the same way, so the comparison is
	// spelling-independent — no platform caveat needed.
	want := absPath(ext)
	if recorded != want {
		t.Fatalf("recorded workdir = %q, want the canonical path %q", recorded, want)
	}
	// A complete pass measured in ext verifies from an unrelated directory:
	// the recorded absolute path still resolves the tree.
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("Chdir(%s) error = %v", cwd, err)
	}
	tree, err := treeHash(ext)
	if err != nil {
		t.Fatalf("treeHash(ext) error = %v", err)
	}
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "validated", Gate: "1", Tree: tree, RC: &rc, Persona: "supervisor", Workdir: ext}); err != nil {
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:02Z", Task: "T1", Kind: "owns_checked", Tree: tree, Persona: "supervisor", Workdir: ext}); err != nil {
		t.Fatalf("append owns_checked: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:03Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
		t.Fatalf("append inspected pass: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	if !res.Passed {
		for _, item := range res.Items {
			if !item.Pass {
				t.Errorf("unexpected %s %s: %s", item.Rule, map[bool]string{true: "INCONCLUSIVE", false: "FAIL"}[item.Inconclusive], item.Reason)
			}
		}
	}
}

// TestVerifyAliasWorkdirRecordsNoWorkdir is the behavioural point of the
// canonical workdir normalisation (issue #244): a --workdir spelled as an
// alias of the repo dir — a symlink on Unix, the DOS 8.3 short name on
// Windows — is the same directory, so it is not external and records no
// workdir field. Before absPath resolved EvalSymlinks, an alias read as a
// different path and leaked into the ledger as a fake external workdir.
func TestVerifyAliasWorkdirRecordsNoWorkdir(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	alias := dirAlias(t, dir)
	if alias == "" {
		t.Skip("this host has no alias spelling of a directory (no symlink privilege, 8.3 names disabled)")
	}
	logFinished(t, dir, "T1", "w1")
	if err := InspectTask(dir, "T1", InspectOptions{Dir: dir, Workdir: alias, Verdict: "rework", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask() alias error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "inspected" && e.Workdir != "" {
			t.Errorf("inspected workdir = %q, want none recorded for an alias of the repo dir", e.Workdir)
		}
	}
}

// dirAlias returns a different spelling of dir that canonicalises to the
// same directory: a symlink on Unix, the DOS 8.3 short name on Windows
// (asked of cmd, since 8.3 expansion is the one canonicalisation the
// standard library's path code cannot produce). "" when the host cannot
// make one: no symlink privilege, or 8.3 names disabled for the volume.
// The for item must not be quoted: cmd keeps a quoted item's quotes when
// expanding %~sI, and the short name of a quoted string is meaningless.
func dirAlias(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		out, err := exec.Command("cmd", "/c", `for %I in (`+dir+`) do @echo %~sI`).CombinedOutput()
		if err != nil {
			return ""
		}
		short := strings.TrimSpace(string(out))
		if short == "" || strings.EqualFold(short, dir) {
			return ""
		}
		return short
	}
	alias := filepath.Join(filepath.Dir(dir), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		return ""
	}
	return alias
}

// TestVerifyInvalidWorkdirIsErrorNotInconclusive is the correction guard: an
// operational git failure — a --workdir that is not a readable repository — is
// an error naming the path, never an inconclusive verdict. A verifier that
// cannot read the object database must not report "could not establish".
func TestVerifyInvalidWorkdirIsErrorNotInconclusive(t *testing.T) {
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
		t.Fatalf("append validated: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T01:00:01Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector", Tree: tree}); err != nil {
		t.Fatalf("append inspected: %v", err)
	}
	// A real directory that is not a git repository: the tree is present in
	// dir, but --workdir points the verifier somewhere it cannot read.
	notRepo := filepath.Join(t.TempDir(), "not-a-repo")
	if err := os.MkdirAll(notRepo, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", notRepo, err)
	}
	_, err = VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}, Workdir: notRepo})
	if err == nil {
		t.Fatal("VerifyTasks() = nil error, want an error naming the invalid workdir")
	}
	// ruleT3 canonicalises the workdir with absPath before git runs and the
	// error names that canonical form, so the expected spelling is
	// canonicalised the same way: t.TempDir() may be an alias — an 8.3
	// short name on Windows, a symlink like /var on macOS — and the raw
	// spelling would not appear in the error on such a host (issue #244).
	want := absPath(notRepo)
	if !strings.Contains(err.Error(), want) {
		t.Errorf("VerifyTasks() error = %q, want it to name %s", err, want)
	}
}

// TestVerifyT5LandedOnException checks a landed event preceded by an excepted
// event from a lead session passes T5 with the exception noted.
func TestVerifyT5LandedOnException(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "excepted", Commit: "abc1234", Session: "lead-1", Note: "ran go test by hand", Reason: "status planned"}); err != nil {
		t.Fatalf("append excepted: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "landed", Commit: "abc1234", Note: "exception: ran go test by hand"}); err != nil {
		t.Fatalf("append landed: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	for _, item := range res.Items {
		if item.Rule == "T5" {
			if !item.Pass {
				t.Errorf("T5 failed: %s", item.Reason)
			}
			if !strings.Contains(item.Reason, "exception") {
				t.Errorf("T5 reason should mention exception: %s", item.Reason)
			}
			if !strings.Contains(item.Reason, "lead-1") {
				t.Errorf("T5 reason should mention session lead-1: %s", item.Reason)
			}
			return
		}
	}
	t.Errorf("VerifyTasks() items = %v, want a T5 item mentioning the exception", res.Items)
}

// TestVerifyT4ExceptionFromWorkerSession checks an excepted event whose
// session wrote the task's finished event fails T4.
func TestVerifyT4ExceptionFromWorkerSession(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "finished", Session: "w1"}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "excepted", Commit: "abc1234", Session: "w1", Note: "ran go test by hand", Reason: "status planned"}); err != nil {
		t.Fatalf("append excepted: %v", err)
	}
	res, err := VerifyTasks(dir, VerifyOptions{Dir: dir, Tasks: []string{"T1"}})
	if err != nil {
		t.Fatalf("VerifyTasks() error = %v", err)
	}
	for _, item := range res.Items {
		if item.Rule == "T4" && !item.Pass {
			if !strings.Contains(item.Reason, "worker session") {
				t.Errorf("T4 reason should mention worker session: %s", item.Reason)
			}
			return
		}
	}
	t.Errorf("VerifyTasks() items = %v, want a failing T4 for worker session in excepted event", res.Items)
}
