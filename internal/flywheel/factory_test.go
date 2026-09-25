package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	t.Parallel()
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
	t.Parallel()
	if classifyRun(true, 1, 0, 0, false, "stop", 100, 0, 600, false, false, "") != "done" {
		t.Error("done unit not classified done")
	}
	if classifyRun(true, 1, 0, 0, false, "", 100, 0, 600, false, false, "") != "done" {
		t.Error("done unit with an empty reason not classified done")
	}
	if classifyRun(false, 1, 0, 0, true, "stop", 100, 0, 600, false, false, "") != "provider-error" {
		t.Error("provider-error not classified provider-error")
	}
	if classifyRun(false, 1, 0, 0, false, "length", 100, 0, 600, false, false, "") != "capped" {
		t.Error("capped not classified capped")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 0, 120, 600, false, false, "") != "silent" {
		t.Error("empty old run not classified silent")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 700, 600, false, false, "") != "stalled" {
		t.Error("no-growth 700s not classified stalled at the 600s default")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 400, 600, false, false, "") != "long-step" {
		t.Error("no-growth 400s not classified long-step at the 600s default")
	}
	if classifyRun(false, 10, 3, 0, false, "stop", 100, 0, 600, false, false, "") != "exploring" {
		t.Error("10 steps, 3 reads, 0 edits not classified exploring")
	}
	if classifyRun(false, 10, 3, 1, false, "stop", 100, 0, 600, false, false, "") != "running" {
		t.Error("exploring with an edit not classified running")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 0, 600, false, false, "") != "running" {
		t.Error("healthy run not classified running")
	}
	// A capped or provider-error run still records a finished event, so the
	// signal wins over done: the unit must reach the andon.
	if classifyRun(true, 5, 1, 0, false, "length", 200, 0, 600, false, false, "") != "capped" {
		t.Error("finished capped run not classified capped")
	}
	if classifyRun(true, 5, 1, 0, false, "error", 200, 0, 600, false, false, "") != "provider-error" {
		t.Error("finished provider-error run not classified provider-error")
	}
	if classifyRun(true, 5, 1, 1, false, "stop", 200, 0, 600, false, false, "") != "done" {
		t.Error("clean finished run not classified done")
	}
	// Any other done reason (start-failed, silent, ...) is a failed run, not a
	// silent success (issue #131), including the new stalled reason (#85).
	if classifyRun(true, 0, 0, 0, false, "start-failed", 100, 0, 600, false, false, "") != "failed" {
		t.Error("finished start-failed run not classified failed")
	}
	if classifyRun(true, 0, 0, 0, false, "silent", 100, 0, 600, false, false, "") != "failed" {
		t.Error("finished silent run not classified failed")
	}
	if classifyRun(true, 3, 1, 0, false, "stalled", 100, 0, 600, false, false, "") != "failed" {
		t.Error("finished stalled run not classified failed")
	}
	// A done attempt that wrote files before an unclean finish is failed-dirty
	// instead of plain failed or capped, whatever the unclean reason (issue
	// #163).
	if classifyRun(true, 3, 1, 0, false, "stalled", 100, 0, 600, false, true, "") != "failed-dirty" {
		t.Error("finished stalled run with files not classified failed-dirty")
	}
	if classifyRun(true, 5, 1, 0, false, "length", 200, 0, 600, false, true, "") != "failed-dirty" {
		t.Error("finished capped run with files not classified failed-dirty")
	}
	if classifyRun(true, 5, 1, 0, false, "length", 200, 0, 600, false, false, "") != "capped" {
		t.Error("finished capped run with no files wrongly reclassified failed-dirty")
	}
}

// TestClassifyRunFollowsStallTimeout checks the stalled and long-step
// thresholds scale with the stallTimeout argument instead of the old
// hardcoded 600/300, so the view and the runner agree on a non-default
// worker config (issue #85).
func TestClassifyRunFollowsStallTimeout(t *testing.T) {
	t.Parallel()
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 40, 100, false, false, "") != "running" {
		t.Error("age 40 under a 100s stall timeout not classified running")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 60, 100, false, false, "") != "long-step" {
		t.Error("age 60 over half a 100s stall timeout not classified long-step")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 150, 100, false, false, "") != "stalled" {
		t.Error("age 150 over a 100s stall timeout not classified stalled")
	}
	// The same age classifies differently under a shorter stall timeout,
	// proving the threshold is not still hardcoded.
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 400, 600, false, false, "") != "long-step" {
		t.Error("age 400 under a 600s stall timeout not classified long-step")
	}
	if classifyRun(false, 1, 0, 0, false, "stop", 100, 400, 200, false, false, "") != "stalled" {
		t.Error("age 400 over a 200s stall timeout not classified stalled")
	}
}

