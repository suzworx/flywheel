package flywheel

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppendReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	e := Event{
		TS:   "2026-09-12T00:00:00Z",
		Task: "T1",
		Kind: "planned",
		Owns: []string{"a.go"},
		Note: `back\slash "quoted" & <tag>`,
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	got := evs[0]
	if got.TS != e.TS || got.Task != e.Task || got.Kind != e.Kind {
		t.Errorf("round trip mismatch: got %v", got)
	}
	if got.Note != e.Note {
		t.Errorf("note = %q, want %q", got.Note, e.Note)
	}
	if len(got.Owns) != 1 || got.Owns[0] != "a.go" {
		t.Errorf("owns = %v, want [a.go]", got.Owns)
	}

	// HTML escaping is off: raw bytes carry < and & literally.
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "<") || !strings.Contains(content, "&") {
		t.Errorf("note was HTML-escaped in the log: %q", content)
	}
	if strings.Contains(content, `\u003c`) || strings.Contains(content, `\u0026`) {
		t.Errorf("note was HTML-escaped in the log: %q", content)
	}
}

// TestAppendReadRoundTripAttributed checks the owns_checked event's
// Attributed field round-trips through AppendEvent/ReadEvents and is written
// to the log under the "attributed" key (issue #117).
func TestAppendReadRoundTripAttributed(t *testing.T) {
	dir := t.TempDir()
	e := Event{
		TS: "2026-09-12T00:00:00Z", Task: "A", Kind: "owns_checked",
		Attempt: "r1", Tree: "deadbeef", Attributed: []string{"theirs.go -> B"},
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	if got := evs[0].Attributed; len(got) != 1 || got[0] != "theirs.go -> B" {
		t.Errorf("attributed = %v, want [theirs.go -> B]", got)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(b), `"attributed":["theirs.go -> B"]`) {
		t.Errorf("events.jsonl = %q, want it to contain the attributed key", string(b))
	}
}

func TestAppendSetsTimestampWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "", Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	if evs[0].TS == "" {
		t.Error("AppendEvent() did not stamp TS")
	}
}

func TestValidateRejectsBadTask(t *testing.T) {
	if err := Validate(Event{Task: "T1!", Kind: "planned"}); err == nil {
		t.Error("Validate() accepted task id with '!'")
	} else if !strings.Contains(err.Error(), "task") {
		t.Errorf("Validate() error = %v, want task message", err)
	}
	if err := Validate(Event{Task: "", Kind: "planned"}); err == nil {
		t.Error("Validate() accepted empty task id")
	}
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	if err := Validate(Event{Task: "T1", Kind: "frobnicated"}); err == nil {
		t.Error("Validate() accepted unknown kind")
	} else if !strings.Contains(err.Error(), "kind") {
		t.Errorf("Validate() error = %v, want kind message", err)
	}
}

func TestValidateStaffed(t *testing.T) {
	if err := Validate(Event{Kind: "staffed", Session: "s1"}); err != nil {
		t.Errorf("Validate() rejected staffed without task: %v", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "staffed", Session: "s1"}); err != nil {
		t.Errorf("Validate() rejected staffed with task: %v", err)
	}
	if err := Validate(Event{Kind: "staffed"}); err == nil {
		t.Error("Validate() accepted staffed without session")
	} else if !strings.Contains(err.Error(), "session") {
		t.Errorf("Validate() error = %v, want session message", err)
	}
}

func TestStaffedDefaultsPersonaToLead(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "s1"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	if evs[0].Persona != "lead" {
		t.Errorf("staffed persona = %q, want lead (default when empty)", evs[0].Persona)
	}
	if evs[0].Session != "s1" {
		t.Errorf("staffed session = %q, want s1", evs[0].Session)
	}
}

// TestPlannedAndAmendedDefaultPersonaToPlanner checks every ingestion route
// through AppendEvent — the JSON log path and the generic flag path included —
// records the planner persona on planned and amended events, and that an
// explicit persona wins.
func TestPlannedAndAmendedDefaultPersonaToPlanner(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:00:00Z", Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() planned error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:00:01Z", Task: "T1", Kind: "amended"}); err != nil {
		t.Fatalf("AppendEvent() amended error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:00:02Z", Task: "T2", Kind: "planned", Persona: "foreman"}); err != nil {
		t.Fatalf("AppendEvent() explicit persona error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("ReadEvents() = %d events, want 3", len(evs))
	}
	if evs[0].Persona != "planner" {
		t.Errorf("planned persona = %q, want planner (default when empty)", evs[0].Persona)
	}
	if evs[1].Persona != "planner" {
		t.Errorf("amended persona = %q, want planner (default when empty)", evs[1].Persona)
	}
	if evs[2].Persona != "foreman" {
		t.Errorf("planned persona = %q, want foreman (a set value wins)", evs[2].Persona)
	}
}

