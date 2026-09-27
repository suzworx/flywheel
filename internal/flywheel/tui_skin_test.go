package flywheel

import (
	"strings"
	"testing"
)

// TestTUISkinConfig checks factory.skin: dark, light and none validate, ""
// means dark, and an unknown skin is refused naming the known ones.
func TestTUISkinConfig(t *testing.T) {
	t.Parallel()
	if got := DefaultConfig().FactorySkin(); got != "dark" {
		t.Errorf("FactorySkin() unset = %q, want dark", got)
	}
	for _, name := range []string{"", "dark", "light", "none"} {
		c := DefaultConfig()
		c.Factory = &FactoryConfig{Skin: name}
		if err := c.Validate(); err != nil {
			t.Errorf("factory.skin %q: Validate() = %v, want nil", name, err)
		}
		if s, err := SkinFor(name); err != nil || s.Name != c.FactorySkin() {
			t.Errorf("SkinFor(%q) = %q, %v; want the skin %q", name, s.Name, err, c.FactorySkin())
		}
	}
	c := DefaultConfig()
	c.Factory = &FactoryConfig{Skin: "neon"}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `factory.skin "neon" must be "dark", "light", "none"`) {
		t.Errorf("factory.skin neon: Validate() = %v, want it refused", err)
	}
	if _, err := SkinFor("neon"); err == nil {
		t.Error("SkinFor(neon) = nil error, want it refused")
	}

	// flywheel config get/set: set, read back, clear back to dark.
	c = DefaultConfig()
	if err := c.Set("factory.skin", " light "); err != nil {
		t.Fatalf("Set(factory.skin, light) = %v", err)
	}
	if v, err := c.Get("factory.skin"); err != nil || v != "light" || c.Validate() != nil {
		t.Errorf("Get(factory.skin) after set = %q, %v; Validate() = %v", v, err, c.Validate())
	}
	if err := c.Set("factory.skin", ""); err != nil || c.Factory != nil || c.FactorySkin() != "dark" {
		t.Errorf("Set(factory.skin, \"\") = %v, Factory %v; want it cleared to dark", err, c.Factory)
	}
	if err := c.Set("factory.skin", "neon"); err != nil || c.Validate() == nil {
		t.Errorf("Set(factory.skin, neon) = %v, then Validate() = nil; want Validate to refuse it", err)
	}
}

// colorData is a floor with a unit in every state the skin colours, the
// cursor's row (A0) first.
func colorData() (TUIData, map[string]func(Skin) string) {
	d := makeTestTUIData()
	d.Floor.Units = nil
	want := map[string]func(Skin) string{}
	for _, u := range []struct {
		task, stage, state string
		code               func(Skin) string
	}{
		{"A0", "planned", "", nil},
		{"R1", "building", "running", func(s Skin) string { return s.Running }},
		{"P1", "passed", "", func(s Skin) string { return s.Passed }},
		{"W1", "planned", "waiting", func(s Skin) string { return s.Waiting }},
		{"W2", "finished", "needs-correction", func(s Skin) string { return s.Waiting }},
		{"W3", "finished", "rate-limited", func(s Skin) string { return s.Waiting }},
		{"F1", "finished", "failed", func(s Skin) string { return s.Failed }},
		{"F2", "building", "stalled", func(s Skin) string { return s.Failed }},
		{"F3", "building", "silent", func(s Skin) string { return s.Failed }},
		{"F4", "finished", "capped", func(s Skin) string { return s.Failed }},
		{"S1", "finished", "suspended", func(s Skin) string { return s.Frozen }},
		{"L1", "landed", "done", func(s Skin) string { return s.Landed }},
	} {
		d.Floor.Units = append(d.Floor.Units, Unit{Task: u.task, Stage: u.stage, RunState: u.state, Attempt: "1"})
		if u.code != nil {
			want[u.task] = u.code
		}
	}
	return d, want
}

// TestTUIColorsByState checks that each units row is drawn whole in its
// skin's colour for its state, in the dark and light skins; that the
// header's factory state takes the same colour; and that "none" draws no
// ANSI code at all.
func TestTUIColorsByState(t *testing.T) {
	t.Parallel()
	d, want := colorData()
	for _, name := range []string{"dark", "light"} {
		m := NewTUI()
		m.skin = skins[name]
		frame := m.View(d, 160, 40, true)
		for task, code := range want {
			if !hasRow(frame, code(m.skin)+"  "+task+" ") {
				t.Errorf("%s skin: the %s row is not drawn in %q:\n%q", name, task, code(m.skin), frame)
			}
		}
		if !strings.Contains(frame, m.skin.Running+"factory running") {
			t.Errorf("%s skin: the header's factory state is not coloured running:\n%q", name, frame)
		}
	}
	m := NewTUI()
	m.skin = skins["none"]
	if frame := m.View(d, 160, 40, true); strings.Contains(frame, "\x1b") {
		t.Errorf("none skin drew ANSI codes:\n%q", frame)
	}
}

// TestTUIChangeFlashMarksRows checks that a row whose cells changed between
// two fetches is highlighted for two refreshes, a new row is marked new, a
// row whose age alone moved is not marked, and the first fetch marks none.
func TestTUIChangeFlashMarksRows(t *testing.T) {
	t.Parallel()
	m := NewTUI()
	m.Observe(makeTestTUIData())
	if len(m.marks["units"]) != 0 {
		t.Fatalf("the first fetch marked %v, want none", m.marks["units"])
	}
	changed := makeTestTUIData()
	changed.Floor.Units[1].Steps++      // T2 modified
	changed.Floor.Units[2].LastAge += 9 // T3's age alone: not a change
	changed.Floor.Units = append(changed.Floor.Units, Unit{Task: "T4", Stage: "planned", RunState: "waiting"})
	flashed := func() bool {
		return hasRow(m.View(changed, 160, 20, true), m.skin.Flash+m.skin.Passed+"  T2 ")
	}
	for refresh := 1; refresh <= 3; refresh++ {
		m.Observe(changed)
		want := refresh <= 2
		if got := m.marks["units"]["T2"].kind == "modified"; got != want {
			t.Errorf("refresh %d: T2 marked modified = %v, want %v (%v)", refresh, got, want, m.marks["units"])
		}
		if got := m.marks["units"]["T4"].kind == "new"; got != want {
			t.Errorf("refresh %d: T4 marked new = %v, want %v", refresh, got, want)
		}
		if got := flashed(); got != want {
			t.Errorf("refresh %d: T2 row highlighted = %v, want %v", refresh, got, want)
		}
		if _, ok := m.marks["units"]["T3"]; ok {
			t.Errorf("refresh %d: T3 marked though only its age moved", refresh)
		}
	}
}
