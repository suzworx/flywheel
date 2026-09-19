package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ValidateOptions configures one validation pass.
type ValidateOptions struct {
	Dir     string // flywheel root; default "."
	Workdir string // git working tree the gates run in; default Dir
	// Carry lists repo-relative paths (from the repeatable --carry flag)
	// copied from Dir into Workdir before the gates run, satisfying the
	// brief's declared needs-state: paths in an isolated Workdir (#136).
	Carry []string
	// Live runs the brief's declared live-gate: lines too, after the
	// ordinary gates: the lead's verification pass, never a worker's own
	// mocked run (issue #152).
	Live bool
}

// GateOut reports one gate run.
type GateOut struct {
	Gate        string
	Command     string
	RC          int
	DurationMS  int64
	LogPath     string
	HostBlocked bool
	// Inconclusive is true when the gate failed for a reason attributable
	// entirely to a changed path outside the unit's owns (issue #162): another
	// unit's half-written file, not this unit's own work.
	Inconclusive bool
	// Note carries the persistent-host-block message when HostBlocked is true
	// because the rerun was blocked too, or the "blocked by <paths>" message
	// when Inconclusive is true; empty otherwise.
	Note string
	// Live is true for a live-gate: entry (issue #152), false for an
	// ordinary gate: entry.
	Live bool
}

// FileShape reports one changed file's measured shape: its repo-relative path,
// its line count and, for Markdown files only, the number of ATX heading lines
// (a line whose first non-space characters are one to six '#' followed by a
// space). It is a reading, not a gate: a short file is a fact the lead can see
// at a glance, never an error (issue #130). Headings is omitted from JSON when
// zero.
type FileShape struct {
	Path     string `json:"path"`
	Lines    int    `json:"lines"`
	Headings int    `json:"headings,omitempty"`
}

// GaugeResult reports a full validation pass: the tree hash, one entry per
// gate, and the paths found outside owns.
type GaugeResult struct {
	Tree       string
	Attempt    string
	BriefPaths []string // the base brief, then a delta when the attempt has one
	Gates      []GateOut
	Outside    []string
	// Attributed lists, sorted, "<path> -> <task>" entries for every changed
	// path that sits outside this task's owns but inside another task's owns
	// while that task is in flight (issue #117), and, for a changed path in
	// another worktree that an in-flight task there owns,
	// "<worktree path>: <path> -> <task>" (issue #200): the lead's
	// stray-file review can skip it, but it never counts toward OwnsOK's
	// outside set. A path no in-flight task owns but a lead_edit event covers
	// is attributed "<path> -> lead <session>" instead (issue #228), and the
	// claim covers it only while the path's content still hashes to the value
	// the claim recorded (issue #258).
	Attributed []string
	// Files lists the measured shape of every changed path inside the unit's
	// owns, sorted by path; paths outside owns and flywheel's own bookkeeping
	// are never measured (issue #130). A reading, never a gate.
	Files   []FileShape
	GatesOK bool
	OwnsOK  bool
	// LiveDeclared is how many live-gate: lines the brief declared, set
	// whether or not this pass ran them (issue #152).
	LiveDeclared int
	// LiveRun is true when this pass ran the declared live gates (Live was
	// set on ValidateOptions).
	LiveRun bool
	// Refused carries the needs-state refusal message (issue #136) when
	// validation stopped before running any gate because a brief-declared
	// needs-state: path was neither present in an isolated Workdir nor
	// carried; Gates, Outside and Attributed are all empty in that case.
	Refused string
}

// OK reports whether the whole pass succeeds: every gate passed and nothing
// sits outside owns.
func (r GaugeResult) OK() bool {
	return r.Refused == "" && r.GatesOK && r.OwnsOK
}

// hostBlocked is the Windows Smart App Control message that intermittently
// blocks a freshly built unsigned binary. It is a host problem, not a code
// failure; the gate is rerun once before giving up.
const hostBlocked = "An Application Control policy has blocked this file"

// hostBlockNote builds the note recorded on a validated event and printed by
// `flywheel validate` when a gate's rerun is blocked too: naming the file
// Smart App Control rejected (empty when the output does not parse) and
// pointing at the compile-then-run gate form or CI, never the security
// setting, because rerunning again cannot help.
func hostBlockNote(out []byte) string {
	return "persistent host block: " + parseBlockedFile(out) +
		"; compile then run (go test -c -o <dir>/x.test.exe <pkg> && <dir>/x.test.exe) or run the gate in CI"
}

// parseBlockedFile extracts the path named in a host-block message: the text
// between "fork/exec " and ": An Application Control policy has blocked this
// file" (first occurrence). Empty when it does not parse.
func parseBlockedFile(out []byte) string {
	s := string(out)
	start := strings.Index(s, "fork/exec ")
	if start < 0 {
		return ""
	}
	start += len("fork/exec ")
	suffix := ": " + hostBlocked
	end := strings.Index(s[start:], suffix)
	if end < 0 {
		return ""
	}
	return s[start : start+end]
}

