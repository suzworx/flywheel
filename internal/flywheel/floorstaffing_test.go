package flywheel

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

func TestFloorStaffingLeadLineUnchanged(t *testing.T) {
	t.Parallel()
	// No staffing config: Lead string matches today's behavior
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "lead-1", Persona: "lead", Model: "m1"},
	}
	st := buildStaffing(Config{}, events)
	if st.Lead != "lead-1 (m1)" {
		t.Errorf("Lead = %q, want lead-1 (m1)", st.Lead)
	}
	if len(st.Roles) != 0 {
		t.Errorf("Roles len = %d, want 0 (no config)", len(st.Roles))
	}

	// Not registered
	st = buildStaffing(Config{}, []Event{})
	if st.Lead != "not registered" {
		t.Errorf("Lead = %q, want not registered", st.Lead)
	}
	if len(st.Roles) != 0 {
		t.Errorf("Roles len = %d, want 0", len(st.Roles))
	}
}

func TestFloorStaffingRolesFromConfigAndFloor(t *testing.T) {
	t.Parallel()
	// Config names all three roles, only lead is staffed
	cfg := Config{Staffing: &StaffingConfig{
		Lead:      &RoleConfig{Adapter: "claude", Model: "claude-opus-5", Session: "lead-fw"},
		Inspector: &RoleConfig{Adapter: "claude", Model: "claude-haiku-4", Session: "insp-fw"},
		Auditor:   &RoleConfig{Adapter: "opencode", Model: "", Session: "aud-fw"},
	}}
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "lead-1", Persona: "lead", Model: "claude-opus-5"},
	}
	st := buildStaffing(cfg, events)
	if len(st.Roles) != 3 {
		t.Fatalf("Roles len = %d, want 3", len(st.Roles))
	}
	if st.Roles[0].Name != "lead" || st.Roles[0].Session != "lead-1" {
		t.Errorf("lead role: Name=%q Session=%q, want Name=lead Session=lead-1", st.Roles[0].Name, st.Roles[0].Session)
	}
	if st.Roles[1].Name != "inspector" || st.Roles[1].Session != "" {
		t.Errorf("inspector role: Name=%q Session=%q, want Name=inspector Session=''", st.Roles[1].Name, st.Roles[1].Session)
	}
	if st.Roles[2].Name != "auditor" || st.Roles[2].Session != "" {
		t.Errorf("auditor role: Name=%q Session=%q, want Name=auditor Session=''", st.Roles[2].Name, st.Roles[2].Session)
	}
}

func TestFloorStaffingMismatchFlagged(t *testing.T) {
	t.Parallel()
	cfg := Config{Staffing: &StaffingConfig{
		Lead: &RoleConfig{Adapter: "claude", Model: "claude-opus-5", Session: "lead-fw"},
	}}
	// Lead config says lead-fw but floor shows lead-other
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "lead-other", Persona: "lead", Model: "claude-opus-5"},
	}
	st := buildStaffing(cfg, events)
	if len(st.Roles) != 1 {
		t.Fatalf("Roles len = %d, want 1", len(st.Roles))
	}
	if !st.Roles[0].Mismatch {
		t.Errorf("Mismatch = false, want true")
	}

	// Matching sessions: no mismatch
	events = []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "lead-fw", Persona: "lead", Model: "claude-opus-5"},
	}
	st = buildStaffing(cfg, events)
	if st.Roles[0].Mismatch {
		t.Errorf("Mismatch = true, want false (sessions match)")
	}

	// Never staffed: no mismatch
	st = buildStaffing(cfg, []Event{})
	if st.Roles[0].Mismatch {
		t.Errorf("Mismatch = true, want false (never staffed)")
	}
}

func TestFloorStaffingAndonOnMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitSeeded(dir, false, "", "", false); err != nil {
		t.Fatalf("InitSeeded: %v", err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Workers = []Worker{{Name: "default", Adapter: "opencode", Model: "m1", MaxParallel: 1}}
	cfg.Staffing = &StaffingConfig{
		Lead: &RoleConfig{Adapter: "claude", Model: "claude-opus-5", Session: "lead-fw"},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if err := AppendEvent(dir, Event{
		TS:      time.Now().UTC().Format(time.RFC3339Nano),
		Kind:    "staffed",
		Session: "lead-other",
		Persona: "lead",
		Model:   "claude-opus-5",
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	w := NewWatcher()
	f, err := w.Refresh(dir, time.Now())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var found bool
	for _, a := range f.Andon {
		if a.Task == "staffing/lead" && a.State == "mismatch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Andon missing staffing/lead mismatch entry; got %v", f.Andon)
	}
}

func TestFloorStaffingRenderShowsRoles(t *testing.T) {
	t.Parallel()
	cfg := Config{Staffing: &StaffingConfig{
		Lead:      &RoleConfig{Adapter: "claude", Model: "claude-opus-5", Session: "lead-fw"},
		Inspector: &RoleConfig{Adapter: "claude", Model: "claude-haiku", Session: "insp-fw"},
	}}
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "lead-other", Persona: "lead", Model: "claude-opus-5"},
		{TS: "2026-09-13T00:00:01Z", Kind: "staffed", Session: "insp-fw", Persona: "inspector", Model: "claude-haiku"},
	}
	st := buildStaffing(cfg, events)
	f := Floor{Staffing: st}
	var w bytes.Buffer
	renderFloor(&w, f, 40, 100)
	text := w.String()
	if !strings.Contains(text, "lead") || !strings.Contains(text, "config") || !strings.Contains(text, "floor") {
		t.Errorf("renderFloor missing expected columns; got:\n%s", text)
	}
	if !strings.Contains(text, "!") {
		t.Errorf("renderFloor missing ! for mismatch; got:\n%s", text)
	}
}

func TestFloorStaffingGoldenUnchanged(t *testing.T) {
	t.Parallel()
	// The fixture floor has no staffing config, so this unit must leave the
	// floor byte-identical (#355 review: the first version of this test
	// compared a hand-built floor with itself).
	var buf bytes.Buffer
	RenderText(&buf, fixtureFloor(t), 100, false)
	want, err := os.ReadFile("testdata/factory/floor.golden")
	if err != nil {
		t.Fatalf("read floor.golden: %v", err)
	}
	if normLF(buf.Bytes()) != normLF(want) {
		t.Errorf("RenderText() differs from floor.golden:\n%s", buf.String())
	}
}

func TestFloorStaffingJSONRoles(t *testing.T) {
	t.Parallel()
	cfg := Config{Staffing: &StaffingConfig{
		Lead: &RoleConfig{Adapter: "claude", Model: "claude-opus-5", Session: "lead-fw"},
	}}
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "lead-other", Persona: "lead", Model: "claude-opus-5"},
	}
	st := buildStaffing(cfg, events)
	f := Floor{Dir: "test", Refreshed: time.Now(), Staffing: st}
	var w bytes.Buffer
	RenderJSON(&w, f)
	text := w.String()
	if !strings.Contains(text, "\"roles\"") {
		t.Errorf("RenderJSON missing roles; got:\n%s", text)
	}
	if !strings.Contains(text, "\"mismatch\"") {
		t.Errorf("RenderJSON missing mismatch flag; got:\n%s", text)
	}

	// Without staffing config, no "roles" key
	f2 := Floor{Dir: "test", Refreshed: time.Now(), Staffing: Staffing{Lead: "not registered"}}
	var w2 bytes.Buffer
	RenderJSON(&w2, f2)
	if strings.Contains(w2.String(), "\"roles\"") {
		t.Errorf("RenderJSON should omit roles when empty; got:\n%s", w2.String())
	}
}