func TestValidateRejectsBadAttempt(t *testing.T) {
	if err := Validate(Event{Task: "T1", Kind: "dispatched", Attempt: "x1"}); err == nil {
		t.Error("Validate() accepted attempt x1")
	}
	if err := Validate(Event{Task: "T1", Kind: "dispatched", Attempt: "r"}); err == nil {
		t.Error("Validate() accepted attempt r (no digits)")
	}
	if err := Validate(Event{Task: "T1", Kind: "dispatched", Attempt: "c2"}); err != nil {
		t.Errorf("Validate() rejected valid attempt c2: %v", err)
	}
}

func TestValidateRejectsReviewedWithoutVerdict(t *testing.T) {
	if err := Validate(Event{Task: "T1", Kind: "reviewed"}); err == nil {
		t.Error("Validate() accepted reviewed event without verdict")
	} else if !strings.Contains(err.Error(), "verdict") {
		t.Errorf("Validate() error = %v, want verdict message", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "reviewed", Verdict: "pass"}); err != nil {
		t.Errorf("Validate() rejected reviewed/pass: %v", err)
	}
}

func TestAppendTornLastLineGetsNewlinePrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	// A torn write: a full record with no trailing newline.
	torn := `{"ts":"2026-09-12T00:00:00Z","task":"T0","kind":"planned"}`
	if err := os.WriteFile(path, []byte(torn), 0o644); err != nil {
		t.Fatalf("write torn line: %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:01Z", Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("ReadEvents() = %d events, want 2", len(evs))
	}
	if evs[0].Task != "T0" || evs[1].Task != "T1" {
		t.Errorf("events spliced by torn write: %v", evs)
	}
}

func TestReadEventsConflictMarkerNamesLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	if err := os.WriteFile(path, []byte("<<<<<<< HEAD\n"), 0o644); err != nil {
		t.Fatalf("write conflict marker: %v", err)
	}

	_, err := ReadEvents(dir)
	if err == nil {
		t.Fatal("ReadEvents() accepted a conflict marker")
	}
	if !strings.Contains(err.Error(), "unresolved merge conflict") {
		t.Errorf("ReadEvents() error = %v, want 'unresolved merge conflict'", err)
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Errorf("ReadEvents() error = %v, want line number", err)
	}
}

func TestParseStrictRejectsUnknownFieldLenientAccepts(t *testing.T) {
	line := []byte(`{"ts":"2026-09-12T00:00:00Z","task":"T1","kind":"planned","bogus":1}`)

	if _, err := ParseEvents(bytes.NewReader(line), true); err == nil {
		t.Error("strict ParseEvents() accepted unknown field")
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("strict ParseEvents() error = %v, want unknown field bogus", err)
	}

	evs, err := ParseEvents(bytes.NewReader(line), false)
	if err != nil {
		t.Fatalf("lenient ParseEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("lenient ParseEvents() = %d events, want 1", len(evs))
	}
	if evs[0].Task != "T1" || evs[0].Kind != "planned" {
		t.Errorf("lenient parse mismatch: %v", evs[0])
	}
}

func TestAppendConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: fmt.Sprintf("T%d", i), Kind: "planned"})
		}(i)
	}
	wg.Wait()

	for i := 0; i < 50; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d AppendEvent() error = %v", i, errs[i])
		}
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 50 {
		t.Fatalf("ReadEvents() = %d events, want 50", len(evs))
	}
	seen := map[string]bool{}
	for _, e := range evs {
		seen[e.Task] = true
		if err := Validate(e); err != nil {
			t.Errorf("event %v failed validation: %v", e, err)
		}
	}
	if len(seen) != 50 {
		t.Errorf("distinct tasks = %d, want 50", len(seen))
	}
}

