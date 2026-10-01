package flywheel

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/suzworx/flywheel/internal/term"
)

// TUIAction is an action the factory view asked for (issue #583 k5): what
// it does, the units it acts on, and the flywheel arguments of each command
// it runs, one per target (one for a factory action). The live loop runs
// them (TakeAction); the model never does.
type TUIAction struct {
	Name    string
	Targets []string
	Argv    [][]string
}

// Label is the action as the flash names it: "validate T1 T2".
func (a TUIAction) Label() string {
	return strings.TrimSpace(a.Name + " " + strings.Join(a.Targets, " "))
}

// CommandLine is the first command the action runs, as a shell would take
// it, and how many more follow.
func (a TUIAction) CommandLine() string {
	if len(a.Argv) == 0 {
		return ""
	}
	parts := []string{"flywheel"}
	for _, arg := range a.Argv[0] {
		if arg == "" || strings.ContainsAny(arg, " \t\"") {
			arg = `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
		}
		parts = append(parts, arg)
	}
	line := strings.Join(parts, " ")
	if n := len(a.Argv) - 1; n > 0 {
		line += " (and one per target: " + strings.Join(a.Targets[1:], " ") + ")"
	}
	return line
}

// SetActions gives the model what its actions need: the ledger's directory,
// the session inspect and resume run as ("" none), and whether the view was
// started with --readonly.
func (m *TUI) SetActions(dir, session string, readonly bool) {
	m.actDir, m.session, m.readonly = dir, session, readonly
}

// TakeAction reports the action a confirmed y asked for, and clears it.
func (m *TUI) TakeAction() (TUIAction, bool) {
	a := m.pending
	m.pending = nil
	if a == nil {
		return TUIAction{}, false
	}
	return *a, true
}

// actionKeys are the keys that act, each with the action it names.
var actionKeys = map[rune]string{'v': "validate", 'i': "inspect", 'r': "resume", 'x': "withdraw", 'Z': "suspend", 'R': "resume factory"}

// actionKey handles the action keys, space and Esc with marks in the units
// view and a unit's detail; it reports whether it used the key.
func (m *TUI) actionKey(k term.Key, d TUIData) bool {
	inTable := m.drillKind == ""
	switch {
	case inTable && k.Kind == term.KeyRune && k.Rune == ' ':
		if task := m.getTaskAtCursor(d); task != "" {
			if m.picked[task] {
				delete(m.picked, task)
			} else {
				if m.picked == nil {
					m.picked = map[string]bool{}
				}
				m.picked[task] = true
			}
			m.cursor++
		}
		return true
	case inTable && k.Kind == term.KeyEsc && len(m.picked) > 0:
		m.picked = nil
		m.flash("marks cleared")
		return true
	case k.Kind != term.KeyRune:
		return false
	}
	name, ok := actionKeys[k.Rune]
	if !ok {
		return false
	}
	switch {
	case m.readonly:
		m.flash("read-only: started with --readonly")
		return true
	case m.busy != "":
		m.flash("busy: " + m.busy)
		return true
	}
	dir := []string{"--dir", m.actDir}
	switch name {
	case "suspend", "resume factory":
		argv := append([]string{strings.Fields(name)[0]}, dir...)
		if m.session != "" {
			argv = append(argv, "--session", m.session)
		}
		m.ask(TUIAction{Name: name, Argv: [][]string{argv}})
		return true
	}
	targets := m.targets(d)
	if len(targets) == 0 {
		m.flash("no unit to " + name)
		return true
	}
	if name == "inspect" {
		if m.session == "" {
			m.flash("set FLYWHEEL_SESSION to your own session to inspect")
			return true
		}
		m.verdictFor = targets
		return true
	}
	a := TUIAction{Name: name, Targets: targets}
	for _, t := range targets {
		switch name {
		case "validate":
			a.Argv = append(a.Argv, append([]string{"validate", t}, dir...))
		case "resume":
			a.Argv = append(a.Argv, append([]string{"run", t, "--resume"}, dir...))
		case "withdraw":
			a.Argv = append(a.Argv, append([]string{"log", "--task", t, "--kind", "withdrawn", "--note", "withdrawn from flywheel factory"}, dir...))
		}
	}
	m.ask(a)
	return true
}

// ask puts a on the flash line for a y/N; the command line shows below it.
func (m *TUI) ask(a TUIAction) {
	m.confirm = &a
}

// targets are the marked units in table order, else the unit under the
// cursor: the detail's unit in a unit's detail.
func (m *TUI) targets(d TUIData) []string {
	if len(m.picked) == 0 {
		if m.drillKind != "" {
			return []string{m.drillTask}
		}
		if t := m.getTaskAtCursor(d); unitExists(d, t) {
			return []string{t}
		}
		return nil
	}
	var out []string
	seen := map[string]bool{}
	_, rows := m.Rows(d)
	for _, row := range rows {
		if t := cell(row, 0); m.picked[t] && !seen[t] {
			out, seen[t] = append(out, t), true
		}
	}
	for _, u := range d.Floor.Units {
		if m.picked[u.Task] && !seen[u.Task] {
			out, seen[u.Task] = append(out, u.Task), true
		}
	}
	return out
}

// verdicts are the inspect prompt's keys.
var verdicts = map[rune]string{'p': "pass", 'r': "rework", 's': "scrap", 'e': "escalate"}

// actionPrompt answers the verdict prompt or the y/N of a pending action;
// it reports whether one was up. Ctrl-C still quits.
func (m *TUI) actionPrompt(k term.Key) bool {
	if m.verdictFor == nil && m.confirm == nil {
		return false
	}
	if k.Kind == term.KeyCtrlC {
		m.quit = true
		return true
	}
	if targets := m.verdictFor; targets != nil {
		m.verdictFor = nil
		v, ok := verdicts[k.Rune]
		if k.Kind != term.KeyRune || !ok {
			m.flash("cancelled")
			return true
		}
		a := TUIAction{Name: "inspect " + v, Targets: targets}
		for _, t := range targets {
			a.Argv = append(a.Argv, []string{"inspect", t, "--verdict", v, "--session", m.session, "--dir", m.actDir})
		}
		m.ask(a)
		return true
	}
	a := m.confirm
	m.confirm = nil
	if k.Kind != term.KeyRune || (k.Rune != 'y' && k.Rune != 'Y') {
		m.flash("cancelled")
		return true
	}
	m.pending, m.picked = a, nil
	return true
}

// promptLines are the flash line and the line under it while an action
// prompt is up; ok false when none is.
func (m *TUI) promptLines() (flash, help string, ok bool) {
	switch {
	case m.verdictFor != nil:
		return "inspect " + strings.Join(m.verdictFor, " ") + ": p pass  r rework  s scrap  e escalate  (Esc cancels)", "", true
	case m.confirm != nil:
		return m.confirm.Label() + "? y/N", m.confirm.CommandLine(), true
	}
	return "", "", false
}

// ActionStarted marks a running: the flash reads "running: <command>"
// until ActionDone, and every action key says busy.
func (m *TUI) ActionStarted(a TUIAction) {
	m.busy = a.Label()
	m.running = "running: " + a.CommandLine()
}

// ActionDone records how the running action ended: its exit code and its
// whole output, which :result shows.
func (m *TUI) ActionDone(a TUIAction, code int, output string) {
	m.busy, m.running = "", ""
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(output, "\r\n", "\n"), "\n"), "\n")
	m.result = append([]string{"$ " + a.CommandLine(), fmtExit(code), ""}, lines...)
	msg := a.Label() + ": " + fmtExit(code)
	if last := strings.TrimSpace(lines[len(lines)-1]); code != 0 && last != "" {
		msg += " — " + last
	}
	m.flash(msg)
}

func fmtExit(code int) string { return "exit " + strconv.Itoa(code) }

// openResult shows the last action's output (`:result`); Esc goes back.
func (m *TUI) openResult() {
	if m.result == nil {
		m.flash("no action has run yet")
		return
	}
	m.help = false
	m.drillKind, m.drillTask, m.detailTop = "result", "", 0
	m.resetCrumbs()
	m.pushCrumbs("result")
}

// boundRunes are the runes some view or drill-down already uses; a hotkey
// never takes one, nor a named key, nor a bound Ctrl key.
const boundRunes = " jkhlgGfwtdycFeJuHM WNASC123:/?q-[]virxZRps<>~"

// boundKey reports whether k already does something in the view.
func boundKey(k term.Key) bool {
	switch k.Kind {
	case term.KeyRune:
		return strings.ContainsRune(boundRunes, k.Rune)
	case term.KeyCtrl:
		return strings.ContainsRune("aegwr", k.Rune)
	}
	return true
}

// viewCommand reports whether cmd (without its `:`) opens a view.
func viewCommand(cmd string) bool {
	name, arg, _ := strings.Cut(cmd, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "s", "search":
		return arg != ""
	case "result":
		return arg == ""
	}
	for _, v := range tuiViews {
		if (name == v.name || name == v.alias) && v.name != "search" && arg == "" {
			return true
		}
	}
	return false
}

// LoadHotkeys reads <dir>/.flywheel/hotkeys.json, {"hotkeys": {"<key>":
// ":<view command>"}}, each key one ParseKeySeq token. A key already bound,
// a command that opens no view or a file that does not parse is skipped;
// msg names every skip ("" when none). No file is no hotkeys.
func LoadHotkeys(dir string) (keys map[term.Key]string, msg string) {
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "hotkeys.json"))
	if os.IsNotExist(err) {
		return nil, ""
	}
	var f struct {
		Hotkeys map[string]string `json:"hotkeys"`
	}
	if err == nil {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		return nil, "hotkeys.json skipped: " + err.Error()
	}
	var skipped []string
	for _, name := range slices.Sorted(maps.Keys(f.Hotkeys)) {
		cmd := strings.TrimPrefix(strings.TrimSpace(f.Hotkeys[name]), ":")
		seq, err := ParseKeySeq(name)
		switch {
		case err != nil || len(seq) != 1 || len(seq[0].Keys) != 1:
			skipped = append(skipped, name+" is not one key")
		case boundKey(seq[0].Keys[0]):
			skipped = append(skipped, name+" is already bound")
		case !viewCommand(cmd):
			skipped = append(skipped, name+": :"+cmd+" is no view")
		default:
			if keys == nil {
				keys = map[term.Key]string{}
			}
			keys[seq[0].Keys[0]] = cmd
		}
	}
	if len(skipped) > 0 {
		msg = "hotkeys.json skipped " + strings.Join(skipped, "; ")
	}
	return keys, msg
}

// SetHotkeys binds the hotkeys LoadHotkeys read and flashes what it skipped.
func (m *TUI) SetHotkeys(keys map[term.Key]string, msg string) {
	m.hotkeys = keys
	if msg != "" {
		m.flash(msg)
	}
}

// hotkey runs k's view command as if typed after `:`; false when k is no
// hotkey.
func (m *TUI) hotkey(k term.Key) bool {
	cmd, ok := m.hotkeys[k]
	if ok {
		m.runCommand(cmd)
	}
	return ok
}
