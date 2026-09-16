package flywheel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testNow is the fixed clock the fixture refresh tests are drawn at.
var testNow time.Time

func fixtureNow(t *testing.T) time.Time {
	now, err := time.Parse(time.RFC3339Nano, "2026-09-12T01:00:00Z")
	if err != nil {
		t.Fatalf("parse test now: %v", err)
	}
	return now
}

func unitBy(units []Unit, task string) (Unit, bool) {
	for _, u := range units {
		if u.Task == task {
			return u, true
		}
	}
	return Unit{}, false
}

// andonHas reports whether the andon lists the given task.
func andonHas(a []Andon, task string) bool {
	for _, x := range a {
		if x.Task == task {
			return true
		}
	}
	return false
}

func TestStageOf(t *testing.T) {
	for _, tc := range []struct {
		status string
		reason string
		want   string
	}{
		{"planned", "", "planned"},
		{"dispatched", "", "building"},
		{"running", "", "building"},
		{"finished", "", "finished"},
		{"finished", "stop", "finished"},
		{"finished", "length", "cut-off"},
		{"finished", "start-failed", "failed"},
		{"finished", "silent", "failed"},
		{"passed", "", "passed"},
		{"needs-correction", "", "building"},
		{"rejected", "", "rejected"},
		{"blocked", "", "blocked"},
		{"landed", "", "landed"},
	} {
		if got := stageOf(tc.status, tc.reason); got != tc.want {
			t.Errorf("stageOf(%q, %q) = %q, want %q", tc.status, tc.reason, got, tc.want)
		}
	}
}

func TestClassifyRun(t *testing.T) {
	if classifyRun(true, 1, 0, 0, false, "stop", 100, 0, 600, false, false) != "done" {
		t.Error("done unit not classified done")
	}
	if classifyRun(true, 1, 0, 0, false, "", 100, 0, 600, false, false) != "done" {
		t.Error("done unit with an empty reason not classified done")
	}
	if classifyRun(false, 1, 0, 0, true, "stop", 100, 0, 600, false, false) != "provider-error" {
		t.Error("provider-error not classified provider-error")
	}
	if classifyRun(false, 1, 0, 0, false, "length", 100, 0, 600, false, false) != "capped" {
		t.Error("capped not classified capped")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 0, 120, 600, false, false) != "silent" {
		t.Error("empty old run not classified silent")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 700, 600, false, false) != "stalled" {
		t.Error("no-growth 700s not classified stalled at the 600s default")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 400, 600, false, false) != "long-step" {
		t.Error("no-growth 400s not classified long-step at the 600s default")
	}
	if classifyRun(false, 10, 3, 0, false, "stop", 100, 0, 600, false, false) != "exploring" {
		t.Error("10 steps, 3 reads, 0 edits not classified exploring")
	}
	if classifyRun(false, 10, 3, 1, false, "stop", 100, 0, 600, false, false) != "running" {
		t.Error("exploring with an edit not classified running")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 0, 600, false, false) != "running" {
		t.Error("healthy run not classified running")
	}
	// A capped or provider-error run still records a finished event, so the
	// signal wins over done: the unit must reach the andon.
	if classifyRun(true, 5, 1, 0, false, "length", 200, 0, 600, false, false) != "capped" {
		t.Error("finished capped run not classified capped")
	}
	if classifyRun(true, 5, 1, 0, false, "error", 200, 0, 600, false, false) != "provider-error" {
		t.Error("finished provider-error run not classified provider-error")
	}
	if classifyRun(true, 5, 1, 1, false, "stop", 200, 0, 600, false, false) != "done" {
		t.Error("clean finished run not classified done")
	}
	// Any other done reason (start-failed, silent, ...) is a failed run, not a
	// silent success (issue #131), including the new stalled reason (#85).
	if classifyRun(true, 0, 0, 0, false, "start-failed", 100, 0, 600, false, false) != "failed" {
		t.Error("finished start-failed run not classified failed")
	}
	if classifyRun(true, 0, 0, 0, false, "silent", 100, 0, 600, false, false) != "failed" {
		t.Error("finished silent run not classified failed")
	}
	if classifyRun(true, 3, 1, 0, false, "stalled", 100, 0, 600, false, false) != "failed" {
		t.Error("finished stalled run not classified failed")
	}
	// A done attempt that wrote files before an unclean finish is failed-dirty
	// instead of plain failed or capped, whatever the unclean reason (issue
	// #163).
	if classifyRun(true, 3, 1, 0, false, "stalled", 100, 0, 600, false, true) != "failed-dirty" {
		t.Error("finished stalled run with files not classified failed-dirty")
	}
	if classifyRun(true, 5, 1, 0, false, "length", 200, 0, 600, false, true) != "failed-dirty" {
		t.Error("finished capped run with files not classified failed-dirty")
	}
	if classifyRun(true, 5, 1, 0, false, "length", 200, 0, 600, false, false) != "capped" {
		t.Error("finished capped run with no files wrongly reclassified failed-dirty")
	}
}

