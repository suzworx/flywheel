package flywheel

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/suzworx/flywheel/internal/term"
)

func makeTestTUIData() TUIData {
	return TUIData{
		Floor: Floor{
			Dir:       "/home/user/repo",
			Refreshed: time.Now(),
			Lines: []FloorLine{
				{
					Name:        "worker1",
					Adapter:     "claude",
					Model:       "claude-opus",
					MaxParallel: 2,
					Busy:        1,
				},
				{
					Name:        "worker2",
					Adapter:     "openai",
					Model:       "gpt-4",
					MaxParallel: 3,
					Busy:        2,
				},
			},
			Staffing: Staffing{
				Lead: "test-lead",
			},
			Units: []Unit{
				{
					Task:     "T1",
					Stage:    "building",
					Attempt:  "1",
					Session:  "s1",
					Model:    "claude-opus",
					Steps:    5,
					LastAge:  30,
					RunState: "running",
					Peak:     0,
				},
				{
					Task:     "T2",
					Stage:    "finished",
					Attempt:  "1",
					Session:  "s2",
					Model:    "gpt-4",
					Steps:    10,
					LastAge:  120,
					RunState: "done",
					Peak:     0,
				},
				{
					Task:     "T3",
					Stage:    "building",
					Attempt:  "1",
					Session:  "s3",
					Model:    "claude-opus",
					Steps:    7,
					LastAge:  60,
					RunState: "capped",
					Peak:     50000,
				},
			},
			Andon: []Andon{
				{
					Task:  "T3",
					State: "capped",
					Age:   60,
				},
			},
			Output: Output{
				LandedToday:   5,
				Finished:      20,
				FirstPassRate: 0.85,
				HasReviews:    true,
				Rework:        1.2,
				Tokens:        100000,
				Cost:          2.50,
			},
		},
		Events: []string{
			"event 1",
			"event 2",
			"event 3",
		},
		Detail: nil,
	}
}

func TestTUIDefaultViewIsUnits(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()
	view := m.View(d, 100, 20, false)
	lines := strings.Split(view, "\n")

	if len(lines) != 20 {
		t.Errorf("expected 20 lines, got %d", len(lines))
	}

	if !strings.Contains(view, "Units(all)[3]") {
		t.Errorf("expected 'Units(all)[3]' in view, got:\n%s", view)
	}
}

func TestTUICursorMovesAndClamps(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Down x5 should clamp to last row (index 2, since 0-2 = 3 rows).
	for i := 0; i < 5; i++ {
		m.Update(term.Key{Kind: term.KeyDown}, d)
	}
	if m.cursor != 2 {
		t.Errorf("after Down x5, cursor should be 2 (last), got %d", m.cursor)
	}

	// Up x9 should clamp to first row.
	for i := 0; i < 9; i++ {
		m.Update(term.Key{Kind: term.KeyUp}, d)
	}
	if m.cursor != 0 {
		t.Errorf("after Up x9, cursor should be 0 (first), got %d", m.cursor)
	}
}

func TestTUICommandSwitchesView(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Open command prompt.
	m.Update(term.Key{Kind: term.KeyRune, Rune: ':'}, d)
	if m.promptKind != "command" {
		t.Errorf("expected command prompt, got %q", m.promptKind)
	}

	// Type "w".
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'w'}, d)
	if m.prompt != "w" {
		t.Errorf("expected prompt 'w', got %q", m.prompt)
	}

	// Enter.
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if m.view != "workers" {
		t.Errorf("expected view 'workers', got %q", m.view)
	}

	// Check title contains "Workers".
	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "Workers(all)[2]") {
		t.Errorf("expected 'Workers(all)[2]' in view")
	}
}

func TestTUIUnknownCommandMessage(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	m.Update(term.Key{Kind: term.KeyRune, Rune: ':'}, d)
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'x'}, d)
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'y'}, d)
	m.Update(term.Key{Kind: term.KeyEnter}, d)

	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "unknown view: xy") {
		t.Errorf("expected 'unknown view: xy' in last line of view")
	}
}

