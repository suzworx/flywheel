package flywheel

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/suzworx/flywheel/internal/term"
)

// TUIData is everything one frame shows; the caller fetches it.
type TUIData struct {
	Floor  Floor
	Events []string          // recent events as readable lines, oldest first
	Detail []string          // the drill-down's lines when the model Wants one, else nil
	Why    map[string]string // each unit's UnitWhy (issue #583 k3)

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

	// The metrics (issue #583 k4) by window name (24h, 7d, 30d): 24h for the
	// header stats, and the window :pulse, :metrics and the drill-down show.
	Metrics map[string]TUIMetrics

	// Next is each unit's next action as recover decides it (issue #583
	// k7), filled while the andon view is shown.
	Next map[string]Next
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
	drillKind  string // "", a unit tab (unitTabs) or a local drill-down
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

	// The metrics views (issue #583 k4): the window shown (24h, 7d, 30d),
	// the pulse panel under the cursor, the metric drill-down's part (chart,
	// units) and evidence row, and the drill-down a unit opened from it
	// returns to on Esc.
	window     string
	panel      int
	pulseCols  int // the pulse grid's columns in the last frame
	metricPart string
	evCursor   int
	metricBack *metricReturn

	// The :metrics split (issue #583 k7): by model or worker ("" none), the
	// metric it splits and the row to come back to.
	split       string
	splitMetric string
	splitRow    int

	// The k9s feel (issue #583 k6): the skin, the rows each fetch changed
	// (Observe), fullscreen, and the log tab's wrap, timestamps and follow.
	skin     Skin
	seen     map[string]map[string]string // view → row key → the row's cells, as the last fetch had them
	marks    map[string]map[string]rowMark
	full     bool // f: the drill-down without the header and crumbs
	logWrap  bool // w in the log tab
	logNoTS  bool // t in the log tab: timestamps hidden
	follow   bool // the log tab tails its unit
	followed bool // follow was on and a scroll up paused it
}

// rowMark is a row a fetch changed: new, or modified; left is the refreshes
// it stays highlighted for, this one included.
type rowMark struct {
	kind string
	left int
}

// metricReturn is a metric drill-down to come back to: its metric, part,
// evidence row and crumbs.
type metricReturn struct {
	id, part string
	row      int
	crumbs   []string
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
		window: "24h",
		skin:   skins[DefaultSkin],
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
	"pulse":       "Pulse",
	"metrics":     "Metrics",
}

// drillTitles are the drill-downs' titles in the title bar.
var drillTitles = map[string]string{
	"metric":     "Metric",
	"why":        "Why",
	"explain":    "Explain",
	"brief":      "Brief",
	"log":        "Log",
	"unitcp":     "Checkpoints",
	"findings":   "Findings",
	"unitevents": "Events",
	"learning":   "Learning",
	"checkpoint": "Checkpoint",
	"views":      "Views",
}

// unitTabs are the unit detail's tabs (issue #583 k3), each switched by its
// key; the live fetch fills each one's lines.
var unitTabs = []struct {
	key        rune
	kind, what string
}{
	{'w', "why", "why"}, {'d', "explain", "explain"}, {'y', "brief", "brief"}, {'l', "log", "log"},
	{'c', "unitcp", "checkpoints"}, {'F', "findings", "findings"}, {'e', "unitevents", "events"},
}

// unitTab is the tab kind key switches to, ok false when key is no tab's.
func unitTab(key rune) (kind string, ok bool) {
	for _, t := range unitTabs {
		if t.key == key {
			return t.kind, true
		}
	}
	return "", false
}

// isUnitTab reports whether kind is a unit detail tab.
func isUnitTab(kind string) bool {
	for _, t := range unitTabs {
		if t.kind == kind {
			return true
		}
	}
	return false
}