// TestClassifyRunFollowsStallTimeout checks the stalled and long-step
// thresholds scale with the stallTimeout argument instead of the old
// hardcoded 600/300, so the view and the runner agree on a non-default
// worker config (issue #85).
func TestClassifyRunFollowsStallTimeout(t *testing.T) {
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 40, 100, false, false) != "running" {
		t.Error("age 40 under a 100s stall timeout not classified running")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 60, 100, false, false) != "long-step" {
		t.Error("age 60 over half a 100s stall timeout not classified long-step")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 150, 100, false, false) != "stalled" {
		t.Error("age 150 over a 100s stall timeout not classified stalled")
	}
	// The same age classifies differently under a shorter stall timeout,
	// proving the threshold is not still hardcoded.
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 400, 600, false, false) != "long-step" {
		t.Error("age 400 under a 600s stall timeout not classified long-step")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 400, 200, false, false) != "stalled" {
		t.Error("age 400 over a 200s stall timeout not classified stalled")
	}
}

// TestClassifyRunNoWrites checks the three no-writes conditions (issue #176):
// 20+ steps, zero edits and a recorded no-plan event on the current attempt
// all have to hold, so a quiet start, a run with edits, or a run that stated
// its plan is left exactly as it classified before.
func TestClassifyRunNoWrites(t *testing.T) {
	if classifyRun(false, 20, 0, 0, false, "stop", 100, 0, 600, true, false) != "no-writes" {
		t.Error("20 steps, 0 edits, no-plan recorded not classified no-writes")
	}
	if classifyRun(false, 20, 0, 0, false, "stop", 100, 0, 600, false, false) != "running" {
		t.Error("20 steps, 0 edits, a plan recorded (no no-plan event) wrongly classified no-writes")
	}
	if classifyRun(false, 20, 0, 1, false, "stop", 100, 0, 600, true, false) != "running" {
		t.Error("20 steps with an edit wrongly classified no-writes")
	}
	if classifyRun(false, 19, 0, 0, false, "stop", 100, 0, 600, true, false) != "running" {
		t.Error("19 steps (short of the threshold) wrongly classified no-writes")
	}
	// A run that is both no-writes-eligible and past the stall threshold still
	// needs the threshold itself to report stalled: no-writes is checked
	// first, so it wins here, exactly as the brief specifies.
	if classifyRun(false, 20, 0, 0, false, "stop", 100, 700, 600, true, false) != "no-writes" {
		t.Error("no-writes-eligible and stalled by age not classified no-writes")
	}
	// A done run is unaffected by noPlan in every case.
	if classifyRun(true, 20, 0, 0, false, "stop", 100, 0, 600, true, false) != "done" {
		t.Error("done run wrongly reclassified by noPlan")
	}
}