func TestTUIFilter(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Open filter prompt.
	m.Update(term.Key{Kind: term.KeyRune, Rune: '/'}, d)
	if m.promptKind != "filter" {
		t.Errorf("expected filter prompt")
	}

	// Type "cap".
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'c'}, d)
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'a'}, d)
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'p'}, d)

	// Enter.
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if m.filter != "cap" {
		t.Errorf("expected filter 'cap', got %q", m.filter)
	}

	// Check rows.
	header, rows := m.Rows(d)
	_ = header
	if len(rows) != 1 {
		t.Errorf("expected 1 filtered row (T3 has 'cap' in 'capped'), got %d", len(rows))
	}
	if len(rows) > 0 && rows[0][0] != "T3" {
		t.Errorf("expected T3 in filtered row, got %s", rows[0][0])
	}

	// Check title contains filter text.
	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "Units(/cap)[1]") {
		t.Errorf("expected 'Units(/cap)[1]' in view")
	}

	// Esc should clear filter.
	m.Update(term.Key{Kind: term.KeyEsc}, d)
	_, rows = m.Rows(d)
	if len(rows) != 3 {
		t.Errorf("after Esc, expected 3 rows, got %d", len(rows))
	}
}

func TestTUIEnterWantsExplain(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Move down once to T2.
	m.Update(term.Key{Kind: term.KeyDown}, d)
	if m.cursor != 1 {
		t.Errorf("expected cursor at 1, got %d", m.cursor)
	}

	// Enter to drill down.
	m.Update(term.Key{Kind: term.KeyEnter}, d)

	// Check Wants returns the correct drill-down.
	kind, task, ok := m.Wants()
	if !ok {
		t.Errorf("Wants should return ok=true")
	}
	if kind != "explain" {
		t.Errorf("expected kind 'explain', got %q", kind)
	}
	if task != "T2" {
		t.Errorf("expected task 'T2', got %q", task)
	}

	// Add detail to the data and check the view.
	d.Detail = []string{"line a", "line b"}
	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "line a") || !strings.Contains(view, "line b") {
		t.Errorf("expected detail lines in view")
	}
	if !strings.Contains(view, "Explain") {
		t.Errorf("expected 'Explain' in title bar")
	}
	if !strings.Contains(view, "<units>") && !strings.Contains(view, "<T2>") && !strings.Contains(view, "<explain>") {
		t.Errorf("expected breadcrumbs in view")
	}

	// Esc to exit drill-down.
	m.Update(term.Key{Kind: term.KeyEsc}, d)
	_, _, ok = m.Wants()
	if ok {
		t.Errorf("Wants should return ok=false after Esc")
	}
}

func TestTUILogKey(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Move to T2.
	m.Update(term.Key{Kind: term.KeyDown}, d)

	// Press 'l' to open log.
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'l'}, d)

	kind, task, ok := m.Wants()
	if !ok {
		t.Errorf("Wants should return ok=true")
	}
	if kind != "log" {
		t.Errorf("expected kind 'log', got %q", kind)
	}
	if task != "T2" {
		t.Errorf("expected task 'T2', got %q", task)
	}
}

func TestTUIQuit(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Press 'q'.
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'q'}, d)
	if !m.Quit() {
		t.Errorf("expected Quit() to return true")
	}

	// Test Ctrl-C.
	m2 := NewTUI()
	m2.Update(term.Key{Kind: term.KeyCtrlC}, d)
	if !m2.Quit() {
		t.Errorf("expected Ctrl-C to trigger Quit()")
	}
}

func TestTUIHelp(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Press '?' to open help.
	m.Update(term.Key{Kind: term.KeyRune, Rune: '?'}, d)
	if !m.help {
		t.Errorf("expected help to be shown")
	}

	// Check the frame mentions "filter" and "explain".
	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "filter") {
		t.Errorf("expected 'filter' in help view")
	}
	if !strings.Contains(view, "explain") {
		t.Errorf("expected 'explain' in help view")
	}

	// Esc to exit help.
	m.Update(term.Key{Kind: term.KeyEsc}, d)
	if m.help {
		t.Errorf("expected help to be closed after Esc")
	}
}

func TestTUIWidthRespected(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	width := 40
	height := 12
	view := m.View(d, width, height, false)
	lines := strings.Split(view, "\n")

	if len(lines) != height {
		t.Errorf("expected exactly %d lines, got %d", height, len(lines))
	}

	for i, line := range lines {
		// Count runes to respect multi-byte characters.
		runeCount := len([]rune(line))
		if runeCount > width {
			t.Errorf("line %d exceeds width %d: %d runes (line: %q)", i, width, runeCount, line)
		}
	}
}