func TestNewKindsValidate(t *testing.T) {
	for _, k := range []string{"worker_plan", "no-plan", "report", "lost"} {
		if err := Validate(Event{Task: "T1", Kind: k}); err != nil {
			t.Errorf("Validate() rejected kind %s: %v", k, err)
		}
	}
	if err := Validate(Event{Task: "T1", Kind: "bogus"}); err == nil {
		t.Error("Validate() accepted unknown kind")
	} else if !strings.Contains(err.Error(), "worker_plan") || !strings.Contains(err.Error(), "no-plan") ||
		!strings.Contains(err.Error(), "report") || !strings.Contains(err.Error(), "lost") {
		t.Errorf("Validate() error = %v, want the full kind list", err)
	}
}

// TestNoPlanKindRoundTrips checks the no-plan kind carries its task and
// attempt like the other run kinds and round-trips through the event log
// (issue #65).
func TestNoPlanKindRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-15T00:00:00Z", Task: "T1", Kind: "no-plan", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	if evs[0].Kind != "no-plan" || evs[0].Task != "T1" || evs[0].Attempt != "r1" {
		t.Errorf("no-plan round trip mismatch: %v", evs[0])
	}
	if err := Validate(Event{Task: "T1", Kind: "no-plan", Attempt: "bad"}); err == nil {
		t.Error("Validate() accepted no-plan with a malformed attempt")
	}
}