func TestLiveRun(t *testing.T) {
	for _, s := range []string{"running", "exploring", "long-step", "silent", "stalled", "no-writes"} {
		if !liveRun(s) {
			t.Errorf("liveRun(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"capped", "provider-error", "failed", "failed-dirty", "done", "landed", "waiting"} {
		if liveRun(s) {
			t.Errorf("liveRun(%q) = true, want false", s)
		}
	}
}

func TestFixtureRefresh(t *testing.T) {
	stalled := filepath.Join("testdata", "factory", ".flywheel", "runs", "stalled.r1.jsonl")
	mt, err := time.Parse(time.RFC3339Nano, "2026-09-12T00:30:00Z")
	if err != nil {
		t.Fatalf("parse stalled mtime: %v", err)
	}
	if err := os.Chtimes(stalled, mt, mt); err != nil {
		t.Fatalf("set stalled mtime: %v", err)
	}

	var w Watcher = NewWatcher()
	fl, err := w.Refresh("testdata/factory", fixtureNow(t))
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if len(fl.Lines) != 1 || fl.Lines[0].Name != "default" {
		t.Errorf("lines = %v, want default worker", fl.Lines)
	}
	if fl.Lines[0].Busy != 3 {
		t.Errorf("default line busy = %d, want 3 live in-flight (capped and provider-error are dead)", fl.Lines[0].Busy)
	}
	if fl.Staffing.Lead != "not registered" {
		t.Errorf("staffing lead = %q, want not registered", fl.Staffing.Lead)
	}

	want := map[string]string{
		"done":           "done",
		"running":        "running",
		"exploring":      "exploring",
		"stalled":        "stalled",
		"capped":         "capped",
		"provider-error": "provider-error",
		"planned":        "waiting",
	}
	for task, state := range want {
		u, ok := unitBy(fl.Units, task)
		if !ok {
			t.Errorf("unit %s missing", task)
			continue
		}
		if u.RunState != state {
			t.Errorf("unit %s run state = %q, want %q", task, u.RunState, state)
		}
	}
	if u, ok := unitBy(fl.Units, "done"); ok && u.Stage != "landed" {
		t.Errorf("done stage = %q, want landed", u.Stage)
	}
	if u, ok := unitBy(fl.Units, "planned"); ok && u.Stage != "planned" {
		t.Errorf("planned stage = %q, want planned", u.Stage)
	}

	if len(fl.Andon) != 3 {
		t.Errorf("andon = %v, want exactly the 3 bad units", fl.Andon)
	}
	for _, a := range fl.Andon {
		if a.State != "stalled" && a.State != "capped" && a.State != "provider-error" {
			t.Errorf("andon holds %s %s, want a bad unit", a.Task, a.State)
		}
	}
	// The finished capped and provider-error units must reach the andon, while
	// the clean finished (done) unit must not.
	if !andonHas(fl.Andon, "capped") {
		t.Errorf("andon missing capped: %v", fl.Andon)
	}
	if !andonHas(fl.Andon, "provider-error") {
		t.Errorf("andon missing provider-error: %v", fl.Andon)
	}
	if andonHas(fl.Andon, "done") {
		t.Errorf("clean done unit wrongly on andon: %v", fl.Andon)
	}

	if fl.Output.LandedToday != 1 {
		t.Errorf("landed today = %d, want 1", fl.Output.LandedToday)
	}
	if fl.Output.Finished != 1 {
		t.Errorf("finished = %d, want 1", fl.Output.Finished)
	}
	if fl.Output.HasReviews {
		t.Errorf("has reviews = true, want false (no reviewed events)")
	}
	if fl.Output.Tokens != 260 {
		t.Errorf("tokens = %d, want 260", fl.Output.Tokens)
	}
	if fl.Output.Cost != 0.004 {
		t.Errorf("cost = %g, want 0.004", fl.Output.Cost)
	}
}

func TestIncrementalReadsOnlyAppended(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join("testdata", "factory", ".flywheel")
	copyFile(filepath.Join(src, "events.jsonl"), filepath.Join(tmp, ".flywheel", "events.jsonl"), t)
	copyFile(filepath.Join(src, "runs", "running.r1.jsonl"), filepath.Join(tmp, ".flywheel", "runs", "running.r1.jsonl"), t)

	now := fixtureNow(t)
	var w Watcher = NewWatcher()
	fl1, err := w.Refresh(tmp, now)
	if err != nil {
		t.Fatalf("first Refresh() error = %v", err)
	}
	evBytes := w.EventsBytes
	runBytes := w.RunBytes

	eventsPath := filepath.Join(tmp, ".flywheel", "events.jsonl")
	runPath := filepath.Join(tmp, ".flywheel", "runs", "running.r1.jsonl")
	evSize1, _ := os.Stat(eventsPath)
	runSize1, _ := os.Stat(runPath)

	// Append one finished event and one run line.
	toks := new(Tokens)
	*toks = Tokens{Input: 30, Output: 10, Reasoning: 2}
	if err := AppendEvent(tmp, Event{TS: "2026-09-12T00:20:00Z", Task: "extra", Kind: "finished", Attempt: "r1", Steps: 1, Tokens: toks, Cost: 0.001}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	appendLine(runPath, `{"type":"step_finish","sessionID":"ses_running_0000000001","part":{"type":"step_finish","reason":"stop","tokens":{"input":5,"output":1,"reasoning":0,"cache":{"read":10,"write":0}},"cost":0.0001}}`, t)

	evSize2, _ := os.Stat(eventsPath)
	runSize2, _ := os.Stat(runPath)

	fl2, err := w.Refresh(tmp, now)
	if err != nil {
		t.Fatalf("second Refresh() error = %v", err)
	}
	if w.EventsBytes-evBytes != evSize2.Size()-evSize1.Size() {
		t.Errorf("events bytes reread = %d, want %d", w.EventsBytes-evBytes, evSize2.Size()-evSize1.Size())
	}
	if w.RunBytes-runBytes != runSize2.Size()-runSize1.Size() {
		t.Errorf("run bytes reread = %d, want %d", w.RunBytes-runBytes, runSize2.Size()-runSize1.Size())
	}
	if fl2.Output.Finished != fl1.Output.Finished+1 {
		t.Errorf("finished count after append = %d, want %d", fl2.Output.Finished, fl1.Output.Finished+1)
	}
	if u, ok := unitBy(fl2.Units, "extra"); !ok || u.RunState != "done" {
		t.Errorf("extra unit run state = %v, want done", u.RunState)
	}
}

func TestPartialRunLineWaitsForNewline(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join("testdata", "factory", ".flywheel")
	copyFile(filepath.Join(src, "events.jsonl"), filepath.Join(tmp, ".flywheel", "events.jsonl"), t)
	runPath := filepath.Join(tmp, ".flywheel", "runs", "running.r1.jsonl")
	copyFile(filepath.Join(src, "runs", "running.r1.jsonl"), runPath, t)

	now := fixtureNow(t)
	var w Watcher = NewWatcher()
	if _, err := w.Refresh(tmp, now); err != nil {
		t.Fatalf("first Refresh() error = %v", err)
	}
	rel := ".flywheel/runs/running.r1.jsonl"
	wantOff := w.runOff[rel]
	wantSteps := w.runSteps[rel]
	size1 := fileSize(t, runPath)

	line := `{"type":"step_finish","sessionID":"ses_running_0000000001","part":{"type":"step_finish","reason":"stop","tokens":{"input":5,"output":1,"reasoning":0,"cache":{"read":10,"write":0}},"cost":0.0001}}`
	half := len(line) / 2
	appendBytes(runPath, []byte(line[:half]), t)

	fl, err := w.Refresh(tmp, now)
	if err != nil {
		t.Fatalf("partial Refresh() error = %v", err)
	}
	if w.runSteps[rel] != wantSteps {
		t.Errorf("steps after partial line = %d, want %d unchanged", w.runSteps[rel], wantSteps)
	}
	if w.runOff[rel] != wantOff {
		t.Errorf("run offset after partial line = %d, want %d unchanged", w.runOff[rel], wantOff)
	}
	if w.RunBytes != size1 {
		t.Errorf("RunBytes after partial line = %d, want %d", w.RunBytes, size1)
	}

	appendBytes(runPath, []byte(line[half:]+"\n"), t)
	fl, err = w.Refresh(tmp, now)
	if err != nil {
		t.Fatalf("second Refresh() error = %v", err)
	}
	size2 := fileSize(t, runPath)
	if w.runSteps[rel] != wantSteps+1 {
		t.Errorf("steps after completed line = %d, want %d", w.runSteps[rel], wantSteps+1)
	}
	if w.RunBytes != size2 {
		t.Errorf("RunBytes after completed line = %d, want %d (file size)", w.RunBytes, size2)
	}
	_ = fl
}

func TestPartialEventLineWaitsForNewline(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join("testdata", "factory", ".flywheel")
	eventsPath := filepath.Join(tmp, ".flywheel", "events.jsonl")
	copyFile(filepath.Join(src, "events.jsonl"), eventsPath, t)
	copyFile(filepath.Join(src, "runs", "done.r1.jsonl"), filepath.Join(tmp, ".flywheel", "runs", "done.r1.jsonl"), t)

	now := fixtureNow(t)
	var w Watcher = NewWatcher()
	if _, err := w.Refresh(tmp, now); err != nil {
		t.Fatalf("first Refresh() error = %v", err)
	}
	evs := append([]Event{}, w.events...)
	size1 := fileSize(t, eventsPath)

	line := `{"ts":"2026-09-12T00:40:00Z","task":"extra","kind":"dispatched","attempt":"r1","adapter":"opencode","model":"m","path":".flywheel/runs/done.r1.jsonl"}`
	half := len(line) / 2
	appendBytes(eventsPath, []byte(line[:half]), t)

	if _, err := w.Refresh(tmp, now); err != nil {
		t.Fatalf("partial Refresh() error = %v", err)
	}
	if len(w.events) != len(evs) {
		t.Errorf("events after partial line = %d, want %d unchanged", len(w.events), len(evs))
	}
	if w.EventsBytes != size1 {
		t.Errorf("EventsBytes after partial line = %d, want %d", w.EventsBytes, size1)
	}

	appendBytes(eventsPath, []byte(line[half:]+"\n"), t)
	fl, err := w.Refresh(tmp, now)
	if err != nil {
		t.Fatalf("second Refresh() error = %v", err)
	}
	size2 := fileSize(t, eventsPath)
	if len(w.events) != len(evs)+1 {
		t.Errorf("events after completed line = %d, want %d", len(w.events), len(evs)+1)
	}
	if w.EventsBytes != size2 {
		t.Errorf("EventsBytes after completed line = %d, want %d (file size)", w.EventsBytes, size2)
	}
	if u, ok := unitBy(fl.Units, "extra"); !ok || u.Stage != "building" {
		t.Errorf("extra unit stage = %v, want building (derived from completed event)", u.Stage)
	}
}

func fileSize(t *testing.T, path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func appendBytes(path string, b []byte, t *testing.T) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func copyFile(src, dst string, t *testing.T) {
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func appendLine(path, line string, t *testing.T) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func TestBuildStaffingLatestPerRole(t *testing.T) {
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "s1", Persona: "lead", Model: "m1"},
		{TS: "2026-09-13T00:00:01Z", Kind: "staffed", Session: "sf1", Persona: "foreman", Model: "m2"},
		{TS: "2026-09-13T00:00:02Z", Kind: "staffed", Session: "s3", Persona: "lead"},
	}
	st := buildStaffing(events)
	if st.Lead != "s3" {
		t.Errorf("lead = %q, want s3 (latest lead staffed event, parentheses dropped without a model)", st.Lead)
	}
}

func TestBuildStaffingModelShownInParentheses(t *testing.T) {
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "s1", Persona: "lead", Model: "m1"},
	}
	st := buildStaffing(events)
	if st.Lead != "s1 (m1)" {
		t.Errorf("lead = %q, want s1 (m1)", st.Lead)
	}
}

func TestBuildStaffingNotRegisteredWithoutStaffedEvent(t *testing.T) {
	st := buildStaffing([]Event{})
	if st.Lead != "not registered" {
		t.Errorf("lead = %q, want not registered", st.Lead)
	}
}

func TestBuildOutputFirstPassFromInspectedAndReviewed(t *testing.T) {
	// The first verdict per task wins, whether it comes from inspected or
	// reviewed; a later pass on a task whose first verdict was rework must not
	// count.
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Task: "T1", Kind: "reviewed", Verdict: "pass"},
		{TS: "2026-09-13T00:00:00Z", Task: "T2", Kind: "inspected", Verdict: "pass"},
		{TS: "2026-09-13T00:00:01Z", Task: "T3", Kind: "inspected", Verdict: "rework"},
		{TS: "2026-09-13T00:00:02Z", Task: "T3", Kind: "reviewed", Verdict: "pass"},
		{TS: "2026-09-13T00:00:00Z", Task: "T4", Kind: "reviewed", Verdict: "correct"},
		{TS: "2026-09-13T00:00:00Z", Task: "T5", Kind: "inspected", Verdict: "escalate"},
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-13T01:00:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	o := buildOutput(events, now)
	if !o.HasReviews {
		t.Error("has reviews = false, want true (inspected verdicts count)")
	}
	if o.FirstPassRate != 0.4 {
		t.Errorf("first-pass rate = %g, want 0.4 (T1 and T2 only)", o.FirstPassRate)
	}
	if o.Finished != 0 {
		t.Errorf("finished = %d, want 0 (no finished events)", o.Finished)
	}
}

