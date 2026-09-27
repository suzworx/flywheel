package flywheel

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

var ctxNow = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// ctxFleet registers two roots, alpha and beta, each a ledger planning one
// unit (A1, B1), in a fleet file of their own; it returns the file and dirs.
func ctxFleet(t *testing.T) (file, alpha, beta string) {
	t.Helper()
	var f Fleet
	for _, r := range []struct{ name, task string }{{"alpha", "A1"}, {"beta", "B1"}} {
		dir := t.TempDir()
		if _, err := Init(dir, false); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if err := AppendEvent(dir, Event{TS: ctxNow.Format(time.RFC3339), Task: r.task, Kind: "planned"}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		f.Roots = append(f.Roots, FleetRoot{Name: r.name, Path: dir})
	}
	file = filepath.Join(t.TempDir(), "fleet.json")
	if err := SaveFleet(file, f); err != nil {
		t.Fatalf("SaveFleet: %v", err)
	}
	return file, f.Roots[0].Path, f.Roots[1].Path
}

func ctxClock() time.Time { return ctxNow }

func TestTUICtxViewListsSortsFiltersTheFleet(t *testing.T) {
	t.Parallel()
	file, alpha, _ := ctxFleet(t)
	m := NewTUI()
	m.runCommand("fleet")
	d, err := TUIFetcherIn(alpha, ctxClock, TUIFetchOptions{FleetFile: file})(m)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	header, rows := m.Rows(d)
	if strings.Join(header, " ") != "NAME KIND STATE RUNNING ANDON PAUSED HEALTH LAST PATH" {
		t.Errorf("header = %v", header)
	}
	if len(rows) != 2 || rows[0][0] != "alpha(*)" || rows[1][0] != "beta" || rows[0][1] != FleetKindRoot {
		t.Fatalf("rows = %v, want alpha(*) then beta", rows)
	}
	if d.CtxName != "alpha" {
		t.Errorf("CtxName = %q, want alpha from the registry", d.CtxName)
	}
	for range 2 {
		m.Update(term.Key{Kind: term.KeyRune, Rune: 'N'}, d)
	}
	if _, rows = m.Rows(d); rows[0][0] != "beta" {
		t.Errorf("sorted by name descending: first row %v, want beta", rows[0])
	}
	m.filter = "bet"
	if _, rows = m.Rows(d); len(rows) != 1 || rows[0][0] != "beta" {
		t.Errorf("filtered /bet = %v, want only beta", rows)
	}

	// An empty registry is one line saying how to register a root.
	empty := NewTUI()
	empty.runCommand("ctx")
	d, err = TUIFetcherIn(alpha, ctxClock, TUIFetchOptions{FleetFile: filepath.Join(t.TempDir(), "none.json")})(empty)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, rows = empty.Rows(d); len(rows) != 1 || !strings.Contains(rows[0][0], "flywheel fleet add <path>") {
		t.Errorf("empty fleet rows = %v, want the fleet add hint", rows)
	}
}

// ctxSwitched opens :ctx in alpha, presses Enter on beta and switches.
func ctxSwitched(t *testing.T) (*TUI, func(*TUI) (TUIData, error), TUIData) {
	t.Helper()
	file, alpha, _ := ctxFleet(t)
	m := NewTUI()
	m.runCommand("ctx")
	d, err := TUIFetcherIn(alpha, ctxClock, TUIFetchOptions{FleetFile: file})(m)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	m.Update(term.Key{Kind: term.KeyDown}, d)
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	c, ok := m.TakeCtx()
	if !ok || c.Name != "beta" || c.Kind != FleetKindRoot {
		t.Fatalf("TakeCtx() = %+v, %v; want beta", c, ok)
	}
	fetch, d, err := m.switchCtx(TUIIO{Ctx: TUIReroot(ctxClock, TUIFetchOptions{FleetFile: file})}, c)
	if err != nil {
		t.Fatalf("switchCtx: %v", err)
	}
	return m, fetch, d
}

func TestTUICtxSwitchReRootsAndKeepsOnFailure(t *testing.T) {
	t.Parallel()
	m, fetch, d := ctxSwitched(t)
	if m.ViewName() != "units" || strings.Join(m.Crumbs(), " ") != "units" {
		t.Errorf("after the switch: view %q crumbs %v, want units", m.ViewName(), m.Crumbs())
	}
	d, err := fetch(m)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !unitExists(d, "B1") || unitExists(d, "A1") {
		t.Errorf("units after the switch = %+v, want beta's B1 only", d.Floor.Units)
	}
	m.runCommand("ctx")
	if d, err = fetch(m); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, rows := m.Rows(d); len(rows) != 2 || rows[0][0] != "alpha" || rows[1][0] != "beta(*)" {
		t.Errorf(":ctx after the switch = %v, want beta starred", rows)
	}

	// A context whose ledger is gone keeps the old one and flashes why.
	gone := filepath.Join(t.TempDir(), "gone")
	data := TUIData{Fleet: []FleetRow{{FleetLedger: FleetLedger{Name: "gone", Path: gone, Kind: FleetKindRoot}}}}
	keys := make(chan term.Key, 8)
	for _, r := range ":ctx" {
		keys <- term.Key{Kind: term.KeyRune, Rune: r}
	}
	keys <- term.Key{Kind: term.KeyEnter}
	keys <- term.Key{Kind: term.KeyEnter}
	keys <- term.Key{Kind: term.KeyRune, Rune: 'q'}
	out := &bytes.Buffer{}
	tio := TUIIO{Keys: keys, Ticks: make(chan time.Time), Size: func() (int, int) { return 160, 30 }, Out: out,
		Ctx: TUIReroot(ctxClock, TUIFetchOptions{})}
	if err := RunTUILoop(tio, func(*TUI) (TUIData, error) { return data, nil }); err != nil {
		t.Fatalf("RunTUILoop: %v", err)
	}
	frames := strings.Split(out.String(), "\x1b[H")
	last := frames[len(frames)-1]
	if !strings.Contains(last, "ctx gone: no flywheel ledger at") || !strings.Contains(last, "Contexts(") {
		t.Errorf("last frame = %q, want the ctx view kept and the error flashed", last)
	}
}

func TestTUICtxHeaderNamesTheContext(t *testing.T) {
	t.Parallel()
	m, _, d := ctxSwitched(t)
	if view := m.View(d, 160, 30, false); !strings.Contains(view, "ctx beta · repo ") {
		t.Errorf("header after the switch = %q, want ctx beta", view)
	}
	lines := contextLines(TUIData{CtxName: "alpha/wt", CtxKind: FleetKindGitWorktree})
	if !strings.HasPrefix(lines[0], "ctx alpha/wt (git-worktree) · repo ") {
		t.Errorf("worktree context line = %q, want its kind named", lines[0])
	}
}
