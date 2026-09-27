package flywheel

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/suzworx/flywheel/internal/term"
)

// TUIData is everything one frame shows; the caller fetches it.
type TUIData struct {
	Floor  Floor
	Events []string // recent events as readable lines, oldest first
	Detail []string // the drill-down's lines when the model Wants one, else nil

	// The header's context (issue #583).
	Branch   string       // the integration branch, "" when unknown
	Suspend  SuspendState // the factory's freeze
	Paused   []TUIPause   // models a rate limit pauses
	HealthAt time.Time    // the latest health event's time; zero when none
	Version  string       // the flywheel version

	// The navigation views (issue #583 k2); the rows come from tui_views.go.
	Needs        map[string][]string // each unit's needs
	Health       []TUIHealth         // the recent health events, oldest first
	Learnings    []LearningView      // in log order
	Checkpoints  []TUICheckpoint     // filled while the checkpoints view is shown
	Search       []SearchHit         // the search view's results
	SearchCapped bool                // the search stopped at searchLimit results
}

// TUIHealth is one health event: when, and its snapshot.
type TUIHealth struct {
	TS   string
	Snap HealthSnapshot
}

// TUICheckpoint is one checkpoint and its age in seconds (-1 when the
// ledger does not say when it was taken).
type TUICheckpoint struct {
	Checkpoint
	Age int
}

// TUIPause is one model a rate limit pauses, and until when.
type TUIPause struct {
	Model string
	Until time.Time
}

// flashFor is how long a flash message stays up when no key clears it.
const flashFor = 5 * time.Second

// TUI is the interactive factory's state (issue #336): which view is shown,
// the cursor, the filter, the command/filter prompt and the drill-down; and
// (issue #583) the navigation path, the flash message and the layout toggles.
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
	quit       bool
	help       bool

	crumbs     []string // the navigation path: the view, then drill-downs
	flashMsg   string
	flashUntil time.Time
	hideHeader bool // Ctrl-E
	hideCrumbs bool // Ctrl-G
	wide       bool // Ctrl-W: cells untruncated
	refresh    bool // Ctrl-R: a reload the live loop consumes (TakeRefresh)
	now        func() time.Time

	// Navigation (issue #583 k2): the views below the current one, each
	// view's last level (restored when you come back), the `:` commands
	// entered and where `[` / `]` stand in them.
	stack  []navLevel
	saved  map[string]navLevel
	cmds   []string
	cmdPos int

	sort      tuiSort
	matchText string // the filter text matchFn was built for
	matchFn   func(string) bool
	search    string // the `:s` query the search view shows
	drillFind string // the drill-down scrolls to the first line holding it
}

// navLevel is one level of the navigation stack: a view with its filter,
// sort and cursor.
type navLevel struct {
	view, filter string
	sort         tuiSort
	cursor       int
}

// tuiSort is a table's sort: the key (name, age, stage, cost; "" none) and
// its direction.
type tuiSort struct {
	key  string
	desc bool
}

// maxStack bounds the navigation stack; the oldest level falls off.
const maxStack = 20

// NewTUI creates a new TUI with default state.
func NewTUI() *TUI {
	return &TUI{
		view:   "units",
		cursor: 0,
		page:   10,
		crumbs: []string{"units"},
		now:    time.Now,
		saved:  map[string]navLevel{},
	}
}

// level is the current view's navLevel; restore makes l the current one.
func (m *TUI) level() navLevel {
	return navLevel{view: m.view, filter: m.filter, sort: m.sort, cursor: m.cursor}
}

func (m *TUI) restore(l navLevel) {
	m.view, m.filter, m.sort, m.cursor = l.view, l.filter, l.sort, l.cursor
	m.top = 0
	m.resetCrumbs()
}

// resetCrumbs rebuilds the path from the stack: the views below, then the
// current one.
func (m *TUI) resetCrumbs() {
	m.crumbs = m.crumbs[:0]
	for _, l := range m.stack {
		m.crumbs = append(m.crumbs, l.view)
	}
	m.crumbs = append(m.crumbs, m.view)
}

