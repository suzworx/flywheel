package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// No-regression gates (issue #788): a gate marked `gate[vs-base]:` whose own
// reading is a plain failure is measured again at the unit's base commit, in
// a temporary detached worktree, and passes when the unit adds no failure the
// base lacks. The brief's `fail-match:` regexp names the failing-test lines;
// without it only the exit statuses are compared. The base reading is cached
// per (base, command, fail-match) under .flywheel/cache/vs-base/.

// VsBaseReading is a vs-base gate's comparison, recorded as a validated
// event's vs_base object: RC is the unit's own exit code, Failing and
// BaseFailing count the matched lines, New lists (at most 20) unit lines the
// base lacks, and BaseLog is the base run's evidence log (empty when cached).
type VsBaseReading struct {
	Base        string   `json:"base"`
	RC          int      `json:"rc"`
	BaseRC      int      `json:"base_rc"`
	Failing     int      `json:"failing"`
	BaseFailing int      `json:"base_failing"`
	New         []string `json:"new,omitempty"`
	Cached      bool     `json:"cached,omitempty"`
	BaseLog     string   `json:"base_log,omitempty"`
}

// vsBaseSpec is what a vs-base gate needs beyond an ordinary gate: the
// brief's fail-match and the needs-state carried into the base worktree the
// way run --worktree carries it (prepareWorktree). nil marks an ordinary gate.
type vsBaseSpec struct {
	FailMatch               string
	Links, Copies, Installs []string
	SetupTimeout            time.Duration
}

// vsBaseCache is one cached base reading.
type vsBaseCache struct {
	Base      string   `json:"base"`
	Command   string   `json:"command"`
	FailMatch string   `json:"fail_match"`
	RC        int      `json:"rc"`
	Failing   []string `json:"failing"`
}

// vsBaseCacheName is the cache file name of base's reading of command.
func vsBaseCacheName(base, command, failMatch string) string {
	sum := sha256.Sum256([]byte(command + "\x00" + failMatch))
	return base + "-" + hex.EncodeToString(sum[:])[:16] + ".json"
}

// failingLines returns out's lines (CR stripped, trailing space trimmed) that
// re matches, deduplicated and sorted; none when re is nil.
func failingLines(out []byte, re *regexp.Regexp) []string {
	if re == nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimRightFunc(strings.TrimSuffix(l, "\r"), func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' })
		if re.MatchString(l) && !slices.Contains(lines, l) {
			lines = append(lines, l)
		}
	}
	slices.Sort(lines)
	return lines
}

// measureBase returns base's reading of gate: from the cache, else from a run
// in a temporary detached worktree at base with the spec's needs-state
// carried in. why is set, and nothing cached, when the base cannot be
// measured (a setup error or a host-blocked run).
func measureBase(dir, task, attempt, logSuffix, base, gate string, spec *vsBaseSpec, re *regexp.Regexp) (c vsBaseCache, cached bool, baseLog, why string, err error) {
	cacheDir := filepath.Join(dir, ".flywheel", "cache", "vs-base")
	name := vsBaseCacheName(base, gate, spec.FailMatch)
	if b, rerr := os.ReadFile(filepath.Join(cacheDir, name)); rerr == nil && json.Unmarshal(b, &c) == nil && c.Base == base && c.Command == gate && c.FailMatch == spec.FailMatch {
		return c, true, "", "", nil
	}
	tmp, terr := os.MkdirTemp("", "flywheel-vsbase-")
	if terr != nil {
		return c, false, "", "temp dir: " + terr.Error(), nil
	}
	wt := filepath.Join(tmp, "tree")
	defer func() {
		_, _ = gitRead(dir, []string{"worktree", "remove", "--force", wt})
		_ = os.RemoveAll(tmp)
		_, _ = gitRead(dir, []string{"worktree", "prune"})
	}()
	if _, werr := gitRead(dir, []string{"worktree", "add", "--detach", wt, base}); werr != nil {
		return c, false, "", "worktree at base: " + werr.Error(), nil
	}
	if lerr := linkNeedsState(dir, wt, spec.Links); lerr != nil {
		return c, false, "", "needs-state link: " + lerr.Error(), nil
	}
	if _, cerr := copyNeedsState(dir, wt, spec.Copies); cerr != nil {
		return c, false, "", "needs-state copy: " + cerr.Error(), nil
	}
	if len(spec.Installs) > 0 {
		if _, _, ierr := installNeedsState(dir, wt, task, spec.Installs, spec.SetupTimeout); ierr != nil {
			return c, false, "", "needs-state install: " + ierr.Error(), nil
		}
	}
	rc, _, out, stages, rerr := runGateStages(wt, gate, base)
	if rerr != nil {
		return c, false, "", "run on base: " + rerr.Error(), nil
	}
	if strings.Contains(string(out), hostBlocked) {
		if rc, _, out, stages, rerr = runGateStages(wt, gate, base); rerr != nil {
			return c, false, "", "run on base: " + rerr.Error(), nil
		}
	}
	baseLog = ".flywheel/evidence/" + task + "/" + attempt + "/gate-" + logSuffix + ".base.log"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(baseLog)), out, 0o644); err != nil {
		return c, false, "", "", fmt.Errorf("write %s: %w", baseLog, err)
	}
	if strings.Contains(string(out), hostBlocked) {
		return c, false, baseLog, "host-blocked on base: " + hostBlockNote(out), nil
	}
	if _, status, masked := maskedStage(rc, stages); masked {
		rc = status
	}
	c = vsBaseCache{Base: base, Command: gate, FailMatch: spec.FailMatch, RC: rc, Failing: failingLines(out, re)}
	b, _ := json.MarshalIndent(c, "", "  ")
	if err := atomicWrite(cacheDir, name, "vsbase-*.json", append(b, '\n')); err != nil {
		return c, false, baseLog, "", err
	}
	return c, false, baseLog, "", nil
}