// TestClassifyRunNoWrites checks the three no-writes conditions (issue #176):
// 20+ steps, zero edits and a recorded no-plan event on the current attempt
// all have to hold, so a quiet start, a run with edits, or a run that stated
// its plan is left exactly as it classified before.
func TestClassifyRunNoWrites(t *testing.T) {
	t.Parallel()
	if classifyRun(false, 20, 0, 0, false, "stop", 100, 0, 600, true, false, "") != "no-writes" {
		t.Error("20 steps, 0 edits, no-plan recorded not classified no-writes")
	}
	if classifyRun(false, 20, 0, 0, false, "stop", 100, 0, 600, false, false, "") != "running" {
		t.Error("20 steps, 0 edits, a plan recorded (no no-plan event) wrongly classified no-writes")
	}
	if classifyRun(false, 20, 0, 1, false, "stop", 100, 0, 600, true, false, "") != "running" {
		t.Error("20 steps with an edit wrongly classified no-writes")
	}
	if classifyRun(false, 19, 0, 0, false, "stop", 100, 0, 600, true, false, "") != "running" {
		t.Error("19 steps (short of the threshold) wrongly classified no-writes")
	}
	// A run that is both no-writes-eligible and past the stall threshold still
	// needs the threshold itself to report stalled: no-writes is checked
	// first, so it wins here, exactly as the brief specifies.
	if classifyRun(false, 20, 0, 0, false, "stop", 100, 700, 600, true, false, "") != "no-writes" {
		t.Error("no-writes-eligible and stalled by age not classified no-writes")
	}
	// A done run is unaffected by noPlan in every case.
	if classifyRun(true, 20, 0, 0, false, "stop", 100, 0, 600, true, false, "") != "done" {
		t.Error("done run wrongly reclassified by noPlan")
	}
}

// TestClassifyRunBlocked checks a clean stop shows the caller's stopState
// instead of done, only for a clean stop, and that the helper derives it from
// the attempt's events: blocked from a permission-denied signal (over
// no-writes), no-writes from a stop finish with an empty wrote list and no
// signal at all (issue #364).
// TestClassifyRunRateLimited checks that a done attempt whose reason is
// rate-limited shows its own run state, wrote or not, and reaches the andon
// like provider-error (issue #380).
func TestClassifyRunRateLimited(t *testing.T) {
	t.Parallel()
	for _, wrote := range []bool{false, true} {
		if got := classifyRun(true, 26, 0, 3, false, "rate-limited", 100, 0, 600, false, wrote, ""); got != "rate-limited" {
			t.Errorf("classifyRun(done, rate-limited, wrote %v) = %q, want rate-limited", wrote, got)
		}
	}
	if got := classifyRun(true, 26, 0, 3, false, "error", 100, 0, 600, false, false, ""); got != "provider-error" {
		t.Errorf("classifyRun(done, error) = %q, want provider-error", got)
	}
	andon := buildAndon([]Unit{{Task: "T1", RunState: "rate-limited", LastAge: 5}, {Task: "T2", RunState: "done"}}, nil, nil)
	if len(andon) != 1 || andon[0].Task != "T1" || andon[0].State != "rate-limited" {
		t.Errorf("buildAndon() = %v, want one rate-limited entry for T1", andon)
	}
}

// TestClassifyRunAbandoned checks that a done attempt whose reason is
// abandoned-job shows its own run state, wrote or not, and reaches the andon
// like provider-error (issue #390).
func TestClassifyRunAbandoned(t *testing.T) {
	t.Parallel()
	for _, wrote := range []bool{false, true} {
		if got := classifyRun(true, 12, 0, 3, false, "abandoned-job", 100, 0, 600, false, wrote, ""); got != "abandoned-job" {
			t.Errorf("classifyRun(done, abandoned-job, wrote %v) = %q, want abandoned-job", wrote, got)
		}
	}
	andon := buildAndon([]Unit{{Task: "T1", RunState: "abandoned-job", LastAge: 5}, {Task: "T2", RunState: "done"}}, nil, nil)
	if len(andon) != 1 || andon[0].Task != "T1" || andon[0].State != "abandoned-job" {
		t.Errorf("buildAndon() = %v, want one abandoned-job entry for T1", andon)
	}
}

