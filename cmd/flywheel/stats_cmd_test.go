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
	if err := runStatsMetrics(&out, metricsDir(t), "24h", 0, false, metricsNow); err != nil {
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
	if err := runStatsMetrics(&out, metricsDir(t), "24h", 0, true, metricsNow); err != nil {
		t.Fatal(err)
	}
	var rep flywheel.MetricsReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("--json does not parse: %v\n%s", err, out.String())
	}
	if rep.Flow.Throughput != 1 || len(rep.Buckets) != 24 || len(rep.Flow.WIPSeries) != 24 || len(rep.Cost.SpendSeries) != 24 {
		t.Errorf("json report = throughput %d, %d buckets, series %d/%d", rep.Flow.Throughput, len(rep.Buckets), len(rep.Flow.WIPSeries), len(rep.Cost.SpendSeries))
	}
	if err := runStatsMetrics(&out, metricsDir(t), "1y", 0, true, metricsNow); err == nil {
		t.Error("window 1y accepted")
	}
}

// TestStatsWIPStaleAfter (issue #590): a negative --wip-stale-after is a
// usage error, the flag reaches the window, and --metrics --json carries
// "stale". On the unfixed code the flag is unknown (exit 2 for every call,
// so the 0-exit assertion fails) and the JSON has no "stale".
func TestStatsWIPStaleAfter(t *testing.T) {
	t.Parallel()
	dir := metricsDir(t)
	run := func(args ...string) (int, string, string) {
		var out, errb strings.Builder
		code := statsMain(args, &out, &errb, metricsNow)
		return code, out.String(), errb.String()
	}
	if code, _, errs := run("--dir", dir, "--metrics", "--wip-stale-after", "-1h"); code != 2 || !strings.Contains(errs, "--wip-stale-after") {
		t.Errorf("--wip-stale-after -1h = %d, want 2 (usage)\n%s", code, errs)
	}
	code, out, errs := run("--dir", dir, "--metrics", "--json")
	if code != 0 || !strings.Contains(out, `"stale":`) || !strings.Contains(out, `"stale_oldest":`) {
		t.Fatalf("--metrics --json = %d, want \"stale\" and \"stale_oldest\"\n%s%s", code, out, errs)
	}
	// d-limited's last event is 09-02T04:00 and f-running's 09-02T09:01: an
	// 18h threshold at 09-03T00:00 makes d-limited stale (20h) and keeps
	// f-running in WIP.
	var rep flywheel.MetricsReport
	code, out, errs = run("--dir", dir, "--metrics", "--json", "--wip-stale-after", "18h")
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || rep.Flow.WIP != 1 || rep.Flow.Stale != 1 || rep.Flow.StaleOldest != 20*time.Hour {
		t.Errorf("--wip-stale-after 18h = %d, wip %d stale %d oldest %v, want 1 1 20h\n%s", code, rep.Flow.WIP, rep.Flow.Stale, rep.Flow.StaleOldest, errs)
	}
}
