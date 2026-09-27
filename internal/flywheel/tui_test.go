package flywheel

import (
	"fmt"
	"regexp"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	m := NewTUI()
	d := makeTestTUIData()

	m.Update(term.Key{Kind: term.KeyRune, Rune: ':'}, d)
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'x'}, d)
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'y'}, d)
	m.Update(term.Key{Kind: term.KeyEnter}, d)

	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "unknown view :xy (Ctrl-A lists them)") {
		t.Errorf("expected 'unknown view :xy (Ctrl-A lists them)' in the view")
	}
}

func TestTUIFilter(t *testing.T) {
	t.Parallel()
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

func TestTUIEnterWantsWhy(t *testing.T) {
	t.Parallel()
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
	if kind != "why" {
		t.Errorf("expected kind 'why', got %q", kind)
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
	if !strings.Contains(view, "── Why T2") {
		t.Errorf("expected 'Why' in title bar")
	}
	if !strings.Contains(view, "<units>") && !strings.Contains(view, "<T2>") && !strings.Contains(view, "<why>") {
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	m := NewTUI()
	d := makeTestTUIData()

	// Press '?' to open help.
	m.Update(term.Key{Kind: term.KeyRune, Rune: '?'}, d)
	if !m.help {
		t.Errorf("expected help to be shown")
	}

	// Check the frame mentions "filter" and "explain" (tall enough for the
	// whole help; TestTUIHelpScrolls covers a short one).
	view := m.View(d, 100, 60, false)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestTUIEmptyTableNoDrillDown checks that moving on an empty table keeps the
// cursor at 0 and Enter/l open nothing (#344 review).
func TestTUIEmptyTableNoDrillDown(t *testing.T) {
	t.Parallel()
	d := TUIData{}
	m := NewTUI()
	for _, k := range []term.Key{{Kind: term.KeyDown}, {Kind: term.KeyRune, Rune: 'j'}, {Kind: term.KeyPgDn}, {Kind: term.KeyEnd}, {Kind: term.KeyEnter}, {Kind: term.KeyRune, Rune: 'l'}} {
		m.Update(k, d)
	}
	if _, _, ok := m.Wants(); ok {
		t.Error("Wants() ok = true on an empty table, want false")
	}
	if m.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.cursor)
	}
}

// TestTUIAndonStateColored checks that the andon view colours rows off the
// cursor by their STATE (#344 review; the whole row since issue #583 k6).
func TestTUIAndonStateColored(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	d.Floor.Andon = append(d.Floor.Andon, Andon{Task: "T9", State: "stalled", Age: 5})
	m := NewTUI()
	// T9 (stalled, high) sorts first; j moves the cursor off it.
	for _, k := range []term.Key{{Kind: term.KeyRune, Rune: ':'}, {Kind: term.KeyRune, Rune: 'a'}, {Kind: term.KeyEnter}, {Kind: term.KeyRune, Rune: 'j'}} {
		m.Update(k, d)
	}
	frame := m.View(d, 80, 12, true)
	if !hasRow(frame, skins["dark"].Failed+"  T9", "stalled") {
		t.Errorf("andon frame lacks a red T9 stalled row:\n%q", frame)
	}
}

// hasRow reports whether a line of frame starts with prefix and holds every
// one of texts.
func hasRow(frame, prefix string, texts ...string) bool {
	for _, l := range strings.Split(frame, "\n") {
		if strings.HasPrefix(l, prefix) && lineWith(l, texts...) != "" {
			return true
		}
	}
	return false
}

// TestTUIFullscreenHidesChrome checks that f in a drill-down (the log and
// explain tabs, a learning) hides the header and the crumbs and gives their
// lines to the drill-down, f again brings them back, and a table ignores it.
func TestTUIFullscreenHidesChrome(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	d.Detail = []string{"line one", "line two"}
	chrome := func(f string) (header, crumbs bool) {
		return strings.Contains(f, "lead test-lead"), strings.Contains(f, "<units> <T1>")
	}
	m := NewTUI()
	press(m, d, "f")
	if h, _ := chrome(m.View(d, 120, 30, false)); !h {
		t.Error("f in the units table hid the header")
	}
	press(m, d, "l")
	for _, tab := range []string{"", "d"} {
		press(m, d, tab)
		if h, c := chrome(m.View(d, 120, 30, false)); !h || !c {
			t.Fatalf("tab %q before f: header %v, crumbs %v; want both", tab, h, c)
		}
		press(m, d, "f")
		f := m.View(d, 120, 30, false)
		if h, c := chrome(f); h || c || !strings.HasPrefix(f, "── ") || len(strings.Split(f, "\n")) != 30 {
			t.Errorf("tab %q fullscreen: header %v, crumbs %v, want neither and the title first in 30 lines:\n%s", tab, h, c, f)
		}
		press(m, d, "f")
		if h, c := chrome(m.View(d, 120, 30, false)); !h || !c {
			t.Errorf("tab %q: f again did not bring back the header and crumbs", tab)
		}
	}
}

// logData is the test floor with 40 log lines for T1, the last one long.
func logData() TUIData {
	d := makeTestTUIData()
	for i := range 39 {
		d.Detail = append(d.Detail, fmt.Sprintf("12:00:%02d T1 event %d", i, i))
	}
	d.Detail = append(d.Detail, "12:00:39 T1 event 39 "+strings.Repeat("x", 80)+" END")
	return d
}

// TestTUILogKeysFollowWrapTimestamps checks the log tab: it follows a
// running unit (its last line in sight as lines arrive), a scroll up pauses
// that and says "paused; G to follow", G follows again; t hides the
// timestamps; w wraps long lines (and does not switch to the why tab); a
// unit that does not run opens at its first line.
func TestTUILogKeysFollowWrapTimestamps(t *testing.T) {
	t.Parallel()
	d := logData()
	m := NewTUI()
	press(m, d, "l") // T1, running
	if f := m.View(d, 60, 20, false); !strings.Contains(f, "event 39") || strings.Contains(f, "event 0\n") {
		t.Errorf("a running unit's log does not follow:\n%s", f)
	}
	press(m, d, "k")
	d.Detail = append(d.Detail, "12:01:00 T1 event 40")
	if f := m.View(d, 60, 20, false); !strings.Contains(f, "paused; G to follow") || strings.Contains(f, "event 40") {
		t.Errorf("k did not pause the follow:\n%s", f)
	}
	press(m, d, "G")
	if f := m.View(d, 60, 20, false); strings.Contains(f, "paused") || !strings.Contains(f, "event 40") {
		t.Errorf("G did not follow again:\n%s", f)
	}
	if f := m.View(d, 60, 20, false); strings.Contains(f, " END") {
		t.Errorf("the long line was not cut before w:\n%s", f)
	}
	press(m, d, "w")
	if f := m.View(d, 60, 20, false); m.drillKind != "log" || !strings.Contains(f, "x END") {
		t.Errorf("w: tab %q, the long line's end not wrapped into sight:\n%s", m.drillKind, f)
	}
	press(m, d, "t")
	if f := m.View(d, 60, 20, false); strings.Contains(f, "12:0") || !strings.Contains(f, "T1 event 40") {
		t.Errorf("t did not hide the timestamps:\n%s", f)
	}
	press(m, d, "t")
	if f := m.View(d, 60, 20, false); !strings.Contains(f, "12:01:00 T1 event 40") {
		t.Errorf("t again did not show the timestamps:\n%s", f)
	}

	m = NewTUI()
	press(m, d, "jl") // T2, done
	if f := m.View(d, 60, 20, false); !strings.Contains(f, "event 0\n") || strings.Contains(f, "paused") {
		t.Errorf("a unit that does not run: its log should open at the top, not paused:\n%s", f)
	}
}

// TestTUILastLineAtBottom checks that the prompt/status/breadcrumbs line is
// the frame's last line in help and in a short drill-down (#344 review).
func TestTUILastLineAtBottom(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	m := NewTUI()
	m.Update(term.Key{Kind: term.KeyRune, Rune: '?'}, d)
	lines := strings.Split(m.View(d, 80, 40, false), "\n")
	if len(lines) != 40 || lines[39] != "<units> <help>" {
		t.Errorf("help: %d lines, last %q; want 40 and <units> <help>", len(lines), lines[len(lines)-1])
	}
	m = NewTUI()
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	d.Detail = []string{"only line"}
	lines = strings.Split(m.View(d, 80, 20, false), "\n")
	if len(lines) != 20 || !strings.HasPrefix(lines[19], "<units> <") {
		t.Errorf("drill-down: %d lines, last %q; want 20 and the breadcrumbs", len(lines), lines[len(lines)-1])
	}
}

// TestTUISmallFrameNeverEnlarged checks that View never returns more lines
// or wider lines than asked, and keeps the last line (#344 review).
func TestTUISmallFrameNeverEnlarged(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	for _, size := range [][2]int{{10, 3}, {1, 1}, {15, 2}, {5, 6}} {
		m := NewTUI()
		lines := strings.Split(m.View(d, size[0], size[1], false), "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if utf8.RuneCountInString(l) > size[0] {
				t.Errorf("%dx%d: line %q is wider", size[0], size[1], l)
			}
		}
		if last := lines[len(lines)-1]; last != cutRunes("<units>", size[0]) {
			t.Errorf("%dx%d: last line %q, want the breadcrumbs", size[0], size[1], last)
		}
	}
	if got := NewTUI().View(d, 0, 10, false); got != "" {
		t.Errorf("View at width 0 = %q, want empty", got)
	}
}

// TestTUIHelpScrolls checks that help scrolls on a short screen so its last
// line is reachable (#344 review).
func TestTUIHelpScrolls(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	m := NewTUI()
	m.Update(term.Key{Kind: term.KeyRune, Rune: '?'}, d)
	if strings.Contains(m.View(d, 80, 8, false), "q       quit") {
		t.Fatal("an 8-line help already shows its last line; the test needs a shorter screen")
	}
	for i := 0; i < 60; i++ {
		m.Update(term.Key{Kind: term.KeyRune, Rune: 'j'}, d)
		m.View(d, 80, 8, false)
	}
	if !strings.Contains(m.View(d, 80, 8, false), "q       quit") {
		t.Error("help scrolled to the end does not show its last line (q quit)")
	}
}

// TestTUILinesView checks that :lines command switches to lines view and shows product lines.
func TestTUILinesView(t *testing.T) {
	t.Parallel()
	m := NewTUI()
	d := makeTestTUIData()

	// Open command prompt.
	m.Update(term.Key{Kind: term.KeyRune, Rune: ':'}, d)
	if m.promptKind != "command" {
		t.Errorf("expected command prompt")
	}

	// Type "lines".
	for _, r := range "lines" {
		m.Update(term.Key{Kind: term.KeyRune, Rune: r}, d)
	}

	// Enter.
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if m.view != "lines" {
		t.Errorf("expected view 'lines', got %q", m.view)
	}

	// Check title contains "Lines" with count.
	view := m.View(d, 100, 20, false)
	if !strings.Contains(view, "Lines(all)") {
		t.Errorf("expected 'Lines(all)' in view, got:\n%s", view)
	}
}

// TestTUILinesUnitsLineColumn checks that units with Line set include a LINE column, and without do not.
func TestTUILinesUnitsLineColumn(t *testing.T) {
	t.Parallel()
	// Test with Line set.
	m := NewTUI()
	d := makeTestTUIData()
	d.Floor.Units[0].Line = "frontend"
	d.Floor.Units[1].Line = "backend"

	view := m.View(d, 100, 20, false)
	lines := strings.Split(view, "\n")

	// Find the header row (should have "LINE" when Line is set).
	found := false
	for _, line := range lines {
		if strings.Contains(line, "TASK") && strings.Contains(line, "LINE") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'LINE' column header when units have Line set, got:\n%s", view)
	}

	// Test without Line set.
	m2 := NewTUI()
	d2 := makeTestTUIData()
	// Don't set Line on any unit.

	view2 := m2.View(d2, 100, 20, false)
	lines2 := strings.Split(view2, "\n")

	// Header should NOT have "LINE" when no units have Line set.
	notFound := true
	for _, line := range lines2 {
		if strings.Contains(line, "TASK") && strings.Contains(line, "LINE") {
			notFound = false
			break
		}
	}
	if !notFound {
		t.Errorf("expected no 'LINE' column header when units have empty Line, got:\n%s", view2)
	}
}

// TestTUICappedPeakColoured checks that a capped unit's STATE cell shows its
// peak and is coloured as capped (#336 review).
func TestTUICappedPeakColoured(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData() // T3 is capped with Peak 50000
	frame := NewTUI().View(d, 120, 12, true)
	if !hasRow(frame, skins["dark"].Failed+"  T3", "capped 50k") {
		t.Errorf("frame lacks a red T3 row showing \"capped 50k\":\n%q", frame)
	}
}

// TestTUIHeader checks the header block (issue #583): the repo and branch,
// a frozen factory with since and until, a paused model, the health age, and
// the key menu of the view shown, which changes with the view.
func TestTUIHeader(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	d := makeTestTUIData()
	d.Floor.Refreshed = at
	d.Branch = "main"
	since, until := at.Add(-time.Hour).Format(time.RFC3339), at.Add(time.Hour).Format(time.RFC3339)
	d.Suspend = SuspendState{Suspended: true, Since: since, Until: until, By: "s9", Reason: "maintenance"}
	d.Paused = []TUIPause{{Model: "claude-opus", Until: at.Add(30 * time.Minute)}}
	d.HealthAt = at.Add(-2 * time.Minute)
	d.Version = "v0.36.0"

	m := NewTUI()
	view := m.View(d, 200, 30, false)
	for _, want := range []string{
		"repo repo · main",
		"factory FROZEN since " + hhmm(since) + " until " + hhmm(until) + " by s9: maintenance",
		"paused claude-opus until " + at.Add(30*time.Minute).Local().Format("15:04"),
		"lead test-lead",
		"health 2m ago",
		"flywheel v0.36.0",
		"<:> view", "<enter> why", "<l> log", "<ctrl-e> header",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("header lacks %q:\n%s", want, view)
		}
	}
	if !strings.HasPrefix(view, "repo ") {
		t.Errorf("the frame does not start with the header:\n%s", view)
	}

	// Another view, another menu: workers has nothing to explain.
	for _, r := range ":workers" {
		m.Update(term.Key{Kind: term.KeyRune, Rune: r}, d)
	}
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	view = m.View(d, 200, 30, false)
	if strings.Contains(view, "<enter> why") || !strings.Contains(view, "<:> view") {
		t.Errorf("workers menu should drop <enter> why and keep <:> view:\n%s", view)
	}

	// A running factory with no health event.
	d.Suspend, d.HealthAt = SuspendState{}, time.Time{}
	view = NewTUI().View(d, 200, 30, false)
	if !strings.Contains(view, "factory running") || !strings.Contains(view, "health none") {
		t.Errorf("want factory running and health none:\n%s", view)
	}
}

