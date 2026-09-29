package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/term"
)

// actionData is a floor of three units, T1 T2 T3 in table order.
func actionData() TUIData {
	return TUIData{Floor: Floor{Units: []Unit{{Task: "T1"}, {Task: "T2"}, {Task: "T3"}}}}
}

// actionTUI is a model whose actions run on dir as session.
func actionTUI(dir, session string, readonly bool) *TUI {
	m := NewTUI()
	m.SetActions(dir, session, readonly)
	return m
}

// TestTUIActionConfirm checks the y/N (issue #583 k5): v then n cancels and
// leaves nothing pending; v then y makes one pending validate of T1.
func TestTUIActionConfirm(t *testing.T) {
	t.Parallel()
	d, dir := actionData(), filepath.Join("some", "dir")
	m := actionTUI(dir, "", false)
	press(m, d, "v")
	if ask, help, ok := m.promptLines(); !ok || ask != "validate T1? y/N" || help != "flywheel validate T1 --dir "+dir {
		t.Errorf("after v the prompt is %q / %q (%v), want validate T1? y/N over its command", ask, help, ok)
	}
	press(m, d, "n")
	if _, ok := m.TakeAction(); ok {
		t.Error("v n left an action pending")
	}
	if got := m.flashLine(); got != "cancelled" {
		t.Errorf("v n flashes %q, want cancelled", got)
	}
	press(m, d, "vy")
	a, ok := m.TakeAction()
	if !ok || len(a.Argv) != 1 || !slices.Equal(a.Argv[0], []string{"validate", "T1", "--dir", dir}) {
		t.Fatalf("v y pending = %+v (%v), want one validate T1 --dir %s", a, ok, dir)
	}
	if _, ok := m.TakeAction(); ok {
		t.Error("TakeAction did not clear the pending action")
	}
}

// TestTUIActionCommands checks each action key's exact command, and that i
// without a session says so and leaves nothing pending.
func TestTUIActionCommands(t *testing.T) {
	t.Parallel()
	const dir = "D"
	for _, c := range []struct {
		keys string
		want []string
	}{
		{"v", []string{"validate", "T1", "--dir", dir}},
		{"ip", []string{"inspect", "T1", "--verdict", "pass", "--session", "lead-1", "--dir", dir}},
		{"ie", []string{"inspect", "T1", "--verdict", "escalate", "--session", "lead-1", "--dir", dir}},
		{"r", []string{"run", "T1", "--resume", "--dir", dir}},
		{"x", []string{"log", "--task", "T1", "--kind", "withdrawn", "--note", "withdrawn from flywheel factory", "--dir", dir}},
		{"Z", []string{"suspend", "--dir", dir, "--session", "lead-1"}},
		{"R", []string{"resume", "--dir", dir, "--session", "lead-1"}},
	} {
		d := actionData()
		m := actionTUI(dir, "lead-1", false)
		press(m, d, c.keys+"y")
		a, ok := m.TakeAction()
		if !ok || len(a.Argv) != 1 || !slices.Equal(a.Argv[0], c.want) {
			t.Errorf("%s y: pending %+v (%v), want argv %q", c.keys, a, ok, c.want)
		}
	}
	// Without a session, the factory's resume runs without one and inspect
	// refuses.
	d := actionData()
	m := actionTUI(dir, "", false)
	press(m, d, "Ry")
	if a, _ := m.TakeAction(); len(a.Argv) != 1 || !slices.Equal(a.Argv[0], []string{"resume", "--dir", dir}) {
		t.Errorf("R y with no session: %+v, want resume --dir %s", a, dir)
	}
	press(m, d, "i")
	if got := m.flashLine(); got != "set FLYWHEEL_SESSION to your own session to inspect" {
		t.Errorf("i with no session flashes %q", got)
	}
	press(m, d, "py")
	if a, ok := m.TakeAction(); ok {
		t.Errorf("i with no session left %+v pending", a)
	}
	// In a unit's detail the action targets that unit.
	m = actionTUI(dir, "", false)
	press(m, d, "j")
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	press(m, d, "xy")
	if a, _ := m.TakeAction(); !slices.Equal(a.Targets, []string{"T2"}) {
		t.Errorf("x y in T2's detail targets %q, want T2", a.Targets)
	}
}

// TestTUIMarks checks the marks: space on T3 then T1, v y makes one action
// on both in table order, the rows show ●, and the marks clear.
func TestTUIMarks(t *testing.T) {
	t.Parallel()
	d := actionData()
	m := actionTUI("D", "", false)
	press(m, d, "jj ")
	m.cursor = 0
	press(m, d, " ")
	if v := m.View(d, 80, 20, false); strings.Count(v, "●") != 2 {
		t.Errorf("two marked rows draw %d ●:\n%s", strings.Count(v, "●"), v)
	}
	press(m, d, "vy")
	a, ok := m.TakeAction()
	if !ok || !slices.Equal(a.Targets, []string{"T1", "T3"}) || len(a.Argv) != 2 || a.Argv[1][1] != "T3" {
		t.Errorf("v y on marks T1 T3: %+v (%v), want one action on T1 then T3", a, ok)
	}
	if len(m.picked) != 0 {
		t.Errorf("marks after the action ran: %v, want none", m.picked)
	}
	press(m, d, " ")
	esc(m, d)
	if len(m.picked) != 0 || m.ViewName() != "units" {
		t.Errorf("esc with marks: marks %v, view %s; want them cleared, still units", m.picked, m.ViewName())
	}
}