// openNeed opens the why tab of the first need of the unit shown that has
// not landed (Shift-J).
func (m *TUI) openNeed(d TUIData) {
	stage := map[string]string{}
	for _, u := range d.Floor.Units {
		stage[u.Task] = u.Stage
	}
	for _, n := range d.Needs[m.drillTask] {
		if stage[n] != "landed" {
			if _, ok := stage[n]; !ok {
				m.flash("need " + n + " is not a unit of this factory")
				return
			}
			m.drillUnit(d, n, "why")
			return
		}
	}
	m.flash(m.drillTask + " has no unmet need")
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
		if m.drillKind == "metric" && m.metricKey(k, d) {
			return
		}
		if m.drillKey(k) {
			return
		}
		switch k.Kind {
		case term.KeyEsc:
			// A unit opened from a metric's evidence goes back to it.
			if m.metricReturnTo() {
				return
			}
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
			if kind, ok := unitTab(k.Rune); ok && isUnitTab(m.drillKind) {
				m.drillKind, m.detailTop = kind, 0
				m.crumbs[len(m.crumbs)-1] = kind
				m.startFollow(d)
				return
			}
			switch k.Rune {
			case 'J':
				if isUnitTab(m.drillKind) {
					m.openNeed(d)
				}
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
	// The metrics views' keys (issue #583 k4): the window, the pulse panels.
	if (m.view == "pulse" || m.view == "metrics") && m.pulseKey(k) {
		return
	}
	if m.view == "metrics" && m.splitKey(k, rows) {
		return
	}

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

// enter is Enter on row: a unit's detail at its why tab (units, andon, tree), a
// learning in full, a checkpoint's changed paths, or a search result's unit
// at the match (its log for an event, else its explanation).
func (m *TUI) enter(d TUIData, row []string) {
	switch m.view {
	case "units", "andon", "tree":
		m.drill(d, "why")
	case "metrics":
		if m.split == "" {
			m.openMetric(metricOfRow(row))
		}
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
	m.startFollow(d)
}

// startFollow sets the log tab's follow as it opens: on while its unit
// runs, else off (and nothing paused).
func (m *TUI) startFollow(d TUIData) {
	m.follow, m.followed = false, false
	if m.drillKind != "log" {
		return
	}
	for _, u := range d.Floor.Units {
		if u.Task == m.drillTask && stateTokens[strings.Fields(u.RunState + " -")[0]] == "running" {
			m.follow = true
		}
	}
}

// logLines are a unit tab's lines as drawn: in the log tab, without the
// leading clock when t hid the timestamps, and cut into width-rune pieces
// when w wraps them; any other tab's as they are.
func (m *TUI) logLines(lines []string, width int) []string {
	if m.drillKind != "log" || (!m.logNoTS && !m.logWrap) {
		return lines
	}
	var out []string
	for _, l := range lines {
		if m.logNoTS {
			if _, rest, ok := strings.Cut(l, " "); ok {
				l = rest
			}
		}
		r := []rune(l)
		for m.logWrap && width > 0 && len(r) > width {
			out, r = append(out, string(r[:width])), r[width:]
		}
		out = append(out, string(r))
	}
	return out
}

// drillKey handles the keys every drill-down shares (issue #583 k6): f
// fullscreen, G the last line; and the log tab's: w wrap, t timestamps, G
// follow again, and a scroll up pauses the follow. It reports whether it
// used the key; a scroll it lets through.
func (m *TUI) drillKey(k term.Key) bool {
	log := m.drillKind == "log"
	switch {
	case k.Kind == term.KeyRune && k.Rune == 'f':
		m.full = !m.full
	case k.Kind == term.KeyRune && k.Rune == 'G':
		m.detailTop = 1 << 30 // View clamps it to the last page
		m.follow, m.followed = log, false
	case log && k.Kind == term.KeyRune && k.Rune == 'w':
		m.logWrap = !m.logWrap
	case log && k.Kind == term.KeyRune && k.Rune == 't':
		m.logNoTS = !m.logNoTS
	case log && m.follow && (k.Kind == term.KeyUp || k.Kind == term.KeyPgUp || (k.Kind == term.KeyRune && k.Rune == 'k')):
		m.follow, m.followed = false, true
		return false
	default:
		return false
	}
	return true
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
	{"pulse", "p", "the metrics dashboard: flow, quality, reliability, cost, capacity, by model"},
	{"metrics", "m", "every metric: value, trend, change and definition"},
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
	case "metrics":
		if m.split != "" {
			header, rows = splitRows(d, m.window, m.split)
		} else {
			header, rows = metricRows(d, m.window)
		}
	default:
		return []string{}, [][]string{}
	}
	// The views built in tui_views.go and tui_dash.go are pure: the filter
	// applies here.
	switch m.view {
	case "tree", "health", "learnings", "checkpoints", "search", "metrics":
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
		header = []string{"TASK", "LINE", "STAGE", "ATT", "SESSION", "MODEL", "STEPS", "AGE", "STATE", "WHY"}
	} else {
		header = []string{"TASK", "STAGE", "ATT", "SESSION", "MODEL", "STEPS", "AGE", "STATE", "WHY"}
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
		// WHY last (issue #583 k3): the frame cuts it to fit; the detail has it whole.
		row = append(row, d.Why[u.Task])
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

// rowsAndon returns the header and filtered rows for the andon view
// (andonRows: worst first, with each entry's next step).
func (m *TUI) rowsAndon(d TUIData) (header []string, rows [][]string) {
	header, all := andonRows(d)
	for _, row := range all {
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

// hintRows is the fewest rows a key menu of that many hints takes.
const hintRows = 4

// Key menus shared by the views: tableHints lead every table view,
// layoutHints close every menu.
var (
	tableHints  = []hint{{":", "view"}, {"/", "filter"}, {"esc", "back"}, {"-", "last view"}, {"[ ]", "history"}, {"ctrl-a", "views"}}
	sortHints   = []hint{{"N/A/S/C", "sort"}}
	layoutHints = []hint{{"?", "help"}, {"q", "quit"}, {"ctrl-e", "header"}, {"ctrl-g", "crumbs"}, {"ctrl-w", "wide"}, {"ctrl-r", "reload"}}
	scrollHints = []hint{{"j/k", "scroll"}, {"esc", "back"}}
	unitHints   = []hint{{"enter", "why"}, {"l", "log"}}
	tabHints    = []hint{{"w", "why"}, {"d", "explain"}, {"y", "brief"}, {"l", "log"}, {"c", "checkpoints"},
		{"F", "findings"}, {"e", "events"}, {"J", "need"}}
	fullHints = []hint{{"f", "fullscreen"}}
	// In the log tab w wraps, so the why tab is Esc and Enter away.
	logHints = join([]hint{{"w", "wrap"}, {"t", "timestamps"}, {"G", "follow"}}, tabHints[1:])
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
	"pulse":       join(tableHints, []hint{{"h/j/k/l", "panel"}, {"enter", "drill"}, {"1/2/3", "24h/7d/30d"}}, layoutHints),
	"metrics":     join(tableHints, []hint{{"enter", "drill"}, {"1/2/3", "24h/7d/30d"}, {"M/W", "by model/worker"}}, sortHints, layoutHints),
	"metric":      join([]hint{{"h", "chart"}, {"u", "units"}, {"enter", "unit"}}, scrollHints, fullHints, layoutHints),
	"why":         join(tabHints, scrollHints, fullHints, layoutHints),
	"explain":     join(tabHints, scrollHints, fullHints, layoutHints),
	"brief":       join(tabHints, scrollHints, fullHints, layoutHints),
	"log":         join(logHints, scrollHints, fullHints, layoutHints),
	"unitcp":      join(tabHints, scrollHints, fullHints, layoutHints),
	"findings":    join(tabHints, scrollHints, fullHints, layoutHints),
	"unitevents":  join(tabHints, scrollHints, fullHints, layoutHints),
	"learning":    join(scrollHints, fullHints, layoutHints),
	"checkpoint":  join(scrollHints, fullHints, layoutHints),
	"views":       join(scrollHints, fullHints, layoutHints),
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
	out = append(out, fmt.Sprintf("lead %s · workers %d/%d · landed today %d · %s",
		d.Floor.Staffing.Lead, busy, most, d.Floor.Output.LandedToday, fmtUSD(d.Floor.Output.Cost)))
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

// headerBlock is the header: the context lines on the left (at most three
// fifths of the width), the key menu in the rest (hintColumns), every line
// cut to width.
func (m *TUI) headerBlock(d TUIData, width int) []string {
	left := contextLines(d)
	leftW := 0
	for _, l := range left {
		leftW = max(leftW, utf8.RuneCountInString(l))
	}
	leftW = min(leftW, width*3/5)
	// The stats (issue #583 k4) sit in the middle while the whole key menu
	// still fits beside them.
	hints, avail := m.hintsFor(), width-leftW-3
	mid, midW := headerStats(d), 0
	for _, l := range mid {
		midW = max(midW, utf8.RuneCountInString(l))
	}
	if _, full := layHints(hints, maxHintRows); mid != nil && avail-midW-3 >= full {
		avail -= midW + 3
	} else {
		mid = nil
	}
	cols := hintColumns(hints, avail)
	rows := len(mid)
	for _, col := range cols {
		rows = max(rows, len(col))
	}
	out := make([]string, max(len(left), rows))
	for r := range out {
		line := ""
		if r < len(left) {
			line = cutRunes(left[r], leftW)
		}
		line += strings.Repeat(" ", leftW-utf8.RuneCountInString(line)+3)
		if mid != nil {
			cell := ""
			if r < len(mid) {
				cell = mid[r]
			}
			line += cell + strings.Repeat(" ", midW-utf8.RuneCountInString(cell)+3)
		}
		for _, col := range cols {
			if r < len(col) {
				line += col[r] + "  "
			}
		}
		out[r] = cutRunes(strings.TrimRight(line, " "), width)
	}
	return out
}

// maxHintRows is the most rows the key menu takes.
const maxHintRows = 5

// hintColumns lays hints out in columns within avail runes: the fewest rows
// from hintRows to maxHintRows whose columns fit. When none do, it drops
// whole hints from the end, never cutting one, and keeps "<?> help" last so
// the dropped ones stay one key away.
func hintColumns(hints []hint, avail int) [][]string {
	if len(hints) == 0 {
		return nil
	}
	for rows := min(hintRows, len(hints)); rows <= maxHintRows; rows++ {
		if cols, w := layHints(hints, rows); w <= avail {
			return cols
		}
	}
	help := hint{"?", "help"}
	var rest []hint
	for _, h := range hints {
		if h != help {
			rest = append(rest, h)
		}
	}
	for n := len(rest); n >= 0; n-- {
		if cols, w := layHints(append(rest[:n:n], help), maxHintRows); w <= avail {
			return cols
		}
	}
	return nil
}

// layHints lays hints out in columns of rows entries, each padded to its
// widest, and returns them with their width, two spaces between columns.
func layHints(hints []hint, rows int) (cols [][]string, width int) {
	for i := 0; i < len(hints); i += rows {
		var col []string
		w := 0
		for _, h := range hints[i:min(i+rows, len(hints))] {
			col = append(col, "<"+h.key+"> "+h.label)
			w = max(w, utf8.RuneCountInString(col[len(col)-1]))
		}
		for j := range col {
			col[j] += strings.Repeat(" ", w-utf8.RuneCountInString(col[j]))
		}
		if len(cols) > 0 {
			width += 2
		}
		cols, width = append(cols, col), width+w
	}
	return cols, width
}

// View renders one full frame, exactly height lines, each at most width
// runes wide; color adds ANSI colours and reverse video for the cursor row.
// A frame too short for the layout keeps its first lines and the last one
// (the prompt, status or breadcrumbs); a non-positive size renders "".
func (m *TUI) View(d TUIData, width, height int, color bool) string {
	if width < 1 || height < 1 {
		return ""
	}

	// The "none" skin draws no colour at all, the cursor's reverse included.
	color = color && m.skin.Name != "none"
	// Fullscreen (f) gives a drill-down the header's and the crumbs' lines.
	full := m.full && m.drillKind != ""

	// The bottom: the flash line, then the prompt or the crumbs on the last
	// line; a frame too short for both keeps the last.
	var foot []string
	switch {
	case m.promptKind == "command":
		foot = []string{m.flashLine(), ":" + m.prompt}
	case m.promptKind == "filter":
		foot = []string{m.flashLine(), "/" + m.prompt}
	case !m.hideCrumbs && !full:
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
	if !m.hideHeader && !full {
		if hdr := m.headerBlock(d, width); height-len(foot)-len(hdr) >= 2+minTableRows {
			lines = hdr
		}
	}
	// hdrLines are painted once the frame is laid out on plain text.
	hdrLines := len(lines)
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
	if m.drillKind == "metric" {
		def, _ := metricByID(m.drillTask)
		titleBar = fmt.Sprintf("── Metric %s %s ──", def.name, m.window)
	} else if m.drillKind != "" {
		titleBar = strings.TrimRight(fmt.Sprintf("── %s %s", drillTitles[m.drillKind], m.drillTask), " ") + " ──"
		if m.drillKind == "log" && m.followed {
			titleBar += " paused; G to follow ──"
		}
	} else if m.help {
		titleBar = "── Help ──"
	} else if m.view == "pulse" {
		titleBar = fmt.Sprintf("── Pulse %s ──", m.window)
	} else {
		viewName := viewNames[m.view]
		count := fmt.Sprint(rowCount)
		if m.view == "metrics" {
			viewName += " " + m.window
			if def, ok := metricByID(m.splitMetric); ok && m.split != "" {
				viewName += " · " + def.name + " by " + m.split
			}
		}
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
			"          checkpoints (c), pulse (p), metrics (m), quit (q)",
			"  :s txt  search the ledger, run logs, reports and briefs for txt",
			"Metrics (:pulse, :metrics):",
			"  1 2 3   the window: 24h, 7d, 30d",
			"  h j k l the pulse panel (or the arrows); enter its metric",
			"  enter   a metric's drill-down: h its chart, u the units behind",
			"          it (worst first), enter a unit's why",
			"  M W     :metrics split by model / by worker; esc returns",
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
			"  enter   a unit's detail (why and timeline), a learning, a",
			"          checkpoint's paths, or a search result at its match",
			"  l       a unit's log",
			"Unit detail tabs:",
			"  w why and timeline, d explain, y brief, l log, c checkpoints,",
			"  F review findings, e events; J opens the first unmet need",
			"  f       fullscreen: the drill-down without header and crumbs",
			"  G       the last line; in the log, follow the unit again",
			"Log tab:",
			"  w wrap long lines, t show or hide the timestamps; the log",
			"  follows a running unit until you scroll up (G follows again)",
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
		detail, cur := d.Detail, -1
		if m.drillKind == "metric" {
			detail, cur = m.metricLines(d, width)
		} else if isUnitTab(m.drillKind) {
			// The unit detail: its why line leads every tab.
			detail = append([]string{"why: " + d.Why[m.drillTask], ""}, m.logLines(d.Detail, width)...)
		} else if _, ok := drillTitles[m.drillKind]; ok {
			detail = localDetail(d, m.drillKind, m.drillTask)
		}
		// A log that follows its unit shows its last page.
		if m.drillKind == "log" && m.follow {
			m.detailTop = len(detail)
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
		// The evidence row under the cursor stays in sight.
		if cur >= 0 {
			m.detailTop = max(min(m.detailTop, cur), cur-visible+1)
		}
		m.detailTop = max(0, min(m.detailTop, len(detail)-visible))
		for i := m.detailTop; i < m.detailTop+visible && i < len(detail); i++ {
			lines = append(lines, cutRunes(detail[i], width))
		}
	} else if m.view == "pulse" {
		lines = append(lines, m.pulseLines(d, width)...)
	} else {
		// Table view: long cells cut to maxCell unless wide (Ctrl-W); whole
		// keeps the uncut rows, whose first cells key the change marks.
		whole := rows
		if !m.wide {
			rows = cutCells(rows)
		}
		widths := m.computeColumnWidths(header, rows)
		// The andon's SIGNAL is the state its rows are coloured by.
		paintHeader := header
		if i := slices.Index(header, "SIGNAL"); i >= 0 {
			paintHeader = slices.Clone(header)
			paintHeader[i] = "STATE"
		}

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
			// cursor row in reverse video, else the whole row in the skin's
			// colour of its state, highlighted while a fetch's change marks it.
			if color {
				rowLineCut = m.paintRow(paintHeader, row, rowLineCut, isCursor, m.marked(cell(whole[i], 0)))
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
	if color {
		for i := 0; i < hdrLines && i < len(lines); i++ {
			lines[i] = m.skin.header(lines[i])
		}
	}
	for i, f := range foot {
		f = cutRunes(f, width)
		if color && i == len(foot)-1 && m.promptKind == "" && f == cutRunes(m.crumbLine(), width) {
			f = m.skin.Paint(m.skin.Crumb, f)
		}
		lines = append(lines, f)
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
