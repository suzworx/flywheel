package flywheel

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/suzworx/flywheel/internal/term"
)

// TUIData is everything one frame shows; the caller fetches it.
type TUIData struct {
	Floor  Floor
	Events []string // recent events as readable lines, oldest first
	Detail []string // the drill-down's lines when the model Wants one, else nil
}

// TUI is the interactive factory's state (issue #336): which view is shown,
// the cursor, the filter, the command/filter prompt and the drill-down.
type TUI struct {
	view       string // units, workers, andon, events
	cursor     int
	top        int // first visible row
	page       int // visible page size
	filter     string
	prompt     string // command/filter input text
	promptKind string // "", "command", "filter"
	drillKind  string // "", "explain", "log"
	drillTask  string
	detailTop  int // first visible line in drill-down
	statusMsg  string
	quit       bool
	help       bool
}

// NewTUI creates a new TUI with default state.
func NewTUI() *TUI {
	return &TUI{
		view:   "units",
		cursor: 0,
		page:   10,
	}
}

// cutRunes returns at most n runes; when s is longer, returns the first n-1
// runes plus "~". It counts runes, not terminal cells: the standard library
// has no East Asian width table, and the floor's cells (task ids, models,
// states, paths) are ASCII in practice.
func cutRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "~"
}

// viewNames are the views' titles in the title bar.
var viewNames = map[string]string{
	"units":   "Units",
	"workers": "Workers",
	"andon":   "Andon",
	"events":  "Events",
}