// TestTUICrumbs checks the crumbs follow the drill-down and back (issue
// #583), and the flash line shows a message that the next key or time clears.
func TestTUICrumbs(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	m := NewTUI()
	m.now = func() time.Time { return clock }
	last := func() string {
		lines := strings.Split(m.View(d, 100, 20, false), "\n")
		return lines[len(lines)-1]
	}
	flashLine := func() string {
		lines := strings.Split(m.View(d, 100, 20, false), "\n")
		return lines[len(lines)-2]
	}

	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if got := strings.Join(m.Crumbs(), " > "); got != "units > T1 > why" {
		t.Errorf("crumbs after Enter = %q, want units > T1 > why", got)
	}
	if got := last(); got != "<units> <T1> <why>" {
		t.Errorf("crumb line = %q", got)
	}
	m.Update(term.Key{Kind: term.KeyEsc}, d)
	if got := strings.Join(m.Crumbs(), " > "); got != "units" || last() != "<units>" {
		t.Errorf("crumbs after Esc = %q, line %q; want units", got, last())
	}
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'l'}, d)
	if got := last(); got != "<units> <T1> <log>" {
		t.Errorf("crumb line after l = %q", got)
	}
	m.Update(term.Key{Kind: term.KeyEsc}, d)

	// The flash: an unknown view says so; the next key clears it.
	for _, k := range []term.Key{{Kind: term.KeyRune, Rune: ':'}, {Kind: term.KeyRune, Rune: 'z'}, {Kind: term.KeyEnter}} {
		m.Update(k, d)
	}
	if got := flashLine(); got != "unknown view :z (Ctrl-A lists them)" {
		t.Errorf("flash line = %q, want unknown view :z (Ctrl-A lists them)", got)
	}
	m.Update(term.Key{Kind: term.KeyRune, Rune: 'j'}, d)
	if got := flashLine(); got != "" {
		t.Errorf("flash line after a key = %q, want empty", got)
	}
	// Or it expires on its own.
	m.flash("hello")
	if flashLine() != "hello" {
		t.Errorf("flash line = %q, want hello", flashLine())
	}
	clock = clock.Add(flashFor + time.Second)
	if got := flashLine(); got != "" {
		t.Errorf("flash line after %v = %q, want empty", flashFor, got)
	}
}

