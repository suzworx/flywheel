package flywheel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tokens is a run's token accounting, summed across the run's steps.
// Output never includes reasoning tokens: Reasoning is counted separately for every adapter.
type Tokens struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	Reasoning  int `json:"reasoning"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// Event is one JSON object per line in .flywheel/events.jsonl. The event log
// is the source of truth; state.json and flywheel.md are derived from it.
type Event struct {
	TS      string `json:"ts"`
	Task    string `json:"task"`
	Kind    string `json:"kind"`
	Session string `json:"session,omitempty"`
	Model   string `json:"model,omitempty"`
	Attempt string `json:"attempt,omitempty"`
	RC      *int   `json:"rc,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Signal is a signal event's condition name (issue #37): a uniform record
	// of a run condition already detected and recorded under its own kind,
	// so the factory's troubles have one countable shape. Only the signal
	// kind may carry it.
	Signal  string   `json:"signal,omitempty"`
	Verdict string   `json:"verdict,omitempty"`
	Brief   string   `json:"brief,omitempty"`
	Needs   []string `json:"needs,omitempty"`
	Owns    []string `json:"owns,omitempty"`
	// Header is the parsed brief header (owns, needs, needs-state, gates,
	// live-gates, exclusive, review, sha256) carried by planned, amended and
	// dispatched events (issue #259): the gate set a pass is measured against
	// lives in the append-only log, not in a mutable file on disk. It is
	// authoritative over the brief file when present; Owns and Needs above
	// remain as a summary for consumers that read them.
	Header *BriefHeader `json:"header,omitempty"`
	// Commit is a landed event's commit id (flywheel land --commit) and the
	// HEAD commit of the workdir a validated/owns_checked reading was taken
	// at (issue #196): "which commit" has one name in the ledger.
	Commit string `json:"commit,omitempty"`
	Note   string `json:"note,omitempty"`
	// LeadImplemented marks a landed event whose unit the lead implemented
	// directly instead of a worker (issue #198). Omitted on ordinary landings.
	LeadImplemented bool    `json:"lead_implemented,omitempty"`
	Adapter         string  `json:"adapter,omitempty"`
	Path            string  `json:"path,omitempty"`
	SHA256          string  `json:"sha256,omitempty"`
	Tokens          *Tokens `json:"tokens,omitempty"`
	Cost            float64 `json:"cost,omitempty"`
	Steps           int     `json:"steps,omitempty"`
	PeakReasoning   int     `json:"peak_reasoning,omitempty"`
	// Wrote is a finished event's distinct paths written by edit/write tool
	// calls during the attempt, sorted, at most 50 entries; empty when the
	// attempt made no edits (issue #163).
	Wrote []string `json:"wrote,omitempty"`
	Tree  string   `json:"tree,omitempty"`
	// Workdir is the git working tree a validated, owns_checked or inspected
	// reading was taken in, recorded only when it differs from the flywheel
	// root (issue #244): the ledger says where a reading happened, so verify
	// can resolve the tree object in the right repository. Omitted on
	// ordinary same-dir readings, so existing ledgers are unchanged.
	Workdir    string            `json:"workdir,omitempty"`
	Gate       string            `json:"gate,omitempty"`
	Command    string            `json:"command,omitempty"`
	DurationMS int64             `json:"duration_ms,omitempty"`
	Outside    []string          `json:"outside,omitempty"`
	Baseline   map[string]string `json:"baseline,omitempty"`
	Baselined  []string          `json:"baselined,omitempty"`
	Attributed []string          `json:"attributed,omitempty"`
	// Files is an owns_checked event's measured shape of every changed path
	// inside the unit's owns, sorted by path (issue #130), so a truncated
	// document is visible in the ledger without re-reading the tree.
	Files []FileShape `json:"files,omitempty"`
	// Worktrees is a dispatched event's snapshot of the repo's OTHER
	// worktrees at dispatch time: worktree path -> {path -> sha256} for every
	// path changedPaths reports there (issue #87). Nil when dir is not a git
	// repo or the repo has no other worktrees.
	Worktrees map[string]map[string]string `json:"worktrees,omitempty"`
	Persona   string                       `json:"persona,omitempty"`
	GoalID    string                       `json:"goal_id,omitempty"`
	Goal      *GoalSpec                    `json:"goal,omitempty"`
	// Increment is a dispatched event's increment number when `flywheel run --increment N` sent only
	// increment N of the brief (issue #83); 0 means the whole brief.
	Increment int `json:"increment,omitempty"`
	// Learning fields (issue #38): a learning event carries severity, title,
	// observed, evidence, ask and signals; a dismissed event carries id
	// (the learning it targets, ^L-[0-9]+$) and reuses Note for the reason.
	Severity string   `json:"severity,omitempty"`
	Title    string   `json:"title,omitempty"`
	Observed string   `json:"observed,omitempty"`
	Evidence string   `json:"evidence,omitempty"`
	Ask      string   `json:"ask,omitempty"`
	Signals  []string `json:"signals,omitempty"`
	ID       string   `json:"id,omitempty"`
	// Prev is the lineHash of the log's last complete line when this event was
	// appended (issue #57): the tamper-evidence chain `flywheel verify --log`
	// checks. Set by AppendEvents only; any value a caller supplies is overwritten.
	Prev string `json:"prev,omitempty"`
}