// Update applies one key press to the model; d is the data the current frame
// shows, so moves are clamped to its rows.
func (m *TUI) Update(k term.Key, d TUIData) {
	// Alt keys are ignored everywhere.
	if k.Alt {
		return
	}

	// Drill-down mode (explain/log view).
	if m.drillKind != "" {
		switch k.Kind {
		case term.KeyEsc:
			m.drillKind = ""
			m.drillTask = ""
			m.statusMsg = ""
		case term.KeyDown:
			m.detailTop++
			m.statusMsg = ""
		case term.KeyUp:
			m.detailTop--
			if m.detailTop < 0 {
				m.detailTop = 0
			}
			m.statusMsg = ""
		case term.KeyPgDn:
			m.detailTop += m.page
			m.statusMsg = ""
		case term.KeyPgUp:
			m.detailTop -= m.page
			if m.detailTop < 0 {
				m.detailTop = 0
			}
			m.statusMsg = ""
		case term.KeyRune:
			if k.Rune == 'j' {
				m.detailTop++
				m.statusMsg = ""
			} else if k.Rune == 'k' {
				m.detailTop--
				if m.detailTop < 0 {
					m.detailTop = 0
				}
				m.statusMsg = ""
			} else if k.Rune == 'q' {
				m.quit = true
			} else if k.Rune == '?' {
				m.drillKind = ""
				m.drillTask = ""
				m.help = true
				m.detailTop = 0
				m.statusMsg = ""
			}
		case term.KeyCtrlC:
			m.quit = true
		}
		return
	}

	// Help mode: scrolls like a drill-down, so a short screen still reaches
	// every line (#344 review).
	if m.help {
		switch k.Kind {
		case term.KeyEsc:
			m.help = false
			m.statusMsg = ""
		case term.KeyDown:
			m.detailTop++
		case term.KeyUp:
			m.detailTop = max(0, m.detailTop-1)
		case term.KeyPgDn:
			m.detailTop += m.page
		case term.KeyPgUp:
			m.detailTop = max(0, m.detailTop-m.page)
		case term.KeyRune:
			switch k.Rune {
			case '?':
				m.help = false
				m.statusMsg = ""
			case 'q':
				m.quit = true
			case 'j':
				m.detailTop++
			case 'k':
				m.detailTop = max(0, m.detailTop-1)
			}
		case term.KeyCtrlC:
			m.quit = true
		}
		return
	}

	// Prompt mode (command or filter).
	if m.promptKind != "" {
		switch k.Kind {
		case term.KeyBackspace:
			if r := []rune(m.prompt); len(r) > 0 {
				m.prompt = string(r[:len(r)-1])
			}
		case term.KeyEsc:
			wasFilter := m.promptKind == "filter"
			m.prompt = ""
			m.promptKind = ""
			if wasFilter {
				m.filter = ""
			}
			m.statusMsg = ""
		case term.KeyEnter:
			if m.promptKind == "command" {
				m.executeCommand()
			} else if m.promptKind == "filter" {
				m.promptKind = ""
				m.filter = m.prompt
				m.cursor = 0
				m.statusMsg = ""
			}
		case term.KeyRune:
			m.prompt += string(k.Rune)
		case term.KeyCtrlC:
			m.quit = true
		default:
			// Ignore other keys in prompt mode.
		}
		return
	}

	// Table mode.
	_, rows := m.Rows(d)
	n := len(rows)
	defer m.clampCursor(n)

	switch k.Kind {
	case term.KeyEsc:
		m.filter = ""
		m.cursor = 0
		m.statusMsg = ""
	case term.KeyDown:
		m.cursor++
		if m.cursor >= n {
			m.cursor = n - 1
		}
		m.statusMsg = ""
	case term.KeyUp:
		m.cursor--
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.statusMsg = ""
	case term.KeyRune:
		switch k.Rune {
		case 'j':
			m.cursor++
			if m.cursor >= n {
				m.cursor = n - 1
			}
			m.statusMsg = ""
		case 'k':
			m.cursor--
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.statusMsg = ""
		case 'g':
			m.cursor = 0
			m.statusMsg = ""
		case 'G':
			m.cursor = n - 1
			if m.cursor < 0 {
				m.cursor = 0
			}
			m.statusMsg = ""
		case ':':
			m.promptKind = "command"
			m.prompt = ""
			m.statusMsg = ""
		case '/':
			m.promptKind = "filter"
			m.prompt = ""
			m.statusMsg = ""
		case '?':
			m.help = true
			m.detailTop = 0
			m.statusMsg = ""
		case 'q':
			m.quit = true
		case 'l':
			if m.view == "units" || m.view == "andon" {
				if m.cursor < len(rows) && len(rows) > 0 {
					m.drillKind = "log"
					m.drillTask = m.getTaskAtCursor(d)
					m.detailTop = 0
					m.statusMsg = ""
				}
			}
		}
	case term.KeyPgDn:
		m.cursor += m.page
		if m.cursor >= n {
			m.cursor = n - 1
		}
		m.statusMsg = ""
	case term.KeyPgUp:
		m.cursor -= m.page
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.statusMsg = ""
	case term.KeyHome:
		m.cursor = 0
		m.statusMsg = ""
	case term.KeyEnd:
		m.cursor = n - 1
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.statusMsg = ""
	case term.KeyEnter:
		if (m.view == "units" || m.view == "andon") && m.cursor < len(rows) && len(rows) > 0 {
			m.drillKind = "explain"
			m.drillTask = m.getTaskAtCursor(d)
			m.detailTop = 0
			m.statusMsg = ""
		}
	case term.KeyCtrlC:
		m.quit = true
	}
}

// clampCursor keeps the cursor on one of n rows: 0 when there are none, so
// a move on an empty table never leaves it negative (#344 review).
func (m *TUI) clampCursor(n int) {
	m.cursor = max(0, min(m.cursor, n-1))
}

// executeCommand processes the command entered by the user.
func (m *TUI) executeCommand() {
	cmd := strings.TrimSpace(m.prompt)
	m.prompt = ""
	m.promptKind = ""

	switch cmd {
	case "u", "units":
		m.view = "units"
		m.cursor = 0
		m.filter = ""
		m.statusMsg = ""
	case "w", "workers":
		m.view = "workers"
		m.cursor = 0
		m.filter = ""
		m.statusMsg = ""
	case "a", "andon":
		m.view = "andon"
		m.cursor = 0
		m.filter = ""
		m.statusMsg = ""
	case "e", "events":
		m.view = "events"
		m.cursor = 0
		m.filter = ""
		m.statusMsg = ""
	case "q", "quit":
		m.quit = true
	default:
		m.statusMsg = fmt.Sprintf("unknown view: %s", cmd)
	}
}

