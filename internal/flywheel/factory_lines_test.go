package flywheel

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFloorLinesLineFor(t *testing.T) {
	events := []Event{
		{Kind: "dispatched", Task: "T1", Attempt: "r1", Line: "cli"},
		{Kind: "dispatched", Task: "T1", Attempt: "r2", Line: "docs"},
	}
	if got := lineFor(events, "T1", "r2"); got != "docs" {
		t.Errorf("lineFor(events, \"T1\", \"r2\") = %q, want \"docs\"", got)
	}
	if got := lineFor(events, "T1", "r1"); got != "cli" {
		t.Errorf("lineFor(events, \"T1\", \"r1\") = %q, want \"cli\"", got)
	}
	if got := lineFor(events, "T2", "r1"); got != "" {
		t.Errorf("lineFor(events, \"T2\", \"r1\") = %q, want \"\"", got)
	}
}

func TestFloorLinesBuildCounts(t *testing.T) {
	cfg := Config{
		Lines: []Line{
			{Name: "cli", Worker: "w1", Owns: []string{"internal/"}},
			{Name: "docs", Worker: "w2"},
		},
	}
	units := []Unit{
		{Task: "A", Line: "cli", Stage: "building"},
		{Task: "B", Line: "cli", Stage: "landed"},
		{Task: "C", Line: "docs", Stage: "building"},
		{Task: "D", Line: "", Stage: "building"},
	}
	pls := buildProductLines(cfg, units)
	if len(pls) != 3 {
		t.Errorf("buildProductLines returned %d entries, want 3", len(pls))
		return
	}
	if pls[0].Name != "cli" || pls[0].Units != 2 || pls[0].Building != 1 || pls[0].Landed != 1 {
		t.Errorf("cli line: got Units=%d, Building=%d, Landed=%d, want 2, 1, 1", pls[0].Units, pls[0].Building, pls[0].Landed)
	}
	if pls[1].Name != "docs" || pls[1].Units != 1 || pls[1].Building != 1 || pls[1].Landed != 0 {
		t.Errorf("docs line: got Units=%d, Building=%d, Landed=%d, want 1, 1, 0", pls[1].Units, pls[1].Building, pls[1].Landed)
	}
	if pls[2].Name != "(none)" || pls[2].Units != 1 {
		t.Errorf("(none) line: got Name=%q, Units=%d, want \"(none)\", 1", pls[2].Name, pls[2].Units)
	}
}

func TestFloorLinesNoConfigNil(t *testing.T) {
	cfg := Config{Lines: []Line{}}
	units := []Unit{{Task: "A", Line: "cli", Stage: "building"}}
	pls := buildProductLines(cfg, units)
	if pls != nil {
		t.Errorf("buildProductLines with no Lines returned %v, want nil", pls)
	}
}

func TestFloorLinesRenderSection(t *testing.T) {
	cfg := Config{
		Lines: []Line{
			{Name: "cli", Worker: "w1", Owns: []string{"internal/"}},
		},
	}
	units := []Unit{
		{Task: "A", Line: "cli", Stage: "building"},
		{Task: "B", Line: "cli", Stage: "landed"},
	}
	f := Floor{
		Dir:          "/repo",
		Refreshed:    time.Now().UTC(),
		ProductLines: buildProductLines(cfg, units),
		Units:        units,
	}
	var buf bytes.Buffer
	RenderText(&buf, f, 80, false)
	out := buf.String()
	if !strings.Contains(out, "\nlines\n") {
		t.Error("render output missing \\nlines\\n section")
	}
	if !strings.Contains(out, "cli") {
		t.Error("render output missing 'cli' product line name")
	}
	if !strings.Contains(out, "w1") {
		t.Error("render output missing 'w1' worker")
	}
	if !strings.Contains(out, "units 2") {
		t.Error("render output missing 'units 2'")
	}
	if !strings.Contains(out, "building 1") {
		t.Error("render output missing 'building 1'")
	}
	if !strings.Contains(out, "owns internal/") {
		t.Error("render output missing 'owns internal/'")
	}
}