// TestOffCourseKindRoundTrips checks the off-course kind carries its task,
// attempt and note like the other run kinds and round-trips through the
// event log (issue #72).
func TestOffCourseKindRoundTrips(t *testing.T) {
	dir := t.TempDir()
	note := "/outside/a.go, /outside/b.go, /outside/c.go, /outside/d.go, /outside/e.go"
	if err := AppendEvent(dir, Event{TS: "2026-09-15T00:00:00Z", Task: "T1", Kind: "off-course", Attempt: "r1", Note: note}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	if evs[0].Kind != "off-course" || evs[0].Task != "T1" || evs[0].Attempt != "r1" || evs[0].Note != note {
		t.Errorf("off-course round trip mismatch: %v", evs[0])
	}
	if err := Validate(Event{Task: "T1", Kind: "off-course", Attempt: "bad"}); err == nil {
		t.Error("Validate() accepted off-course with a malformed attempt")
	}
}

// TestSignalKindValidate checks the signal kind (issue #37): a signal event
// requires a Signal from the exported Signals set and a task; an unknown value
// is rejected with a message naming it and the allowed set; and no other kind
// may carry a Signal.
func TestSignalKindValidate(t *testing.T) {
	for _, s := range []string{"no-plan", "off-course", "no-writes", "capped", "provider-error", "stalled", "silent", "failed-dirty", "git-write", "permission-denied"} {
		if err := Validate(Event{Task: "T1", Kind: "signal", Signal: s}); err != nil {
			t.Errorf("Validate() rejected signal/%s: %v", s, err)
		}
	}
	if err := Validate(Event{Task: "T1", Kind: "signal"}); err == nil {
		t.Error("Validate() accepted a signal event without a signal")
	} else if !strings.Contains(err.Error(), "signal") {
		t.Errorf("Validate() error = %v, want signal message", err)
	}
	if err := Validate(Event{Kind: "signal", Signal: "no-plan"}); err == nil {
		t.Error("Validate() accepted a signal event without a task")
	} else if !strings.Contains(err.Error(), "task") {
		t.Errorf("Validate() error = %v, want task message", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "signal", Signal: "bogus"}); err == nil {
		t.Error("Validate() accepted an unknown signal value")
	} else if !strings.Contains(err.Error(), `"bogus"`) ||
		!strings.Contains(err.Error(), "no-plan") || !strings.Contains(err.Error(), "failed-dirty") {
		t.Errorf("Validate() error = %v, want it naming the value and the allowed set", err)
	}
	for _, k := range []string{"planned", "no-plan", "finished", "off-course"} {
		if err := Validate(Event{Task: "T1", Kind: k, Signal: "no-plan"}); err == nil {
			t.Errorf("Validate() accepted a signal on kind %s", k)
		} else if !strings.Contains(err.Error(), "signal") {
			t.Errorf("Validate() error = %v, want signal message", err)
		}
	}
	if err := Validate(Event{Task: "T1", Kind: "bogus"}); err == nil {
		t.Error("Validate() accepted unknown kind")
	} else if !strings.Contains(err.Error(), "signal") {
		t.Errorf("Validate() error = %v, want signal listed in the kind message", err)
	}
}

// TestSignalFieldRoundTrips checks a signal event round-trips with the
// condition under the "signal" key, which is omitted when empty (issue #37).
func TestSignalFieldRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{
		TS: "2026-09-16T00:00:00Z", Task: "T1", Kind: "signal", Signal: "capped",
		Attempt: "r1", Session: "s1", Path: ".flywheel/runs/T1.r1.jsonl",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:01Z", Task: "T2", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() plain error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("ReadEvents() = %d events, want 2", len(evs))
	}
	got := evs[0]
	if got.Kind != "signal" || got.Signal != "capped" || got.Attempt != "r1" ||
		got.Session != "s1" || got.Path != ".flywheel/runs/T1.r1.jsonl" {
		t.Errorf("signal round trip mismatch: %v", got)
	}
	if evs[1].Signal != "" {
		t.Errorf("plain event signal = %q, want empty", evs[1].Signal)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("events.jsonl = %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `"signal":"capped"`) {
		t.Errorf("line 0 = %q, want signal:\"capped\"", lines[0])
	}
	if strings.Contains(lines[1], "signal") {
		t.Errorf("line 1 = %q, want signal omitted when empty", lines[1])
	}
}

func TestEventNewFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	toks := new(Tokens)
	*toks = Tokens{Input: 100, Output: 20, Reasoning: 5, CacheRead: 900, CacheWrite: 10}
	e := Event{
		TS:      "2026-09-12T00:00:00Z",
		Task:    "T1",
		Kind:    "finished",
		Session: "ses_test_1",
		Adapter: "opencode",
		Path:    ".flywheel/runs/T1.r1.jsonl",
		SHA256:  "abc123",
		Tokens:  toks,
		Cost:    0.004,
		Steps:   12,
		Reason:  "stop",
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	got := evs[0]
	if got.Adapter != "opencode" || got.Path != ".flywheel/runs/T1.r1.jsonl" || got.SHA256 != "abc123" {
		t.Errorf("round trip adapter/path/sha256 mismatch: %v", got)
	}
	if got.Cost != 0.004 || got.Steps != 12 {
		t.Errorf("round trip cost/steps mismatch: %v", got)
	}
	if got.Tokens == nil || got.Tokens.Input != 100 || got.Tokens.Output != 20 ||
		got.Tokens.Reasoning != 5 || got.Tokens.CacheRead != 900 || got.Tokens.CacheWrite != 10 {
		t.Errorf("round trip tokens mismatch: %v", got.Tokens)
	}

	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	content := string(b)
	for _, k := range []string{`"adapter"`, `"path"`, `"sha256"`, `"tokens"`, `"cost"`, `"steps"`, `"cache_read"`} {
		if !strings.Contains(content, k) {
			t.Errorf("log missing key %s: %q", k, content)
		}
	}

	// A plain event must not emit the new optional fields.
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:01Z", Task: "T2", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() plain error = %v", err)
	}
	b, err = os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("re-read events.jsonl: %v", err)
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 2 || strings.Contains(lines[1], "adapter") || strings.Contains(lines[1], "tokens") {
		t.Errorf("plain event emitted the new optional fields: %q", lines[1])
	}
}

func TestValidateNewGaugeKinds(t *testing.T) {
	if err := Validate(Event{Task: "T1", Kind: "validated"}); err == nil {
		t.Error("Validate() accepted validated without gate and tree")
	} else if !strings.Contains(err.Error(), "gate and tree") {
		t.Errorf("Validate() error = %v, want gate and tree message", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "validated", Gate: "1", Tree: "abc123"}); err != nil {
		t.Errorf("Validate() rejected validated/gate/tree: %v", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "inspected"}); err == nil {
		t.Error("Validate() accepted inspected without verdict")
	} else if !strings.Contains(err.Error(), "verdict") {
		t.Errorf("Validate() error = %v, want verdict message", err)
	}
	for _, v := range []string{"pass", "rework", "scrap", "escalate"} {
		if err := Validate(Event{Task: "T1", Kind: "inspected", Verdict: v}); err != nil {
			t.Errorf("Validate() rejected inspected/%s: %v", v, err)
		}
	}
	if err := Validate(Event{Task: "T1", Kind: "inspected", Verdict: "maybe"}); err == nil {
		t.Error("Validate() accepted inspected verdict maybe")
	}
	if err := Validate(Event{Task: "T1", Kind: "owns_checked"}); err != nil {
		t.Errorf("Validate() rejected owns_checked: %v", err)
	}
}