// TestMissingNewlineReasonComesFromFinishedEvent checks a run file whose last
// line was never newline-terminated (a mid-write cut off) still classifies
// correctly: the finished event's reason, always a complete line in the event
// log, decides the run state and stage instead of the unreadable run-file
// tail (issue #131).
func TestMissingNewlineReasonComesFromFinishedEvent(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "cutoff", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "cutoff", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "cutoff", Kind: "started", Session: "s1"},
		{TS: "2026-09-15T00:00:01Z", Task: "cutoff", Kind: "finished", Attempt: "r1", Reason: "length"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	run := filepath.Join(dir, ".flywheel", "runs", "cutoff.r1.jsonl")
	if err := os.MkdirAll(filepath.Dir(run), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	// The final line has no trailing newline, so readRun's own scan can never
	// fold it in and sees only the earlier stop-reasoned step.
	content := `{"type":"step_finish","sessionID":"s1","part":{"type":"step_finish","reason":"stop"}}` + "\n" +
		`{"type":"text","sessionID":"s1","part":{"type":"text","text":"mid-write"}}`
	if err := os.WriteFile(run, []byte(content), 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "cutoff")
	if !ok {
		t.Fatal("unit cutoff missing")
	}
	if u.RunState != "capped" {
		t.Errorf("run state = %q, want capped", u.RunState)
	}
	if u.Stage != "cut-off" {
		t.Errorf("stage = %q, want cut-off", u.Stage)
	}
	if !andonHas(fl.Andon, "cutoff") {
		t.Errorf("andon missing cutoff: %v", fl.Andon)
	}
}

// TestPeakReasoningFillsUnit checks a capped unit's Peak is filled from its
// current attempt's latest finished event's peak_reasoning, and a unit with
// no such finished event keeps Peak at 0 (issue #84).
func TestPeakReasoningFillsUnit(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "capped2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "capped2", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "capped2", Kind: "started", Session: "s1"},
		{TS: "2026-09-15T00:00:01Z", Task: "capped2", Kind: "finished", Attempt: "r1", Reason: "length", PeakReasoning: 50},
		{TS: "2026-09-15T00:00:00Z", Task: "nopeak", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "nopeak", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "nopeak", Kind: "started", Session: "s2"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "capped2")
	if !ok {
		t.Fatal("unit capped2 missing")
	}
	if u.Peak != 50 {
		t.Errorf("capped2 Peak = %d, want 50", u.Peak)
	}
	np, ok := unitBy(fl.Units, "nopeak")
	if !ok {
		t.Fatal("unit nopeak missing")
	}
	if np.Peak != 0 {
		t.Errorf("nopeak Peak = %d, want 0 (no finished event yet)", np.Peak)
	}
}

// TestStartFailedFinishIsFailedOnAndon checks a finished event with reason
// start-failed is a failed run state, reaches the andon, and its stage is
// failed (issue #131).
func TestStartFailedFinishIsFailedOnAndon(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "died", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "died", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:01Z", Task: "died", Kind: "finished", Attempt: "r1", Reason: "start-failed"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "died")
	if !ok {
		t.Fatal("unit died missing")
	}
	if u.RunState != "failed" {
		t.Errorf("run state = %q, want failed", u.RunState)
	}
	if u.Stage != "failed" {
		t.Errorf("stage = %q, want failed", u.Stage)
	}
	if !andonHas(fl.Andon, "died") {
		t.Errorf("andon missing died: %v", fl.Andon)
	}
}

// TestUncleanFinishWithFilesIsFailedDirty checks a done attempt with an
// unclean reason that also lists written files classifies failed-dirty
// (not plain failed), reaches the andon, and is not live (issue #163).
func TestUncleanFinishWithFilesIsFailedDirty(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "dirty", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "dirty", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:01Z", Task: "dirty", Kind: "finished", Attempt: "r1", Reason: "start-failed", Wrote: []string{"a.go", "b.go"}},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "dirty")
	if !ok {
		t.Fatal("unit dirty missing")
	}
	if u.RunState != "failed-dirty" {
		t.Errorf("run state = %q, want failed-dirty", u.RunState)
	}
	if !andonHas(fl.Andon, "dirty") {
		t.Errorf("andon missing dirty: %v", fl.Andon)
	}
	if liveRun(u.RunState) {
		t.Errorf("failed-dirty run state %q wrongly classified live", u.RunState)
	}
}

// TestCappedWithFilesIsFailedDirty checks a capped (reason length) attempt
// that also wrote files classifies failed-dirty, not capped, while a capped
// attempt with no files stays capped (issue #163).
func TestCappedWithFilesIsFailedDirty(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "cappeddirty", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "cappeddirty", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:01Z", Task: "cappeddirty", Kind: "finished", Attempt: "r1", Reason: "length", Wrote: []string{"c.go"}},
		{TS: "2026-09-15T00:00:00Z", Task: "cappedclean", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "cappedclean", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:01Z", Task: "cappedclean", Kind: "finished", Attempt: "r1", Reason: "length"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	dirty, ok := unitBy(fl.Units, "cappeddirty")
	if !ok {
		t.Fatal("unit cappeddirty missing")
	}
	if dirty.RunState != "failed-dirty" {
		t.Errorf("run state = %q, want failed-dirty", dirty.RunState)
	}
	clean, ok := unitBy(fl.Units, "cappedclean")
	if !ok {
		t.Fatal("unit cappedclean missing")
	}
	if clean.RunState != "capped" {
		t.Errorf("run state = %q, want capped (no files written)", clean.RunState)
	}
}

// TestStopFinishIsDoneAndFinished checks a normal clean finish (reason stop)
// still classifies done, stays off the andon, and keeps the finished stage
// (issue #131 must not regress the common case).
func TestStopFinishIsDoneAndFinished(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "clean", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "clean", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "clean", Kind: "started", Session: "s1"},
		{TS: "2026-09-15T00:00:01Z", Task: "clean", Kind: "finished", Attempt: "r1", Reason: "stop"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	run := filepath.Join(dir, ".flywheel", "runs", "clean.r1.jsonl")
	if err := os.MkdirAll(filepath.Dir(run), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	line := `{"type":"step_finish","sessionID":"s1","part":{"type":"step_finish","reason":"stop"}}` + "\n"
	if err := os.WriteFile(run, []byte(line), 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "clean")
	if !ok {
		t.Fatal("unit clean missing")
	}
	if u.RunState != "done" {
		t.Errorf("run state = %q, want done", u.RunState)
	}
	if u.Stage != "finished" {
		t.Errorf("stage = %q, want finished", u.Stage)
	}
	if andonHas(fl.Andon, "clean") {
		t.Errorf("clean done unit wrongly on andon: %v", fl.Andon)
	}
}

// noStepsRunFile writes a run file of n step_finish lines (reason stop, no
// tool_use lines, so zero reads and zero edits) for the no-writes tests
// (issue #176).
func noStepsRunFile(t *testing.T, dir, task, attempt string, n int) {
	run := filepath.Join(dir, ".flywheel", "runs", task+"."+attempt+".jsonl")
	if err := os.MkdirAll(filepath.Dir(run), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	content := ""
	for i := 0; i < n; i++ {
		content += `{"type":"step_finish","sessionID":"s1","part":{"type":"step_finish","reason":"stop"}}` + "\n"
	}
	if err := os.WriteFile(run, []byte(content), 0o644); err != nil {
		t.Fatalf("write run file: %v", err)
	}
}

// TestNoWritesReachesAndon checks a live run that reached 20 steps with zero
// edits and a recorded no-plan event on its current attempt classifies
// no-writes, stays live, and reaches the andon (issue #176).
func TestNoWritesReachesAndon(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "quiet", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "quiet", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "quiet", Kind: "started", Session: "s1"},
		{TS: "2026-09-15T00:00:01Z", Task: "quiet", Kind: "no-plan", Attempt: "r1"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	noStepsRunFile(t, dir, "quiet", "r1", 20)
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "quiet")
	if !ok {
		t.Fatal("unit quiet missing")
	}
	if u.RunState != "no-writes" {
		t.Errorf("run state = %q, want no-writes", u.RunState)
	}
	if !liveRun(u.RunState) {
		t.Errorf("no-writes run state %q not live", u.RunState)
	}
	if !andonHas(fl.Andon, "quiet") {
		t.Errorf("andon missing quiet: %v", fl.Andon)
	}
}

// TestNoWritesUntouchedWithPlan checks the identical run WITH its plan
// recorded (no no-plan event) does not classify no-writes and does not reach
// the andon (issue #176): a legitimately read-only unit that stated its plan
// is left alone.
func TestNoWritesUntouchedWithPlan(t *testing.T) {
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "stated", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "stated", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "stated", Kind: "started", Session: "s1"},
		{TS: "2026-09-15T00:00:01Z", Task: "stated", Kind: "worker_plan"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	noStepsRunFile(t, dir, "stated", "r1", 20)
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatalf("parse now: %v", err)
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "stated")
	if !ok {
		t.Fatal("unit stated missing")
	}
	if u.RunState == "no-writes" {
		t.Errorf("run state = %q, wrongly no-writes despite a recorded plan", u.RunState)
	}
	if andonHas(fl.Andon, "stated") {
		t.Errorf("andon wrongly holds stated: %v", fl.Andon)
	}
}