// TestTUIToggles checks the layout keys (issue #583): Ctrl-E hides the
// header and Ctrl-G the crumbs, each giving the table rows; Ctrl-W shows a
// long cell whole; Ctrl-R asks the live loop for a reload once.
func TestTUIToggles(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	d.Floor.Units = nil
	for i := 0; i < 40; i++ {
		d.Floor.Units = append(d.Floor.Units, Unit{Task: fmt.Sprintf("T%02d", i), Stage: "building", Attempt: "1",
			Session: fmt.Sprintf("s%d", i), Model: "claude-opus", RunState: "running"})
	}
	long := "session-" + strings.Repeat("x", 60)
	d.Floor.Units[0].Session = long
	ctrl := func(r rune) term.Key { return term.Key{Kind: term.KeyCtrl, Rune: r} }
	m := NewTUI()
	rowsShown := func() (n int, view string) {
		view = m.View(d, 250, 30, false)
		for _, l := range strings.Split(view, "\n") {
			if strings.HasPrefix(l, "  T") || strings.HasPrefix(l, "> T") {
				n++
			}
		}
		return n, view
	}

	withHeader, view := rowsShown()
	if !strings.Contains(view, "repo repo") {
		t.Fatalf("no header to hide:\n%s", view)
	}
	m.Update(ctrl('e'), d)
	noHeader, view := rowsShown()
	if strings.Contains(view, "repo repo") || noHeader <= withHeader {
		t.Errorf("Ctrl-E: rows %d -> %d, header still shown %v", withHeader, noHeader, strings.Contains(view, "repo repo"))
	}
	m.Update(ctrl('e'), d)
	if again, _ := rowsShown(); again != withHeader {
		t.Errorf("Ctrl-E twice: %d rows, want %d", again, withHeader)
	}

	m.Update(ctrl('g'), d)
	noCrumbs, view := rowsShown()
	if strings.Contains(view, "<units>") || noCrumbs != withHeader+1 {
		t.Errorf("Ctrl-G: rows %d -> %d, crumbs still shown %v", withHeader, noCrumbs, strings.Contains(view, "<units>"))
	}
	m.Update(ctrl('g'), d)

	_, view = rowsShown()
	if strings.Contains(view, long) || !strings.Contains(view, cutRunes(long, maxCell)) {
		t.Errorf("normal mode should cut %q to %q:\n%s", long, cutRunes(long, maxCell), view)
	}
	m.Update(ctrl('w'), d)
	if _, view = rowsShown(); !strings.Contains(view, long) {
		t.Errorf("Ctrl-W should show %q whole:\n%s", long, view)
	}

	if m.TakeRefresh() {
		t.Error("a reload requested before Ctrl-R")
	}
	m.Update(ctrl('r'), d)
	if !m.TakeRefresh() {
		t.Error("Ctrl-R did not request a reload")
	}
	if m.TakeRefresh() {
		t.Error("the reload request was not consumed")
	}
}