// ValidateTask loads the task's planned brief, hashes the exact tree with a
// throwaway git index, runs each declared gate with bash (cmd /C on Windows,
// sh elsewhere), records one validated event per gate, checks the owns
// boundary, and records an owns_checked event. The shared git index and
// working tree are never touched.
func ValidateTask(dir, task string, o ValidateOptions) (GaugeResult, error) {
	if o.Dir == "" {
		o.Dir = "."
	}
	events, err := ReadEvents(o.Dir)
	if err != nil {
		return GaugeResult{}, err
	}
	wd := o.Workdir
	if wd == "" {
		if recorded := recordedWorkdir(events, task); recorded != "" {
			wd = recorded
		} else {
			wd = o.Dir
		}
	}
	// A relative --workdir names a place against the process's current
	// directory; normalise it (and the comparison against dir) to absolute form
	// so the "differs from dir" test below and the recorded provenance both
	// stay true when the ledger is read from elsewhere (issue #244).
	wd = absPath(wd)
	header, briefPaths, err := AttemptBrief(o.Dir, events, task)
	if err != nil {
		return GaugeResult{}, err
	}
	if len(header.Gates) == 0 {
		return GaugeResult{}, fmt.Errorf("brief %s declares no gate: lines; add a `gate:` line to the brief header", briefPaths[0])
	}
	// An isolated Workdir does not hold machine state outside the repo (a
	// database, a local stack, git-ignored env files); with no --workdir the
	// tree is the repo and needs-state: is satisfied by definition (#136).
	if !samePath(wd, o.Dir) {
		if err := carryPaths(o.Dir, wd, o.Carry); err != nil {
			return GaugeResult{}, err
		}
		if refusal := missingNeedsState(wd, header.NeedsState); refusal != "" {
			return GaugeResult{Refused: refusal}, nil
		}
	}
	attempt := ""
	for _, e := range events {
		if e.Task == task && e.Attempt != "" {
			attempt = e.Attempt
		}
	}
	if attempt == "" {
		attempt = "r1"
	}
	tree, err := treeHash(wd)
	if err != nil {
		return GaugeResult{}, err
	}
	evDir := filepath.Join(o.Dir, ".flywheel", "evidence", task, attempt)
	if err := os.MkdirAll(evDir, 0o755); err != nil {
		return GaugeResult{}, fmt.Errorf("create %s: %w", evDir, err)
	}

	var res GaugeResult
	res.Tree = tree
	res.Attempt = attempt
	res.BriefPaths = briefPaths
	res.GatesOK = true

	for i, gate := range header.Gates {
		n := strconv.Itoa(i + 1)
		// HEAD is resolved per reading, not hoisted above the loop: a gate can
		// take minutes and another process can move HEAD meanwhile, so
		// resolving once per pass would record a history position no reading
		// was taken at (issue #240).
		commit := headCommit(wd)
		out, err := runAndRecordGate(o.Dir, wd, task, attempt, tree, commit, header.Owns, n, n, gate, false)
		if err != nil {
			return GaugeResult{}, err
		}
		res.Gates = append(res.Gates, out)
		if out.RC != 0 || out.HostBlocked {
			res.GatesOK = false
		}
	}
	res.LiveDeclared = len(header.LiveGates)
	res.LiveRun = o.Live
	if o.Live {
		for i, gate := range header.LiveGates {
			n := strconv.Itoa(i + 1)
			// same per-reading resolution as the ordinary gates: HEAD moves
			// between readings, so each live gate carries the commit current
			// at the moment it ran, never one hoisted from the pass start
			// (issue #240).
			commit := headCommit(wd)
			out, err := runAndRecordGate(o.Dir, wd, task, attempt, tree, commit, header.Owns, "live"+n, "live-"+n, gate, true)
			if err != nil {
				return GaugeResult{}, err
			}
			res.Gates = append(res.Gates, out)
			if out.RC != 0 || out.HostBlocked {
				res.GatesOK = false
			}
		}
	}
	// resolved right before the owns check, at the moment THIS reading is
	// taken, for the same reason the gates resolve per reading: the check runs
	// after every gate, so hoisting the resolution to the pass start would
	// record a history position no reading was taken at (issue #240).
	commit := headCommit(wd)
	return finishValidate(o.Dir, wd, task, attempt, tree, commit, header.Owns, header.NeedsState, events, res)
}