func TestClassifyRunBlocked(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"stop", ""} {
		for _, s := range []string{"blocked", "no-writes"} {
			if got := classifyRun(true, 3, 0, 0, false, reason, 100, 0, 600, false, false, s); got != s {
				t.Errorf("classifyRun(done, %q, stopState %q) = %q, want %q", reason, s, got, s)
			}
		}
	}
	if got := classifyRun(true, 3, 0, 0, false, "start-failed", 100, 0, 600, false, false, "blocked"); got != "failed" {
		t.Errorf("unclean finish with stopState = %q, want failed", got)
	}
	if got := classifyRun(false, 1, 0, 0, false, "stop", 100, 0, 600, false, false, "blocked"); got != "running" {
		t.Errorf("live run with stopState = %q, want running", got)
	}
	evs := []Event{
		{Task: "T1", Attempt: "r1", Kind: "finished", Reason: "stop"},
		{Task: "T1", Attempt: "r1", Kind: "signal", Signal: "permission-denied"},
		{Task: "T2", Attempt: "r1", Kind: "finished", Reason: "stop"},
		{Task: "T3", Attempt: "r1", Kind: "finished", Reason: "stop"},
		{Task: "T5", Attempt: "r1", Kind: "finished", Reason: "stop", Wrote: []string{"a.go"}},
		{Task: "T6", Attempt: "r1", Kind: "finished", Reason: "length"},
	}
	for _, c := range []struct{ task, attempt, want string }{
		{"T1", "r1", "blocked"}, {"T2", "r1", "no-writes"}, {"T3", "r2", ""}, {"T4", "r1", ""},
		{"T5", "r1", ""}, {"T6", "r1", ""},
	} {
		if got := stopStateFor(evs, c.task, c.attempt, "finished"); got != c.want {
			t.Errorf("stopStateFor(%s %s) = %q, want %q", c.task, c.attempt, got, c.want)
		}
	}
	// Only a unit still awaiting judgement shows a stop state: once passed,
	// rejected or landed, a stop that wrote nothing (or was denied) is done.
	for _, status := range []string{"passed", "rejected", "landed", "blocked"} {
		for _, task := range []string{"T1", "T2"} {
			if got := stopStateFor(evs, task, "r1", status); got != "" {
				t.Errorf("stopStateFor(%s r1, %s) = %q, want none", task, status, got)
			}
		}
	}
	if got := classifyRun(true, 3, 0, 0, false, "stop", 100, 0, 600, false, false, stopStateFor(evs, "T2", "r1", "landed")); got != "done" {
		t.Errorf("landed unit with no writes = %q, want done", got)
	}
	andon := buildAndon([]Unit{{Task: "T1", RunState: "blocked"}, {Task: "T2", RunState: "no-writes"}, {Task: "T3", RunState: "done"}}, nil, nil)
	if len(andon) != 2 {
		t.Errorf("buildAndon() = %v, want the blocked and no-writes units", andon)
	}
}

func TestLiveRun(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"running", "exploring", "long-step", "silent", "stalled", "no-writes"} {
		if !liveRun(s) {
			t.Errorf("liveRun(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"blocked", "capped", "provider-error", "failed", "failed-dirty", "done", "landed", "waiting"} {
		if liveRun(s) {
			t.Errorf("liveRun(%q) = true, want false", s)
		}
	}
}

func TestFixtureRefresh(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "s1", Persona: "lead", Model: "m1"},
		{TS: "2026-09-13T00:00:01Z", Kind: "staffed", Session: "sf1", Persona: "foreman", Model: "m2"},
		{TS: "2026-09-13T00:00:02Z", Kind: "staffed", Session: "s3", Persona: "lead"},
	}
	st := buildStaffing(Config{}, events)
	if st.Lead != "s3" {
		t.Errorf("lead = %q, want s3 (latest lead staffed event, parentheses dropped without a model)", st.Lead)
	}
}

func TestBuildStaffingModelShownInParentheses(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "s1", Persona: "lead", Model: "m1"},
	}
	st := buildStaffing(Config{}, events)
	if st.Lead != "s1 (m1)" {
		t.Errorf("lead = %q, want s1 (m1)", st.Lead)
	}
}

func TestBuildStaffingNotRegisteredWithoutStaffedEvent(t *testing.T) {
	t.Parallel()
	st := buildStaffing(Config{}, []Event{})
	if st.Lead != "not registered" {
		t.Errorf("lead = %q, want not registered", st.Lead)
	}
}