func TestTUIEventsNewestFirst(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Switch to events view.
	m.Update(term.Key{Kind: term.KeyRune, Rune: ':'}, d)
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'e'}, d)
	m.Update(term.Key{Kind: term.KeyEnter}, d)

	if m.view != "events" {
		t.Errorf("expected view to be 'events', got %q", m.view)
	}

	// Get rows.
	header, rows := m.Rows(d)
	_ = header
	if len(rows) != 3 {
		t.Errorf("expected 3 event rows, got %d", len(rows))
	}

	// Events should be newest first: d.Events = ["event 1", "event 2", "event 3"]
	// In rowsEvents, we iterate from len-1 down to 0, so: ["event 3", "event 2", "event 1"].
	if len(rows) > 0 {
		if rows[0][0] != "event 3" {
			t.Errorf("first event should be 'event 3' (newest), got %q", rows[0][0])
		}
		if rows[2][0] != "event 1" {
			t.Errorf("last event should be 'event 1' (oldest), got %q", rows[2][0])
		}
	}
}

func TestTUIScrollKeepsCursorVisible(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()
	// Create a Floor with 30 units (T00..T29).
	d.Floor.Units = []Unit{}
	for i := 0; i < 30; i++ {
		d.Floor.Units = append(d.Floor.Units, Unit{
			Task:     fmt.Sprintf("T%02d", i),
			Stage:    "building",
			Attempt:  "1",
			Session:  fmt.Sprintf("s%d", i),
			Model:    "claude-opus",
			Steps:    5,
			LastAge:  30,
			RunState: "running",
			Peak:     0,
		})
	}

	// Move to end (G key).
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'G'}, d)

	view := m.View(d, 80, 12, false)
	if !strings.Contains(view, "> T29") {
		t.Errorf("expected '> T29' in view after G, got:\n%s", view)
	}
	if strings.Contains(view, "T00") {
		t.Errorf("expected 'T00' not in view after G")
	}

	// Move to beginning (g key).
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'g'}, d)
	view = m.View(d, 80, 12, false)
	if !strings.Contains(view, "> T00") {
		t.Errorf("expected '> T00' in view after g, got:\n%s", view)
	}
}

func TestTUIDetailScroll(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()
	d.Floor.Units = []Unit{{Task: "T1", Stage: "building", Attempt: "1", Session: "s1", Model: "claude", Steps: 1, LastAge: 1, RunState: "running", Peak: 0}}

	// Create 50 lines of detail.
	d.Detail = []string{}
	for i := 0; i < 50; i++ {
		d.Detail = append(d.Detail, fmt.Sprintf("line %d", i))
	}

	// Enter to open explain drill-down.
	m.Update(term.Key{Kind: term.KeyEnter}, d)

	view := m.View(d, 80, 12, false)
	if !strings.Contains(view, "line 0") {
		t.Errorf("expected 'line 0' in view at start, got:\n%s", view)
	}

	// Page down (PgDn).
	m.Update(term.Key{Kind: term.KeyPgDn}, d)
	view = m.View(d, 80, 12, false)
	if strings.Contains(view, "line 0") {
		t.Errorf("expected 'line 0' not in view after PgDn")
	}

	// Move up many times to get back to the beginning.
	for i := 0; i < 100; i++ {
		m.Update(term.Key{Kind: term.KeyRune, Rune: 'k'}, d)
	}
	view = m.View(d, 80, 12, false)
	if !strings.Contains(view, "line 0") {
		t.Errorf("expected 'line 0' in view after many 'k' moves, got:\n%s", view)
	}

	// Page down many times; should not panic and should contain line 49 after 20 PgDn.
	for i := 0; i < 20; i++ {
		m.Update(term.Key{Kind: term.KeyPgDn}, d)
	}
	view = m.View(d, 80, 12, false)
	if !strings.Contains(view, "line 49") {
		t.Errorf("expected 'line 49' in view after 20 PgDn, got:\n%s", view)
	}
}