// TestSessionKindsValidate checks the three session-boundary kinds added for
// issue #62: session_start and session_end require a session, session_command
// requires a session and a note, and the unknown-kind error message lists all
// three (task stays optional on every one, like staffed).
func TestSessionKindsValidate(t *testing.T) {
	for _, k := range []string{"session_start", "session_end"} {
		if err := Validate(Event{Kind: k}); err == nil {
			t.Errorf("Validate() accepted %s without session", k)
		} else if !strings.Contains(err.Error(), "session") {
			t.Errorf("Validate() error = %v, want session message", err)
		}
		if err := Validate(Event{Kind: k, Session: "s1"}); err != nil {
			t.Errorf("Validate() rejected %s with session: %v", k, err)
		}
		if err := Validate(Event{Task: "T1", Kind: k, Session: "s1"}); err != nil {
			t.Errorf("Validate() rejected %s carrying a task: %v", k, err)
		}
	}
	if err := Validate(Event{Kind: "session_command"}); err == nil {
		t.Error("Validate() accepted session_command without session")
	} else if !strings.Contains(err.Error(), "session") {
		t.Errorf("Validate() error = %v, want session message", err)
	}
	if err := Validate(Event{Kind: "session_command", Session: "s1"}); err == nil {
		t.Error("Validate() accepted session_command without note")
	} else if !strings.Contains(err.Error(), "note") {
		t.Errorf("Validate() error = %v, want note message", err)
	}
	if err := Validate(Event{Kind: "session_command", Session: "s1", Note: "flywheel inspect t1"}); err != nil {
		t.Errorf("Validate() rejected valid session_command: %v", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "bogus"}); err == nil {
		t.Error("Validate() accepted unknown kind")
	} else if !strings.Contains(err.Error(), "session_start") || !strings.Contains(err.Error(), "session_command") ||
		!strings.Contains(err.Error(), "session_end") {
		t.Errorf("Validate() error = %v, want the session kinds listed", err)
	}
}

func TestValidateGoalEvent(t *testing.T) {
	ok := Event{Kind: "goal", Goal: &GoalSpec{ID: "g1", Title: "Ship", Status: "active"}}
	if err := Validate(ok); err != nil {
		t.Errorf("Validate() rejected a valid goal event: %v", err)
	}
	if err := Validate(Event{Kind: "goal"}); err == nil {
		t.Error("Validate() accepted a goal event without a goal spec")
	} else if !strings.Contains(err.Error(), "goal spec") {
		t.Errorf("Validate() error = %v, want goal spec message", err)
	}
	for _, id := range []string{"", "g1!", "not valid"} {
		if err := Validate(Event{Kind: "goal", Goal: &GoalSpec{ID: id, Title: "Ship", Status: "active"}}); err == nil {
			t.Errorf("Validate() accepted goal id %q", id)
		}
	}
	if err := Validate(Event{Kind: "goal", Goal: &GoalSpec{ID: "g1", Title: "", Status: "active"}}); err == nil {
		t.Error("Validate() accepted an empty goal title")
	} else if !strings.Contains(err.Error(), "title") {
		t.Errorf("Validate() error = %v, want title message", err)
	}
	for _, s := range []string{"active", "met", "failed", "abandoned"} {
		if err := Validate(Event{Kind: "goal", Goal: &GoalSpec{ID: "g1", Title: "Ship", Status: s}}); err != nil {
			t.Errorf("Validate() rejected goal status %s: %v", s, err)
		}
	}
	if err := Validate(Event{Kind: "goal", Goal: &GoalSpec{ID: "g1", Title: "Ship", Status: "maybe"}}); err == nil {
		t.Error("Validate() accepted goal status maybe")
	} else if !strings.Contains(err.Error(), "status") {
		t.Errorf("Validate() error = %v, want status message", err)
	}
}

