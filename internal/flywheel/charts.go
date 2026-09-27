package flywheel

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Terminal chart primitives for the factory dashboards (issue #583). Every
// renderer is a pure function from numbers to plain lines of text: widths and
// heights are in terminal cells, output never exceeds them, trailing spaces are
// trimmed, and no colour codes are emitted (the TUI colours whole cells or
// lines later).

var (
	sparkLevels = []rune("▁▂▃▄▅▆▇█")
	barEighths  = []rune("▏▎▍▌▋▊▉") // 1/8 .. 7/8 of a cell
	heatShades  = []rune(" ░▒▓█")
	// flowShades are the StackedFlow band fills in stage order, all from the
	// Block Elements range, so each is one cell and no two bands look alike.
	flowShades = []rune("░▒▓█▚▞▙▜")
)

// chartLabelMax caps the width of a label column.
const chartLabelMax = 20

// scale maps v in [lo, hi] onto 0..cells, rounding to the nearest cell and
// clamping outside the range; an empty range or no cells maps to 0.
func scale(v, lo, hi float64, cells int) int {
	if cells <= 0 || !(hi > lo) || math.IsNaN(v) || v <= lo {
		return 0
	}
	if v >= hi {
		return cells
	}
	return int(math.Round((v - lo) / (hi - lo) * float64(cells)))
}

// cellCount is the width of s in terminal cells (every chart rune is one cell).
func cellCount(s string) int { return utf8.RuneCountInString(s) }

// fitCells cuts s to at most w cells.
func fitCells(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) > w {
		r = r[:w]
	}
	return string(r)
}

// padCells fits s to exactly w cells.
func padCells(s string, w int) string {
	s = fitCells(s, w)
	return s + strings.Repeat(" ", w-cellCount(s))
}

// chartLine fits s to w cells and trims trailing spaces.
func chartLine(s string, w int) string { return strings.TrimRight(fitCells(s, w), " ") }