func TestFloorLinesRenderLineColumn(t *testing.T) {
	units := []Unit{
		{Task: "A", Line: "cli", Stage: "building"},
		{Task: "B", Line: "", Stage: "landed"},
	}
	f := Floor{
		Dir:       "/repo",
		Refreshed: time.Now().UTC(),
		Units:     units,
	}
	var buf bytes.Buffer
	RenderText(&buf, f, 80, false)
	out := buf.String()
	if !strings.Contains(out, "LINE") {
		t.Error("render output missing 'LINE' column header when units have lines")
	}
	if !strings.Contains(out, "cli") {
		t.Error("render output missing unit line 'cli' in table")
	}

	units2 := []Unit{
		{Task: "A", Line: "", Stage: "building"},
		{Task: "B", Line: "", Stage: "landed"},
	}
	f2 := Floor{
		Dir:       "/repo",
		Refreshed: time.Now().UTC(),
		Units:     units2,
	}
	var buf2 bytes.Buffer
	RenderText(&buf2, f2, 80, false)
	out2 := buf2.String()
	if strings.Contains(out2, "LINE") {
		t.Error("render output has 'LINE' column when no units have lines")
	}
	if strings.Contains(out2, "\nlines\n") {
		t.Error("render output has \\nlines\\n section when ProductLines is empty")
	}
}

func TestFloorLinesJSON(t *testing.T) {
	cfg := Config{
		Lines: []Line{
			{Name: "cli", Worker: "w1"},
		},
	}
	units := []Unit{
		{Task: "A", Line: "cli", Stage: "building"},
	}
	f := Floor{
		Dir:          "/repo",
		Refreshed:    time.Now().UTC(),
		ProductLines: buildProductLines(cfg, units),
		Units:        units,
	}
	var buf bytes.Buffer
	RenderJSON(&buf, f)
	out := buf.Bytes()
	var parsed map[string]interface{}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}
	pls, ok := parsed["product_lines"]
	if !ok {
		t.Error("JSON missing 'product_lines' key when ProductLines present")
	}
	plsList := pls.([]interface{})
	if len(plsList) > 0 {
		pl := plsList[0].(map[string]interface{})
		if pl["name"] != "cli" {
			t.Errorf("product_lines[0].name = %v, want \"cli\"", pl["name"])
		}
	}
	units2, ok := parsed["units"]
	if !ok {
		t.Error("JSON missing 'units' key")
		return
	}
	unitsList := units2.([]interface{})
	if len(unitsList) > 0 {
		u := unitsList[0].(map[string]interface{})
		if u["line"] != "cli" {
			t.Errorf("units[0].line = %v, want \"cli\"", u["line"])
		}
	}

	// Test with empty ProductLines
	f2 := Floor{
		Dir:       "/repo",
		Refreshed: time.Now().UTC(),
		Units:     []Unit{{Task: "B", Line: "", Stage: "landed"}},
	}
	var buf2 bytes.Buffer
	RenderJSON(&buf2, f2)
	out2 := buf2.Bytes()
	var parsed2 map[string]interface{}
	if err := json.Unmarshal(out2, &parsed2); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}
	if _, ok := parsed2["product_lines"]; ok {
		t.Error("JSON has 'product_lines' key when ProductLines is empty")
	}
	units3, ok := parsed2["units"]
	if !ok {
		t.Error("JSON missing 'units' key")
		return
	}
	unitsList3 := units3.([]interface{})
	if len(unitsList3) > 0 {
		u := unitsList3[0].(map[string]interface{})
		if _, ok := u["line"]; ok && u["line"] != "" {
			t.Error("JSON has non-empty 'line' key in units when lines are empty")
		}
	}
}

// TestFloorLinesFitWidth checks that the LINE column and the lines section
// never push a line past the render width: the column's cells come out of
// MODEL, TASK and SESSION, and a long owns list is cut.
func TestFloorLinesFitWidth(t *testing.T) {
	long := strings.Repeat("x", 60)
	fl := Floor{
		Refreshed:    time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC),
		ProductLines: []ProductLine{{Name: "cli", Worker: "w1", Owns: []string{long, long}, Units: 1}},
		Units:        []Unit{{Task: long, Line: "documentation", Stage: "building", Session: long, Model: long, RunState: "running"}},
	}
	for _, width := range []int{80, 100, 140} {
		var b bytes.Buffer
		RenderText(&b, fl, width, false)
		for _, line := range strings.Split(b.String(), "\n") {
			if strings.HasPrefix(line, "  landed today") {
				continue // the output summary is not this unit's
			}
			if n := len([]rune(line)); n > width {
				t.Errorf("width %d: line is %d runes: %q", width, n, line)
			}
		}
		if !strings.Contains(b.String(), "LINE") || !strings.Contains(b.String(), "document~") && !strings.Contains(b.String(), "documen~") {
			t.Errorf("width %d: want a LINE column with the truncated line name:\n%s", width, b.String())
		}
	}
}
