package flywheel

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureFloor refreshes the factory fixture at the fixed test clock and
// returns the resulting Floor. It pins the stalled run's old mtime the same
// way factory_test does, so the stalled unit classifies as stalled.
func fixtureFloor(t *testing.T) Floor {
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
	return fl
}

func TestRenderTextGolden(t *testing.T) {
	fl := fixtureFloor(t)
	var buf bytes.Buffer
	RenderText(&buf, fl, 100, false)
	got := normLF(buf.Bytes())
	want, err := os.ReadFile("testdata/factory/floor.golden")
	if err != nil {
		t.Fatalf("read floor.golden: %v", err)
	}
	if got != normLF(want) {
		t.Errorf("RenderText() differs from floor.golden:\n%s", got)
	}
}

func TestRenderJSON(t *testing.T) {
	fl := fixtureFloor(t)
	var buf bytes.Buffer
	RenderJSON(&buf, fl)
	text := string(buf.Bytes())
	var j jFloor
	if err := json.Unmarshal(buf.Bytes(), &j); err != nil {
		t.Fatalf("RenderJSON() does not parse: %v\n%s", err, text)
	}
	if len(j.Units) != 7 {
		t.Errorf("units = %d, want 7", len(j.Units))
	}
	if len(j.Andon) != 3 {
		t.Errorf("andon = %d, want 3", len(j.Andon))
	}
	if len(j.Lines) != 1 || j.Lines[0].Name != "default" || j.Lines[0].Busy != 3 {
		t.Errorf("lines = %v, want default line busy 3 live", j.Lines)
	}
	if j.Staffing.Lead != "not registered" {
		t.Errorf("lead = %q, want not registered", j.Staffing.Lead)
	}
	if j.Output.LandedToday != 1 || j.Output.Finished != 1 {
		t.Errorf("output = %v, want landed 1 finished 1", j.Output)
	}
	if j.Output.Tokens != 260 || j.Output.Cost != 0.004 {
		t.Errorf("tokens/cost = %d %g, want 260 0.004", j.Output.Tokens, j.Output.Cost)
	}
	if j.Output.HasReviews {
		t.Errorf("has_reviews = true, want false (no reviewed events)")
	}
}

func TestRenderTextWidth(t *testing.T) {
	// 80 stays the minimum layout; the extra (width-80) columns are given to
	// MODEL (two thirds) and TASK (one third). At width 120 a 40-character
	// model id is shown in full; at width 80 the table keeps the fixed layout.
	model40 := strings.Repeat("m", 40)
	task := strings.Repeat("t", 16)
	fl := Floor{
		Refreshed: time.Date(2026, 9, 12, 1, 0, 0, 0, time.UTC),
		Lines:     []FloorLine{{Name: "default", Adapter: "opencode", Model: model40, MaxParallel: 4, Busy: 1}},
		Units:     []Unit{{Task: task, Stage: "building", Model: model40, RunState: "running"}},
	}
	var b80, b120 bytes.Buffer
	RenderText(&b80, fl, 80, false)
	RenderText(&b120, fl, 120, false)
	out80 := b80.String()
	out120 := b120.String()
	if !strings.Contains(out120, model40) {
		t.Errorf("width 120: the 40-character model id is not shown in full:\n%s", out120)
	}
	if strings.Contains(out80, task) {
		t.Errorf("width 80: task shown in full, want the fixed 12-column TASK truncation:\n%s", out80)
	}
	if !strings.Contains(out120, task) {
		t.Errorf("width 120: task not shown in full with the wider TASK column:\n%s", out120)
	}
}