func TestValidateLearningEvent(t *testing.T) {
	ok := Event{Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "runs", Ask: "repeat"}
	if err := Validate(ok); err != nil {
		t.Errorf("Validate() rejected a valid learning event: %v", err)
	}
	for _, sev := range []string{"", "P3", "high"} {
		e := ok
		e.Severity = sev
		if err := Validate(e); err == nil {
			t.Errorf("Validate() accepted learning severity %q", sev)
		} else if !strings.Contains(err.Error(), "severity") {
			t.Errorf("Validate() error = %v, want severity message", err)
		}
	}
	for _, field := range []string{"title", "observed", "evidence", "ask"} {
		e := ok
		switch field {
		case "title":
			e.Title = ""
		case "observed":
			e.Observed = ""
		case "evidence":
			e.Evidence = ""
		case "ask":
			e.Ask = ""
		}
		if err := Validate(e); err == nil {
			t.Errorf("Validate() accepted learning with empty %s", field)
		}
	}
}

func TestValidateExternalReading(t *testing.T) {
	ok := Event{Task: "t1", Kind: "validated", Gate: "1", Tree: "t", Source: "external", Evidence: "https://ci/run/1", Session: "lead", Commit: "abc1234"}
	if err := Validate(ok); err != nil {
		t.Errorf("Validate() rejected a valid external reading: %v", err)
	}
	owns := ok
	owns.Kind = "owns_checked"
	if err := Validate(owns); err != nil {
		t.Errorf("Validate() rejected a valid external owns_checked: %v", err)
	}
	for _, field := range []string{"evidence", "session", "commit"} {
		e := ok
		switch field {
		case "evidence":
			e.Evidence = ""
		case "session":
			e.Session = ""
		case "commit":
			e.Commit = "nothex"
		}
		if err := Validate(e); err == nil {
			t.Errorf("Validate() accepted an external reading without %s", field)
		} else if !strings.Contains(err.Error(), "external") {
			t.Errorf("Validate() error = %v, want external message", err)
		}
	}
	other := ok
	other.Kind = "inspected"
	if err := Validate(other); err == nil {
		t.Error("Validate() accepted a source on an inspected event")
	}
	bad := ok
	bad.Source = "ci"
	if err := Validate(bad); err == nil {
		t.Error("Validate() accepted source \"ci\"")
	}
}

func TestValidateDismissedEvent(t *testing.T) {
	ok := Event{Task: "t1", Kind: "dismissed", ID: "L-01", Note: "fixed"}
	if err := Validate(ok); err != nil {
		t.Errorf("Validate() rejected a valid dismissed event: %v", err)
	}
	for _, id := range []string{"", "L01", "l-01", "L-"} {
		e := ok
		e.ID = id
		if err := Validate(e); err == nil {
			t.Errorf("Validate() accepted dismissed id %q", id)
		} else if !strings.Contains(err.Error(), "id") {
			t.Errorf("Validate() error = %v, want id message", err)
		}
	}
	empty := ok
	empty.Note = ""
	if err := Validate(empty); err == nil {
		t.Error("Validate() accepted dismissed without a note")
	} else if !strings.Contains(err.Error(), "note") {
		t.Errorf("Validate() error = %v, want note message", err)
	}
}

func TestValidateRejectsGoalOnOtherKinds(t *testing.T) {
	g := &GoalSpec{ID: "g1", Title: "Ship", Status: "active"}
	for _, k := range []string{"planned", "staffed", "dispatched", "finished"} {
		e := Event{Task: "T1", Kind: k, Goal: g}
		if k == "staffed" {
			e.Session = "s1"
			e.Task = ""
		}
		if err := Validate(e); err == nil {
			t.Errorf("Validate() accepted a goal on kind %s", k)
		} else if !strings.Contains(err.Error(), "goal") {
			t.Errorf("Validate() error = %v, want goal message", err)
		}
	}
}

func TestGoalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	spec := &GoalSpec{ID: "g1", Title: "Ship status", Acceptance: []string{"go test ./..."}, Required: []string{"t1"}, Status: "active"}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T00:00:00Z", Kind: "goal", Goal: spec}); err != nil {
		t.Fatalf("AppendEvent() goal error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-14T00:00:01Z", Task: "t1", Kind: "planned", GoalID: "g1"}); err != nil {
		t.Fatalf("AppendEvent() planned error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("ReadEvents() = %d events, want 2", len(evs))
	}
	if evs[0].Kind != "goal" || evs[0].Goal == nil || evs[0].Goal.ID != "g1" || evs[0].Goal.Title != "Ship status" {
		t.Errorf("goal round trip mismatch: %v", evs[0])
	}
	if evs[1].Task != "t1" || evs[1].GoalID != "g1" {
		t.Errorf("planned goal_id round trip mismatch: %v", evs[1])
	}
}