// getTaskAtCursor returns the task ID at the current cursor position.
func (m *TUI) getTaskAtCursor(d TUIData) string {
	_, rows := m.Rows(d)
	if m.cursor < 0 || m.cursor >= len(rows) {
		return ""
	}

	// The first cell of each row is the task.
	if len(rows[m.cursor]) > 0 {
		return rows[m.cursor][0]
	}
	return ""
}

// matchesFilter reports whether a row matches the current filter or prompt filter.
func (m *TUI) matchesFilter(row []string) bool {
	filterText := m.filter
	if m.promptKind == "filter" {
		filterText = m.prompt
	}
	if filterText == "" {
		return true
	}
	joined := strings.ToLower(strings.Join(row, " "))
	return strings.Contains(joined, strings.ToLower(filterText))
}

// Rows returns the current view's rows after the filter, each a slice of
// cells, and the header cells.
func (m *TUI) Rows(d TUIData) (header []string, rows [][]string) {
	switch m.view {
	case "units":
		return m.rowsUnits(d)
	case "workers":
		return m.rowsWorkers(d)
	case "andon":
		return m.rowsAndon(d)
	case "events":
		return m.rowsEvents(d)
	}
	return []string{}, [][]string{}
}

// rowsUnits returns the header and filtered rows for the units view.
func (m *TUI) rowsUnits(d TUIData) (header []string, rows [][]string) {
	header = []string{"TASK", "STAGE", "ATT", "MODEL", "STEPS", "AGE", "STATE"}
	for _, u := range d.Floor.Units {
		row := []string{
			u.Task,
			u.Stage,
			u.Attempt,
			u.Model,
			fmt.Sprintf("%d", u.Steps),
			HumanAge(u.LastAge),
			u.RunState,
		}
		if m.matchesFilter(row) {
			rows = append(rows, row)
		}
	}
	return header, rows
}

// rowsWorkers returns the header and filtered rows for the workers view.
func (m *TUI) rowsWorkers(d TUIData) (header []string, rows [][]string) {
	header = []string{"NAME", "ADAPTER", "MODEL", "MAX", "BUSY"}
	for _, l := range d.Floor.Lines {
		row := []string{
			l.Name,
			l.Adapter,
			l.Model,
			fmt.Sprintf("%d", l.MaxParallel),
			fmt.Sprintf("%d", l.Busy),
		}
		if m.matchesFilter(row) {
			rows = append(rows, row)
		}
	}
	return header, rows
}

// rowsAndon returns the header and filtered rows for the andon view.
func (m *TUI) rowsAndon(d TUIData) (header []string, rows [][]string) {
	header = []string{"TASK", "AGE", "STATE"} // STATE last, as in units: it is the coloured cell
	for _, a := range d.Floor.Andon {
		row := []string{
			a.Task,
			HumanAge(a.Age),
			a.State,
		}
		if m.matchesFilter(row) {
			rows = append(rows, row)
		}
	}
	return header, rows
}

// rowsEvents returns the header and filtered rows for the events view.
func (m *TUI) rowsEvents(d TUIData) (header []string, rows [][]string) {
	header = []string{"EVENT"}
	// Newest first.
	for i := len(d.Events) - 1; i >= 0; i-- {
		row := []string{d.Events[i]}
		if m.matchesFilter(row) {
			rows = append(rows, row)
		}
	}
	return header, rows
}