func TestHumanAge(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    string
	}{
		{59, "59s"},
		{60, "1m"},
		{3599, "59m"},
		{3600, "1h"},
		{172799, "47h"},
		{172800, "2d"},
	} {
		if got := HumanAge(tc.seconds); got != tc.want {
			t.Errorf("HumanAge(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestRenderTextPassedRejectedStages(t *testing.T) {
	fl := Floor{
		Refreshed: time.Date(2026, 9, 12, 1, 0, 0, 0, time.UTC),
		Units: []Unit{
			{Task: "p", Stage: "passed", Attempt: "r1", RunState: "done"},
			{Task: "x", Stage: "rejected", Attempt: "r1", RunState: "done"},
		},
	}
	var buf bytes.Buffer
	RenderText(&buf, fl, 80, false)
	out := buf.String()
	if !strings.Contains(out, "passed") {
		t.Errorf("passed unit's stage not rendered:\n%s", out)
	}
	if !strings.Contains(out, "rejected") {
		t.Errorf("rejected unit's stage not rendered:\n%s", out)
	}
}

// TestRenderTextCappedShowsPeak checks a capped unit's RUN cell carries its
// peak reasoning figure, a capped unit with no recorded peak still renders
// plain "capped", and every other state is unaffected (issue #84).
func TestRenderTextCappedShowsPeak(t *testing.T) {
	fl := Floor{
		Refreshed: time.Date(2026, 9, 12, 1, 0, 0, 0, time.UTC),
		Units: []Unit{
			{Task: "peaked", Stage: "cut-off", Attempt: "r1", RunState: "capped", Peak: 50},
			{Task: "nopeak", Stage: "cut-off", Attempt: "r1", RunState: "capped", Peak: 0},
			{Task: "clean", Stage: "finished", Attempt: "r1", RunState: "done", Peak: 0},
		},
	}
	var buf bytes.Buffer
	RenderText(&buf, fl, 80, false)
	out := buf.String()
	if !strings.Contains(out, "capped 50") {
		t.Errorf("capped unit with a peak does not show capped 50:\n%s", out)
	}
	if strings.Contains(out, "capped 0") {
		t.Errorf("capped unit with no peak wrongly shows capped 0:\n%s", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("done unit's RUN cell missing:\n%s", out)
	}
}

// TestRenderJSONCarriesPeakReasoning checks the units[] JSON entry carries
// peak_reasoning, 0 when none (issue #84).
func TestRenderJSONCarriesPeakReasoning(t *testing.T) {
	fl := Floor{
		Units: []Unit{
			{Task: "peaked", RunState: "capped", Peak: 50},
			{Task: "clean", RunState: "done", Peak: 0},
		},
	}
	var buf bytes.Buffer
	RenderJSON(&buf, fl)
	var j jFloor
	if err := json.Unmarshal(buf.Bytes(), &j); err != nil {
		t.Fatalf("RenderJSON() does not parse: %v\n%s", err, buf.String())
	}
	if len(j.Units) != 2 {
		t.Fatalf("units = %d, want 2", len(j.Units))
	}
	if j.Units[0].PeakReasoning != 50 {
		t.Errorf("peaked unit peak_reasoning = %d, want 50", j.Units[0].PeakReasoning)
	}
	if j.Units[1].PeakReasoning != 0 {
		t.Errorf("clean unit peak_reasoning = %d, want 0", j.Units[1].PeakReasoning)
	}
	if !strings.Contains(buf.String(), `"peak_reasoning": 0`) {
		t.Errorf("JSON does not carry peak_reasoning 0 explicitly:\n%s", buf.String())
	}
}

func TestRenderOutputFormats(t *testing.T) {
	fl := Floor{Output: Output{LandedToday: 1, Finished: 1, Rework: 0.33333, Tokens: 260, Cost: 0.001597088}}
	var buf bytes.Buffer
	RenderText(&buf, fl, 80, false)
	out := buf.String()
	if !strings.Contains(out, "rework 0.33") {
		t.Errorf("rework not fixed to 2 decimals: %q", out)
	}
	if !strings.Contains(out, "cost $0.0016") {
		t.Errorf("cost not fixed to 4 decimals: %q", out)
	}
}