// runAndRecordGate runs one declared gate — ordinary or live — through the
// path every gate shares: runGate, a rerun when the host blocks the first
// attempt, the evidence log write at
// ".flywheel/evidence/<task>/<attempt>/gate-<logSuffix>.log", and a
// validated event carrying gateID as its Gate field. live sets GateOut.Live
// (issue #152); the recorded reading is otherwise identical to an ordinary
// gate's.
func runAndRecordGate(dir, wd, task, attempt, tree, commit string, owns []string, gateID, logSuffix, gate string, live bool) (GateOut, error) {
	logRel := ".flywheel/evidence/" + task + "/" + attempt + "/gate-" + logSuffix + ".log"
	logPath := filepath.Join(dir, logRel)
	rc, dur, out, err := runGate(wd, gate)
	if err != nil {
		return GateOut{}, err
	}
	blocked := strings.Contains(string(out), hostBlocked)
	if blocked {
		rc2, dur2, out2, err2 := runGate(wd, gate)
		if err2 != nil {
			return GateOut{}, err2
		}
		rc, dur, out = rc2, dur2, out2
		blocked = strings.Contains(string(out), hostBlocked)
	}
	if err := os.WriteFile(logPath, out, 0o644); err != nil {
		return GateOut{}, fmt.Errorf("write %s: %w", logPath, err)
	}
	sum := sha256.Sum256(out)
	rcPtr := new(int)
	*rcPtr = rc
	ev := Event{
		TS: "", Task: task, Kind: "validated", Attempt: attempt,
		Gate: gateID, Command: gate, Tree: tree, Commit: commit, RC: rcPtr,
		DurationMS: dur, SHA256: hex.EncodeToString(sum[:]), Path: logRel,
		Persona: "supervisor", Workdir: workdirField(wd, dir),
	}
	var note string
	inconclusive := false
	if blocked {
		ev.Reason = "host-blocked"
		note = hostBlockNote(out)
		ev.Note = note
	} else if rc != 0 {
		if paths := inconclusivePaths(wd, owns, out); len(paths) > 0 {
			inconclusive = true
			note = inconclusiveNote(paths)
			ev.Reason = "inconclusive"
			ev.Note = note
		}
	}
	if err := AppendEvent(dir, ev); err != nil {
		return GateOut{}, err
	}
	return GateOut{
		Gate: gateID, Command: gate, RC: rc, DurationMS: dur,
		LogPath: logRel, HostBlocked: blocked, Inconclusive: inconclusive, Note: note,
		Live: live,
	}, nil
}

// finishValidate runs the owns check, records owns_checked, refreshes derived
// state, and returns the result. A changed path outside owns is excused when
// it was in the dispatched baseline and its content is unchanged: the lead or
// another worker left it dirty before this unit started. A path matching a
// declared needs-state: entry is excused the same way flywheel's own
// bookkeeping is (issue #136): it is machine state the harness carried in,
// never the unit's own work, whether or not this run actually carried it.
// When the task's dispatched event carries a worktrees snapshot (issue #87),
// every other worktree's current changed paths are compared against it too:
// a path that is new or whose sha changed is never excused by this task's
// own owns list, except flywheel's own bookkeeping (issue #186), which that
// worktree's factory view rewrites on every state refresh and is never
// anyone's work. Such a path is outside, listed as "<worktree path>:
// <path>", unless an in-flight task in that worktree's own event log owns it
// (issue #200): then it is attributed as "<worktree path>: <path> ->
// <task>" instead. A candidate path no in-flight task owns is attributed
// "<path> -> lead <session>" when a lead_edit event declared for it by a
// non-worker session before this reading covers it (issue #228): the lead's
// own mid-wave edit, declared after the fact, never a worker's stray. A
// claim covers p only while p's current content still hashes to the value
// recorded on the event (or the path is still absent, for a deletion
// marker): the lead declared that edit, not the file forever (issue #258).
func finishValidate(dir, wd, task, attempt, tree, commit string, owns, needsState []string, events []Event, res GaugeResult) (GaugeResult, error) {
	changed, err := unitChangedPaths(wd, dispatchBase(events, task, attempt), task)
	if err != nil {
		return GaugeResult{}, err
	}
	base := baselineFor(events, task)
	var candidates []string
	var baselined []string
	for _, p := range changed {
		if !ownsContains(owns, p) && !ownsContains(needsState, p) {
			if bh, ok := base[p]; ok && fileSHA(wd, p) == bh {
				baselined = append(baselined, p)
				continue
			}
			candidates = append(candidates, p)
		}
	}
	// Re-read the log here, immediately before the attribution: gates take
	// minutes, and a lead_edit appended while they ran must qualify for this
	// reading (issue #258). The fresh slice feeds the claim lookup only; the
	// snapshot passed in stays authoritative for the attempt and brief
	// resolution above, which must be stable across the pass.
	fresh, err := ReadEvents(dir)
	if err != nil {
		return GaugeResult{}, fmt.Errorf("re-read %s before owns check: %w", dir, err)
	}
	reading := now()
	attributed, outside := attributeOutside(dir, task, wd, events, fresh, candidates, reading)
	if snap := worktreesFor(events, task); snap != nil {
		wtPaths := make([]string, 0, len(snap))
		for p := range snap {
			wtPaths = append(wtPaths, p)
		}
		sort.Strings(wtPaths)
		for _, wtPath := range wtPaths {
			wtBase := snap[wtPath]
			wtChanged, err := changedPaths(wtPath)
			if err != nil {
				continue
			}
			for _, p := range wtChanged {
				if isFlywheelOwnPath(p) {
					continue
				}
				if bh, ok := wtBase[p]; !ok || fileSHA(wtPath, p) != bh {
					if owner := worktreeOwner(wtPath, p, reading); owner != "" {
						attributed = append(attributed, wtPath+": "+p+" -> "+owner)
						continue
					}
					outside = append(outside, wtPath+": "+p)
				}
			}
		}
	}
	sort.Strings(attributed)
	res.Outside = outside
	res.Attributed = attributed
	res.OwnsOK = len(outside) == 0
	res.Files = measureFiles(wd, owns, changed)
	if err := AppendEvent(dir, Event{
		TS: "", Task: task, Kind: "owns_checked", Attempt: attempt,
		Tree: tree, Commit: commit, Outside: outside, Baselined: baselined, Attributed: attributed,
		Files: res.Files, Persona: "supervisor", Workdir: workdirField(wd, dir),
	}); err != nil {
		return GaugeResult{}, err
	}
	_, _ = WriteState(dir)
	return res, nil
}

