package flywheel

import (
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

var (
	chartFlow = [][]float64{
		{1, 1, 2, 2, 3, 3, 3, 4, 4, 4},
		{0, 1, 1, 2, 2, 3, 3, 3, 4, 4},
		{0, 0, 1, 1, 2, 2, 3, 3, 3, 4},
		{0, 0, 0, 1, 1, 2, 2, 3, 3, 3},
		{0, 0, 0, 0, 1, 1, 2, 2, 3, 3},
	}
	chartFlowNames = []string{"landed", "passed", "finished", "building", "planned"}
	chartPoints    = []float64{2, 1.5, 4, 2, 2.5, 1, 4, 2, 3, 8, 2, 4, 1.5, 2, 4}
	chartHeat      = [][]float64{{0, 1, 4, 7, 10}, {10, 7, 4, 1, 0}, {0, 0, 4, 0, 0}}
	chartBuckets   = []HistBucket{{"<1h", 6}, {"1-2h", 10}, {"2-4h", 8}, {"4-8h", 5}, {">8h", 2}}
	chartMarkers   = []Marker{{"p50 2h40m", 2}, {"p90 6h05m", 3}, {"lost", 9}}
	chartBars      = []BarRow{{"opus-5-5", 2.63, "$2.63"}, {"sonnet-5", 1.05, "$1.05"}, {"haiku-4-5", 0.44, "$0.44"}}
)

func TestChartGolden(t *testing.T) {
	t.Parallel()
	control, _ := ControlChart(chartPoints, 3, 1.9, 60, 10, []string{"w1", "w2", "w3", "w4", "w5"})
	flow, err := StackedFlow(chartFlow, chartFlowNames, 60, 8)
	if err != nil {
		t.Fatalf("StackedFlow: %v", err)
	}
	cases := map[string][]string{
		"sparkline": {Sparkline([]float64{3, 4, 6, 9, 8, 6, 9, 2, 1, 5}, 20)},
		"hbars":     HBars(chartBars, 40),
		"histogram": Histogram(chartBuckets, chartMarkers, 50),
		"flow":      flow,
		"control":   control,
		"heatmap":   Heatmap(chartHeat, []string{"Mon", "Tue", "Wed"}, []string{"00", "04", "08", "12", "16"}),
	}
	for name, lines := range cases {
		raw, err := os.ReadFile("testdata/charts/" + name + ".txt")
		if err != nil {
			t.Errorf("read %s golden: %v; rendered:\n%s", name, err, strings.Join(lines, "\n"))
			continue
		}
		want := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if got := strings.Join(lines, "\n") + "\n"; got != want {
			t.Errorf("%s differs from its golden:\n%s", name, got)
		}
	}
}

func TestChartSparkline(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in    []float64
		width int
		want  string
	}{
		{[]float64{0, 1, 2, 3, 4, 5, 6, 7}, 8, "▁▂▃▄▅▆▇█"},
		{[]float64{0, 0, 10, 10}, 2, "▁█"},
		{[]float64{0, 2, 1, 1, 10, 10}, 3, "▁▁█"},
		{[]float64{5, 5, 5}, 10, "▄▄▄"},
		{nil, 10, ""},
		{[]float64{1, 2}, 0, ""},
	}
	for _, c := range cases {
		if got := Sparkline(c.in, c.width); got != c.want {
			t.Errorf("Sparkline(%v, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestChartHBarsScaling(t *testing.T) {
	t.Parallel()
	got := HBars([]BarRow{{"a", 8, ""}, {"b", 3.5, ""}, {"c", 0.125, ""}, {"d", 0, ""}}, 10)
	want := []string{"a ████████", "b ███▌", "c ▏", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("HBars = %q, want %q", got, want)
	}
}

func TestChartHistogramMarkers(t *testing.T) {
	t.Parallel()
	got := Histogram(chartBuckets, chartMarkers, 50)
	for i, line := range got {
		has50, has90 := strings.Contains(line, "▲ p50 2h40m"), strings.Contains(line, "▲ p90 6h05m")
		if has50 != (i == 2) || has90 != (i == 3) || strings.Contains(line, "lost") {
			t.Errorf("line %d %q: p50 %v p90 %v", i, line, has50, has90)
		}
	}
}

func TestChartControlOutOfLimit(t *testing.T) {
	t.Parallel()
	lines, over := ControlChart(chartPoints, 3, 1.9, 60, 10, nil)
	if !reflect.DeepEqual(over, []int{9}) {
		t.Errorf("out-of-limit indexes = %v, want [9]", over)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "+2σ 6.8") {
		t.Errorf("no labelled +2σ line:\n%s", strings.Join(lines, "\n"))
	}
}

func TestChartHeatmapLevels(t *testing.T) {
	t.Parallel()
	got := Heatmap([][]float64{{0, 1, 4, 7, 10}}, nil, nil)
	if want := []string{"  ░ ▒ ▓ █"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Heatmap = %q, want %q", got, want)
	}
}

func TestChartBounds(t *testing.T) {
	t.Parallel()
	check := func(name string, lines []string, width, height int) {
		if height > 0 && len(lines) > height {
			t.Errorf("%s at %dx%d: %d lines", name, width, height, len(lines))
		}
		for _, l := range lines {
			if cellCount(l) > width {
				t.Errorf("%s at %dx%d: %q is %d cells", name, width, height, l, cellCount(l))
			}
		}
	}
	for w := 0; w <= 70; w++ {
		for h := 0; h <= 12; h += 3 {
			control, _ := ControlChart(chartPoints, 3, 1.9, w, h, []string{"week 1", "week 2", "week 3"})
			check("ControlChart", control, w, h)
			flow, _ := StackedFlow(chartFlow, chartFlowNames, w, h)
			check("StackedFlow", flow, w, h)
		}
		check("Sparkline", []string{Sparkline(chartPoints, w)}, w, 0)
		check("HBars", HBars(chartBars, w), w, 0)
		check("Histogram", Histogram(chartBuckets, chartMarkers, w), w, 0)
	}
}

func TestChartFlowDistinctBands(t *testing.T) {
	t.Parallel()
	fills := string(flowShades)
	for _, n := range []int{5, 8} {
		series, names := make([][]float64, n), make([]string, n)
		for k := range series {
			series[k], names[k] = []float64{1, 1, 1, 1}, "s"+strconv.Itoa(k)
		}
		lines, err := StackedFlow(series, names, 80, n+2)
		if err != nil {
			t.Fatalf("%d bands: %v", n, err)
		}
		plot, legend := map[rune]bool{}, map[rune]int{}
		for _, r := range strings.Join(lines[:len(lines)-1], "") {
			if strings.ContainsRune(fills, r) {
				plot[r] = true
			}
		}
		for _, r := range lines[len(lines)-1] {
			if strings.ContainsRune(fills, r) {
				legend[r]++
			}
		}
		if len(plot) != n || len(legend) != n {
			t.Errorf("%d bands drew %d distinct fills and a legend of %d:\n%s", n, len(plot), len(legend), strings.Join(lines, "\n"))
		}
		for r, c := range legend {
			if c != 1 {
				t.Errorf("%d bands: legend lists %q %d times", n, r, c)
			}
		}
	}
	if _, err := StackedFlow(make([][]float64, 9), nil, 80, 10); err == nil {
		t.Error("StackedFlow with 9 series: want an error, got nil")
	}
}