// popLevel goes back one level (Esc); false when the stack is empty.
func (m *TUI) popLevel() bool {
	if len(m.stack) == 0 {
		return false
	}
	m.saved[m.view] = m.level()
	l := m.stack[len(m.stack)-1]
	m.stack = m.stack[:len(m.stack)-1]
	m.restore(l)
	return true
}

// swapLevel swaps the current view with the previous one (`-`).
func (m *TUI) swapLevel() {
	if len(m.stack) == 0 {
		m.flash("no previous view")
		return
	}
	cur := m.level()
	m.saved[m.view] = cur
	l := m.stack[len(m.stack)-1]
	m.stack[len(m.stack)-1] = cur
	m.restore(l)
}

// flash shows msg on the flash line until the next key or flashFor passes.
func (m *TUI) flash(msg string) {
	m.flashMsg = msg
	m.flashUntil = m.now().Add(flashFor)
}

// flashLine is the flash message still up, "" when none.
func (m *TUI) flashLine() string {
	if m.flashMsg == "" || !m.now().Before(m.flashUntil) {
		return ""
	}
	return m.flashMsg
}

// Crumbs returns the navigation path, e.g. [units T3 log].
func (m *TUI) Crumbs() []string {
	return append([]string(nil), m.crumbs...)
}

// pushCrumbs extends the path; popCrumbs cuts it back to its first n crumbs.
func (m *TUI) pushCrumbs(c ...string) { m.crumbs = append(m.crumbs, c...) }
func (m *TUI) popCrumbs(n int)        { m.crumbs = m.crumbs[:min(n, len(m.crumbs))] }

// setView pushes the current view on the stack and switches to view, with
// the cursor, filter and sort it had when you left it (fresh the first
// time). Switching to the view shown changes nothing, except that the
// search view always starts at its first result.
func (m *TUI) setView(view string) {
	if view == m.view {
		if view == "search" {
			m.cursor, m.top = 0, 0
		}
		return
	}
	m.saved[m.view] = m.level()
	m.stack = append(m.stack, m.level())
	if len(m.stack) > maxStack {
		m.stack = m.stack[1:]
	}
	l, ok := m.saved[view]
	if !ok || view == "search" {
		l = navLevel{view: view}
	}
	m.restore(l)
}

// TakeRefresh reports whether Ctrl-R asked for a reload, and clears it.
func (m *TUI) TakeRefresh() bool {
	r := m.refresh
	m.refresh = false
	return r
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
	"units":       "Units",
	"workers":     "Workers",
	"andon":       "Andon",
	"events":      "Events",
	"lines":       "Lines",
	"tree":        "Tree",
	"health":      "Health",
	"learnings":   "Learnings",
	"checkpoints": "Checkpoints",
}

// drillTitles are the drill-downs' titles in the title bar.
var drillTitles = map[string]string{
	"explain":    "Explain",
	"log":        "Log",
	"learning":   "Learning",
	"checkpoint": "Checkpoint",
	"views":      "Views",
}