// chartNum formats a value compactly: integers without a fraction, others to
// one decimal place.
func chartNum(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// labelWidth is the width of a label column: the longest label, at most
// chartLabelMax.
func labelWidth(labels []string) int {
	w := 0
	for _, l := range labels {
		if c := cellCount(l); c > w {
			w = c
		}
	}
	if w > chartLabelMax {
		w = chartLabelMax
	}
	return w
}

// hbar draws v as a bar of full cells plus a fractional last cell, scaled so
// that max fills w cells, padded to w cells.
func hbar(v, max float64, w int) string {
	e := scale(v, 0, max, w*8)
	s := strings.Repeat("█", e/8)
	if e%8 > 0 {
		s += string(barEighths[e%8-1])
	}
	return padCells(s, w)
}

// Sparkline draws values on eight levels ▁..█, one cell per value; more values
// than width are resampled by averaging consecutive runs down to width cells.
// All-equal values draw a flat middle line; no values (or no width) draw "".
func Sparkline(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}
	n, out := len(values), len(values)
	if out > width {
		out = width
	}
	avg := make([]float64, out)
	for i := range avg {
		from, to := i*n/out, (i+1)*n/out
		sum := 0.0
		for _, v := range values[from:to] {
			sum += v
		}
		avg[i] = sum / float64(to-from)
	}
	lo, hi := avg[0], avg[0]
	for _, v := range avg {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	var b strings.Builder
	for _, v := range avg {
		l := len(sparkLevels)/2 - 1
		if hi > lo {
			l = scale(v, lo, hi, len(sparkLevels)-1)
		}
		b.WriteRune(sparkLevels[l])
	}
	return b.String()
}

// BarRow is one row of HBars: a label, the value the bar scales and the text
// printed after the bar (e.g. "$2.63").
type BarRow struct {
	Label string
	Value float64
	Text  string
}

// HBars draws one horizontal bar per row, in the order given: a label column
// padded to the longest label (at most 20 cells), a bar of █ with a fractional
// last cell (▏▎▍▌▋▊▉) scaled so the largest value fills the bar area, then the
// row's Text aligned after the bar area. Negative values draw no bar.
func HBars(rows []BarRow, width int) []string {
	if len(rows) == 0 || width <= 0 {
		return nil
	}
	labels := make([]string, len(rows))
	tw, max := 0, 0.0
	for i, r := range rows {
		labels[i] = r.Label
		if c := cellCount(r.Text); c > tw {
			tw = c
		}
		max = math.Max(max, r.Value)
	}
	lw := labelWidth(labels)
	barW := width
	if lw > 0 {
		barW -= lw + 1
	}
	if tw > 0 {
		barW -= tw + 1
	}
	if barW < 1 {
		barW = 1
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		line := ""
		if lw > 0 {
			line = padCells(r.Label, lw) + " "
		}
		line += hbar(r.Value, max, barW)
		if r.Text != "" {
			line += " " + r.Text
		}
		out = append(out, chartLine(line, width))
	}
	return out
}

// HistBucket is one histogram row: its label and how many samples fell in it.
type HistBucket struct {
	Label string
	Count int
}

// Marker annotates a histogram bucket, e.g. Label "p50 2h40m" on the bucket
// holding the median.
type Marker struct {
	Label       string
	BucketIndex int
}

// Histogram draws one row per bucket (label, bar, count) with each marker drawn
// beside its bucket as "▲ <label>"; several markers on one bucket are joined
// and markers with an index outside the buckets are dropped.
func Histogram(buckets []HistBucket, markers []Marker, width int) []string {
	if len(buckets) == 0 || width <= 0 {
		return nil
	}
	notes := make([]string, len(buckets))
	for _, m := range markers {
		if m.BucketIndex < 0 || m.BucketIndex >= len(buckets) {
			continue
		}
		if notes[m.BucketIndex] != "" {
			notes[m.BucketIndex] += "  "
		}
		notes[m.BucketIndex] += "▲ " + m.Label
	}
	cw := 0
	for _, b := range buckets {
		if c := len(strconv.Itoa(b.Count)); c > cw {
			cw = c
		}
	}
	rows := make([]BarRow, len(buckets))
	for i, b := range buckets {
		text := strconv.Itoa(b.Count)
		if notes[i] != "" {
			text = padCells(text, cw) + "  " + notes[i]
		}
		rows[i] = BarRow{Label: b.Label, Value: float64(b.Count), Text: text}
	}
	return HBars(rows, width)
}

// StackedFlow draws a cumulative flow diagram in height lines: height-1 plot
// rows and a legend line. Each series is one stage's count per bucket, bottom
// stage first, stacked as bands each filled with its own character, in order
// ░ ▒ ▓ █ ▚ ▞ ▙ ▜; a fill is never reused, so more series than that is an
// error. Buckets are spread across the plot columns (a bucket spans several
// columns when there are fewer buckets than columns, and buckets are sampled
// when there are more). The y-axis labels the top row with the largest total
// and the bottom row with 0; the legend names each band's fill, dropping
// entries that do not fit.
func StackedFlow(series [][]float64, names []string, width, height int) ([]string, error) {
	if len(series) > len(flowShades) {
		return nil, fmt.Errorf("stacked flow: %d series but only %d distinct band fills", len(series), len(flowShades))
	}
	rows, nb := height-1, 0
	for _, s := range series {
		if len(s) > nb {
			nb = len(s)
		}
	}
	if nb == 0 || rows < 1 || width <= 0 {
		return nil, nil
	}
	totals := make([]float64, nb)
	for _, s := range series {
		for b, v := range s {
			if v > 0 {
				totals[b] += v
			}
		}
	}
	max := 0.0
	for _, t := range totals {
		max = math.Max(max, t)
	}
	top := chartNum(max)
	lw := cellCount(top)
	cols := width - lw - 1
	if cols < 1 {
		return nil, nil
	}
	grid := make([][]rune, rows)
	for r := range grid {
		grid[r] = []rune(strings.Repeat(" ", cols))
	}
	for c := 0; c < cols; c++ {
		b := c * nb / cols
		cum, prev := 0.0, 0
		for k, s := range series {
			if b < len(s) && s[b] > 0 {
				cum += s[b]
			}
			h := scale(cum, 0, max, rows)
			for r := prev; r < h; r++ {
				grid[rows-1-r][c] = flowShades[k]
			}
			prev = h
		}
	}
	out := make([]string, 0, height)
	for r := range grid {
		label, axis := "", "│"
		switch {
		case r == 0:
			label, axis = top, "┤"
		case r == rows-1:
			label, axis = "0", "┤"
		}
		out = append(out, chartLine(strings.Repeat(" ", lw-cellCount(label))+label+axis+string(grid[r]), width))
	}
	legend := strings.Repeat(" ", lw+1)
	for k := range series {
		entry := string(flowShades[k])
		if k < len(names) && names[k] != "" {
			entry += " " + names[k]
		}
		if k > 0 {
			entry = "  " + entry
		}
		if cellCount(legend)+cellCount(entry) <= width {
			legend += entry
		}
	}
	return append(out, chartLine(legend, width)), nil
}

// ControlChart draws points as ● in height lines: height-2 plot rows, the
// x-axis and a line of x labels. A point's column is its index scaled to the
// plot width and its row its value scaled between min(0, lowest) and the
// highest of the points and mean+2σ. The mean is a ─ line labelled "mean <v>"
// and mean+2σ a ┄ line labelled "+2σ <v>" (dropped when it shares the mean's
// row); the y-axis ticks the top (the max), 0 and the mean, and x labels are
// spread evenly along the bottom, skipping any that would collide. It also
// returns the indexes of the points above mean+2σ so the view can colour them.
func ControlChart(points []float64, mean, sigma float64, width, height int, xLabels []string) ([]string, []int) {
	ucl := mean + 2*sigma
	var over []int
	for i, p := range points {
		if p > ucl {
			over = append(over, i)
		}
	}
	rows := height - 2
	if len(points) == 0 || rows < 1 || width <= 0 {
		return nil, over
	}
	lo, hi := math.Min(0, mean), math.Max(ucl, mean)
	for _, p := range points {
		lo, hi = math.Min(lo, p), math.Max(hi, p)
	}
	if !(hi > lo) {
		hi = lo + 1
	}
	rowOf := func(v float64) int { return rows - 1 - scale(v, lo, hi, rows-1) }
	ticks := make([]string, rows)
	for _, t := range []float64{hi, lo, mean} {
		if r := rowOf(t); ticks[r] == "" {
			ticks[r] = chartNum(t)
		}
	}
	notes := make([]string, rows)
	meanRow, uclRow := rowOf(mean), rowOf(ucl)
	notes[meanRow] = "mean " + chartNum(mean)
	if uclRow != meanRow {
		notes[uclRow] = "+2σ " + chartNum(ucl)
	}
	lw, rw := labelWidth(ticks), 0
	for _, n := range notes {
		if c := cellCount(n); c+1 > rw {
			rw = c + 1
		}
	}
	cols := width - lw - 1 - rw
	if cols < 1 {
		return nil, over
	}
	grid := make([][]rune, rows)
	for r := range grid {
		fill := " "
		switch r {
		case meanRow:
			fill = "─"
		case uclRow:
			fill = "┄"
		}
		grid[r] = []rune(strings.Repeat(fill, cols))
	}
	for i, p := range points {
		grid[rowOf(p)][scale(float64(i), 0, float64(len(points)-1), cols-1)] = '●'
	}
	out := make([]string, 0, height)
	for r := range grid {
		axis := "│"
		if ticks[r] != "" {
			axis = "┤"
		}
		line := strings.Repeat(" ", lw-cellCount(ticks[r])) + ticks[r] + axis + string(grid[r])
		if notes[r] != "" {
			line += " " + notes[r]
		}
		out = append(out, chartLine(line, width))
	}
	axis := []rune(strings.Repeat(" ", lw) + "└" + strings.Repeat("─", cols))
	labels := []rune(strings.Repeat(" ", width))
	next := 0
	for j, l := range xLabels {
		pos := lw + 1 + scale(float64(j), 0, float64(len(xLabels)-1), cols-1)
		end := pos + cellCount(l)
		if l == "" || pos < next || end > width {
			continue
		}
		copy(labels[pos:], []rune(l))
		axis[pos] = '┬'
		next = end + 1
	}
	out = append(out, chartLine(string(axis), width), chartLine(string(labels), width))
	return out, over
}

// Heatmap shades each cell by its value on five levels " ░▒▓█": zero (or
// less) is blank and positive values split into four levels up to the grid's
// maximum. Row labels sit on the left (at most 20 cells), column labels on a
// header line on top; each cell is as wide as the widest column label.
func Heatmap(grid [][]float64, rowLabels, colLabels []string) []string {
	nc := 0
	max := 0.0
	for _, row := range grid {
		if len(row) > nc {
			nc = len(row)
		}
		for _, v := range row {
			max = math.Max(max, v)
		}
	}
	if nc == 0 {
		return nil
	}
	cw := labelWidth(colLabels)
	if cw < 1 {
		cw = 1
	}
	lw := labelWidth(rowLabels)
	prefix := ""
	if lw > 0 {
		prefix = strings.Repeat(" ", lw+1)
	}
	var out []string
	if len(colLabels) > 0 {
		head := make([]string, nc)
		for j := range head {
			if j < len(colLabels) {
				head[j] = padCells(colLabels[j], cw)
			} else {
				head[j] = strings.Repeat(" ", cw)
			}
		}
		out = append(out, strings.TrimRight(prefix+strings.Join(head, " "), " "))
	}
	for i, row := range grid {
		line := ""
		if lw > 0 {
			label := ""
			if i < len(rowLabels) {
				label = rowLabels[i]
			}
			line = padCells(label, lw) + " "
		}
		cellsOut := make([]string, nc)
		for j := range cellsOut {
			l := 0
			if j < len(row) && row[j] > 0 {
				if l = scale(row[j], 0, max, len(heatShades)-1); l < 1 {
					l = 1
				}
			}
			cellsOut[j] = strings.Repeat(string(heatShades[l]), cw)
		}
		out = append(out, strings.TrimRight(line+strings.Join(cellsOut, " "), " "))
	}
	return out
}