// TestPeakReasoningRoundTrips checks the finished event's peak_reasoning
// round-trips and is omitted from the log when zero (issue #84).
func TestPeakReasoningRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{
		TS: "2026-09-15T00:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1",
		Reason: "length", PeakReasoning: 50,
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: "2026-09-15T00:00:01Z", Task: "T2", Kind: "finished", Attempt: "r1", Reason: "stop",
	}); err != nil {
		t.Fatalf("AppendEvent() plain error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("ReadEvents() = %d events, want 2", len(evs))
	}
	if evs[0].PeakReasoning != 50 {
		t.Errorf("peak_reasoning = %d, want 50", evs[0].PeakReasoning)
	}
	if evs[1].PeakReasoning != 0 {
		t.Errorf("plain event peak_reasoning = %d, want 0", evs[1].PeakReasoning)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("events.jsonl = %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `"peak_reasoning":50`) {
		t.Errorf("line 0 = %q, want peak_reasoning:50", lines[0])
	}
	if strings.Contains(lines[1], "peak_reasoning") {
		t.Errorf("line 1 = %q, want peak_reasoning omitted when zero", lines[1])
	}
}

// TestWroteFieldRoundTrips checks the finished event's wrote field round-trips
// through the event log, sorted paths intact, and is omitted from the line
// when empty (issue #163).
func TestWroteFieldRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{
		TS: "2026-09-16T00:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1",
		Reason: "length", Wrote: []string{"a.go", "b.go"},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS: "2026-09-16T00:00:01Z", Task: "T2", Kind: "finished", Attempt: "r1", Reason: "stop",
	}); err != nil {
		t.Fatalf("AppendEvent() plain error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("ReadEvents() = %d events, want 2", len(evs))
	}
	if want := []string{"a.go", "b.go"}; len(evs[0].Wrote) != 2 || evs[0].Wrote[0] != want[0] || evs[0].Wrote[1] != want[1] {
		t.Errorf("wrote = %v, want %v", evs[0].Wrote, want)
	}
	if len(evs[1].Wrote) != 0 {
		t.Errorf("plain event wrote = %v, want empty", evs[1].Wrote)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("events.jsonl = %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `"wrote":["a.go","b.go"]`) {
		t.Errorf("line 0 = %q, want wrote:[\"a.go\",\"b.go\"]", lines[0])
	}
	if strings.Contains(lines[1], "wrote") {
		t.Errorf("line 1 = %q, want wrote omitted when empty", lines[1])
	}
}

// TestCommitFieldRoundTrips checks the event's commit field (issue #196)
// round-trips through the log under the "commit" key and is omitted when
// empty, and that an event carrying it validates.
func TestCommitFieldRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{
		TS: "2026-09-17T00:00:00Z", Task: "T1", Kind: "validated", Gate: "1", Tree: "abc123", Commit: "abc1234",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:00:01Z", Task: "T2", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() plain error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("ReadEvents() = %d events, want 2", len(evs))
	}
	if evs[0].Commit != "abc1234" {
		t.Errorf("commit = %q, want abc1234", evs[0].Commit)
	}
	if evs[1].Commit != "" {
		t.Errorf("plain event commit = %q, want empty", evs[1].Commit)
	}
	if err := Validate(Event{Task: "T1", Kind: "validated", Gate: "1", Tree: "abc123", Commit: "abc1234"}); err != nil {
		t.Errorf("Validate() rejected an event carrying commit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("events.jsonl = %d lines, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `"commit":"abc1234"`) {
		t.Errorf("line 0 = %q, want commit:\"abc1234\"", lines[0])
	}
	if strings.Contains(lines[1], "commit") {
		t.Errorf("line 1 = %q, want commit omitted when empty", lines[1])
	}
}

func TestEventNewGaugeFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	e := Event{
		TS:         "2026-09-13T00:00:00Z",
		Task:       "T1",
		Kind:       "validated",
		Tree:       "abc123",
		Gate:       "1",
		Command:    "go test ./internal/flywheel/",
		DurationMS: 1234,
		Outside:    []string{"x.go", "y.go"},
		Persona:    "supervisor",
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("ReadEvents() = %d events, want 1", len(evs))
	}
	got := evs[0]
	if got.Tree != "abc123" || got.Gate != "1" || got.Command != "go test ./internal/flywheel/" || got.DurationMS != 1234 {
		t.Errorf("round trip tree/gate/command/duration mismatch: %v", got)
	}
	if len(got.Outside) != 2 || got.Outside[0] != "x.go" || got.Outside[1] != "y.go" {
		t.Errorf("round trip outside mismatch: %v", got.Outside)
	}
	if got.Persona != "supervisor" {
		t.Errorf("persona = %q, want supervisor", got.Persona)
	}

	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	content := string(b)
	for _, k := range []string{`"tree"`, `"gate"`, `"command"`, `"duration_ms"`, `"outside"`, `"persona"`} {
		if !strings.Contains(content, k) {
			t.Errorf("log missing key %s: %q", k, content)
		}
	}
}

