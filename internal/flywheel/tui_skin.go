package flywheel

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The factory view's skins (issue #583 k6): every colour the interactive
// view draws comes from one of these token sets, never from a literal in a
// view. factory.skin in the config picks one; "none" draws no colour.

// Skin is one colour scheme: an ANSI SGR sequence per token, "" for none.
type Skin struct {
	Name string

	// The row states.
	Running, Passed, Waiting, Failed, Frozen, Landed string

	// The chrome: the header's text, the crumbs, the menu's <key>s, and the
	// highlight of a row that changed since the previous refresh.
	Header, Crumb, MenuKey, Flash string
}

// DefaultSkin is the skin factory.skin means when unset.
const DefaultSkin = "dark"

// skins are the built-in skins, by name.
var skins = map[string]Skin{
	"dark": {Name: "dark",
		Running: "\x1b[36m", Passed: "\x1b[32m", Waiting: "\x1b[33m", Failed: "\x1b[31m", Frozen: "\x1b[35m", Landed: "\x1b[2m",
		Header: "\x1b[37m", Crumb: "\x1b[36m", MenuKey: "\x1b[35m", Flash: "\x1b[1m"},
	"light": {Name: "light",
		Running: "\x1b[34m", Passed: "\x1b[32m", Waiting: "\x1b[38;5;130m", Failed: "\x1b[31m", Frozen: "\x1b[35m", Landed: "\x1b[90m",
		Header: "\x1b[30m", Crumb: "\x1b[34m", MenuKey: "\x1b[35m", Flash: "\x1b[1m"},
	"none": {Name: "none"},
}

// SkinNames are the values factory.skin takes, the default first.
var SkinNames = []string{"dark", "light", "none"}

// SkinFor is the built-in skin name picks ("" is DefaultSkin); an unknown
// name is an error listing the known ones.
func SkinFor(name string) (Skin, error) {
	if name == "" {
		name = DefaultSkin
	}
	s, ok := skins[name]
	if !ok {
		return Skin{}, fmt.Errorf("factory.skin %q must be %s", name, strings.Join(quoted(SkinNames), ", "))
	}
	return s, nil
}

// stateTokens name the token each state word is drawn in: a unit's run
// state, its stage, an andon's state, the factory's state.
var stateTokens = map[string]string{
	"running": "running", "exploring": "running", "long-step": "running", "started": "running", "building": "running",
	"passed": "passed", "done": "passed", "validated": "passed", "inspected": "passed",
	"waiting": "waiting", "planned": "waiting", "dispatched": "waiting", "queued": "waiting", "finished": "waiting",
	"blocked": "waiting", "needs-correction": "waiting", "rate-limited": "waiting", "stacked": "waiting",
	"failed": "failed", "failed-dirty": "failed", "stalled": "failed", "silent": "failed", "capped": "failed",
	"provider-error": "failed", "mismatch": "failed", "no-writes": "failed", "abandoned-job": "failed", "lost": "failed",
	"suspended": "frozen", "frozen": "frozen",
	"landed": "landed", "withdrawn": "landed",
}

// State is the colour of a state word ("capped 50k" is capped), "" when the
// skin gives it none.
func (s Skin) State(state string) string {
	word, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(state)), " ")
	switch stateTokens[word] {
	case "running":
		return s.Running
	case "passed":
		return s.Passed
	case "waiting":
		return s.Waiting
	case "failed":
		return s.Failed
	case "frozen":
		return s.Frozen
	case "landed":
		return s.Landed
	}
	return ""
}

// Paint wraps text in code and a reset; text alone when code is "".
func (s Skin) Paint(code, text string) string {
	if code == "" || text == "" {
		return text
	}
	return code + text + ansiReset
}

// menuKeyRe finds the key menu's <key>s and the crumbs' tags.
var menuKeyRe = regexp.MustCompile(`<[^<>\s][^<>]*>`)

// header paints one header line: its <key>s in MenuKey, the factory's state
// in the colour of that state, the rest in Header.
func (s Skin) header(line string) string {
	line = menuKeyRe.ReplaceAllStringFunc(line, func(k string) string { return s.Paint(s.MenuKey, k) })
	line = strings.Replace(line, "factory running", s.Paint(s.Running, "factory running"), 1)
	line = strings.Replace(line, "factory FROZEN", s.Paint(s.Frozen, "factory FROZEN"), 1)
	if s.Header == "" {
		return line
	}
	return s.Header + strings.ReplaceAll(line, ansiReset, ansiReset+s.Header) + ansiReset
}

// rowState is the state a table row is coloured by: landed when its STAGE
// is, else its STATE, else its STAGE; "" when it has neither.
func rowState(header, row []string) string {
	stage, state := "", ""
	if i := slices.Index(header, "STAGE"); i >= 0 && i < len(row) {
		stage = row[i]
	}
	if i := slices.Index(header, "STATE"); i >= 0 && i < len(row) {
		state = row[i]
	}
	if stage == "landed" || state == "" || state == "-" {
		return stage
	}
	return state
}

// paintRow colours a drawn table row (line, cut to the frame): the cursor
// row in reverse video, else the colour of the row's state, in Flash too
// while a fetch's change marks it.
func (m *TUI) paintRow(header, row []string, line string, cursor, marked bool) string {
	if cursor {
		return "\x1b[7m" + line + ansiReset
	}
	code := m.skin.State(rowState(header, row))
	if marked {
		code = m.skin.Flash + code
	}
	return m.skin.Paint(code, line)
}

// flashViews are the views whose rows are marked when a fetch changes them.
var flashViews = []string{"units", "andon", "tree", "workers", "lines"}

// flashRefreshes is how many refreshes a changed or new row stays marked.
const flashRefreshes = 2

// Observe records a fetch's data, like k9s's MODIFIED and NEW markers: in
// every view of flashViews, unfiltered, a row whose cells (all but AGE and
// WHY, which move with the clock) differ from the previous fetch's is marked
// modified, a row that was not there new, each for flashRefreshes refreshes.
// The first fetch marks nothing. Rows are keyed by their first cell.
func (m *TUI) Observe(d TUIData) {
	if m.seen == nil {
		m.seen, m.marks = map[string]map[string]string{}, map[string]map[string]rowMark{}
	}
	for _, v := range flashViews {
		c := *m // the view's rows unfiltered, without touching the model shown
		c.view, c.filter, c.promptKind, c.sort = v, "", "", tuiSort{}
		header, rows := c.Rows(d)
		skip := map[int]bool{slices.Index(header, "AGE"): true, slices.Index(header, "WHY"): true}
		now := map[string]string{}
		for _, row := range rows {
			var cells []string
			for i, cell := range row {
				if !skip[i] {
					cells = append(cells, cell)
				}
			}
			now[cell(row, 0)] = strings.Join(cells, "\x00")
		}
		marks := map[string]rowMark{}
		for k, mk := range m.marks[v] {
			if _, ok := now[k]; ok && mk.left > 1 {
				marks[k] = rowMark{mk.kind, mk.left - 1}
			}
		}
		if prev, ok := m.seen[v]; ok {
			for k, sig := range now {
				if old, had := prev[k]; !had {
					marks[k] = rowMark{"new", flashRefreshes}
				} else if old != sig {
					marks[k] = rowMark{"modified", flashRefreshes}
				}
			}
		}
		m.seen[v], m.marks[v] = now, marks
	}
}

// marked reports whether the view shown marks the row keyed key.
func (m *TUI) marked(key string) bool {
	_, ok := m.marks[m.view][key]
	return ok
}