func TestBuildOutputFirstPassFromInspectedAndReviewed(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestFloorPausedModel checks a rate-limited unit shows its reset on the
// floor and its model gets one paused andon entry until then (issue #383).
func TestFloorPausedModel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC)
	reset := now.Add(20 * time.Minute)
	clock := reset.Local().Format("15:04")
	for _, e := range []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "lim", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "lim", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: "2026-09-15T00:00:30Z", Task: "lim", Kind: "finished", Attempt: "r1", Model: "m1", Reason: "rate-limited", ResetAt: reset.Format(time.RFC3339)},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	var w Watcher = NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "lim")
	if !ok || u.RunState != "rate-limited" || !u.ResetAt.Equal(reset) {
		t.Fatalf("unit lim = %+v, want run state rate-limited with reset %v", u, reset)
	}
	var paused int
	for _, a := range fl.Andon {
		if a.Task == "model/m1" {
			paused++
			if a.State != "paused until "+clock {
				t.Errorf("andon state = %q, want paused until %s", a.State, clock)
			}
		}
	}
	if paused != 1 || !andonHas(fl.Andon, "lim") {
		t.Errorf("andon = %v, want lim and one model/m1 entry", fl.Andon)
	}
	var text, js strings.Builder
	RenderText(&text, fl, 160, false)
	if !strings.Contains(text.String(), "rate-limited until "+clock) {
		t.Errorf("rendered text lacks %q:\n%s", "rate-limited until "+clock, text.String())
	}
	RenderJSON(&js, fl)
	if !strings.Contains(js.String(), `"run_state": "rate-limited"`) || !strings.Contains(js.String(), `"reset_at": "`+reset.Format(time.RFC3339)+`"`) {
		t.Errorf("JSON lacks run_state rate-limited and reset_at:\n%s", js.String())
	}
	// After the reset the model is no longer paused.
	fl, err = w.Refresh(dir, reset.Add(time.Minute))
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if andonHas(fl.Andon, "model/m1") {
		t.Errorf("andon after the reset = %v, want no model/m1", fl.Andon)
	}
}

// TestFloorWorktreeBase checks a unit dispatched into a worktree carries its
// workdir and 7-character base, the text view grows a TREE column showing the
// worktree's last path element and base, and the JSON view carries both; a unit
// in the main checkout shows neither (issue #394).
func TestFloorWorktreeBase(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 9, 15, 0, 1, 0, 0, time.UTC)
	wt := filepath.Join(dir, ".flywheel", "worktrees", "CP-A")
	for _, e := range []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "CP-A", Kind: "planned", Brief: "a.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "CP-A", Kind: "dispatched", Attempt: "r1", Workdir: wt, Base: "abcdef1234567890"},
		{TS: "2026-09-15T00:00:00Z", Task: "main", Kind: "planned", Brief: "m.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "main", Kind: "dispatched", Attempt: "r1", Base: "1234567890abcdef"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	w := NewWatcher()
	fl, err := w.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "CP-A")
	if !ok || u.Workdir != wt || u.Base != "abcdef1" {
		t.Fatalf("unit CP-A = %+v, want workdir %q base abcdef1", u, wt)
	}
	m, ok := unitBy(fl.Units, "main")
	if !ok || m.Workdir != "" || m.Base != "1234567" {
		t.Fatalf("unit main = %+v, want no workdir, base 1234567", m)
	}
	var text, js strings.Builder
	RenderText(&text, fl, 100, false)
	for _, want := range []string{"TREE", "CP-A@abcdef1"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("rendered text lacks %q:\n%s", want, text.String())
		}
	}
	for _, line := range strings.Split(text.String(), "\n") {
		if len(line) > 100 {
			t.Errorf("line wider than 100: %q", line)
		}
	}
	RenderJSON(&js, fl)
	wantWD, _ := json.Marshal(wt)
	if !strings.Contains(js.String(), `"workdir": `+string(wantWD)) || !strings.Contains(js.String(), `"base": "abcdef1"`) {
		t.Errorf("JSON lacks workdir and base:\n%s", js.String())
	}
	// Without a worktree unit the text view has no TREE column.
	fl.Units = []Unit{m}
	text.Reset()
	RenderText(&text, fl, 100, false)
	if strings.Contains(text.String(), "TREE") {
		t.Errorf("text without a worktree unit shows TREE:\n%s", text.String())
	}
}