// View renders one full frame, exactly height lines, each at most width
// runes wide; color adds ANSI colours and reverse video for the cursor row.
// A frame too short for the layout keeps its first lines and the last one
// (the prompt, status or breadcrumbs); a non-positive size renders "".
func (m *TUI) View(d TUIData, width, height int, color bool) string {
	if width < 1 || height < 1 {
		return ""
	}

	lines := []string{}

	// Line 1: Header line
	repoDir := filepath.Base(d.Floor.Dir)
	totalBusy := 0
	totalMax := 0
	for _, l := range d.Floor.Lines {
		totalBusy += l.Busy
		totalMax += l.MaxParallel
	}
	headerLine := fmt.Sprintf("flywheel factory · %s · lead %s · workers %d/%d · landed today %d · $%.2f",
		repoDir, d.Floor.Staffing.Lead, totalBusy, totalMax, d.Floor.Output.LandedToday, d.Floor.Output.Cost)
	lines = append(lines, cutRunes(headerLine, width))

	// Line 2: Hints line
	hintsLine := "<:>view  </>filter  <enter>explain  <l>log  <?>help  <q>quit"
	lines = append(lines, cutRunes(hintsLine, width))

	// Line 3: Title bar
	filterText := "all"
	if m.promptKind == "filter" {
		filterText = fmt.Sprintf("/%s", m.prompt)
	} else if m.filter != "" {
		filterText = fmt.Sprintf("/%s", m.filter)
	}
	header, rows := m.Rows(d)
	rowCount := len(rows)

	titleBar := ""
	if m.drillKind != "" {
		titleDisplay := "Explain"
		if m.drillKind == "log" {
			titleDisplay = "Log"
		}
		titleBar = fmt.Sprintf("── %s %s ──", titleDisplay, m.drillTask)
	} else if m.help {
		titleBar = "── Help ──"
	} else {
		viewName := viewNames[m.view]
		titleBar = fmt.Sprintf("── %s(%s)[%d] ──", viewName, filterText, rowCount)
	}
	// Pad with ─ to width (by rune count).
	titleRuneCount := len([]rune(titleBar))
	for titleRuneCount < width {
		titleBar += "─"
		titleRuneCount++
	}
	lines = append(lines, cutRunes(titleBar, width))

	// Help screen
	if m.help {
		helpText := []string{
			"Navigation:",
			"  j/Down  move cursor down",
			"  k/Up    move cursor up",
			"  g/Home  move to top",
			"  G/End   move to bottom",
			"  PgDn    page down",
			"  PgUp    page up",
			"Prompts:",
			"  :       open command prompt",
			"  /       open filter prompt",
			"  enter   explain (drill-down)",
			"  l       show log (drill-down)",
			"  ?       show this help",
			"  q       quit",
		}
		visible := max(1, height-4)
		m.page = visible
		m.detailTop = max(0, min(m.detailTop, len(helpText)-visible))
		for i := m.detailTop; i < len(helpText) && i < m.detailTop+visible; i++ {
			lines = append(lines, cutRunes(helpText[i], width))
		}
	} else if m.drillKind != "" {
		// Drill-down view shows detail lines with scrolling.
		visible := height - 4
		if visible < 1 {
			visible = 1
		}
		m.page = visible
		// Clamp detailTop to show the last visible line.
		maxDetailTop := len(d.Detail) - visible
		if maxDetailTop < 0 {
			maxDetailTop = 0
		}
		if m.detailTop > maxDetailTop {
			m.detailTop = maxDetailTop
		}
		// Draw Detail lines [detailTop, detailTop+visible).
		for i := m.detailTop; i < m.detailTop+visible && i < len(d.Detail); i++ {
			if len(lines) < height-1 {
				lines = append(lines, cutRunes(d.Detail[i], width))
			}
		}
	} else {
		// Table view.
		widths := m.computeColumnWidths(header, rows)

		// Clamp cursor to rows and keep visible.
		m.cursor = min(m.cursor, len(rows)-1)
		if m.cursor < 0 {
			m.cursor = 0
		}
		visible := height - 5
		if visible < 1 {
			visible = 1
		}
		m.page = visible
		if m.cursor < m.top {
			m.top = m.cursor
		}
		if m.cursor >= m.top+visible {
			m.top = m.cursor - visible + 1
		}
		if m.top < 0 {
			m.top = 0
		}

		// Print header row (with cursor prefix for alignment).
		headerRow := m.formatRowWithColumns(header, widths)
		headerRowLine := "  " + headerRow
		headerRowCut := cutRunes(headerRowLine, width)
		if len(lines) < height-1 {
			lines = append(lines, headerRowCut)
		}

		// Print data rows [top, top+visible).
		for i := m.top; i < m.top+visible && i < len(rows); i++ {
			if len(lines) >= height-1 {
				break
			}
			row := rows[i]
			cursor := "  "
			isCursor := i == m.cursor
			if isCursor {
				cursor = "> "
			}
			rowLineCut := cutRunes(cursor+m.formatRowWithColumns(row, widths), width)

			// Colour after cutting, so widths are measured on plain text: the
			// cursor row in reverse video, else the STATE cell (the last cell of
			// the units and andon views) when it survived the cut whole.
			switch state := row[len(row)-1]; {
			case !color:
			case isCursor:
				rowLineCut = "\x1b[7m" + rowLineCut + "\x1b[0m"
			case (m.view == "units" || m.view == "andon") && strings.HasSuffix(rowLineCut, state):
				rowLineCut = strings.TrimSuffix(rowLineCut, state) + paint(true, stateColor(state), state)
			}

			lines = append(lines, rowLineCut)
		}

	}
	// Pad the body so the last line sits on the frame's bottom row in every
	// mode (#344 review).
	for len(lines) < height-1 {
		lines = append(lines, "")
	}

	// Last line: status, prompt, or breadcrumbs.
	lastLine := ""
	if m.promptKind == "command" {
		lastLine = fmt.Sprintf(":%s", m.prompt)
	} else if m.promptKind == "filter" {
		lastLine = fmt.Sprintf("/%s", m.prompt)
	} else if m.statusMsg != "" {
		lastLine = m.statusMsg
	} else if m.drillKind != "" {
		lastLine = fmt.Sprintf("<%s> <%s> <%s>", m.view, m.drillTask, m.drillKind)
	} else {
		lastLine = fmt.Sprintf("<%s>", m.view)
	}
	// Exactly height lines: a short frame drops body lines, never the last.
	if len(lines) > height-1 {
		lines = lines[:height-1]
	}
	lines = append(lines, cutRunes(lastLine, width))

	return strings.Join(lines, "\n")
}