// Update applies one key press to the model; d is the data the current frame
// shows, so moves are clamped to its rows.
func (m *TUI) Update(k term.Key, d TUIData) {
	// Alt keys are ignored everywhere.
	if k.Alt {
		return
	}
	// Any key clears the flash; the key may set a new one.
	m.flashMsg = ""

	// The layout toggles work in every mode (issue #583).
	if k.Kind == term.KeyCtrl {
		switch k.Rune {
		case 'e':
			m.hideHeader = !m.hideHeader
		case 'g':
			m.hideCrumbs = !m.hideCrumbs
		case 'w':
			m.wide = !m.wide
			if m.wide {
				m.flash("wide: every cell untruncated")
			} else {
				m.flash("normal: long cells cut")
			}
		case 'r':
			m.refresh = true
			m.flash("reloading")
		case 'a':
			// Every view and alias, in the drill-down pane.
			if m.promptKind == "" {
				m.help = false
				m.drillKind, m.drillTask, m.detailTop = "views", "", 0
				m.resetCrumbs()
				m.pushCrumbs("views")
			}
		}
		return
	}

	// Drill-down mode (explain/log view).
	if m.drillKind != "" {
		switch k.Kind {
		case term.KeyEsc:
			m.drillKind = ""
			m.drillTask = ""
			m.drillFind = ""
			m.resetCrumbs()
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
			case 'j':
				m.detailTop++
			case 'k':
				m.detailTop = max(0, m.detailTop-1)
			case 'q':
				m.quit = true
			case '?':
				m.drillKind = ""
				m.drillTask = ""
				m.resetCrumbs()
				m.openHelp()
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
			m.closeHelp()
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
				m.closeHelp()
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
		case term.KeyEnter:
			if m.promptKind == "command" {
				m.executeCommand()
			} else if m.promptKind == "filter" {
				m.promptKind = ""
				m.filter = m.prompt
				m.cursor = 0
				if _, literal := filterMatcher(m.filter); literal {
					m.flash("literal match: /" + m.filter + " is not a valid regex")
				}
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
	// clampCursor keeps every move below on the rows of the view shown
	// after the key, which may be another view.
	defer func() {
		_, after := m.Rows(d)
		m.clampCursor(len(after))
	}()

	switch k.Kind {
	case term.KeyEsc:
		// A filtered view clears its filter first; then Esc goes back a level.
		if m.filter != "" {
			m.filter = ""
			m.cursor = 0
		} else {
			m.popLevel()
		}
	case term.KeyDown:
		m.cursor++
	case term.KeyUp:
		m.cursor--
	case term.KeyRune:
		switch k.Rune {
		case 'j':
			m.cursor++
		case 'k':
			m.cursor--
		case 'g':
			m.cursor = 0
		case 'G':
			m.cursor = n - 1
		case ':':
			m.promptKind = "command"
			m.prompt = ""
		case '/':
			m.promptKind = "filter"
			m.prompt = ""
		case '?':
			m.openHelp()
		case 'q':
			m.quit = true
		case 'l':
			if m.view == "units" || m.view == "andon" || m.view == "tree" {
				if m.cursor < len(rows) && len(rows) > 0 {
					m.drill(d, "log")
				}
			}
		case '-':
			m.swapLevel()
		case '[':
			m.historyStep(-1)
		case ']':
			m.historyStep(1)
		case 'N', 'A', 'S', 'C':
			m.sortBy(sortKeys[k.Rune], d)
		}
	case term.KeyPgDn:
		m.cursor += m.page
	case term.KeyPgUp:
		m.cursor = max(0, m.cursor-m.page)
	case term.KeyHome:
		m.cursor = 0
	case term.KeyEnd:
		m.cursor = n - 1
	case term.KeyEnter:
		if m.cursor < len(rows) && len(rows) > 0 {
			m.enter(d, rows[m.cursor])
		}
	case term.KeyCtrlC:
		m.quit = true
	}
}

// openHelp shows the help screen and adds it to the path; closeHelp leaves
// it and drops it from the path.
func (m *TUI) openHelp() {
	m.help = true
	m.detailTop = 0
	m.pushCrumbs("help")
}

func (m *TUI) closeHelp() {
	m.help = false
	m.popCrumbs(len(m.crumbs) - 1)
}

// drill opens a drill-down of the row under the cursor, unless that row is
// not a unit: a condition of the floor itself (staffing/<role>) has no task
// to explain, so it says so instead of asking for one (#357 review).
func (m *TUI) drill(d TUIData, kind string) {
	m.drillUnit(d, m.getTaskAtCursor(d), kind)
}

// enter is Enter on row: a unit's explanation (units, andon, tree), a
// learning in full, a checkpoint's changed paths, or a search result's unit
// at the match (its log for an event, else its explanation).
func (m *TUI) enter(d TUIData, row []string) {
	switch m.view {
	case "units", "andon", "tree":
		m.drill(d, "explain")
	case "learnings":
		m.drillLocal("learning", row[0])
	case "checkpoints":
		m.drillLocal("checkpoint", row[0]+"/"+row[1])
	case "search":
		kind := "explain"
		if row[0] == "event" {
			kind = "log"
		}
		m.drillUnit(d, row[1], kind)
		if m.drillKind != "" {
			m.drillFind = m.search
		}
	}
}

// drillLocal opens a drill-down whose lines the model builds from TUIData
// itself (detailLines): a learning, a checkpoint.
func (m *TUI) drillLocal(kind, id string) {
	m.drillKind, m.drillTask, m.detailTop = kind, id, 0
	m.pushCrumbs(id, kind)
}

// sortKeys are the sort keys Shift-N/A/S/C pick.
var sortKeys = map[rune]string{'N': "name", 'A': "age", 'S': "stage", 'C': "cost"}

// sortBy sorts the table by key; the same key again flips the direction.
func (m *TUI) sortBy(key string, d TUIData) {
	if m.view == "tree" {
		m.flash("the tree keeps its order")
		return
	}
	header, _ := m.Rows(d)
	if sortColumn(header, key) < 0 {
		m.flash("no " + key + " column in this view")
		return
	}
	if m.sort.key == key {
		m.sort.desc = !m.sort.desc
	} else {
		m.sort = tuiSort{key: key}
	}
	m.cursor = 0
}

// drillUnit opens a drill-down of task, unless it is not a unit.
func (m *TUI) drillUnit(d TUIData, task, kind string) {
	if !unitExists(d, task) {
		m.flash(task + " is a condition of the floor, not a unit: nothing to " + kind)
		return
	}
	m.drillKind = kind
	m.drillTask = task
	m.detailTop = 0
	m.pushCrumbs(task, kind)
}

// unitExists reports whether task names one of the floor's units.
func unitExists(d TUIData, task string) bool {
	for _, u := range d.Floor.Units {
		if u.Task == task {
			return true
		}
	}
	return false
}

// clampCursor keeps the cursor on one of n rows: 0 when there are none, so
// a move on an empty table never leaves it negative (#344 review).
func (m *TUI) clampCursor(n int) {
	m.cursor = max(0, min(m.cursor, n-1))
}

// tuiViews are the views `:` opens, with their aliases, in the order Ctrl-A
// lists them.
var tuiViews = []struct{ name, alias, what string }{
	{"units", "u", "every unit: stage, attempt, session, model, age, state"},
	{"workers", "w", "the worker lines and the staffed roles"},
	{"andon", "a", "the units that stopped the line"},
	{"events", "e", "the recent events, newest first"},
	{"lines", "l", "the product lines"},
	{"tree", "t", "the needs tree of every unit not landed"},
	{"health", "h", "the recent health events"},
	{"learnings", "lr", "the learnings, newest first"},
	{"checkpoints", "c", "the saved checkpoints of interrupted attempts"},
	{"search", "s", "search <text>: the ledger, the run logs, the reports and the briefs"},
}

// executeCommand runs the command in the prompt and records it in the
// command history.
func (m *TUI) executeCommand() {
	cmd := strings.TrimSpace(m.prompt)
	m.prompt = ""
	m.promptKind = ""
	if m.runCommand(cmd) {
		m.cmds = append(m.cmds, cmd)
		m.cmdPos = len(m.cmds) - 1
	}
}

// historyStep runs the command by steps from the current one in the command
// history (`[` is -1, `]` is +1), without recording it again.
func (m *TUI) historyStep(by int) {
	i := m.cmdPos + by
	if i < 0 || i >= len(m.cmds) {
		m.flash("no more commands in the history")
		return
	}
	m.cmdPos = i
	m.runCommand(m.cmds[i])
}

// runCommand runs one `:` command; false when it named no view.
func (m *TUI) runCommand(cmd string) bool {
	name, arg, _ := strings.Cut(cmd, " ")
	switch name {
	case "q", "quit":
		m.quit = true
		return true
	case "s", "search":
		if arg = strings.TrimSpace(arg); arg == "" {
			m.flash("search what? :s <text>")
			return false
		}
		m.search = arg
		m.setView("search")
		return true
	}
	for _, v := range tuiViews {
		if (name == v.name || name == v.alias) && v.name != "search" && arg == "" {
			m.setView(v.name)
			return true
		}
	}
	m.flash(fmt.Sprintf("unknown view :%s (Ctrl-A lists them)", cmd))
	return false
}

// SearchQuery is the text the search view searches for, "" when it is not
// shown: the live fetch runs the search only then.
func (m *TUI) SearchQuery() string {
	if m.view != "search" {
		return ""
	}
	return m.search
}

// ViewName is the table view shown, so the live fetch reads only what it
// needs.
func (m *TUI) ViewName() string { return m.view }

// getTaskAtCursor returns the task ID at the current cursor position.
func (m *TUI) getTaskAtCursor(d TUIData) string {
	_, rows := m.Rows(d)
	if m.cursor < 0 || m.cursor >= len(rows) {
		return ""
	}

	// The first cell of each row is the task; the tree draws it after its
	// branch.
	if len(rows[m.cursor]) > 0 {
		return strings.TrimLeft(rows[m.cursor][0], treeChars)
	}
	return ""
}

// matchesFilter reports whether a row matches the current filter or prompt
// filter (filterMatcher: a regex, `!` inverse, `-f` fuzzy).
func (m *TUI) matchesFilter(row []string) bool {
	filterText := m.filter
	if m.promptKind == "filter" {
		filterText = m.prompt
	}
	if filterText == "" {
		return true
	}
	if m.matchFn == nil || filterText != m.matchText {
		m.matchFn, _ = filterMatcher(filterText)
		m.matchText = filterText
	}
	return m.matchFn(strings.Join(row, " "))
}

// Rows returns the current view's rows after the filter and the sort, each
// a slice of cells, and the header cells.
func (m *TUI) Rows(d TUIData) (header []string, rows [][]string) {
	switch m.view {
	case "units":
		header, rows = m.rowsUnits(d)
	case "workers":
		header, rows = m.rowsWorkers(d)
	case "andon":
		header, rows = m.rowsAndon(d)
	case "events":
		header, rows = m.rowsEvents(d)
	case "lines":
		header, rows = m.rowsLines(d)
	case "tree":
		header, rows = treeRows(d)
	case "health":
		header, rows = healthRows(d)
	case "learnings":
		header, rows = learningRows(d)
	case "checkpoints":
		header, rows = checkpointRows(d)
	case "search":
		header, rows = searchRows(d.Search)
	default:
		return []string{}, [][]string{}
	}
	// The views built in tui_views.go are pure: the filter applies here.
	switch m.view {
	case "tree", "health", "learnings", "checkpoints", "search":
		var kept [][]string
		for _, row := range rows {
			if m.matchesFilter(row) {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	return header, sortRows(header, rows, m.sort)
}

// rowsUnits returns the header and filtered rows for the units view.
func (m *TUI) rowsUnits(d TUIData) (header []string, rows [][]string) {
	hasLine := false
	for _, u := range d.Floor.Units {
		if u.Line != "" {
			hasLine = true
			break
		}
	}
	if hasLine {
		header = []string{"TASK", "LINE", "STAGE", "ATT", "SESSION", "MODEL", "STEPS", "AGE", "STATE"}
	} else {
		header = []string{"TASK", "STAGE", "ATT", "SESSION", "MODEL", "STEPS", "AGE", "STATE"}
	}
	for _, u := range d.Floor.Units {
		var row []string
		if hasLine {
			row = []string{
				u.Task,
				u.Line,
				u.Stage,
				u.Attempt,
				u.Session,
				u.Model,
				fmt.Sprintf("%d", u.Steps),
				HumanAge(u.LastAge),
				u.RunState,
			}
		} else {
			row = []string{
				u.Task,
				u.Stage,
				u.Attempt,
				u.Session,
				u.Model,
				fmt.Sprintf("%d", u.Steps),
				HumanAge(u.LastAge),
				u.RunState,
			}
		}
		// Capped unit with Peak > 0 shows peak reasoning in STATE.
		if u.RunState == "capped" && u.Peak > 0 {
			row[len(row)-1] = "capped " + tokensK(Tokens{Reasoning: u.Peak})
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
	for _, r := range d.Floor.Staffing.Roles {
		// Straight from the configured fields, never split out of the
		// display line (#357 review).
		adapter := r.ConfAdapter
		if adapter == "" {
			adapter = "-"
		}
		busy := r.Session
		if busy == "" {
			busy = "not registered"
		}
		if r.Mismatch {
			busy += " !"
		}
		row := []string{r.Name, adapter, r.ConfModel, "-", busy}
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

// rowsLines returns the header and filtered rows for the lines view.
func (m *TUI) rowsLines(d TUIData) (header []string, rows [][]string) {
	header = []string{"NAME", "WORKER", "UNITS", "BUILDING", "LANDED", "OWNS"}
	for _, pl := range d.Floor.ProductLines {
		owns := strings.Join(pl.Owns, ", ")
		row := []string{
			pl.Name,
			pl.Worker,
			fmt.Sprintf("%d", pl.Units),
			fmt.Sprintf("%d", pl.Building),
			fmt.Sprintf("%d", pl.Landed),
			owns,
		}
		if m.matchesFilter(row) {
			rows = append(rows, row)
		}
	}
	return header, rows
}

// hint is one entry of the header's key menu.
type hint struct{ key, label string }

// hintRows is how many rows the key menu fills before it starts a column.
const hintRows = 4

// Key menus shared by the views: tableHints lead every table view,
// layoutHints close every menu.
var (
	tableHints  = []hint{{":", "view"}, {"/", "filter"}, {"esc", "back"}, {"-", "last view"}, {"[ ]", "history"}, {"ctrl-a", "views"}}
	sortHints   = []hint{{"N/A/S/C", "sort"}}
	layoutHints = []hint{{"?", "help"}, {"q", "quit"}, {"ctrl-e", "header"}, {"ctrl-g", "crumbs"}, {"ctrl-w", "wide"}, {"ctrl-r", "reload"}}
	scrollHints = []hint{{"j/k", "scroll"}, {"esc", "back"}}
	unitHints   = []hint{{"enter", "explain"}, {"l", "log"}}
)

// viewHints is the key menu of each view (a table view, a drill-down kind or
// help): the keys valid there, in the order the menu lists them.
var viewHints = map[string][]hint{
	"units":       join(tableHints, unitHints, sortHints, layoutHints),
	"andon":       join(tableHints, unitHints, sortHints, layoutHints),
	"workers":     join(tableHints, sortHints, layoutHints),
	"events":      join(tableHints, sortHints, layoutHints),
	"lines":       join(tableHints, sortHints, layoutHints),
	"tree":        join(tableHints, unitHints, layoutHints),
	"health":      join(tableHints, sortHints, layoutHints),
	"learnings":   join(tableHints, []hint{{"enter", "learning"}}, sortHints, layoutHints),
	"checkpoints": join(tableHints, []hint{{"enter", "paths"}}, sortHints, layoutHints),
	"search":      join(tableHints, []hint{{"enter", "open at match"}}, sortHints, layoutHints),
	"explain":     join(scrollHints, layoutHints),
	"log":         join(scrollHints, layoutHints),
	"learning":    join(scrollHints, layoutHints),
	"checkpoint":  join(scrollHints, layoutHints),
	"views":       join(scrollHints, layoutHints),
	"help":        join(scrollHints, layoutHints),
}

func join(parts ...[]hint) []hint {
	var out []hint
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// hintsFor is the key menu of the screen shown: help, the drill-down, or the
// table view.
func (m *TUI) hintsFor() []hint {
	switch {
	case m.help:
		return viewHints["help"]
	case m.drillKind != "":
		return viewHints[m.drillKind]
	}
	return viewHints[m.view]
}

// contextLines are the header's left column: where the factory is, whether
// it runs, what pauses it, who leads it, its last health check and version.
func contextLines(d TUIData) []string {
	busy, most := 0, 0
	for _, l := range d.Floor.Lines {
		busy += l.Busy
		most += l.MaxParallel
	}
	repo := "repo " + filepath.Base(d.Floor.Dir)
	if d.Branch != "" {
		repo += " · " + d.Branch
	}
	factory := "factory running"
	if s := d.Suspend; s.Suspended {
		factory = "factory FROZEN since " + hhmm(s.Since)
		if s.Until != "" {
			factory += " until " + hhmm(s.Until)
		}
		factory += " by " + s.By + ": " + s.Reason
	}
	out := []string{repo, factory}
	for _, p := range d.Paused {
		out = append(out, "paused "+p.Model+" until "+p.Until.Local().Format("15:04"))
	}
	out = append(out, fmt.Sprintf("lead %s · workers %d/%d · landed today %d · $%.2f",
		d.Floor.Staffing.Lead, busy, most, d.Floor.Output.LandedToday, d.Floor.Output.Cost))
	health := "health none"
	if !d.HealthAt.IsZero() {
		health = "health " + HumanAge(max(0, int(d.Floor.Refreshed.Sub(d.HealthAt).Seconds()))) + " ago"
	}
	version := d.Version
	if version == "" {
		version = "dev"
	}
	return append(out, health, "flywheel "+version)
}

// headerBlock is the header: the context lines on the left, the key menu in
// columns of up to hintRows rows on the right, every line cut to width.
func (m *TUI) headerBlock(d TUIData, width int) []string {
	left := contextLines(d)
	leftW := 0
	for _, l := range left {
		leftW = max(leftW, utf8.RuneCountInString(l))
	}
	hints := m.hintsFor()
	// One column per hintRows hints, each as wide as its widest entry.
	var cols [][]string
	for i := 0; i < len(hints); i += hintRows {
		var col []string
		w := 0
		for _, h := range hints[i:min(i+hintRows, len(hints))] {
			col = append(col, "<"+h.key+"> "+h.label)
			w = max(w, utf8.RuneCountInString(col[len(col)-1]))
		}
		for j := range col {
			col[j] += strings.Repeat(" ", w-utf8.RuneCountInString(col[j]))
		}
		cols = append(cols, col)
	}
	out := make([]string, max(len(left), min(hintRows, len(hints))))
	for r := range out {
		line := ""
		if r < len(left) {
			line = left[r]
		}
		line += strings.Repeat(" ", leftW-utf8.RuneCountInString(line)+3)
		for _, col := range cols {
			if r < len(col) {
				line += col[r] + "  "
			}
		}
		out[r] = cutRunes(strings.TrimRight(line, " "), width)
	}
	return out
}

// View renders one full frame, exactly height lines, each at most width
// runes wide; color adds ANSI colours and reverse video for the cursor row.
// A frame too short for the layout keeps its first lines and the last one
// (the prompt, status or breadcrumbs); a non-positive size renders "".
func (m *TUI) View(d TUIData, width, height int, color bool) string {
	if width < 1 || height < 1 {
		return ""
	}

	// The bottom: the flash line, then the prompt or the crumbs on the last
	// line; a frame too short for both keeps the last.
	var foot []string
	switch {
	case m.promptKind == "command":
		foot = []string{m.flashLine(), ":" + m.prompt}
	case m.promptKind == "filter":
		foot = []string{m.flashLine(), "/" + m.prompt}
	case !m.hideCrumbs:
		foot = []string{m.flashLine(), m.crumbLine()}
	default:
		foot = []string{m.flashLine()}
	}
	for len(foot) > 1 && height-len(foot) < 1 {
		foot = foot[1:]
	}

	// The header, unless hidden or the frame is too short to keep a title,
	// a column header and minTableRows rows below it.
	const minTableRows = 5
	var lines []string
	if !m.hideHeader {
		if hdr := m.headerBlock(d, width); height-len(foot)-len(hdr) >= 2+minTableRows {
			lines = hdr
		}
	}
	// body is how many lines the title and the content below it get.
	body := height - len(foot) - len(lines)

	// The title bar.
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
		titleBar = strings.TrimRight(fmt.Sprintf("── %s %s", drillTitles[m.drillKind], m.drillTask), " ") + " ──"
	} else if m.help {
		titleBar = "── Help ──"
	} else {
		viewName := viewNames[m.view]
		count := fmt.Sprint(rowCount)
		if m.view == "search" {
			viewName = fmt.Sprintf("Search %q", m.search)
			if d.SearchCapped {
				count = fmt.Sprintf("%d+ (narrow the search)", searchLimit)
			}
		}
		sortText := ""
		if m.sort.key != "" {
			sortText = " ↑" + m.sort.key
			if m.sort.desc {
				sortText = " ↓" + m.sort.key
			}
		}
		titleBar = fmt.Sprintf("── %s(%s)[%s]%s ──", viewName, filterText, count, sortText)
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
			"Views (:name or :alias):",
			"  :       open command prompt: units (u), workers (w), andon (a),",
			"          events (e), lines (l), tree (t), health (h), learnings (lr),",
			"          checkpoints (c), quit (q)",
			"  :s txt  search the ledger, run logs, reports and briefs for txt",
			"  ctrl-a  list every view and alias",
			"History:",
			"  esc     back: leave the drill-down or help, clear the filter,",
			"          then go back to the previous view",
			"  -       swap to the previous view",
			"  [ / ]   step back / forward through the : commands entered",
			"Filter (/):",
			"  /re     case-insensitive regex (an invalid one matches literally)",
			"  /!re    inverse: the rows the regex does not match",
			"  /-f txt fuzzy: every character of txt, in order",
			"Sort (again flips the direction):",
			"  N A S C by name, age, stage/state, cost",
			"Drill-down:",
			"  enter   explain a unit, open a learning, a checkpoint's paths,",
			"          or a search result at its match",
			"  l       show log (drill-down)",
			"Layout:",
			"  ctrl-e  show or hide the header",
			"  ctrl-g  show or hide the crumbs",
			"  ctrl-w  wide: show every cell untruncated",
			"  ctrl-r  reload the factory now",
			"  ?       show this help",
			"  q       quit",
		}
		visible := max(1, body-1)
		m.page = visible
		m.detailTop = max(0, min(m.detailTop, len(helpText)-visible))
		for i := m.detailTop; i < len(helpText) && i < m.detailTop+visible; i++ {
			lines = append(lines, cutRunes(helpText[i], width))
		}
	} else if m.drillKind != "" {
		// Drill-down view shows detail lines with scrolling; detailTop is
		// clamped so the last page stays full.
		detail := d.Detail
		if _, ok := drillTitles[m.drillKind]; ok && m.drillKind != "explain" && m.drillKind != "log" {
			detail = localDetail(d, m.drillKind, m.drillTask)
		}
		// A search result's drill-down opens at its match, once its lines
		// have arrived.
		if m.drillFind != "" && len(detail) > 0 {
			for i, l := range detail {
				if runeIndex(lowerRunes(l), lowerRunes(m.drillFind)) >= 0 {
					m.detailTop = i
					break
				}
			}
			m.drillFind = ""
		}
		visible := max(1, body-1)
		m.page = visible
		m.detailTop = max(0, min(m.detailTop, len(detail)-visible))
		for i := m.detailTop; i < m.detailTop+visible && i < len(detail); i++ {
			lines = append(lines, cutRunes(detail[i], width))
		}
	} else {
		// Table view: long cells cut to maxCell unless wide (Ctrl-W).
		if !m.wide {
			rows = cutCells(rows)
		}
		widths := m.computeColumnWidths(header, rows)

		// Clamp cursor to rows and keep visible.
		m.cursor = max(0, min(m.cursor, len(rows)-1))
		visible := max(1, body-2)
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
		lines = append(lines, cutRunes(headerRowLine, width))

		// Print data rows [top, top+visible).
		for i := m.top; i < m.top+visible && i < len(rows); i++ {
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
				// The colour comes from the state word: "capped 50k" is capped.
				word, _, _ := strings.Cut(state, " ")
				rowLineCut = strings.TrimSuffix(rowLineCut, state) + paint(true, stateColor(word), state)
			}

			lines = append(lines, rowLineCut)
		}

	}
	// Exactly height lines: pad the body so the footer sits on the frame's
	// bottom rows in every mode (#344 review); a short frame drops body
	// lines, never the footer.
	for len(lines) < height-len(foot) {
		lines = append(lines, "")
	}
	lines = lines[:height-len(foot)]
	for _, f := range foot {
		lines = append(lines, cutRunes(f, width))
	}

	return strings.Join(lines, "\n")
}

// crumbLine is the navigation path as k9s draws it, each crumb a tag:
// "<units> <T3> <log>".
func (m *TUI) crumbLine() string {
	tags := make([]string, len(m.crumbs))
	for i, c := range m.crumbs {
		tags[i] = "<" + c + ">"
	}
	return strings.Join(tags, " ")
}

// maxCell is the widest a table cell other than a row's last is drawn
// outside wide mode.
const maxCell = 40

// cutCells returns rows with every cell but the last cut to maxCell runes.
func cutCells(rows [][]string) [][]string {
	out := make([][]string, len(rows))
	for i, row := range rows {
		out[i] = append([]string(nil), row...)
		for j := 0; j < len(row)-1; j++ {
			out[i][j] = cutRunes(row[j], maxCell)
		}
	}
	return out
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