// inFlightOwners returns, sorted, every task id other than task whose derived
// status (Derive) is dispatched, running or finished: still working, so a
// path it owns is not blamed on task. landed, passed and rejected (and every
// other status) are excluded.
func inFlightOwners(events []Event, task string) []string {
	st := Derive(events)
	var owners []string
	for _, ts := range st.Tasks {
		if ts.ID == task {
			continue
		}
		switch ts.Status {
		case "dispatched", "running", "finished":
			owners = append(owners, ts.ID)
		}
	}
	sort.Strings(owners)
	return owners
}

// attributeOutside splits changed paths already known to sit outside task's
// own owns (and not excused by the baseline) into attributed entries, sorted
// "<path> -> <task>", and the paths that remain outside because no in-flight
// task's brief owns them (issue #117). A path no in-flight task owns is
// attributed "<path> -> lead <session>" instead when a lead_edit event covers
// it and its claiming session and time both pass the guards below (issue
// #228). Owners are tried in sorted order, so a path two in-flight tasks both
// claim attributes to the alphabetically first. A task whose brief cannot be
// read is skipped rather than erroring: an unreadable brief is never treated
// as an owner. events is the pass's snapshot, kept stable for the owner and
// brief resolution; fresh is the log re-read right before this reading, used
// for the lead_edit claim lookup only, so a claim appended while the gates
// ran still qualifies (issue #258). reading is the moment this owns check
// runs: a lead_edit only covers readings taken strictly after it.
func attributeOutside(dir, task, wd string, events, fresh []Event, candidates []string, reading time.Time) (attributed, outside []string) {
	if len(candidates) == 0 {
		return nil, nil
	}
	owners := inFlightOwners(events, task)
	for _, p := range candidates {
		owner := ""
		for _, other := range owners {
			header, _, err := AttemptBrief(dir, events, other)
			if err != nil {
				continue
			}
			if ownsContains(header.Owns, p) {
				owner = other
				break
			}
		}
		if owner == "" {
			if lead := leadClaimingSession(wd, fresh, task, p, reading); lead != "" {
				owner = "lead " + lead
			}
		}
		if owner == "" {
			outside = append(outside, p)
			continue
		}
		attributed = append(attributed, p+" -> "+owner)
	}
	sort.Strings(attributed)
	return attributed, outside
}

// leadClaimingSession returns the session of the lead_edit event covering
// path p for this reading, or "" when no claim does. Three guards, all
// required (issues #228, #258): the declaring session must not be a worker
// session of the task being validated (workerSessionOf, the same worker-event
// set sessionClash uses for T4); the claim's TS must be strictly before the
// reading's own time — a claim never retroactively blesses a stray an earlier
// validation already reported; and the claim must still be bound to the
// path's content — e.Baseline[p] records the sha256 claim-edit read at claim
// time (or "deleted" when the path was absent then), and p is covered only
// while fileSHA(wd, p) still equals it. A claim with no recorded hash for p
// binds nothing and is ignored: the lead declared one edit, not a permanent
// exemption for the path (issue #258). Matching reuses ownsContains, the same
// rule the sibling-task attribution uses. When several claims cover p, the
// most recent one wins.
func leadClaimingSession(wd string, events []Event, task, p string, reading time.Time) string {
	sess := ""
	for _, e := range events {
		if e.Kind != "lead_edit" || !ownsContains(e.Owns, p) {
			continue
		}
		want, ok := e.Baseline[p]
		if !ok || fileSHA(wd, p) != want {
			continue
		}
		if workerSessionOf(task, events, e.Session) {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.TS)
		if err != nil || !t.Before(reading) {
			continue
		}
		sess = e.Session
	}
	return sess
}

