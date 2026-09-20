package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStationOfEveryStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		reason string
		want   string
	}{
		{"planned", "", "queue"},
		{"dispatched", "", "build"},
		{"running", "", "build"},
		{"finished", "", "measure"},
		{"finished", "stop", "measure"},
		{"finished", "length", "measure"},
		{"finished", "error", "measure"},
		{"passed", "", "land"},
		{"needs-correction", "", "build"},
		{"blocked", "", "blocked"},
		{"rejected", "", "scrap"},
		{"landed", "", "landed"},
		{"lost", "", "lost"},
		{"", "", "queue"},
	} {
		if got := StationOf(tc.status, tc.reason); got != tc.want {
			t.Errorf("StationOf(%q, %q) = %q, want %q", tc.status, tc.reason, got, tc.want)
		}
	}
}

func TestStationForSplitsMeasureAndInspect(t *testing.T) {
	// A finished unit without complete readings → "measure"
	ts1 := TaskState{ID: "task1", Status: "finished", Attempt: "r1"}
	events1 := []Event{
		{Task: "task1", Kind: "finished", Attempt: "r1", TS: "2026-09-12T00:50:00Z"},
	}
	if got := StationFor(ts1, events1); got != "measure" {
		t.Errorf("unready unit: StationFor() = %q, want measure", got)
	}

	// The same unit after the readings inspectionReady wants → "inspect"
	rc := 0
	events2 := []Event{
		{Task: "task1", Kind: "finished", Attempt: "r1", TS: "2026-09-12T00:50:00Z"},
		{Task: "task1", Kind: "owns_checked", Attempt: "r1", TS: "2026-09-12T00:51:00Z"},
		{Task: "task1", Kind: "validated", Attempt: "r1", Gate: "gate1", Reason: "", RC: &rc, Tree: "", TS: "2026-09-12T00:52:00Z"},
	}
	if got := StationFor(ts1, events2); got != "inspect" {
		t.Errorf("ready unit: StationFor() = %q, want inspect", got)
	}
}

func TestInWIPExcludesQueueAndTerminals(t *testing.T) {
	for _, tc := range []struct {
		station string
		want    bool
	}{
		{"queue", false},
		{"build", true},
		{"measure", true},
		{"inspect", true},
		{"land", true},
		{"landed", false},
		{"blocked", true},
		{"scrap", false},
		{"lost", false},
	} {
		if got := InWIP(tc.station); got != tc.want {
			t.Errorf("InWIP(%q) = %v, want %v", tc.station, got, tc.want)
		}
	}
}

func TestLineOfPrefersDispatchedThenBrief(t *testing.T) {
	cfg := Config{Lines: []Line{
		{Name: "cli", Worker: "w1", Owns: []string{"internal/"}},
		{Name: "docs", Worker: "w1", Owns: []string{"docs/"}},
	}}
	dir := t.TempDir()

	// The dispatched event's line wins.
	dispatched := []Event{{Task: "task1", Kind: "dispatched", Attempt: "r1", Line: "cli"}}
	if got := LineOf(cfg, dir, dispatched, "task1"); got != "cli" {
		t.Errorf("dispatched line: LineOf() = %q, want cli", got)
	}

	// Planned but never dispatched: the brief decides, and its path is
	// relative to dir — the branch that needs the directory.
	brief := "owns: docs/guide.md\nneeds: none\ngate: exit 0\n\n# TASK: y\n"
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	planned := []Event{{TS: "2026-09-19T00:00:00Z", Task: "task2", Kind: "planned", Brief: "b.txt"}}
	if got := LineOf(cfg, dir, planned, "task2"); got != "docs" {
		t.Errorf("planned-only: LineOf() = %q, want docs (its brief owns docs/)", got)
	}
	// The same ledger read from elsewhere finds no brief and claims no line.
	if got := LineOf(cfg, t.TempDir(), planned, "task2"); got != "" {
		t.Errorf("brief missing: LineOf() = %q, want empty", got)
	}

	if got := LineOf(cfg, dir, []Event{}, "unknown"); got != "" {
		t.Errorf("unknown task: LineOf() = %q, want empty", got)
	}
}