// kinds is the set of event kinds understood by Derive.
var kinds = map[string]bool{
	"planned":         true,
	"dispatched":      true,
	"started":         true,
	"worker_plan":     true,
	"no-plan":         true,
	"off-course":      true,
	"finished":        true,
	"report":          true,
	"reviewed":        true,
	"blocked":         true,
	"lost":            true,
	"landed":          true,
	"amended":         true,
	"lead_edit":       true,
	"validated":       true,
	"owns_checked":    true,
	"inspected":       true,
	"staffed":         true,
	"session_start":   true,
	"session_command": true,
	"session_end":     true,
	"goal":            true,
	"learning":        true,
	"dismissed":       true,
	"signal":          true,
	"excepted":        true,
	"allow_untriaged": true,
}

// Signals is the set of condition names a signal event may carry (issue #37):
// no-plan, off-course, no-writes, capped, provider-error, stalled, silent and
// failed-dirty. The last two and no-writes and failed-dirty are still derived
// states in the factory view; run.go emits the others where it already
// detects them.
var Signals = map[string]bool{
	"no-plan": true, "off-course": true, "no-writes": true, "capped": true,
	"provider-error": true, "stalled": true, "silent": true, "failed-dirty": true,
}

// severities is the set of severities a learning event may carry.
var severities = map[string]bool{"P0": true, "P1": true, "P2": true}

// learningIDOK reports whether s matches ^L-[0-9]+$.
func learningIDOK(s string) bool {
	if len(s) < 3 || s[0] != 'L' || s[1] != '-' {
		return false
	}
	for i := 2; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isTaskChar reports whether c is allowed in a task id: ^[A-Za-z0-9._-]+$.
func isTaskChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' ||
		c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
}

func taskOK(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isTaskChar(s[i]) {
			return false
		}
	}
	return true
}