func TestTUICtrlCQuitsEverywhere(t *testing.T) {
	d := makeTestTUIData()

	// Ctrl-C in drill-down mode.
	m1 := NewTUI()
	m1.Update(term.Key{Kind: term.KeyEnter}, d) // Open explain drill-down.
	m1.Update(term.Key{Kind: term.KeyCtrlC}, d)
	if !m1.Quit() {
		t.Errorf("expected Ctrl-C to quit in drill-down mode")
	}

	// Ctrl-C with command prompt open.
	m2 := NewTUI()
	m2.Update(term.Key{Kind: term.KeyRune, Rune: ':'}, d)
	m2.Update(term.Key{Kind: term.KeyCtrlC}, d)
	if !m2.Quit() {
		t.Errorf("expected Ctrl-C to quit in command prompt mode")
	}

	// Ctrl-C with filter prompt open.
	m3 := NewTUI()
	m3.Update(term.Key{Kind: term.KeyRune, Rune: '/'}, d)
	m3.Update(term.Key{Kind: term.KeyCtrlC}, d)
	if !m3.Quit() {
		t.Errorf("expected Ctrl-C to quit in filter prompt mode")
	}

	// Ctrl-C in help mode.
	m4 := NewTUI()
	m4.Update(term.Key{Kind: term.KeyRune, Rune: '?'}, d)
	m4.Update(term.Key{Kind: term.KeyCtrlC}, d)
	if !m4.Quit() {
		t.Errorf("expected Ctrl-C to quit in help mode")
	}
}

func TestTUIColumnsAligned(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	view := m.View(d, 100, 20, false)
	lines := strings.Split(view, "\n")

	// Find the header line and a data row.
	headerLine := ""
	dataLine := ""
	for _, line := range lines {
		if strings.Contains(line, "TASK") && strings.Contains(line, "STAGE") {
			headerLine = line
		} else if strings.Contains(line, "T1") {
			dataLine = line
		}
	}

	if headerLine == "" || dataLine == "" {
		t.Errorf("could not find header and data lines")
		return
	}

	// Find the rune index of "STAGE" in the header.
	stageIdx := strings.Index(headerLine, "STAGE")
	if stageIdx < 0 {
		t.Errorf("could not find 'STAGE' in header")
		return
	}

	// Find the rune index of the stage text in the data row.
	// The stage for T1 is "building".
	buildingIdx := strings.Index(dataLine, "building")
	if buildingIdx < 0 {
		t.Errorf("could not find 'building' in data line")
		return
	}

	// The rune indices should be aligned.
	headerStageRuneIdx := len([]rune(headerLine[:stageIdx]))
	dataBuildingRuneIdx := len([]rune(dataLine[:buildingIdx]))

	if headerStageRuneIdx != dataBuildingRuneIdx {
		t.Errorf("columns not aligned: STAGE at rune %d, building at rune %d", headerStageRuneIdx, dataBuildingRuneIdx)
	}
}

func TestTUIMultibyteWidth(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	widths := []int{30, 41, 57}
	for _, w := range widths {
		view := m.View(d, w, 20, false)
		lines := strings.Split(view, "\n")

		if len(lines) != 20 {
			t.Errorf("width %d: expected 20 lines, got %d", w, len(lines))
		}

		for i, line := range lines {
			if !utf8.ValidString(line) {
				t.Errorf("width %d, line %d: invalid UTF-8", w, i)
			}
			runes := []rune(line)
			if len(runes) > w {
				t.Errorf("width %d, line %d: %d runes exceeds width (line: %q)", w, i, len(runes), line)
			}
		}
	}
}

func TestTUIColorCursorRow(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Test with color enabled.
	view := m.View(d, 100, 20, true)
	if !strings.Contains(view, "\x1b[7m") {
		t.Errorf("expected reverse video escape in view with color=true")
	}

	// Find a row with "capped" state and check for color.
	if strings.Contains(view, "\x1b[31m") { // ansiRed for capped.
		// Found color for capped state; good.
	}

	// Test with color disabled.
	view = m.View(d, 100, 20, false)
	if strings.Contains(view, "\x1b[") {
		t.Errorf("expected no escape sequences with color=false")
	}
}

func TestTUILiveFilter(t *testing.T) {
	m := NewTUI()
	d := makeTestTUIData()

	// Open filter prompt.
	m.Update(term.Key{Kind: term.KeyRune, Rune: '/'}, d)
	// Type "c".
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'c'}, d)
	// Type "a".
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'a'}, d)
	// At this point, prompt is "ca", no Enter.

	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "Units(/ca)[1]") {
		t.Errorf("expected 'Units(/ca)[1]' in view while typing filter, got:\n%s", view)
	}

	// Esc to clear filter.
	m.Update(term.Key{Kind: term.KeyEsc}, d)
	view = m.View(d, 100, 20, false)
	if !strings.Contains(view, "Units(all)[3]") {
		t.Errorf("expected 'Units(all)[3]' in view after Esc, got:\n%s", view)
	}
}
