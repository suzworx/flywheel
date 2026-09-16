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