// workerSessionOf reports whether sess is a worker session of task: it wrote
// one of the task's started, finished, dispatched, report or worker_plan
// events — the same worker-event set sessionClash uses for T4.
func workerSessionOf(task string, events []Event, sess string) bool {
	for _, e := range events {
		if e.Task == task && e.Session == sess {
			switch e.Kind {
			case "started", "finished", "dispatched", "report", "worker_plan":
				return true
			}
		}
	}
	return false
}

// unlandedOwners returns, sorted, every task in a sibling worktree's own
// ledger that has been dispatched and has not landed: until it lands, a unit
// that ran owns the uncommitted files in its own worktree — including edits
// made after it passed inspection — so they are never charged to a unit
// validating elsewhere (issue #278). A task that was only planned never ran,
// so it accounts for nothing; a landed task no longer owns anything.
func unlandedOwners(events []Event) []string {
	dispatched := map[string]bool{}
	for _, e := range events {
		if e.Kind == "dispatched" {
			dispatched[e.Task] = true
		}
	}
	st := Derive(events)
	var owners []string
	for _, ts := range st.Tasks {
		if dispatched[ts.ID] && ts.Status != "landed" {
			owners = append(owners, ts.ID)
		}
	}
	sort.Strings(owners)
	return owners
}

// worktreeOwner reports the task in worktree W whose brief owns the changed
// path p (relative to W) and which was dispatched and has not landed, or ""
// when no such task there owns it (issue #200, #278). It reads W's own event
// log — that worktree's own record of what it is building — so the
// attribution is trustworthy. An unreadable sibling log is never an error: it attributes
// nothing. Owners are tried in unlandedOwners' sorted order, matching
// attributeOutside, and a task whose brief cannot be read is skipped, never
// treated as an owner. A path no brief owner claims is attributed "lead <session>"
// when a lead_edit event covers it in W's ledger, the claim's session is not a
// worker session of that owner, the claim predates the reading, and the path's
// current content still hashes to the claim's Baseline (issue #339). A claim
// counts only while W still has a dispatched, unlanded unit — a claim in a
// worktree with nothing in flight excuses nothing.
func worktreeOwner(W, p string, reading time.Time) string {
	wEvents, err := ReadEvents(W)
	if err != nil {
		return ""
	}
	owners := unlandedOwners(wEvents)
	for _, other := range owners {
		header, _, err := AttemptBrief(W, wEvents, other)
		if err != nil {
			continue
		}
		if ownsContains(header.Owns, p) {
			return other
		}
	}
	for _, other := range owners {
		if s := leadClaimingSession(W, wEvents, other, p, reading); s != "" {
			return "lead " + s
		}
	}
	return ""
}

// worktreesFor returns the first dispatched event's worktrees snapshot for
// the task, or nil when that event carries none. Mirrors baselineFor: a
// later dispatched event's snapshot never widens or replaces the first.
func worktreesFor(events []Event, task string) map[string]map[string]string {
	for _, e := range events {
		if e.Task == task && e.Kind == "dispatched" {
			return e.Worktrees
		}
	}
	return nil
}

// treeHash returns the SHA-1 tree id of wd computed with a temporary index
// file, so the shared git index is never mutated: git read-tree HEAD, git
// add -A, git write-tree, then the temp index is removed.
func treeHash(wd string) (string, error) {
	tmp, err := os.CreateTemp("", "fw-index-*")
	if err != nil {
		return "", fmt.Errorf("create temp index: %w", err)
	}
	idx := tmp.Name()
	tmp.Close()
	os.Remove(idx)
	defer os.Remove(idx)
	if _, err := gitRun(wd, idx, []string{"read-tree", "HEAD"}); err != nil {
		return "", err
	}
	if _, err := gitRun(wd, idx, []string{"add", "-A"}); err != nil {
		return "", err
	}
	// flywheel's own bookkeeping (flywheel.md and .flywheel/) is never part of
	// a unit's tree: validate writes evidence and refreshes state after
	// measuring, so including them would change the hash between validate and
	// inspect and no reading could ever match its tree.
	if _, err := gitRun(wd, idx, []string{"rm", "-r", "--cached", "--ignore-unmatch", "--", ".flywheel", "flywheel.md"}); err != nil {
		return "", err
	}
	out, err := gitRun(wd, idx, []string{"write-tree"})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// gitRun runs git with the throwaway index, returning stdout only. Its stdout
// is data (write-tree); stderr is kept for error messages. A failing git call
// surfaces as an error naming the command.
func gitRun(wd, idx string, args []string) (string, error) {
	rc, stdout, stderr, err := runCmdSplit(wd, gitArgs(args), append(os.Environ(), "GIT_INDEX_FILE="+idx))
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	if rc != 0 {
		return "", fmt.Errorf("git %v failed (rc=%d): %s", args, rc, strings.TrimSpace(string(append(stderr, stdout...))))
	}
	return string(stdout), nil
}

// gitArgs prefixes git onto a subcommand's arguments.
func gitArgs(args []string) []string {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, "git")
	for _, a := range args {
		argv = append(argv, a)
	}
	return argv
}