// attemptOK reports whether s matches ^[rc][0-9]+$: r1 first fresh run,
// c1.. corrections.
func attemptOK(s string) bool {
	if len(s) < 2 || s[0] != 'r' && s[0] != 'c' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// floorLevel reports whether kind may carry an empty task: staffed, lead_edit
// and the three session kinds describe the floor itself rather than a task,
// and goal events are validated against Goal instead of Task.
func floorLevel(kind string) bool {
	switch kind {
	case "staffed", "lead_edit", "goal", "session_start", "session_command", "session_end":
		return true
	default:
		return false
	}
}

// Validate enforces the task pattern, the attempt pattern, the kind set and
// the reviewed-requires-verdict rule. A signal event must carry a Signal from
// the exported Signals set, and no other kind may carry one. staffed,
// lead_edit, session_start, session_command and session_end are floor-level
// events: they may carry an empty task (every other kind requires one) but
// staffed, lead_edit and the two session-boundary kinds must carry a session,
// and session_command must also carry a note.
func Validate(e Event) error {
	if !floorLevel(e.Kind) && !taskOK(e.Task) {
		return fmt.Errorf("event task %q does not match ^[A-Za-z0-9._-]+$", e.Task)
	}
	if e.Kind == "staffed" && e.Session == "" {
		return fmt.Errorf("staffed event must carry a session")
	}
	if e.Kind == "lead_edit" && e.Session == "" {
		return fmt.Errorf("lead_edit event must carry a session")
	}
	if (e.Kind == "session_start" || e.Kind == "session_end") && e.Session == "" {
		return fmt.Errorf("%s event must carry a session", e.Kind)
	}
	if e.Kind == "session_command" {
		if e.Session == "" {
			return fmt.Errorf("session_command event must carry a session")
		}
		if e.Note == "" {
			return fmt.Errorf("session_command event must carry a note")
		}
	}
	if e.Goal != nil && e.Kind != "goal" {
		return fmt.Errorf("event kind %q cannot carry a goal", e.Kind)
	}
	if e.Kind == "goal" {
		if e.Goal == nil {
			return fmt.Errorf("goal event must carry a goal spec")
		}
		if !taskOK(e.Goal.ID) {
			return fmt.Errorf("goal id %q does not match ^[A-Za-z0-9._-]+$", e.Goal.ID)
		}
		if e.Goal.Title == "" {
			return fmt.Errorf("goal event must carry a non-empty title")
		}
		if !goalStatuses[e.Goal.Status] {
			return fmt.Errorf("goal status %q is not one of active, met, failed, abandoned", e.Goal.Status)
		}
	}
	if !kinds[e.Kind] {
		return fmt.Errorf("event kind %q is not one of planned, dispatched, started, worker_plan, no-plan, off-course, finished, report, reviewed, blocked, lost, landed, amended, lead_edit, validated, owns_checked, inspected, staffed, session_start, session_command, session_end, goal, learning, dismissed, signal, excepted, allow_untriaged", e.Kind)
	}
	if e.Kind == "signal" {
		if e.Signal == "" {
			return fmt.Errorf("signal event must carry a signal")
		}
		if !Signals[e.Signal] {
			return fmt.Errorf("signal %q is not one of no-plan, off-course, no-writes, capped, provider-error, stalled, silent, failed-dirty", e.Signal)
		}
	}
	if e.Signal != "" && e.Kind != "signal" {
		return fmt.Errorf("event kind %q cannot carry a signal", e.Kind)
	}
	if e.Kind == "learning" {
		if !severities[e.Severity] {
			return fmt.Errorf("learning event severity %q is not one of P0, P1, P2", e.Severity)
		}
		if e.Title == "" || e.Observed == "" || e.Evidence == "" || e.Ask == "" {
			return fmt.Errorf("learning event must carry title, observed, evidence, and ask")
		}
	}
	if e.Kind == "dismissed" {
		if !learningIDOK(e.ID) {
			return fmt.Errorf("dismissed event id %q does not match ^L-[0-9]+$", e.ID)
		}
		if e.Note == "" {
			return fmt.Errorf("dismissed event must carry a note")
		}
	}
	if e.Kind == "excepted" {
		if e.Note == "" || e.Session == "" || !CommitOK(e.Commit) {
			return fmt.Errorf("excepted event must carry a note (the evidence), a session and the commit it covers")
		}
	}
	if e.Kind == "allow_untriaged" {
		if e.Note == "" || e.Task == "" || !CommitOK(e.Commit) {
			return fmt.Errorf("allow_untriaged event must carry a task, a note (the reason) and the commit it covers")
		}
	}
	if e.Increment < 0 || (e.Increment != 0 && e.Kind != "dispatched") {
		return fmt.Errorf("event increment %d: only a dispatched event may carry one, and it must be >= 1", e.Increment)
	}
	if e.Attempt != "" && !attemptOK(e.Attempt) {
		return fmt.Errorf("event attempt %q does not match ^[rc][0-9]+$", e.Attempt)
	}
	if e.Kind == "reviewed" && e.Verdict != "pass" && e.Verdict != "correct" && e.Verdict != "reject" {
		return fmt.Errorf("reviewed event must carry verdict pass, correct, or reject (got %q)", e.Verdict)
	}
	if e.Kind == "inspected" && e.Verdict != "pass" && e.Verdict != "rework" && e.Verdict != "scrap" && e.Verdict != "escalate" {
		return fmt.Errorf("inspected event must carry verdict pass, rework, scrap, or escalate (got %q)", e.Verdict)
	}
	if e.Kind == "validated" && (e.Gate == "" || e.Tree == "") {
		return fmt.Errorf("validated event must carry gate and tree")
	}
	return nil
}

// workdirField returns the workdir a reading event should record, or "" when
// the reading was taken in the flywheel root itself (issue #244): a
// validated, owns_checked or inspected event records where its tree was
// measured only when that differs from the repo dir, so ordinary ledgers stay
// unchanged. Both paths are normalised to canonical absolute form before the
// comparison and the recorded value, so a relative --workdir recorded from one
// directory still resolves when the ledger is read from elsewhere: the
// recorded provenance must not depend on the reader's working directory.
func workdirField(wd, dir string) string {
	if samePath(wd, dir) {
		return ""
	}
	return absPath(wd)
}

// absPath returns p normalised to an absolute, canonical path: relative
// inputs are resolved against the process working directory, symlinks are
// followed, and DOS 8.3 short names are expanded — filepath.EvalSymlinks
// does the last two on Windows and Unix alike, so two runs in the same
// directory always record the same string (issue #244). A path that cannot
// be resolved (for instance one that does not exist yet) falls back to its
// absolute, cleaned form; the input is returned unchanged only when it
// cannot even be made absolute.
func absPath(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	return filepath.Clean(a)
}

// marshalEvent encodes e as one JSON line (no trailing newline) with HTML
// escaping off, so notes round-trip byte-for-byte.
func marshalEvent(e Event) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return nil, err
	}
	line := b.Bytes()
	return line[:len(line)-1], nil
}