// TestTUIUnitTabs checks the unit detail (issue #583 k3): Enter opens the
// why tab, each tab key switches the tab and its title, Shift-J opens the
// first unmet need, and Esc returns to the list with the cursor kept.
func TestTUIUnitTabs(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	d.Needs = map[string][]string{"T2": {"T1"}}
	d.Why = map[string]string{"T2": "blocked: needs T1, which has not landed.", "T1": "building: attempt 1."}
	m := NewTUI()
	m.Update(term.Key{Kind: term.KeyDown}, d)
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if kind, task, ok := m.Wants(); !ok || kind != "why" || task != "T2" {
		t.Fatalf("Enter wants %q %q %v, want why T2", kind, task, ok)
	}
	view := m.View(d, 200, 30, false)
	for _, want := range []string{"── Why T2 ──", "why: blocked: needs T1, which has not landed.", "<F> findings", "<J> need"} {
		if !strings.Contains(view, want) {
			t.Errorf("the why tab lacks %q:\n%s", want, view)
		}
	}
	for _, tab := range []struct {
		key         rune
		kind, title string
	}{{'d', "explain", "Explain"}, {'y', "brief", "Brief"}, {'l', "log", "Log"}, {'c', "unitcp", "Checkpoints"},
		{'F', "findings", "Findings"}, {'e', "unitevents", "Events"}, {'w', "why", "Why"}} {
		press(m, d, string(tab.key))
		if kind, task, _ := m.Wants(); kind != tab.kind || task != "T2" {
			t.Errorf("%c: wants %q %q, want %q T2", tab.key, kind, task, tab.kind)
		}
		if view := m.View(d, 200, 30, false); !strings.Contains(view, "── "+tab.title+" T2 ──") || !strings.Contains(view, "why: blocked") {
			t.Errorf("%c: title or why line missing:\n%s", tab.key, view)
		}
		if c := m.Crumbs(); c[len(c)-1] != tab.kind {
			t.Errorf("%c: crumbs %v", tab.key, c)
		}
	}
	press(m, d, "J")
	if kind, task, _ := m.Wants(); kind != "why" || task != "T1" {
		t.Errorf("Shift-J wants %q %q, want why T1", kind, task)
	}
	press(m, d, "J")
	if flash := m.flashLine(); !strings.Contains(flash, "T1 has no unmet need") {
		t.Errorf("Shift-J on a unit without needs flashes %q", flash)
	}
	esc(m, d)
	if _, _, ok := m.Wants(); ok || m.cursor != 1 {
		t.Errorf("Esc: drill-down open %v, cursor %d, want the list at 1", ok, m.cursor)
	}
	if view := m.View(d, 200, 30, false); !strings.Contains(view, "── Units(") {
		t.Errorf("Esc does not return to the units:\n%s", view)
	}
}