// TestTUIReadonly checks --readonly: every action key flashes read-only and
// nothing is pending; space still marks; the header says read-only.
func TestTUIReadonly(t *testing.T) {
	t.Parallel()
	d := actionData()
	m := actionTUI("D", "lead-1", true)
	for _, k := range "virxZR" {
		press(m, d, string(k))
		if got := m.flashLine(); got != "read-only: started with --readonly" {
			t.Errorf("%c read-only flashes %q", k, got)
		}
		press(m, d, "y")
		if a, ok := m.TakeAction(); ok {
			t.Errorf("%c y read-only left %+v pending", k, a)
		}
	}
	press(m, d, " ")
	if !m.picked["T1"] {
		t.Error("space read-only did not mark T1")
	}
	if v := m.View(d, 120, 30, false); !strings.Contains(v, "read-only") {
		t.Errorf("the read-only header does not say read-only:\n%s", v)
	}
}

// TestTUIHotkeys checks .flywheel/hotkeys.json: a valid entry loads and
// opens its view; a bound key, an unknown view and invalid JSON are each
// skipped with a message; no file is no hotkeys and no message.
func TestTUIHotkeys(t *testing.T) {
	t.Parallel()
	write := func(body string) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".flywheel", "hotkeys.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	keys, msg := LoadHotkeys(write(`{"hotkeys": {"K": ":pulse", "<ctrl-y>": ":andon", "j": ":events", "X": ":nope"}}`))
	if len(keys) != 2 || !strings.Contains(msg, "j is already bound") || !strings.Contains(msg, "X: :nope is no view") {
		t.Errorf("LoadHotkeys = %v, %q; want K and ctrl-y, skipping j and X", keys, msg)
	}
	d := actionData()
	m := NewTUI()
	m.SetHotkeys(keys, msg)
	if m.flashLine() != msg {
		t.Errorf("SetHotkeys flashes %q, want %q", m.flashLine(), msg)
	}
	press(m, d, "K")
	if m.ViewName() != "pulse" {
		t.Errorf("K opens %s, want pulse", m.ViewName())
	}
	m.Update(term.Key{Kind: term.KeyCtrl, Rune: 'y'}, d)
	if m.ViewName() != "andon" {
		t.Errorf("ctrl-y opens %s, want andon", m.ViewName())
	}
	if keys, msg := LoadHotkeys(write(`{"hotkeys": `)); keys != nil || !strings.HasPrefix(msg, "hotkeys.json skipped: ") {
		t.Errorf("invalid JSON: %v, %q; want nothing loaded and a message", keys, msg)
	}
	if keys, msg := LoadHotkeys(t.TempDir()); keys != nil || msg != "" {
		t.Errorf("no file: %v, %q; want nothing", keys, msg)
	}
}

// TestTUIActionRuns checks the live loop runs a confirmed action through
// TUIIO.Run, flashes its exit and last line, and keeps its output for
// :result.
func TestTUIActionRuns(t *testing.T) {
	t.Parallel()
	keys := make(chan term.Key, 8)
	for _, r := range "vy" {
		keys <- term.Key{Kind: term.KeyRune, Rune: r}
	}
	var ran [][]string
	run := func(argv []string) (string, int) {
		ran = append(ran, argv)
		return "checking\nboom\n", 5
	}
	// The fetch the action's end starts types :result, then ends the input;
	// the loop runs one fetch at a time, so sent needs no lock.
	sent := false
	fetch := func(m *TUI) (TUIData, error) {
		if m.result != nil && !sent {
			sent = true
			for _, r := range ":result" {
				keys <- term.Key{Kind: term.KeyRune, Rune: r}
			}
			keys <- term.Key{Kind: term.KeyEnter}
			close(keys)
		}
		return actionData(), nil
	}
	out := &strings.Builder{}
	tio := TUIIO{Keys: keys, Size: func() (int, int) { return 100, 20 }, Out: out, Dir: "D", Run: run}
	if err := RunTUILoop(tio, fetch); err != nil {
		t.Fatalf("RunTUILoop: %v", err)
	}
	if len(ran) != 1 || !slices.Equal(ran[0], []string{"validate", "T1", "--dir", "D"}) {
		t.Errorf("ran %q, want validate T1 --dir D", ran)
	}
	for _, want := range []string{"validate T1: exit 5 — boom", "── Result", "checking"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the frames never show %q", want)
		}
	}
}