// TestUncleanFinishWithFilesIsFailedDirty checks a done attempt with an
// unclean reason that also lists written files classifies failed-dirty
// (not plain failed), reaches the andon, and is not live (issue #163).
func TestUncleanFinishWithFilesIsFailedDirty(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
// TestFloorOpenFindings checks the floor after an agent review (issue #389):
// the unit stands at inspect, its open blocking findings raise a review-open
// andon with their count, and a dismissal clears it.
func TestFloorOpenFindings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "T1", Kind: "started", Attempt: "r1", Session: "w-1"},
		{TS: "2026-09-15T00:00:01Z", Task: "T1", Kind: "finished", Attempt: "r1", Reason: "stop", Wrote: []string{"a.go"}},
	}
	major := blockerA
	major.Severity, major.File, major.Claim = "major", "c.go", "Leaks a handle"
	events = append(events, roundEvents(1, blockerA, major, minorB)...)
	if err := AppendEvents(dir, events); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(dir, ".flywheel", "runs", "T1.r1.jsonl")
	if err := os.MkdirAll(filepath.Dir(run), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run, []byte(`{"type":"step_finish","sessionID":"w-1","part":{"type":"step_finish","reason":"stop"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now, err := time.Parse(time.RFC3339Nano, "2026-09-15T00:01:00Z")
	if err != nil {
		t.Fatal(err)
	}
	w1, w2 := NewWatcher(), NewWatcher()
	fl, err := w1.Refresh(dir, now)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	u, ok := unitBy(fl.Units, "T1")
	if !ok {
		t.Fatal("unit T1 missing")
	}
	if u.Station != "inspect" || u.Open != 2 {
		t.Errorf("unit station %q open %d, want inspect and 2", u.Station, u.Open)
	}
	found := false
	for _, a := range fl.Andon {
		found = found || a.Task == "T1" && a.State == "review-open (2)"
	}
	if !found {
		t.Errorf("andon lacks T1 review-open (2): %+v", fl.Andon)
	}
	if err := AppendEvents(dir, []Event{
		{Task: "T1", Kind: "finding_response", Session: "lead-1", Finding: "T1-r1-1", Verdict: "disputed", Note: "dismissed: by design"},
		{Task: "T1", Kind: "finding_response", Session: "lead-1", Finding: "T1-r1-2", Verdict: "disputed", Note: "dismissed: by design"},
	}); err != nil {
		t.Fatal(err)
	}
	if fl, err = w2.Refresh(dir, now); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	for _, a := range fl.Andon {
		if a.Task == "T1" && strings.HasPrefix(a.State, "review-open") {
			t.Errorf("review-open andon after the dismissals: %+v", a)
		}
	}
}

func TestStopFinishIsDoneAndFinished(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	events := []Event{
		{TS: "2026-09-15T00:00:00Z", Task: "clean", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-15T00:00:00Z", Task: "clean", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-15T00:00:00Z", Task: "clean", Kind: "started", Session: "s1"},
		// A stop finish that wrote nothing shows no-writes (issue #364), so the
		// common case writes a file.
		{TS: "2026-09-15T00:00:01Z", Task: "clean", Kind: "finished", Attempt: "r1", Reason: "stop", Wrote: []string{"a.go"}},
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
	t.Parallel()
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
	t.Parallel()
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

// TestStackedRunState checks a finished unit in its task worktree whose base
// landed as a squash shows run state stacked and reaches the andon; once
// rebased it does not (issue #414).
func TestStackedRunState(t *testing.T) {
	t.Parallel()
	dir, _, _ := stackedRepo(t)
	if err := AppendEvent(dir, Event{Task: "B", Kind: "finished", Attempt: "r1"}); err != nil {
		t.Fatal(err)
	}
	w := NewWatcher()
	fl, err := w.Refresh(dir, time.Now())
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if u, ok := unitBy(fl.Units, "B"); !ok || u.RunState != "stacked" {
		t.Fatalf("unit B = %+v (found %v), want run state stacked", u, ok)
	}
	if !andonHas(fl.Andon, "B") {
		t.Errorf("andon missing B: %v", fl.Andon)
	}
	if _, _, err := RebaseUnit(dir, "B", ""); err != nil {
		t.Fatalf("RebaseUnit() error = %v", err)
	}
	w = NewWatcher()
	if fl, err = w.Refresh(dir, time.Now()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if u, _ := unitBy(fl.Units, "B"); u.RunState == "stacked" {
		t.Errorf("unit B still stacked after the rebase")
	}
}