func TestFloorStaffingTUIWorkersView(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Workers:  []Worker{{Name: "w1", Adapter: "opencode", Model: "m1", MaxParallel: 2}},
		Staffing: &StaffingConfig{Lead: &RoleConfig{Adapter: "claude", Model: "claude-opus-5", Session: "lead-fw"}},
	}
	events := []Event{
		{TS: "2026-09-13T00:00:00Z", Kind: "staffed", Session: "lead-other", Persona: "lead", Model: "claude-opus-5"},
	}
	st := buildStaffing(cfg, events)
	d := TUIData{Floor: Floor{Lines: []FloorLine{{Name: "w1", Adapter: "opencode", Model: "m1", MaxParallel: 2}}, Staffing: st}}
	tui := NewTUI()
	tui.view = "workers"
	header, rows := tui.Rows(d)
	if len(header) != 5 {
		t.Errorf("header len = %d, want 5", len(header))
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2 (1 worker + 1 role)", len(rows))
	}
	if rows[1][0] != "lead" {
		t.Errorf("role name = %q, want lead", rows[1][0])
	}
	if rows[1][1] != "claude" {
		t.Errorf("role adapter = %q, want claude", rows[1][1])
	}
	if !strings.Contains(rows[1][4], "!") {
		t.Errorf("role busy cell missing ! for mismatch: %q", rows[1][4])
	}
}

// TestFloorStaffingWorkersViewUnconfiguredRole checks that a role the floor
// registered but the config does not name renders in the workers view: it
// has no configured fields to split (#355 review: that panicked).
func TestFloorStaffingWorkersViewUnconfiguredRole(t *testing.T) {
	t.Parallel()
	d := TUIData{Floor: Floor{
		Lines:    []FloorLine{{Name: "default", Adapter: "sim", Model: "m", MaxParallel: 1}},
		Staffing: Staffing{Lead: "s1", Roles: []FloorRole{{Name: "lead", Session: "s1", Model: "m"}}},
	}}
	m := NewTUI()
	m.view = "workers"
	_, rows := m.Rows(d)
	if len(rows) != 2 {
		t.Fatalf("workers rows = %d, want the worker and the role", len(rows))
	}
	if role := rows[1]; role[0] != "lead" || role[1] != "-" || role[2] != "" || role[4] != "s1" {
		t.Errorf("role row = %v, want [lead - <empty> - s1]", role)
	}
	if frame := m.View(d, 80, 12, false); !strings.Contains(frame, "lead") {
		t.Errorf("frame does not show the role:\n%s", frame)
	}
}

// TestFloorStaffingWorkersColumnsFromFields checks that the workers view
// takes the configured adapter and model from the role's fields, not by
// splitting its display line (#357 review).
func TestFloorStaffingWorkersColumnsFromFields(t *testing.T) {
	t.Parallel()
	cfg := Config{Staffing: &StaffingConfig{
		Lead:      &RoleConfig{Session: "s2"},                                 // session only
		Inspector: &RoleConfig{Model: "m2"},                                   // model only
		Auditor:   &RoleConfig{Adapter: "claude", Model: "m1", Session: "s1"}, // all three
	}}
	d := TUIData{Floor: Floor{Staffing: buildStaffing(cfg, nil)}}
	m := NewTUI()
	m.view = "workers"
	_, rows := m.Rows(d)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want three roles", len(rows))
	}
	want := [][3]string{{"lead", "-", ""}, {"inspector", "-", "m2"}, {"auditor", "claude", "m1"}}
	for i, w := range want {
		if rows[i][0] != w[0] || rows[i][1] != w[1] || rows[i][2] != w[2] {
			t.Errorf("row %d = %v, want name %q adapter %q model %q", i, rows[i], w[0], w[1], w[2])
		}
	}
}

// TestFloorStaffingAndonRowHasNoDrillDown checks that a staffing condition
// in the andon view says so instead of asking Explain for a task that does
// not exist (#357 review).
func TestFloorStaffingAndonRowHasNoDrillDown(t *testing.T) {
	t.Parallel()
	d := TUIData{Floor: Floor{
		Units: []Unit{{Task: "T1", Stage: "building", RunState: "running"}},
		Andon: []Andon{{Task: "staffing/lead", State: "mismatch"}},
	}}
	m := NewTUI()
	m.view = "andon"
	m.Update(term.Key{Kind: term.KeyEnter}, d)
	if _, _, ok := m.Wants(); ok {
		t.Error("Wants() ok = true for a staffing andon row, want no drill-down")
	}
	if frame := m.View(d, 90, 12, false); !strings.Contains(frame, "not a unit") {
		t.Errorf("frame does not explain why:\n%s", frame)
	}
}
