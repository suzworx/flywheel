package flywheel

import (
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/term"
)

// press feeds each rune of s to m as a key; typed sends a `:` or `/` prompt
// with its text and Enter.
func press(m *TUI, d TUIData, s string) {
	for _, r := range s {
		m.Update(term.Key{Kind: term.KeyRune, Rune: r}, d)
	}
}

func typed(m *TUI, d TUIData, s string) {
	press(m, d, s)
	m.Update(term.Key{Kind: term.KeyEnter}, d)
}

func esc(m *TUI, d TUIData) { m.Update(term.Key{Kind: term.KeyEsc}, d) }

// firstCells is the first cell of every row the view shows.
func firstCells(m *TUI, d TUIData) string {
	_, rows := m.Rows(d)
	var out []string
	for _, r := range rows {
		out = append(out, r[0])
	}
	return strings.Join(out, " ")
}

// TestTUINav checks the navigation stack (issue #583 k2): Esc goes back a
// level with its cursor, `-` swaps to the previous view, `[` `]` walk the
// commands entered, and Esc clears a filter before leaving the view.
func TestTUINav(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	m := NewTUI()
	press(m, d, "j")
	typed(m, d, ":w")
	press(m, d, "j")
	typed(m, d, ":a")
	if got := strings.Join(m.Crumbs(), " "); got != "units workers andon" {
		t.Errorf("crumbs = %q, want units workers andon", got)
	}
	esc(m, d)
	if m.view != "workers" || m.cursor != 1 || strings.Join(m.Crumbs(), " ") != "units workers" {
		t.Errorf("after Esc: view %q cursor %d crumbs %v; want workers, 1, [units workers]", m.view, m.cursor, m.Crumbs())
	}
	press(m, d, "-")
	if m.view != "units" || m.cursor != 1 {
		t.Errorf("after -: view %q cursor %d; want units with its cursor 1", m.view, m.cursor)
	}
	press(m, d, "-")
	if m.view != "workers" {
		t.Errorf("after - again: view %q, want workers", m.view)
	}
	press(m, d, "-[")
	if m.view != "workers" {
		t.Errorf("after [: view %q, want workers (the first command)", m.view)
	}
	press(m, d, "]")
	if m.view != "andon" {
		t.Errorf("after ]: view %q, want andon (the second command)", m.view)
	}
	typed(m, d, "/T3")
	esc(m, d)
	if m.view != "andon" || m.filter != "" {
		t.Errorf("first Esc: view %q filter %q; want andon, no filter", m.view, m.filter)
	}
	esc(m, d)
	if m.view == "andon" {
		t.Errorf("second Esc stayed on andon, want the previous level")
	}
}

// TestTUIFilterModes checks the `/` modes: a case-insensitive regex, an
// invalid regex matching literally with a flash, `!` inverse, `-f` fuzzy.
func TestTUIFilterModes(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	for _, c := range []struct{ filter, want, flash string }{
		{"/t[13]", "T1 T3", ""},
		{"/(capped", "", "literal match"},
		{"/!T1", "T2 T3", ""},
		{"/-f t3cp", "T3", ""},
	} {
		m := NewTUI()
		typed(m, d, c.filter)
		if got := firstCells(m, d); got != c.want {
			t.Errorf("%s: rows %q, want %q", c.filter, got, c.want)
		}
		if got := m.flashLine(); !strings.Contains(got, c.flash) || (c.flash == "" && got != "") {
			t.Errorf("%s: flash %q, want %q", c.filter, got, c.flash)
		}
		if view := m.View(d, 100, 20, false); !strings.Contains(view, "Units("+c.filter+")") {
			t.Errorf("%s: title does not show the filter", c.filter)
		}
	}
}