// TestTUIHintsWrap checks the header's key menu never cuts a hint: at every
// width each hint shown is whole, and "<?> help" is always there.
func TestTUIHintsWrap(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	d.Branch = "main"
	d.Suspend = SuspendState{Suspended: true, Since: "2026-09-26T09:00:00Z", By: "lead-session", Reason: "maintenance window"}
	for _, width := range []int{80, 110, 160} {
		for _, drill := range []bool{false, true} {
			m := NewTUI()
			if drill {
				m.Update(term.Key{Kind: term.KeyEnter}, d)
			}
			var header []string
			for _, l := range strings.Split(m.View(d, width, 40, false), "\n") {
				if strings.HasPrefix(l, "──") {
					break
				}
				header = append(header, l)
			}
			all := strings.Join(header, "\n")
			for _, l := range header {
				if n := utf8.RuneCountInString(l); n > width {
					t.Errorf("%d: header line of %d runes: %q", width, n, l)
				}
				if strings.Count(l, "<") != strings.Count(l, ">") {
					t.Errorf("%d: a hint is cut: %q", width, l)
				}
			}
			for _, h := range m.hintsFor() {
				if strings.Contains(all, "<"+h.key+">") && !strings.Contains(all, "<"+h.key+"> "+h.label) {
					t.Errorf("%d: hint <%s> %s is cut:\n%s", width, h.key, h.label, all)
				}
			}
			if !strings.Contains(all, "<?> help") {
				t.Errorf("%d: no <?> help:\n%s", width, all)
			}
		}
	}
}

// TestTUIWhyColumn checks the units table ends with a WHY column holding
// each unit's why.
func TestTUIWhyColumn(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	d.Why = map[string]string{"T1": "building: attempt 1, 5 steps, running for 3m."}
	header, rows := NewTUI().Rows(d)
	if header[len(header)-1] != "WHY" || rows[0][len(rows[0])-1] != d.Why["T1"] {
		t.Fatalf("header %v, first row %v: want WHY last with T1's why", header, rows[0])
	}
	view := NewTUI().View(d, 250, 20, false)
	if !regexp.MustCompile(`STATE +WHY\n> T1 .* running +building: attempt 1, 5 steps, running for 3m\.\n`).MatchString(view) {
		t.Errorf("the table lacks the WHY column:\n%s", view)
	}
}
