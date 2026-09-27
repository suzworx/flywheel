package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// metricsDir is a temp flywheel dir whose ledger is the engine's fixture.
func metricsDir(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "flywheel", "testdata", "metrics-ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// metricsNow ends the 24h window at the end of the fixture's 2026-09-02.
var metricsNow = time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

func TestMetricsCLIFlags(t *testing.T) {
	t.Parallel()
	fs, o := statsFlags()
	if err := fs.Parse([]string{"--metrics", "--window", "7d", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !o.metrics || o.window != "7d" || !o.jsonOut {
		t.Errorf("options = %+v, want metrics 7d json", o)
	}
	if fs, o := statsFlags(); fs.Parse(nil) != nil || o.metrics || o.window != "24h" {
		t.Errorf("defaults = %+v, want no metrics and window 24h", o)
	}
}

func TestMetricsCLITable(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := runStatsMetrics(&out, metricsDir(t), "24h", false, metricsNow); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, family := range []string{"\nFlow:\n", "\nQuality:\n", "\nReliability:\n", "\nCost:\n", "\nCapacity:\n"} {
		if !strings.Contains(text, family) {
			t.Errorf("output lacks family %q:\n%s", strings.TrimSpace(family), text)
		}
	}
	// 09-02 against 09-01: one landing against two, 2h frozen against none,
	// $0.75 against $4.80, and two units in progress at the end against one.
	want := map[string][]string{
		"throughput": {"throughput", "1", "↓"},
		"frozen":     {"frozen", "2h0m0s", "↑"},
		"spend":      {"spend", "$0.7500", "↓"},
		"wip":        {"wip", "2", "↑"},
	}
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && want[f[0]] != nil {
			if !slices.Equal(f, want[f[0]]) {
				t.Errorf("row %q = %q, want %q", f[0], f, want[f[0]])
			}
			delete(want, f[0])
		}
	}
	for name := range want {
		t.Errorf("no %s row in:\n%s", name, text)
	}
}

func TestMetricsCLIJSON(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	if err := runStatsMetrics(&out, metricsDir(t), "24h", true, metricsNow); err != nil {
		t.Fatal(err)
	}
	var rep flywheel.MetricsReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("--json does not parse: %v\n%s", err, out.String())
	}
	if rep.Flow.Throughput != 1 || len(rep.Buckets) != 24 || len(rep.Flow.WIPSeries) != 24 || len(rep.Cost.SpendSeries) != 24 {
		t.Errorf("json report = throughput %d, %d buckets, series %d/%d", rep.Flow.Throughput, len(rep.Buckets), len(rep.Flow.WIPSeries), len(rep.Cost.SpendSeries))
	}
	if err := runStatsMetrics(&out, metricsDir(t), "1y", true, metricsNow); err == nil {
		t.Error("window 1y accepted")
	}
}