// runGate runs one gate command through bash -c when bash is on PATH, cmd /C
// on Windows, or sh -c elsewhere. It returns the exit code, elapsed time and
// combined output; a spawn failure (not an exit) is an error.
func runGate(wd, command string) (rc int, durMS int64, out []byte, err error) {
	var argv []string
	if _, berr := exec.LookPath("bash"); berr == nil {
		argv = []string{"bash", "-c", command}
	} else if runtime.GOOS == "windows" {
		argv = []string{"cmd", "/C", command}
	} else {
		argv = []string{"sh", "-c", command}
	}
	t0 := time.Now()
	grc, gout, gerr := runCmd(wd, argv, nil)
	dur := time.Since(t0).Milliseconds()
	if gerr != nil {
		return 0, dur, nil, gerr
	}
	return grc, dur, gout, nil
}

// runCmd runs argv in wd with the given environment (nil inherits the caller's
// environment) and returns the exit code and mixed output. Gates keep combined
// output because that is their log; data-bearing git commands use runCmdSplit.
// A spawn failure surfaces as an error.
func runCmd(wd string, argv, env []string) (rc int, out []byte, err error) {
	rc, stdout, stderr, err := runCmdSplit(wd, argv, env)
	if err != nil {
		return rc, nil, err
	}
	return rc, append(stdout, stderr...), nil
}

// runCmdSplit runs argv in wd and returns the exit code and stdout and stderr
// separately, so git commands whose stdout is data can ignore stderr chatter
// (for example git's "LF will be replaced by CRLF" warning on Windows with
// core.autocrlf). A spawn failure surfaces as an error.
func runCmdSplit(wd string, argv, env []string) (rc int, stdout, stderr []byte, err error) {
	cmd := exec.Command(argv[0])
	cmd.Args = argv
	cmd.Dir = wd
	if env != nil {
		cmd.Env = env
	}
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return 0, nil, nil, fmt.Errorf("start %v: %w", argv, err)
	}
	// Wait returns an error on a nonzero exit; the exit code is read from
	// ProcessState, so the error is expected and ignored.
	_ = cmd.Wait()
	rc = 0
	if cmd.ProcessState != nil {
		rc = cmd.ProcessState.ExitCode()
	}
	return rc, outBuf.Bytes(), errBuf.Bytes(), nil
}