// needsNewlinePrefix reports whether path is non-empty and does not end in a
// newline (a torn write from a crash), in which case the next record must be
// prefixed with a newline so it does not splice onto the previous record.
func needsNewlinePrefix(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() == 0 {
		return false, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		f.Close()
		return false, fmt.Errorf("seek %s: %w", path, err)
	}
	var b [1]byte
	if _, err := f.Read(b[:1]); err != nil {
		f.Close()
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", path, err)
	}
	return b[0] != '\n', nil
}

// AppendEvent validates e, stamps TS when empty, and appends one JSON line to
// .flywheel/events.jsonl with a single O_APPEND write. If the file ends in a
// torn (non-newline) byte, the same write is prefixed with a newline.
func AppendEvent(dir string, e Event) error {
	return AppendEvents(dir, []Event{e})
}

// AppendEvents appends a batch of events in one write. Every event gets the
// same defaults and validation as AppendEvent before anything is written, so
// one invalid event appends none; events without a timestamp share one
// instant (kindRank orders same-instant kinds); and all the lines go out in
// a single O_APPEND write. Each event's Prev is set to the lineHash of the
// log's last complete line when appended (issue #57), forming a tamper-evident
// chain checked by `flywheel verify --log`; any Prev value a caller supplies is
// overwritten. Records that must stand or fall together — an excepted event
// and the landing it permits — can then never be split by a failure between
// two separate appends.
func AppendEvents(dir string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Work on a copy: defaults, timestamps and prev are set here, never on
	// the caller's events. Validate all of them before any write.
	events = append([]Event(nil), events...)
	for i := range events {
		if events[i].Kind == "staffed" && events[i].Persona == "" {
			events[i].Persona = "lead"
		}
		// planned and amended events default to the planner persona, so every
		// ingestion route — the JSON log path and the generic flag path included —
		// records who decided. A value already set wins.
		if (events[i].Kind == "planned" || events[i].Kind == "amended") && events[i].Persona == "" {
			events[i].Persona = "planner"
		}
		if err := Validate(events[i]); err != nil {
			return err
		}
		if events[i].TS == "" {
			events[i].TS = now
		}
	}
	dot := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dot, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dot, err)
	}
	path := filepath.Join(dot, "events.jsonl")
	prev, err := lastLineHash(path)
	if err != nil {
		return err
	}
	var lines [][]byte
	for i := range events {
		events[i].Prev = prev
		line, err := marshalEvent(events[i])
		if err != nil {
			return err
		}
		lines = append(lines, line)
		prev = lineHash(line)
	}
	prefix, err := needsNewlinePrefix(path)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if prefix {
		if _, err := buf.WriteString("\n"); err != nil {
			return fmt.Errorf("buffer: %w", err)
		}
	}
	for _, line := range lines {
		if _, err := buf.Write(line); err != nil {
			return fmt.Errorf("buffer: %w", err)
		}
		if _, err := buf.WriteString("\n"); err != nil {
			return fmt.Errorf("buffer: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		f.Close()
		return fmt.Errorf("append %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// ReadEvents returns the events in .flywheel/events.jsonl. A missing file
// means no events.
func ReadEvents(dir string) ([]Event, error) {
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return []Event{}, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	evs, err := ParseEvents(f, false)
	f.Close()
	return evs, err
}

// ParseEvents is the shared JSONL parser. Blank lines are skipped; a line
// starting with a git conflict marker is an error naming the line; a line that
// does not decode is an error naming the line. Strict mode rejects unknown
// JSON fields so that typos in flywheel log input surface.
func ParseEvents(r io.Reader, strict bool) ([]Event, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var events []Event
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "<<<<<<<") ||
			strings.HasPrefix(line, "=======") ||
			strings.HasPrefix(line, ">>>>>>>") {
			return nil, fmt.Errorf("unresolved merge conflict at line %d", n)
		}
		var e Event
		var err error
		if strict {
			dec := json.NewDecoder(bytes.NewReader([]byte(line)))
			dec.DisallowUnknownFields()
			err = dec.Decode(&e)
		} else {
			err = json.Unmarshal([]byte(line), &e)
		}
		if err != nil {
			return nil, fmt.Errorf("malformed line %d: %w", n, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	return events, nil
}