// TestTUISort checks Shift-S sorts by stage and flips on the second press,
// stably, with the arrow in the title; Shift-A sorts ages as durations.
func TestTUISort(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	m := NewTUI()
	press(m, d, "S")
	if got := firstCells(m, d); got != "T1 T3 T2" {
		t.Errorf("Shift-S: %q, want T1 T3 T2 (building before finished, T1 before T3)", got)
	}
	if view := m.View(d, 100, 20, false); !strings.Contains(view, "[3] ↑stage") {
		t.Errorf("title does not show ↑stage:\n%s", view)
	}
	press(m, d, "S")
	if got := firstCells(m, d); got != "T2 T1 T3" {
		t.Errorf("Shift-S again: %q, want T2 T1 T3 (equal keys keep their order)", got)
	}
	if view := m.View(d, 100, 20, false); !strings.Contains(view, "↓stage") {
		t.Errorf("title does not show ↓stage")
	}
	press(m, d, "A")
	if got := firstCells(m, d); got != "T1 T3 T2" {
		t.Errorf("Shift-A: %q, want T1 T3 T2 (30s, 1m, 2m)", got)
	}
	press(m, d, "C")
	if !strings.Contains(m.flashLine(), "no cost column") {
		t.Errorf("Shift-C on units: flash %q, want no cost column", m.flashLine())
	}
}

// TestTUIViews checks the tree, health, learnings and checkpoints views
// from fixture data, every alias, Ctrl-A's list and an unknown view.
func TestTUIViews(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	d.Needs = map[string][]string{"T2": {"T1"}, "T3": {"T2"}}
	d.Health = []TUIHealth{{TS: "2026-09-26T11:00:00Z", Snap: HealthSnapshot{Running: 2, Stalled: 1, Andon: 1,
		PausedModels: []PausedModel{{Model: "claude-opus"}}}}}
	d.Learnings = []LearningView{{ID: "L-01", Title: "old", Task: "T1"},
		{ID: "L-02", Title: "gates flaky", Task: "T2", Severity: "major", Observed: "the gate timed out", Dismissed: true}}
	d.Checkpoints = []TUICheckpoint{{Checkpoint: Checkpoint{Task: "T3", Attempt: "2", SHA: "abcdef1234", Paths: []string{"a.go", "b.go"}}, Age: 90}}
	m := NewTUI()

	typed(m, d, ":t")
	view := m.View(d, 100, 30, false)
	for _, want := range []string{"── Tree(all)[3]", "> T3", "  └─ T2", "     └─ T1"} {
		if !strings.Contains(view, want) {
			t.Errorf(":t lacks %q:\n%s", want, view)
		}
	}
	press(m, d, "jj")
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if kind, task, ok := m.Wants(); !ok || kind != "why" || task != "T1" {
		t.Errorf("Enter on the tree's T1: wants %q %q %v", kind, task, ok)
	}
	esc(m, d)

	for _, c := range []struct{ cmd, want string }{
		{":h", "running 2 stalled 1 rate 0 andon 1 paused claude-opus"},
		{":lr", "L-02 gates flaky major T2 dismissed"},
		{":c", "T3 2 abcdef1 2 1m"},
	} {
		typed(m, d, c.cmd)
		_, rows := m.Rows(d)
		row := strings.Join(rows[0], " ")
		if c.cmd == ":h" {
			r := rows[0]
			row = "running " + r[2] + " stalled " + r[3] + " rate " + r[4] + " andon " + r[5] + " paused " + r[6]
		}
		if row != c.want {
			t.Errorf("%s first row %q, want %q", c.cmd, row, c.want)
		}
	}
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if view := m.View(d, 100, 30, false); !strings.Contains(view, "Checkpoint T3/2") || !strings.Contains(view, "  b.go") {
		t.Errorf("Enter on a checkpoint does not show its paths:\n%s", view)
	}
	esc(m, d)
	typed(m, d, ":lr")
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if view := m.View(d, 100, 30, false); !strings.Contains(view, "observed:") || !strings.Contains(view, "the gate timed out") {
		t.Errorf("Enter on a learning does not show it in full:\n%s", view)
	}
	esc(m, d)

	for alias, want := range map[string]string{"u": "units", "w": "workers", "a": "andon", "e": "events",
		"l": "lines", "t": "tree", "h": "health", "lr": "learnings", "c": "checkpoints"} {
		typed(m, d, ":"+alias)
		if m.view != want {
			t.Errorf(":%s opened %q, want %q", alias, m.view, want)
		}
	}
	m.Update(term.Key{Kind: term.KeyCtrl, Rune: 'a'}, d)
	view = m.View(d, 100, 30, false)
	for _, want := range []string{"── Views ──", "learnings    :lr", "checkpoints  :c", ":q quits"} {
		if !strings.Contains(view, want) {
			t.Errorf("Ctrl-A lacks %q:\n%s", want, view)
		}
	}
	esc(m, d)
	typed(m, d, ":zz")
	if got := m.flashLine(); got != "unknown view :zz (Ctrl-A lists them)" {
		t.Errorf("unknown view flash %q", got)
	}
}