// TestAbsPathFallbackWhenMissing checks absPath's fallback contract (issue
// #244): a path that cannot be resolved — one that does not exist yet, the
// legitimate case EvalSymlinks fails on — is still normalised to its
// absolute, cleaned form without erroring, and a relative input always comes
// out absolute.
func TestAbsPathFallbackWhenMissing(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "a") + string(filepath.Separator) + ".." + string(filepath.Separator) + "does-not-exist"
	got := absPath(missing)
	want := filepath.Join(base, "does-not-exist")
	if got != want {
		t.Errorf("absPath(%q) = %q, want the cleaned fallback %q", missing, got, want)
	}
	rel := filepath.Join("missing-relative", "x")
	got = absPath(rel)
	want, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("filepath.Abs(%q) error = %v", rel, err)
	}
	if got != want {
		t.Errorf("absPath(%q) = %q, want the absolute fallback %q", rel, got, want)
	}
}

// TestWorktreesFieldRoundTrips checks a dispatched event's worktrees snapshot
// (worktree path -> {path -> sha256}) round-trips through the event log and
// is omitted when empty (issue #87).
func TestWorktreesFieldRoundTrips(t *testing.T) {
	dir := t.TempDir()
	e := Event{
		TS: "2026-09-16T00:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1",
		Worktrees: map[string]map[string]string{
			"/d/other-wt": {"note.txt": "deadbeef"},
		},
	}
	if err := AppendEvent(dir, e); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:01Z", Task: "T2", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("ReadEvents() = %d events, want 2", len(evs))
	}
	got := evs[0].Worktrees
	if len(got) != 1 {
		t.Fatalf("worktrees = %v, want 1 entry", got)
	}
	files, ok := got["/d/other-wt"]
	if !ok || len(files) != 1 || files["note.txt"] != "deadbeef" {
		t.Errorf("worktrees[/d/other-wt] = %v, want {note.txt: deadbeef}", files)
	}
	if evs[1].Worktrees != nil {
		t.Errorf("worktrees = %v, want nil when never set", evs[1].Worktrees)
	}

	b2, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(b2), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[0], `"worktrees":{"/d/other-wt":{"note.txt":"deadbeef"}}`) {
		t.Errorf("line 0 = %q, want the worktrees snapshot inline", lines[0])
	}
	if strings.Contains(lines[1], "worktrees") {
		t.Errorf("line 1 = %q, want worktrees omitted when unset", lines[1])
	}
}

// TestReadEventsIgnoresUnterminatedTail checks a reader racing a concurrent
// append sees the complete lines only, not an error (#297 review).
func TestReadEventsIgnoresUnterminatedTail(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, ".flywheel", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.WriteString(`{"ts":"2026-09-18T10:01:00Z","task":"T1","ki`); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	f.Close()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v, want the complete lines only", err)
	}
	if len(evs) != 1 || evs[0].Kind != "planned" {
		t.Errorf("ReadEvents() = %+v, want the one complete planned event", evs)
	}
}

// TestValidateIncrement checks only a dispatched event may carry an
// increment, and it must be positive (#295 review).
func TestValidateIncrement(t *testing.T) {
	if err := Validate(Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Increment: 2}); err != nil {
		t.Errorf("Validate(dispatched increment 2) = %v, want nil", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Increment: -2}); err == nil {
		t.Error("Validate(dispatched increment -2) = nil, want an error")
	}
	if err := Validate(Event{Task: "T1", Kind: "finished", Attempt: "r1", Increment: 1}); err == nil {
		t.Error("Validate(finished increment 1) = nil, want an error")
	}
}