// compareVsBase judges a vs-base gate whose own reading failed plainly with
// rc and out. With fail-match it passes when the base fails too, the unit's
// failing set is non-empty (a failure no line names, a build break, never
// passes) and every unit line fails on base; without it, when the base fails.
// note is the reading's note either way; reading is nil when the base could
// not be measured.
func compareVsBase(dir, task, attempt, logSuffix, base, gate string, rc int, out []byte, spec *vsBaseSpec) (pass bool, reading *VsBaseReading, note string, err error) {
	if base == "" {
		return false, nil, "vs-base: no unit base recorded", nil
	}
	var re *regexp.Regexp
	if spec.FailMatch != "" {
		if re, err = regexp.Compile(spec.FailMatch); err != nil {
			return false, nil, "vs-base: fail-match does not compile: " + err.Error(), nil
		}
	}
	c, cached, baseLog, why, err := measureBase(dir, task, attempt, logSuffix, base, gate, spec, re)
	if err != nil {
		return false, nil, "", err
	}
	short := base[:min(12, len(base))]
	if why != "" {
		return false, nil, "vs-base: base " + short + " could not be measured: " + why, nil
	}
	unit := failingLines(out, re)
	var fresh []string
	for _, l := range unit {
		if !slices.Contains(c.Failing, l) {
			fresh = append(fresh, l)
		}
	}
	reading = &VsBaseReading{Base: base, RC: rc, BaseRC: c.RC, Failing: len(unit), BaseFailing: len(c.Failing),
		New: fresh[:min(20, len(fresh))], Cached: cached, BaseLog: baseLog}
	switch {
	case c.RC == 0:
		return false, reading, "vs-base " + short + ": base passes", nil
	case re == nil:
		return true, reading, fmt.Sprintf("passed vs base %s: base fails too (rc %d)", short, c.RC), nil
	case len(unit) == 0:
		return false, reading, "vs-base " + short + ": no fail-match line in the output", nil
	case len(fresh) > 0:
		return false, reading, fmt.Sprintf("%d new failing vs base %s: %s", len(fresh), short, strings.Join(fresh[:min(3, len(fresh))], "; ")), nil
	}
	return true, reading, fmt.Sprintf("passed vs base %s: %d failing on base too", short, len(c.Failing)), nil
}

// vsBasePasses returns, per gate id in order of first appearance, the task's
// latest validated event on tree when it is a vs-base pass (Reason "vs-base"
// with a vs_base reading): a later plain pass or failure of the same gate on
// the same tree supersedes it.
func vsBasePasses(events []Event, task, tree string) []Event {
	var order []string
	latest := map[string]Event{}
	for _, e := range events {
		if e.Task != task || e.Kind != "validated" || e.Tree != tree || tree == "" {
			continue
		}
		if _, ok := latest[e.Gate]; !ok {
			order = append(order, e.Gate)
		}
		latest[e.Gate] = e
	}
	var out []Event
	for _, g := range order {
		if e := latest[g]; e.Reason == "vs-base" && e.VsBase != nil {
			out = append(out, e)
		}
	}
	return out
}

// vsBaseFailing sums the failing lines of the vs-base passes on tree: the
// debt the unit inherited from its base.
func vsBaseFailing(events []Event, task, tree string) int {
	n := 0
	for _, e := range vsBasePasses(events, task, tree) {
		n += e.VsBase.Failing
	}
	return n
}

// VsBaseSummary returns one line per vs-base pass on the tree of the task's
// latest inspected pass, nil when it has none (issue #788).
func VsBaseSummary(dir, task string) ([]string, error) {
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	tree, found := "", false
	for _, e := range events {
		if e.Task == task && e.Kind == "inspected" && e.Verdict == "pass" {
			tree, found = e.Tree, true
		}
	}
	if !found {
		return nil, nil
	}
	var lines []string
	for _, e := range vsBasePasses(events, task, tree) {
		v := e.VsBase
		lines = append(lines, fmt.Sprintf("gate %s passed vs base %s: %d failing on base too (%d on base)", e.Gate, v.Base[:min(12, len(v.Base))], v.Failing, v.BaseFailing))
	}
	return lines, nil
}