func TestBuildProductLinesCountsStations(t *testing.T) {
	cfg := Config{Lines: []Line{{Name: "cli", Worker: "w1"}, {Name: "docs", Worker: "w1"}}}
	units := []Unit{
		{Task: "t1", Line: "cli", Station: "build"},
		{Task: "t2", Line: "cli", Station: "measure"},
		{Task: "t3", Line: "cli", Station: "inspect"},
		{Task: "t4", Line: "docs", Station: "land"},
		{Task: "t5", Line: "docs", Station: "landed"},
	}

	result := buildProductLines(cfg, units)
	if len(result) != 2 {
		t.Errorf("buildProductLines() = %d lines, want 2", len(result))
	}

	// Check cli line
	cli := result[0]
	if cli.Name != "cli" || cli.Units != 3 {
		t.Errorf("cli line: name=%q units=%d, want cli 3", cli.Name, cli.Units)
	}
	if cli.Stations["build"] != 1 || cli.Stations["measure"] != 1 || cli.Stations["inspect"] != 1 {
		t.Errorf("cli line stations: %v, want build:1 measure:1 inspect:1", cli.Stations)
	}
	if cli.WIP != 3 {
		t.Errorf("cli line WIP: %d, want 3", cli.WIP)
	}

	// Check docs line
	docs := result[1]
	if docs.Name != "docs" || docs.Units != 2 {
		t.Errorf("docs line: name=%q units=%d, want docs 2", docs.Name, docs.Units)
	}
	if docs.Stations["land"] != 1 || docs.Stations["landed"] != 1 {
		t.Errorf("docs line stations: %v, want land:1 landed:1", docs.Stations)
	}
	if docs.WIP != 1 {
		t.Errorf("docs line WIP: %d, want 1", docs.WIP)
	}
}

func TestBuildProductLinesCarriesLimit(t *testing.T) {
	cfg := Config{Lines: []Line{{Name: "cli", Worker: "w1", WIP: 3}}}
	units := []Unit{{Task: "t1", Line: "cli", Station: "build"}}

	result := buildProductLines(cfg, units)
	if len(result) != 1 {
		t.Errorf("buildProductLines() = %d lines, want 1", len(result))
	}

	if result[0].Limit != 3 {
		t.Errorf("limit: %d, want 3", result[0].Limit)
	}
}

func TestFloorTextUnchangedByStations(t *testing.T) {
	fl := fixtureFloor(t)
	var buf byteBuffer
	RenderText(&buf, fl, 100, false)
	got := normLF(buf.Bytes())
	want, err := os.ReadFile("testdata/factory/floor.golden")
	if err != nil {
		t.Fatalf("read floor.golden: %v", err)
	}
	if got != normLF(want) {
		t.Errorf("RenderText() differs from floor.golden")
	}
}

func TestConfigLineWIPGetAndValidate(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, ".flywheel", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configJSON := `{"version":1,"workers":[{"name":"default","adapter":"opencode","model":"test"}],"lines":[{"name":"cli","worker":"default","wip":3}]}`
	if err := os.WriteFile(configPath, []byte(configJSON), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	// Get lines.cli.wip
	val, err := cfg.Get("lines.cli.wip")
	if err != nil || val != "3" {
		t.Errorf("Get(lines.cli.wip) = %q %v, want 3 nil", val, err)
	}

	// Negative wip must fail validation
	badCfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "opencode", Model: "test"}},
		Lines:   []Line{{Name: "cli", Worker: "default", WIP: -1}},
	}
	if err := badCfg.Validate(); err == nil {
		t.Errorf("Validate() with negative wip: got nil, want error")
	}

	// Set must refuse
	err = cfg.Set("lines.cli.wip", "5")
	if err == nil || err.Error() != "lines.<name>.wip is not settable; edit .flywheel/config.json" {
		t.Errorf("Set(lines.cli.wip) = %v, want not-settable error", err)
	}
}

// byteBuffer accumulates write() data in memory.
type byteBuffer struct {
	b []byte
}

func (b *byteBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *byteBuffer) Bytes() []byte {
	return b.b
}