// computeColumnWidths computes the max rune width for each column across all rows and header.
func (m *TUI) computeColumnWidths(header []string, rows [][]string) []int {
	if len(header) == 0 {
		return []int{}
	}
	widths := make([]int, len(header))
	for i, h := range header {
		widths[i] = len([]rune(h))
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				rw := len([]rune(cell))
				if rw > widths[i] {
					widths[i] = rw
				}
			}
		}
	}
	return widths
}

// formatRowWithColumns pads every cell but the last to its column width
// (in runes) and separates the cells with two spaces.
func (m *TUI) formatRowWithColumns(row []string, widths []int) string {
	cells := make([]string, len(row))
	for i, cell := range row {
		cells[i] = cell
		if i < len(row)-1 && i < len(widths) {
			cells[i] += strings.Repeat(" ", max(0, widths[i]-utf8.RuneCountInString(cell)))
		}
	}
	return strings.Join(cells, "  ")
}

// Wants reports the drill-down the model shows — kind "explain" or "log"
// and the unit's task — so the caller can put its lines in TUIData.Detail;
// ok false in table mode.
func (m *TUI) Wants() (kind, task string, ok bool) {
	if m.drillKind != "" && m.drillTask != "" {
		return m.drillKind, m.drillTask, true
	}
	return "", "", false
}

// Quit reports whether the user asked to quit.
func (m *TUI) Quit() bool {
	return m.quit
}