// TestTUISearch checks the pure search: a text in an event, a log line and
// a brief, with source, task, line and excerpt; the cap at 500; and Enter on
// a result opening the unit's log at the match.
func TestTUISearch(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 60) + " Disk FULL on the runner " + strings.Repeat("b", 60)
	docs := []SearchDoc{
		{Source: "event", Task: "T1", Attempt: "1", First: 7, Lines: []string{"T1 failed: disk full"}},
		{Source: "event", Task: "T2", First: 8, Lines: []string{"T2 landed"}},
		{Source: "log", Task: "T2", Attempt: "3", First: 1, Lines: []string{"ok", long}},
		{Source: "brief", Task: "T3", First: 1, Lines: []string{"# T3", "", "never fill the disk full"}},
	}
	hits, capped := SearchDocs(docs, "DISK full", searchLimit)
	var got []string
	for _, h := range hits {
		got = append(got, strings.Join([]string{h.Source, h.Task, h.Attempt, strings.Repeat("#", h.Line)}, "|"))
	}
	if want := "event|T1|1|####### log|T2|3|## brief|T3||###"; strings.Join(got, " ") != want || capped {
		t.Errorf("hits %q capped %v, want %q", strings.Join(got, " "), capped, want)
	}
	if len(hits) == 3 {
		ex := hits[1].Excerpt
		if !strings.HasPrefix(ex, "…aaaa") || !strings.HasSuffix(ex, "bbbb…") || !strings.Contains(ex, "Disk FULL on") {
			t.Errorf("excerpt %q, want the match with ~40 characters each side", ex)
		}
		if n := len([]rune(ex)); n != 40+len("Disk FULL")+40+2 {
			t.Errorf("excerpt is %d runes, want %d", n, 40+9+40+2)
		}
	}

	many := SearchDoc{Source: "log", Task: "T1", First: 1}
	for i := 0; i < 600; i++ {
		many.Lines = append(many.Lines, "disk full again")
	}
	if hits, capped := SearchDocs([]SearchDoc{many}, "disk full", searchLimit); len(hits) != 500 || !capped {
		t.Errorf("600 matches: %d hits capped %v, want 500 true", len(hits), capped)
	}

	d := makeTestTUIData()
	d.Search, d.SearchCapped = SearchDocs(append([]SearchDoc{many}, docs...), "disk full", searchLimit)
	m := NewTUI()
	typed(m, d, ":s disk full")
	if view := m.View(d, 120, 20, false); !strings.Contains(view, `Search "disk full"(all)[500+ (narrow the search)]`) {
		t.Errorf("search title:\n%s", view)
	}
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if kind, task, ok := m.Wants(); !ok || kind != "explain" || task != "T1" {
		t.Errorf("Enter on a log result: wants %q %q %v, want explain T1", kind, task, ok)
	}
	esc(m, d)
	d.Search = hits[:1] // the event result
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if kind, task, ok := m.Wants(); !ok || kind != "log" || task != "T1" {
		t.Errorf("Enter on an event result: wants %q %q %v, want log T1", kind, task, ok)
	}
	for i := 0; i < 40; i++ {
		d.Detail = append(d.Detail, "T1 step")
	}
	d.Detail[30] = "T1 failed: disk full"
	if view := m.View(d, 100, 20, false); !strings.Contains(view, "failed: disk full") {
		t.Errorf("the log did not open at the match:\n%s", view)
	}
}

// TestTUISearchNeedsText checks `:s` with no text asks for one and stays.
func TestTUISearchNeedsText(t *testing.T) {
	t.Parallel()
	d := makeTestTUIData()
	m := NewTUI()
	typed(m, d, ":s")
	if got := m.flashLine(); got != "search what? :s <text>" || m.view != "units" {
		t.Errorf("flash %q view %q; want the ask and units", got, m.view)
	}
}