// unitChangedPaths lists the paths the unit changed since its dispatch base
// (issue #332): everything changedPaths reports (uncommitted and untracked),
// plus the paths the branch's own commits since base touched, walking the
// first-parent chain base..HEAD commit by commit (#338 review):
//   - a commit whose Flywheel-Task trailer names other units only is theirs
//     and is skipped (units sharing one branch);
//   - an ordinary commit contributes every path it adds, changes or deletes,
//     with rename detection off so a renamed source counts as deleted;
//   - a merge commit contributes only what differs from all its parents
//     (git diff-tree --cc: conflict resolutions and evil merges), never the
//     files the merged branch brought in.
//
// Paths come NUL-delimited (-z), so git never quotes an unusual name. An
// empty base, or a base git cannot use, falls back to changedPaths alone.
func unitChangedPaths(wd, base, task string) ([]string, error) {
	changed, err := changedPaths(wd)
	if err != nil {
		return nil, err
	}
	if base == "" {
		return changed, nil
	}
	revs, err := gitRead(wd, []string{"rev-list", "--first-parent", "--parents", base + "..HEAD"})
	if err != nil {
		return changed, nil
	}
	seen := make(map[string]bool, len(changed))
	result := append([]string(nil), changed...)
	for _, p := range changed {
		seen[p] = true
	}
	add := func(out string) {
		for _, p := range strings.Split(out, "\x00") {
			if p = strings.TrimSpace(p); p != "" && !seen[p] {
				seen[p] = true
				result = append(result, filepath.ToSlash(p))
			}
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(revs), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		sha := fields[0]
		if owners, err := gitRead(wd, []string{"log", "-1", "--format=%(trailers:key=Flywheel-Task,valueonly,separator=%x2C)", sha}); err == nil {
			if names := strings.TrimSpace(owners); names != "" {
				mine := false
				for _, n := range strings.Split(names, ",") {
					if strings.TrimSpace(n) == task {
						mine = true
					}
				}
				if !mine {
					continue
				}
			}
		}
		var out string
		if len(fields) > 2 { // a merge: sha plus two or more parents
			out, err = gitRead(wd, []string{"diff-tree", "-r", "-z", "--cc", "--name-only", "--no-commit-id", sha})
		} else {
			out, err = gitRead(wd, []string{"diff-tree", "-r", "-z", "--no-renames", "--name-only", "--no-commit-id", "--root", sha})
		}
		if err == nil {
			add(out)
		}
	}
	return result, nil
}

// dispatchBase returns the unit's base: the Base of the task's FIRST
// dispatched event that recorded one, or "". The owns check covers the whole
// unit — every attempt shares its owns — so a correction attempt's check
// still counts what an earlier attempt committed; this matches baselineFor,
// which also reads the first dispatch. attempt is unused and kept for the
// call site's clarity.
func dispatchBase(events []Event, task, attempt string) string {
	_ = attempt
	for _, e := range events {
		if e.Task == task && e.Kind == "dispatched" && e.Base != "" {
			return e.Base
		}
	}
	return ""
}

// changedPaths lists every path that differs from HEAD plus untracked files,
// using read-only git commands. Paths are normalised to forward slashes.
func changedPaths(wd string) ([]string, error) {
	var paths []string
	// NUL-delimited and rename detection off (#338 review): exact names, and
	// a renamed file's source is reported as deleted.
	diff, err := gitRead(wd, []string{"diff", "-z", "--no-renames", "--name-only", "HEAD"})
	if err != nil {
		return nil, err
	}
	untracked, err := gitRead(wd, []string{"ls-files", "-z", "--others", "--exclude-standard"})
	if err != nil {
		return nil, err
	}
	for _, out := range []string{diff, untracked} {
		for _, p := range strings.Split(out, "\x00") {
			if p != "" {
				paths = append(paths, filepath.ToSlash(p))
			}
		}
	}
	return paths, nil
}

// gitRead runs a read-only git command and returns its stdout, or an error if
// the command fails. Stdout is data (diff, ls-files); stderr chatter is never
// parsed as paths.
func gitRead(wd string, args []string) (string, error) {
	rc, stdout, stderr, err := runCmdSplit(wd, gitArgs(args), nil)
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	if rc != 0 {
		return "", fmt.Errorf("git %v failed (rc=%d): %s", args, rc, strings.TrimSpace(string(append(stderr, stdout...))))
	}
	return string(stdout), nil
}

// headCommit returns the workdir's current HEAD commit id, or "" when HEAD
// cannot be resolved: a repo with no commits yet, or git failing for any
// reason. A reading without a commit is still a valid reading; the field is
// additional evidence, never a precondition (issue #196).
func headCommit(wd string) string {
	out, err := gitRead(wd, []string{"rev-parse", "HEAD"})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// fileSHA returns the SHA-256 hex of the file p inside wd, or "deleted"
// when the file no longer exists.
func fileSHA(wd, p string) string {
	b, err := os.ReadFile(filepath.Join(wd, filepath.FromSlash(p)))
	if err != nil {
		return "deleted"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// computeBaseline hashes every path changedPaths lists (the same read-only
// git commands the owns check uses) at this moment, for recording on the
// dispatched event. Not a git repo: no baseline.
func computeBaseline(wd string) map[string]string {
	paths, err := changedPaths(wd)
	if err != nil {
		return map[string]string{}
	}
	base := map[string]string{}
	for _, p := range paths {
		base[p] = fileSHA(wd, p)
	}
	return base
}

// baselineFor returns the first dispatched event's baseline for the task,
// whatever its size: an empty map when that event has none. Later dispatched
// events never widen the excuse set.
func baselineFor(events []Event, task string) map[string]string {
	for _, e := range events {
		if e.Task == task && e.Kind == "dispatched" {
			return e.Baseline
		}
	}
	return nil
}

// isFlywheelOwnPath reports whether p is one of flywheel's own bookkeeping
// paths: flywheel.md at the root, or anything under .flywheel/. These are
// never a unit's own work, in this worktree or any other (issue #186).
func isFlywheelOwnPath(p string) bool {
	p = filepath.ToSlash(p)
	if p == "flywheel.md" {
		return true
	}
	return p == ".flywheel" || strings.HasPrefix(p, ".flywheel/")
}

// ownsContains reports whether a changed path is inside the owns boundary.
// flywheel's own files (flywheel.md at the root and anything under .flywheel/)
// are never outside. A path is inside when it equals an owns entry, or starts
// with an entry ending in '/', or matches an entry as a shell pattern.
func ownsContains(owns []string, p string) bool {
	p = filepath.ToSlash(p)
	if isFlywheelOwnPath(p) {
		return true
	}
	for _, o := range owns {
		o = filepath.ToSlash(o)
		if o == p {
			return true
		}
		if strings.HasSuffix(o, "/") && strings.HasPrefix(p, o) {
			return true
		}
		if m, _ := path.Match(o, p); m {
			return true
		}
	}
	return false
}

// isPathDelim splits a gate's output into path tokens on whitespace or ':',
// matching how compilers and test runners print "file.go:12: message".
func isPathDelim(r rune) bool {
	return r == ':' || unicode.IsSpace(r)
}

// inconclusivePaths scans a failing gate's combined output for repo-relative
// path tokens naming a file that exists in wd, and returns the paths outside
// owns that are also currently changed against HEAD (per changedPaths), in
// first-seen order with duplicates removed. It returns nil the moment the
// output names any path inside owns (issue #162): naming an owns path makes
// the failure ordinary, however many outside paths it also names. It also
// returns nil when no outside changed path is named, or changedPaths fails.
func inconclusivePaths(wd string, owns []string, out []byte) []string {
	changed, err := changedPaths(wd)
	if err != nil {
		return nil
	}
	changedSet := make(map[string]bool, len(changed))
	for _, p := range changed {
		changedSet[p] = true
	}
	seen := make(map[string]bool)
	var outside []string
	for _, tok := range strings.FieldsFunc(string(out), isPathDelim) {
		p := filepath.ToSlash(tok)
		if _, err := os.Stat(filepath.Join(wd, filepath.FromSlash(p))); err != nil {
			continue
		}
		if ownsContains(owns, p) {
			return nil
		}
		if changedSet[p] && !seen[p] {
			seen[p] = true
			outside = append(outside, p)
		}
	}
	return outside
}

// inconclusiveNote builds the note for an inconclusive gate reading: "blocked
// by <paths>", comma-joined, at most five paths, the whole note capped at 200
// characters.
func inconclusiveNote(paths []string) string {
	if len(paths) > 5 {
		paths = paths[:5]
	}
	note := "blocked by " + strings.Join(paths, ", ")
	if len(note) > 200 {
		note = note[:200]
	}
	return note
}

// missingNeedsState returns the refusal message for the first declared
// needs-state path (issue #136) not present in wd, or "" when every declared
// path exists (or none are declared). A trailing "/" marks a directory but
// does not change the presence check: os.Stat succeeds for a directory too.
func missingNeedsState(wd string, needsState []string) string {
	for _, p := range needsState {
		rel := strings.TrimSuffix(p, "/")
		if rel == "" {
			continue
		}
		full := filepath.Join(wd, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err != nil {
			return fmt.Sprintf("needs-state %s is not in the workdir; pass --carry %s", p, p)
		}
	}
	return ""
}

// carryPaths copies each carry path from dir into wd before the gates run
// (issue #136), preserving the relative path and creating parent
// directories; a directory is copied recursively. A carried path that does
// not exist under dir is an error naming it.
func carryPaths(dir, wd string, carry []string) error {
	for _, p := range carry {
		rel := strings.TrimSuffix(p, "/")
		src := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("carry %s: not found under %s", p, dir)
		}
		dst := filepath.Join(wd, filepath.FromSlash(rel))
		if err := copyPath(src, dst); err != nil {
			return fmt.Errorf("carry %s: %w", p, err)
		}
	}
	return nil
}

// copyPath copies src to dst: a single file is copied directly, a directory
// is walked and copied recursively, both preserving relative structure and
// creating parent directories as needed.
func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFileMode(src, dst, info.Mode())
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		return copyFileMode(p, target, fi.Mode())
	})
}

// copyFileMode copies src to dst byte for byte with the given file mode,
// creating dst's parent directory and overwriting an existing dst so a
// repeated carry stays idempotent.
func copyFileMode(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// measureFiles returns the measured shape of every changed path inside the
// unit's owns — the same ownsContains the owns check uses — sorted by path.
// Paths outside owns and flywheel's own bookkeeping are never measured; a file
// that cannot be read is skipped rather than erroring (issue #130).
func measureFiles(wd string, owns []string, changed []string) []FileShape {
	var files []FileShape
	for _, p := range changed {
		if !ownsContains(owns, p) || isFlywheelOwnPath(p) {
			continue
		}
		fs, ok := fileShape(wd, p)
		if !ok {
			continue
		}
		files = append(files, fs)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

// fileShape reads p inside wd and returns its measured shape: the line count
// and, for a Markdown file, the number of ATX heading lines. A file that
// cannot be read (deleted, say) reports ok=false so the caller skips it
// rather than erroring.
func fileShape(wd, p string) (FileShape, bool) {
	b, err := os.ReadFile(filepath.Join(wd, filepath.FromSlash(p)))
	if err != nil {
		return FileShape{}, false
	}
	fs := FileShape{Path: p}
	lines := strings.Split(string(b), "\n")
	switch {
	case len(b) == 0:
		// no lines
	case b[len(b)-1] == '\n':
		fs.Lines = len(lines) - 1
	default:
		fs.Lines = len(lines)
	}
	if strings.HasSuffix(strings.ToLower(p), ".md") {
		for _, line := range lines {
			if isATXHeading(line) {
				fs.Headings++
			}
		}
	}
	return fs, true
}

// isATXHeading reports whether line is a Markdown ATX heading line: its first
// non-space characters are one to six '#' followed by a space. The rule is
// deliberately simple and documented — no fenced-code tracking, no full
// Markdown parsing — so a '#' inside a code block still counts only when the
// whole line is a real ATX heading (issue #130).
func isATXHeading(line string) bool {
	line = strings.TrimLeft(line, " \t")
	if line == "" || line[0] != '#' {
		return false
	}
	h := 0
	for h < len(line) && line[h] == '#' {
		h++
	}
	return h >= 1 && h <= 6 && h < len(line) && line[h] == ' '
}
