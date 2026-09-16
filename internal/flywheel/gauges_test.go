package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// git runs a git command in wd with a test identity and CRLF handling off, so
// trees hash the same on every OS. The commit helpers build the args here.
func git(t *testing.T, wd string, args []string) string {
	t.Helper()
	cmd := exec.Command("git")
	cmd.Dir = wd
	full := []string{"git", "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "core.autocrlf=false"}
	for _, a := range args {
		full = append(full, a)
	}
	cmd.Args = full
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// initRepo makes a throwaway git repo in dir with one committed file a.go.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, []string{"init", "-q"})
	git(t, dir, []string{"config", "core.autocrlf", "false"})
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	git(t, dir, []string{"add", "a.go"})
	git(t, dir, []string{"commit", "-m", "init"})
}

// initTask makes a flywheel dir with a git repo, a committed a.go owned by the
// task, and a planned event pointing at a brief with the given gates.
func initTask(t *testing.T, gates []string) (string, error) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		return "", fmt.Errorf("Init() error = %w", err)
	}
	initRepo(t, dir)
	// Flywheel's own bookkeeping must never enter the unit's tree: ignore it
	// so both treeHash and git write-tree exclude it.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		return "", fmt.Errorf("write .gitignore: %w", err)
	}
	brief := "owns: a.go\nneeds: none\n"
	for _, g := range gates {
		brief = brief + "gate: " + g + "\n"
	}
	brief = brief + "\n# TASK: gauges\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		return "", fmt.Errorf("write brief: %w", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		return "", fmt.Errorf("AppendEvent() error = %w", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	return dir, nil
}

// initTaskLive is initTask plus live-gate: lines in the brief header
// (issue #152).
func initTaskLive(t *testing.T, gates, liveGates []string) (string, error) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		return "", fmt.Errorf("Init() error = %w", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		return "", fmt.Errorf("write .gitignore: %w", err)
	}
	brief := "owns: a.go\nneeds: none\n"
	for _, g := range gates {
		brief = brief + "gate: " + g + "\n"
	}
	for _, g := range liveGates {
		brief = brief + "live-gate: " + g + "\n"
	}
	brief = brief + "\n# TASK: live\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		return "", fmt.Errorf("write brief: %w", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		return "", fmt.Errorf("AppendEvent() error = %w", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	return dir, nil
}

// TestValidateLiveGateNotRunWithoutFlag checks that with Live false a
// declared live gate does not run and records no event, while LiveDeclared
// still reports the count and a mocked pass stays a legitimate OK() on its
// own (issue #152).
func TestValidateLiveGateNotRunWithoutFlag(t *testing.T) {
	dir, err := initTaskLive(t, []string{"exit 0"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskLive() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.LiveDeclared != 1 {
		t.Errorf("LiveDeclared = %d, want 1", res.LiveDeclared)
	}
	if res.LiveRun {
		t.Error("LiveRun = true, want false")
	}
	for _, g := range res.Gates {
		if g.Live {
			t.Errorf("gates = %v, want no live entries when Live is false", res.Gates)
		}
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "validated" && e.Gate == "live1" {
			t.Error("a live1 validated event was recorded despite Live being false")
		}
	}
	if !res.OK() {
		t.Error("OK() = false, want true: the ordinary gate passes and a mocked pass stays legitimate on its own")
	}
}

// TestValidateLiveGateRunsAndRecords checks that with Live true the declared
// live gate runs through the same path as an ordinary gate: a validated
// event with Gate "live1" and its own evidence log (issue #152).
func TestValidateLiveGateRunsAndRecords(t *testing.T) {
	dir, err := initTaskLive(t, []string{"exit 0"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskLive() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Live: true})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.LiveRun {
		t.Error("LiveRun = false, want true")
	}
	var live *GateOut
	for i := range res.Gates {
		if res.Gates[i].Live {
			live = &res.Gates[i]
		}
	}
	if live == nil {
		t.Fatal("no live gate in res.Gates")
	}
	if live.Gate != "live1" || live.RC != 0 {
		t.Errorf("live gate = %+v, want Gate live1 RC 0", live)
	}
	log := filepath.Join(dir, ".flywheel", "evidence", "T1", "r1", "gate-live-1.log")
	if _, err := os.Stat(log); err != nil {
		t.Errorf("live gate evidence log missing: %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "validated" && e.Gate == "live1" {
			found = true
			if e.RC == nil || *e.RC != 0 {
				t.Errorf("live1 event rc = %v, want 0", e.RC)
			}
		}
	}
	if !found {
		t.Error("no validated event recorded for live1")
	}
	if !res.OK() {
		t.Error("OK() = false, want true")
	}
}

// TestValidateLiveGateFailureFailsGatesOK checks a failing live gate sets
// GatesOK false exactly as an ordinary gate does (issue #152).
func TestValidateLiveGateFailureFailsGatesOK(t *testing.T) {
	dir, err := initTaskLive(t, []string{"exit 0"}, []string{"exit 1"})
	if err != nil {
		t.Fatalf("initTaskLive() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Live: true})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.GatesOK || res.OK() {
		t.Error("GatesOK = true, want false: the live gate failed")
	}
}

func TestValidatePassingGates(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Tree == "" {
		t.Error("empty tree hash")
	}
	if len(res.Gates) != 2 {
		t.Fatalf("gates = %v, want 2", len(res.Gates))
	}
	if res.Gates[0].RC != 0 || res.Gates[0].HostBlocked {
		t.Errorf("gate 1 rc = %d, want 0 and not host-blocked", res.Gates[0].RC)
	}
	if res.Gates[1].RC != 0 {
		t.Errorf("gate 2 rc = %d, want 0", res.Gates[1].RC)
	}
	if !res.GatesOK || !res.OwnsOK || !res.OK() {
		t.Errorf("gatesOK/ownsOK = %v/%v, want true/true", res.GatesOK, res.OwnsOK)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	validated := 0
	for _, e := range evs {
		if e.Kind == "validated" {
			validated++
			if e.Gate == "" || e.Tree == "" || e.Command == "" {
				t.Error("validated event missing gate/tree/command")
			}
			if e.Persona != "supervisor" {
				t.Errorf("validated persona = %q, want supervisor", e.Persona)
			}
			if !strings.HasPrefix(e.Path, ".flywheel/evidence/T1/") {
				t.Errorf("validated path = %q", e.Path)
			}
		}
	}
	if validated != 2 {
		t.Errorf("validated events = %d, want 2", validated)
	}
}

func TestValidateFailingGate(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0", "exit 1"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Gates[0].RC != 0 || res.Gates[1].RC != 1 {
		t.Errorf("gates rc = %d/%d, want 0/1", res.Gates[0].RC, res.Gates[1].RC)
	}
	if res.GatesOK || res.OK() {
		t.Errorf("GatesOK = %v, want false", res.GatesOK)
	}
	if !res.OwnsOK {
		t.Error("ownsOK should be true: nothing outside owns")
	}
}

// shaOf returns the hex SHA-256 of the file at path.
func shaOf(path string, t *testing.T) string {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// dispatchedWithBaseline appends a dispatched event carrying a baseline of
// every currently dirty path, the way run.go records one at dispatch.
func dispatchedWithBaseline(t *testing.T, dir string) {
	t.Helper()
	paths, err := changedPaths(dir)
	if err != nil {
		t.Fatalf("changedPaths() error = %v", err)
	}
	base := map[string]string{}
	for _, p := range paths {
		base[p] = shaOf(filepath.Join(dir, filepath.FromSlash(p)), t)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Baseline: base}); err != nil {
		t.Fatalf("AppendEvent() dispatched error = %v", err)
	}
}

// TestValidateBaselineDirtyBeforeDispatch checks a file already dirty when
// dispatched is not reported outside owns and is recorded in baselined.
func TestValidateBaselineDirtyBeforeDispatch(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("lead's edit\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	dispatchedWithBaseline(t, dir)
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (an unchanged baselined file is not outside)", res.OwnsOK)
	}
	if len(res.Outside) != 0 {
		t.Errorf("outside = %v, want nothing", res.Outside)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "owns_checked" {
			if len(e.Baselined) != 1 || e.Baselined[0] != "b.txt" {
				t.Errorf("owns_checked baselined = %v, want [b.txt]", e.Baselined)
			}
			if len(e.Outside) != 0 {
				t.Errorf("owns_checked outside = %v, want nothing", e.Outside)
			}
		}
	}
}

// TestValidateBaselineChangedAfterDispatch checks a baselined file the unit
// modified after dispatch is judged normally: reported outside.
func TestValidateBaselineChangedAfterDispatch(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("lead's edit\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	dispatchedWithBaseline(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("unit's edit\n"), 0o644); err != nil {
		t.Fatalf("rewrite b.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (the unit touched the file after dispatch)", res.OwnsOK)
	}
	if len(res.Outside) != 1 || res.Outside[0] != "b.txt" {
		t.Errorf("outside = %v, want [b.txt]", res.Outside)
	}
}

// TestValidateCleanBaselineChangesNothing checks a baseline over a clean tree
// changes nothing: no outside paths and nothing recorded in baselined.
func TestValidateCleanBaselineChangesNothing(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	dispatchedWithBaseline(t, dir)
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (a clean baseline changes nothing)", res.OwnsOK)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "owns_checked" && (len(e.Baselined) != 0 || len(e.Outside) != 0) {
			t.Errorf("owns_checked baselined/outside = %v/%v, want nothing", e.Baselined, e.Outside)
		}
	}
}

// TestValidateBaselineIgnoresCorrectionBaseline checks only the FIRST
// dispatched event's baseline excuses files: a correction attempt is
// dispatched after the first attempt's edits, so its baseline must never
// widen the excuse set.
func TestValidateBaselineIgnoresCorrectionBaseline(t *testing.T) {
	t.Run("empty first baseline stays empty", func(t *testing.T) {
		dir, err := initTask(t, []string{"exit 0"})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		dispatchedWithBaseline(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "outside.go"), []byte("stray\n"), 0o644); err != nil {
			t.Fatalf("write outside.go: %v", err)
		}
		if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Baseline: map[string]string{"outside.go": shaOf(filepath.Join(dir, "outside.go"), t)}}); err != nil {
			t.Fatalf("AppendEvent() correction dispatched error = %v", err)
		}
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		if err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		if res.OwnsOK || res.OK() {
			t.Errorf("OwnsOK = %v, want false (a correction baseline must not excuse the first attempt's stray file)", res.OwnsOK)
		}
		if len(res.Outside) != 1 || res.Outside[0] != "outside.go" {
			t.Errorf("outside = %v, want [outside.go]", res.Outside)
		}
	})
	t.Run("later baseline never widens", func(t *testing.T) {
		dir, err := initTask(t, []string{"exit 0"})
		if err != nil {
			t.Fatalf("initTask() error = %v", err)
		}
		h1 := shaOf(filepath.Join(dir, "a.go"), t)
		if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Baseline: map[string]string{"a.go": h1}}); err != nil {
			t.Fatalf("AppendEvent() dispatched error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644); err != nil {
			t.Fatalf("write b.go: %v", err)
		}
		h2 := shaOf(filepath.Join(dir, "b.go"), t)
		if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Baseline: map[string]string{"a.go": h1, "b.go": h2}}); err != nil {
			t.Fatalf("AppendEvent() correction dispatched error = %v", err)
		}
		res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
		if err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		if res.OwnsOK || res.OK() {
			t.Errorf("OwnsOK = %v, want false (b.go is outside despite the correction baseline)", res.OwnsOK)
		}
		if len(res.Outside) != 1 || res.Outside[0] != "b.go" {
			t.Errorf("outside = %v, want [b.go]", res.Outside)
		}
	})
}

// dispatchedWithWorktree appends a dispatched event whose Worktrees field
// snapshots wtDir's current changed paths and shas, the way run.go records
// one at dispatch when the repo has another worktree (issue #87).
func dispatchedWithWorktree(t *testing.T, dir, wtDir string) {
	t.Helper()
	paths, err := changedPaths(wtDir)
	if err != nil {
		t.Fatalf("changedPaths() error = %v", err)
	}
	files := map[string]string{}
	for _, p := range paths {
		files[p] = fileSHA(wtDir, p)
	}
	if err := AppendEvent(dir, Event{
		TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1",
		Worktrees: map[string]map[string]string{wtDir: files},
	}); err != nil {
		t.Fatalf("AppendEvent() dispatched error = %v", err)
	}
}

// TestValidateOtherWorktreeChangeIsOutside checks a new file appearing in
// another worktree recorded at dispatch lands in Outside, formatted as
// "<worktree path>: <path>", and fails OK() (issue #87).
func TestValidateOtherWorktreeChangeIsOutside(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "note.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (a new file appeared in the other worktree)", res.OwnsOK)
	}
	want := wt + ": note.txt"
	found := false
	for _, o := range res.Outside {
		if o == want {
			found = true
		}
	}
	if !found {
		t.Errorf("outside = %v, want it to contain %q", res.Outside, want)
	}
}

// TestValidateUnchangedOtherWorktreePasses checks a worktree recorded at
// dispatch with no drift since is not reported outside.
func TestValidateUnchangedOtherWorktreePasses(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	dispatchedWithWorktree(t, dir, wt)
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (the other worktree is unchanged since dispatch)", res.OwnsOK)
	}
}

// TestValidateOtherWorktreeFlywheelMDIsExcused checks that when the other
// worktree's only change is its own flywheel.md board, OwnsOK is true and
// Outside is empty (issue #186): flywheel.md there is the factory view's own
// bookkeeping, not that worktree's unit's work.
func TestValidateOtherWorktreeFlywheelMDIsExcused(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "flywheel.md"), []byte("board\n"), 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (flywheel.md in the other worktree is never that unit's work)", res.OwnsOK)
	}
	if len(res.Outside) != 0 {
		t.Errorf("outside = %v, want none", res.Outside)
	}
}

// TestValidateOtherWorktreeFlywheelMDAndRealFileOnlyRealFails checks that
// when the other worktree changes both its flywheel.md board and a real
// source file, only the real file appears in Outside (issue #186).
func TestValidateOtherWorktreeFlywheelMDAndRealFileOnlyRealFails(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	dispatchedWithWorktree(t, dir, wt)
	if err := os.WriteFile(filepath.Join(wt, "flywheel.md"), []byte("board\n"), 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "note.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (note.txt is a real outside change)", res.OwnsOK)
	}
	want := wt + ": note.txt"
	if len(res.Outside) != 1 || res.Outside[0] != want {
		t.Errorf("outside = %v, want [%s]", res.Outside, want)
	}
}

// TestValidateOtherWorktreeDotFlywheelIsExcused checks that a path under the
// other worktree's .flywheel/ is skipped the same way flywheel.md is (issue
// #186).
func TestValidateOtherWorktreeDotFlywheelIsExcused(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	dispatchedWithWorktree(t, dir, wt)
	if err := os.MkdirAll(filepath.Join(wt, ".flywheel", "evidence"), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel/evidence: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".flywheel", "evidence", "log.txt"), []byte("log\n"), 0o644); err != nil {
		t.Fatalf("write .flywheel/evidence/log.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (.flywheel/ in the other worktree is never that unit's work)", res.OwnsOK)
	}
	if len(res.Outside) != 0 {
		t.Errorf("outside = %v, want none", res.Outside)
	}
}

func TestValidateOutOfOwns(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false", res.OwnsOK)
	}
	if len(res.Outside) != 1 || res.Outside[0] != "b.txt" {
		t.Errorf("outside = %v, want [b.txt]", res.Outside)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "owns_checked" {
			if len(e.Outside) != 1 || e.Outside[0] != "b.txt" {
				t.Errorf("owns_checked outside = %v, want [b.txt]", e.Outside)
			}
			if e.Tree == "" {
				t.Error("owns_checked missing tree")
			}
		}
	}
}

func TestValidateNoGates(t *testing.T) {
	dir, err := initTask(t, []string{})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err == nil {
		t.Error("ValidateTask() accepted a brief with no gate: lines")
	} else if !strings.Contains(err.Error(), "gate:") {
		t.Errorf("error = %v, want it to name the gate fix", err)
	}
}

func TestValidateTreeHashMatchesWriteTree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	idxBefore, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatalf("read .git/index: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	want := git(t, dir, []string{"write-tree"})
	if res.Tree != want {
		t.Errorf("tree = %q, want %q (git write-tree)", res.Tree, want)
	}
	idxAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatalf("read .git/index: %v", err)
	}
	if len(idxBefore) != len(idxAfter) {
		t.Error("real .git/index changed during validation")
	}
	for i := range idxBefore {
		if idxBefore[i] != idxAfter[i] {
			t.Error("real .git/index changed during validation")
			break
		}
	}
}

func TestValidateEvidenceLogWritten(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	log := filepath.Join(dir, ".flywheel", "evidence", "T1", "r1", "gate-1.log")
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read %s: %v", log, err)
	}
	_ = b
}

// TestValidateGateExitThree checks that a failing gate's nonzero exit code is
// recorded on the ExitError pointer target: a gate `exit 3` must record rc=3
// and still let ValidateTask return without error.
func TestValidateGateExitThree(t *testing.T) {
	dir, err := initTask(t, []string{"exit 3"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Gates) != 1 {
		t.Fatalf("gates = %v, want 1", len(res.Gates))
	}
	if res.Gates[0].RC != 3 {
		t.Errorf("gate rc = %d, want 3", res.Gates[0].RC)
	}
	if res.GatesOK {
		t.Error("GatesOK = true, want false for a failing gate")
	}
}

// TestValidateWorkdirEmptyOutOfOwns checks that with Workdir empty the gates
// and owns check run in the task's tree (Dir): an out-of-owns file committed
// under the repo must be reported.
func TestValidateWorkdirEmptyOutOfOwns(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Outside) != 1 || res.Outside[0] != "b.txt" {
		t.Errorf("outside = %v, want [b.txt]", res.Outside)
	}
}

// TestTreeHashMatchesWriteTreeIndex checks the throwaway temp index: treeHash
// must equal git write-tree and must not touch the real .git/index.
func TestTreeHashMatchesWriteTreeIndex(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	idxBefore, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatalf("read .git/index: %v", err)
	}
	tree, err := treeHash(dir)
	if err != nil {
		t.Fatalf("treeHash() error = %v", err)
	}
	want := git(t, dir, []string{"write-tree"})
	if tree != want {
		t.Errorf("tree = %q, want %q (git write-tree)", tree, want)
	}
	idxAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatalf("read .git/index: %v", err)
	}
	if len(idxBefore) != len(idxAfter) {
		t.Error("real .git/index changed during treeHash")
	}
	for i := range idxBefore {
		if idxBefore[i] != idxAfter[i] {
			t.Error("real .git/index changed during treeHash")
			break
		}
	}
}

// gitAuto runs git with the repo's own config (no -c autocrlf override), so a
// repo-level core.autocrlf=true exercises git's CRLF warnings on stderr.
func gitAuto(t *testing.T, wd string, args []string) string {
	t.Helper()
	cmd := exec.Command("git")
	cmd.Dir = wd
	full := append([]string{"git"}, args...)
	cmd.Args = full
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// TestValidateAutocrlfWarningNotOutside checks that git's "LF will be replaced
// by CRLF" warning, printed to stderr when core.autocrlf=true touches an LF
// file inside owns, is never parsed as an outside path.
func TestValidateAutocrlfWarningNotOutside(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	gitAuto(t, dir, []string{"init", "-q"})
	gitAuto(t, dir, []string{"config", "core.autocrlf", "true"})
	gitAuto(t, dir, []string{"config", "user.name", "test"})
	gitAuto(t, dir, []string{"config", "user.email", "test@example.com"})
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	gitAuto(t, dir, []string{"add", "a.go"})
	gitAuto(t, dir, []string{"commit", "-q", "-m", "init"})
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	brief := "owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK: gauges\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	gitAuto(t, dir, []string{"add", "-A"})
	gitAuto(t, dir, []string{"commit", "-q", "-m", "brief"})
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n// change\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.go: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Outside) != 0 {
		t.Errorf("outside = %v, want nothing (stderr CRLF warning must not be a path)", res.Outside)
	}
}

// TestValidateCorrectionDeltaOwnsAndGates checks that validate measures a
// correction's own delta: b.go, owned only by the delta, stays inside owns,
// and the delta's gate: line runs instead of the brief's.
func TestValidateCorrectionDeltaOwnsAndGates(t *testing.T) {
	dir, err := initTask(t, []string{"exit 1"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	dispatchedWithBaseline(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "briefs"), 0o755); err != nil {
		t.Fatalf("mkdir briefs: %v", err)
	}
	delta := "owns: b.go\ngate: exit 0\n\n# DELTA\n"
	deltaRel := filepath.Join(".flywheel", "briefs", "delta.txt")
	if err := os.WriteFile(filepath.Join(dir, deltaRel), []byte(delta), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write b.go: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: filepath.ToSlash(deltaRel)}); err != nil {
		t.Fatalf("AppendEvent() correction dispatched error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Gates) != 1 || res.Gates[0].Command != "exit 0" {
		t.Errorf("gates = %v, want the delta's own exit 0, not the brief's exit 1", res.Gates)
	}
	if !res.OwnsOK {
		t.Errorf("OwnsOK = false, want true (delta.txt owns b.go); outside = %v", res.Outside)
	}
}

// wantHostBlockNote builds the persistent-host-block note ValidateTask must
// record and print for the given parsed file (issue #101).
func wantHostBlockNote(file string) string {
	return "persistent host block: " + file +
		"; compile then run (go test -c -o <dir>/x.test.exe <pkg> && <dir>/x.test.exe) or run the gate in CI"
}

// TestValidatePersistentHostBlockRecordsNote checks a gate whose output
// carries the host-block message on both runs (a rerun can never help)
// records the persistent note, naming the file parsed from "fork/exec
// <path>:", on both the GateOut and the validated event, which keeps its
// Reason host-blocked.
func TestValidatePersistentHostBlockRecordsNote(t *testing.T) {
	gate := "printf 'fork/exec /tmp/go-build/b1/flywheel.test.exe: An Application Control policy has blocked this file\\n'; exit 1"
	dir, err := initTask(t, []string{gate})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.Gates[0].HostBlocked {
		t.Fatalf("HostBlocked = false, want true (both runs blocked)")
	}
	want := wantHostBlockNote("/tmp/go-build/b1/flywheel.test.exe")
	if res.Gates[0].Note != want {
		t.Errorf("note = %q, want %q", res.Gates[0].Note, want)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "validated" {
			found = true
			if e.Reason != "host-blocked" {
				t.Errorf("reason = %q, want host-blocked", e.Reason)
			}
			if e.Note != want {
				t.Errorf("event note = %q, want %q", e.Note, want)
			}
		}
	}
	if !found {
		t.Error("no validated event recorded")
	}
}

// TestValidatePersistentHostBlockFileEmptyWhenUnparsed checks output that
// carries the host-block message without a "fork/exec <path>:" prefix parses
// to an empty file, not an error.
func TestValidatePersistentHostBlockFileEmptyWhenUnparsed(t *testing.T) {
	gate := "printf 'An Application Control policy has blocked this file\\n'; exit 1"
	dir, err := initTask(t, []string{gate})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.Gates[0].HostBlocked {
		t.Fatalf("HostBlocked = false, want true (both runs blocked)")
	}
	want := wantHostBlockNote("")
	if res.Gates[0].Note != want {
		t.Errorf("note = %q, want %q (no fork/exec prefix to parse)", res.Gates[0].Note, want)
	}
}

// TestValidateHostBlockedRerunPassRecordsPass checks a gate blocked on its
// first run but passing on the rerun keeps today's recording: no note, no
// Reason host-blocked, and the passing rc.
func TestValidateHostBlockedRerunPassRecordsPass(t *testing.T) {
	marker := filepath.ToSlash(filepath.Join(t.TempDir(), "ran"))
	gate := fmt.Sprintf(`if [ -f "%s" ]; then exit 0; else touch "%s"; printf 'fork/exec /x: An Application Control policy has blocked this file\n'; exit 1; fi`, marker, marker)
	dir, err := initTask(t, []string{gate})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Gates[0].HostBlocked {
		t.Errorf("HostBlocked = true, want false (the rerun passed)")
	}
	if res.Gates[0].RC != 0 {
		t.Errorf("rc = %d, want 0 (the rerun passed)", res.Gates[0].RC)
	}
	if res.Gates[0].Note != "" {
		t.Errorf("note = %q, want empty (a block followed by a pass keeps today's recording)", res.Gates[0].Note)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "validated" && e.Reason == "host-blocked" {
			t.Error("validated event carries Reason host-blocked, want none (block then pass is today's recording)")
		}
	}
}

// TestValidateInconclusiveGate checks a gate that fails naming only a changed
// path outside owns is recorded inconclusive, with the "blocked by <paths>"
// note, on both the GateOut and the validated event, and still fails GatesOK
// (issue #162).
func TestValidateInconclusiveGate(t *testing.T) {
	gate := `printf 'FAIL b.txt:3: broken\n'; exit 1`
	dir, err := initTask(t, []string{gate})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.Gates[0].Inconclusive {
		t.Fatalf("Inconclusive = false, want true (only an outside changed path is named)")
	}
	want := "blocked by b.txt"
	if res.Gates[0].Note != want {
		t.Errorf("note = %q, want %q", res.Gates[0].Note, want)
	}
	if res.GatesOK || res.OK() {
		t.Error("GatesOK = true, want false: an inconclusive reading is not a pass")
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "validated" {
			found = true
			if e.Reason != "inconclusive" {
				t.Errorf("reason = %q, want inconclusive", e.Reason)
			}
			if e.Note != want {
				t.Errorf("event note = %q, want %q", e.Note, want)
			}
		}
	}
	if !found {
		t.Error("no validated event recorded")
	}
}

// TestValidateInconclusiveGateOwnsPathIsOrdinary checks that naming a path
// inside owns keeps the failure ordinary, even when an outside changed path
// is also named.
func TestValidateInconclusiveGateOwnsPathIsOrdinary(t *testing.T) {
	gate := `printf 'FAIL a.go:1: broken, also b.txt changed\n'; exit 1`
	dir, err := initTask(t, []string{gate})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("stray\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Gates[0].Inconclusive {
		t.Errorf("Inconclusive = true, want false: the output also names a.go, inside owns")
	}
	if res.Gates[0].Note != "" {
		t.Errorf("note = %q, want empty for an ordinary failure", res.Gates[0].Note)
	}
}

// TestValidateOutsidePathUnchangedIsOrdinary checks that naming a real,
// existing path outside owns that is NOT changed against HEAD is an ordinary
// failure, not inconclusive.
func TestValidateOutsidePathUnchangedIsOrdinary(t *testing.T) {
	dir, err := initTask(t, []string{`printf 'see other.txt\n'; exit 1`})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("settled\n"), 0o644); err != nil {
		t.Fatalf("write other.txt: %v", err)
	}
	git(t, dir, []string{"add", "other.txt"})
	git(t, dir, []string{"commit", "-m", "settle other.txt"})
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Gates[0].Inconclusive {
		t.Errorf("Inconclusive = true, want false: other.txt is named but not changed against HEAD")
	}
	if res.Gates[0].Note != "" {
		t.Errorf("note = %q, want empty", res.Gates[0].Note)
	}
}

// TestValidateHostBlockedGateNotInconclusive checks a host-blocked gate keeps
// its own handling and is never also marked inconclusive.
func TestValidateHostBlockedGateNotInconclusive(t *testing.T) {
	gate := "printf 'fork/exec /tmp/go-build/b1/flywheel.test.exe: An Application Control policy has blocked this file\\n'; exit 1"
	dir, err := initTask(t, []string{gate})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.Gates[0].HostBlocked {
		t.Fatalf("HostBlocked = false, want true (both runs blocked)")
	}
	if res.Gates[0].Inconclusive {
		t.Error("Inconclusive = true, want false: a host block keeps its own handling")
	}
}

// TestValidatePassingGateNotInconclusive checks a passing gate is untouched
// by the inconclusive scan.
func TestValidatePassingGateNotInconclusive(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Gates[0].Inconclusive || res.Gates[0].Note != "" {
		t.Errorf("Inconclusive/Note = %v/%q, want false/empty for a passing gate", res.Gates[0].Inconclusive, res.Gates[0].Note)
	}
}

// otherTaskPlanned commits a brief for otherTask owning ownsPath and records
// its planned event, so AttemptBrief can resolve it the way attributeOutside
// needs (issue #117).
func otherTaskPlanned(t *testing.T, dir, otherTask, ownsPath string) {
	t.Helper()
	brief := "owns: " + ownsPath + "\nneeds: none\ngate: exit 0\n\n# TASK: " + otherTask + "\n"
	rel := otherTask + "-brief.txt"
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(brief), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:30:00Z", Task: otherTask, Kind: "planned", Brief: rel}); err != nil {
		t.Fatalf("AppendEvent() planned %s error = %v", otherTask, err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", otherTask + " brief"})
}

// TestValidateAttributedToInFlightOwner checks a changed path outside T1's
// owns, owned by another task's brief while that task is dispatched (in
// flight), is attributed rather than outside, and the owns check passes.
func TestValidateAttributedToInFlightOwner(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	otherTaskPlanned(t, dir, "B", "theirs.go")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "B", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched B error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "theirs.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write theirs.go: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (theirs.go belongs to B, in flight)", res.OwnsOK)
	}
	if len(res.Outside) != 0 {
		t.Errorf("outside = %v, want nothing", res.Outside)
	}
	if len(res.Attributed) != 1 || res.Attributed[0] != "theirs.go -> B" {
		t.Errorf("attributed = %v, want [theirs.go -> B]", res.Attributed)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "owns_checked" {
			found = true
			if len(e.Attributed) != 1 || e.Attributed[0] != "theirs.go -> B" {
				t.Errorf("owns_checked attributed = %v, want [theirs.go -> B]", e.Attributed)
			}
		}
	}
	if !found {
		t.Error("no owns_checked event recorded")
	}
}

// TestValidateNotAttributedWhenOwnerLanded checks the same path with its
// owner task landed (no longer in flight) is still outside and fails.
func TestValidateNotAttributedWhenOwnerLanded(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	otherTaskPlanned(t, dir, "B", "theirs.go")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "B", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched B error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:30:00Z", Task: "B", Kind: "landed"}); err != nil {
		t.Fatalf("AppendEvent() landed B error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "theirs.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatalf("write theirs.go: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false (B has landed, no longer in flight)", res.OwnsOK)
	}
	if len(res.Outside) != 1 || res.Outside[0] != "theirs.go" {
		t.Errorf("outside = %v, want [theirs.go]", res.Outside)
	}
	if len(res.Attributed) != 0 {
		t.Errorf("attributed = %v, want nothing", res.Attributed)
	}
}

// TestValidateNoOwnerStaysOutside checks a stray path no task's brief owns is
// still outside and fails, and never appears in Attributed.
func TestValidateNoOwnerStaysOutside(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nobody.go"), []byte("package n\n"), 0o644); err != nil {
		t.Fatalf("write nobody.go: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK || res.OK() {
		t.Errorf("OwnsOK = %v, want false", res.OwnsOK)
	}
	if len(res.Outside) != 1 || res.Outside[0] != "nobody.go" {
		t.Errorf("outside = %v, want [nobody.go]", res.Outside)
	}
	if len(res.Attributed) != 0 {
		t.Errorf("attributed = %v, want nothing", res.Attributed)
	}
}

// TestValidateOwnPathNeverAttributed checks a changed path inside the
// validated task's own owns never appears in Outside or Attributed.
func TestValidateOwnPathNeverAttributed(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n// change\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.go: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (a.go is T1's own)", res.OwnsOK)
	}
	if len(res.Outside) != 0 || len(res.Attributed) != 0 {
		t.Errorf("outside/attributed = %v/%v, want nothing", res.Outside, res.Attributed)
	}
}

// initTaskNeedsState is initTask plus a needs-state: line in the brief header
// (issue #136), so callers can exercise the isolated-workdir refusal.
func initTaskNeedsState(t *testing.T, gates []string, needsState string) (string, error) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		return "", fmt.Errorf("Init() error = %w", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		return "", fmt.Errorf("write .gitignore: %w", err)
	}
	brief := "owns: a.go\nneeds: none\nneeds-state: " + needsState + "\n"
	for _, g := range gates {
		brief = brief + "gate: " + g + "\n"
	}
	brief = brief + "\n# TASK: needs-state\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		return "", fmt.Errorf("write brief: %w", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		return "", fmt.Errorf("AppendEvent() error = %w", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	return dir, nil
}

// TestValidateNeedsStateMissingRefusesBeforeGates checks a declared
// needs-state: path missing from an isolated --workdir refuses with exit-5
// shaped output (Refused set, OK() false) before any gate runs, and names
// the path (issue #136).
func TestValidateNeedsStateMissingRefusesBeforeGates(t *testing.T) {
	dir, err := initTaskNeedsState(t, []string{"exit 0"}, "data/db.sqlite")
	if err != nil {
		t.Fatalf("initTaskNeedsState() error = %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wt})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OK() {
		t.Error("OK() = true, want false: needs-state path is missing")
	}
	if len(res.Gates) != 0 {
		t.Errorf("gates = %v, want none run", res.Gates)
	}
	if !strings.Contains(res.Refused, "needs-state") || !strings.Contains(res.Refused, "data/db.sqlite") {
		t.Errorf("refused = %q, want it to name needs-state and data/db.sqlite", res.Refused)
	}
}

// TestValidateNeedsStateCarriedFileRunsGates checks carrying a declared file
// satisfies needs-state: the file is copied into the workdir and the gates
// then run (issue #136).
func TestValidateNeedsStateCarriedFileRunsGates(t *testing.T) {
	dir, err := initTaskNeedsState(t, []string{"test -f data/db.sqlite"}, "data/db.sqlite")
	if err != nil {
		t.Fatalf("initTaskNeedsState() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "db.sqlite"), []byte("data\n"), 0o644); err != nil {
		t.Fatalf("write db.sqlite: %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wt, Carry: []string{"data/db.sqlite"}})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Refused != "" {
		t.Errorf("refused = %q, want empty (the path was carried)", res.Refused)
	}
	if len(res.Gates) != 1 || res.Gates[0].RC != 0 {
		t.Errorf("gates = %v, want one passing gate", res.Gates)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK/OK = %v/%v, want true/true: a carried needs-state path is never outside owns; outside = %v", res.OwnsOK, res.OK(), res.Outside)
	}
	if _, err := os.Stat(filepath.Join(wt, "data", "db.sqlite")); err != nil {
		t.Errorf("carried file missing from workdir: %v", err)
	}
}

// TestValidateNeedsStateCarriedDirectoryRunsGates checks carrying a declared
// directory copies it recursively (issue #136).
func TestValidateNeedsStateCarriedDirectoryRunsGates(t *testing.T) {
	dir, err := initTaskNeedsState(t, []string{"test -f sub/nested/carried.txt"}, "sub/")
	if err != nil {
		t.Fatalf("initTaskNeedsState() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir sub/nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested", "carried.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write carried.txt: %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wt, Carry: []string{"sub/"}})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Refused != "" {
		t.Errorf("refused = %q, want empty (the directory was carried)", res.Refused)
	}
	if len(res.Gates) != 1 || res.Gates[0].RC != 0 {
		t.Errorf("gates = %v, want one passing gate", res.Gates)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK/OK = %v/%v, want true/true: a carried needs-state directory is never outside owns; outside = %v", res.OwnsOK, res.OK(), res.Outside)
	}
	if _, err := os.Stat(filepath.Join(wt, "sub", "nested", "carried.txt")); err != nil {
		t.Errorf("carried directory contents missing from workdir: %v", err)
	}
}

// TestValidateCarryMissingUnderDirErrors checks a --carry path that does not
// exist under dir is an error naming it (issue #136).
func TestValidateCarryMissingUnderDirErrors(t *testing.T) {
	dir, err := initTaskNeedsState(t, []string{"exit 0"}, "ghost.txt")
	if err != nil {
		t.Fatalf("initTaskNeedsState() error = %v", err)
	}
	wt := t.TempDir()
	initRepo(t, wt)
	_, err = ValidateTask(dir, "T1", ValidateOptions{Dir: dir, Workdir: wt, Carry: []string{"ghost.txt"}})
	if err == nil {
		t.Fatal("ValidateTask() accepted a --carry path missing under dir")
	}
	if !strings.Contains(err.Error(), "ghost.txt") {
		t.Errorf("error = %v, want it to name ghost.txt", err)
	}
}

// TestValidateNeedsStateNoWorkdirIsNoOp checks that with no --workdir the
// tree is the repo, so needs-state: is satisfied by definition and nothing
// is copied (issue #136).
func TestValidateNeedsStateNoWorkdirIsNoOp(t *testing.T) {
	dir, err := initTaskNeedsState(t, []string{"exit 0"}, "never/there.txt")
	if err != nil {
		t.Fatalf("initTaskNeedsState() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.Refused != "" {
		t.Errorf("refused = %q, want empty (no --workdir: needs-state is a no-op)", res.Refused)
	}
	if !res.OK() {
		t.Errorf("OK() = false, want true: %+v", res)
	}
}

// TestValidateBaselinedPathNotAttributed checks a baselined path is excused
// by the baseline, not counted as attributed, even when another in-flight
// task's brief would also own it.
func TestValidateBaselinedPathNotAttributed(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	otherTaskPlanned(t, dir, "B", "b.txt")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("lead's edit\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	dispatchedWithBaseline(t, dir)
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:30:00Z", Task: "B", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched B error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.OwnsOK || !res.OK() {
		t.Errorf("OwnsOK = %v, want true (b.txt is baselined)", res.OwnsOK)
	}
	if len(res.Attributed) != 0 {
		t.Errorf("attributed = %v, want nothing (baseline excuses first)", res.Attributed)
	}
}

// initTaskOwns is initTask with a custom owns list, so a test can own files
// beyond the committed a.go.
func initTaskOwns(t *testing.T, owns []string, gates []string) (string, error) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		return "", fmt.Errorf("Init() error = %w", err)
	}
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		return "", fmt.Errorf("write .gitignore: %w", err)
	}
	brief := "owns: " + strings.Join(owns, ", ") + "\nneeds: none\n"
	for _, g := range gates {
		brief = brief + "gate: " + g + "\n"
	}
	brief = brief + "\n# TASK: gauges\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		return "", fmt.Errorf("write brief: %w", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		return "", fmt.Errorf("AppendEvent() error = %w", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	return dir, nil
}

// TestValidateFilesRecordsMarkdownShape checks a validated task whose owns
// include a Markdown file records that file's line and heading counts both on
// GaugeResult.Files and on the owns_checked event, with the JSON field names
// "path", "lines" and "headings" in the ledger (issue #130).
func TestValidateFilesRecordsMarkdownShape(t *testing.T) {
	dir, err := initTaskOwns(t, []string{"a.go", "doc.md"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	doc := "# Title\n\none\n## Section A\ntwo\n## Section B\nthree\n"
	if err := os.WriteFile(filepath.Join(dir, "doc.md"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write doc.md: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("files = %v, want one entry for doc.md", res.Files)
	}
	f := res.Files[0]
	if f.Path != "doc.md" || f.Lines != 7 || f.Headings != 3 {
		t.Errorf("file = %+v, want doc.md 7 lines 3 headings", f)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range evs {
		if e.Kind == "owns_checked" {
			found = true
			if len(e.Files) != 1 || e.Files[0].Path != "doc.md" || e.Files[0].Lines != 7 || e.Files[0].Headings != 3 {
				t.Errorf("owns_checked files = %v, want doc.md 7 lines 3 headings", e.Files)
			}
		}
	}
	if !found {
		t.Error("no owns_checked event recorded")
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if !strings.Contains(string(b), `"lines":7`) || !strings.Contains(string(b), `"headings":3`) {
		t.Error("owns_checked event JSON missing \"lines\":7 or \"headings\":3")
	}
}

// TestValidateFilesNonMarkdownZeroHeadings checks a non-Markdown owned file
// records its line count with zero headings, and that headings is omitted from
// the event JSON when zero (issue #130).
func TestValidateFilesNonMarkdownZeroHeadings(t *testing.T) {
	dir, err := initTaskOwns(t, []string{"a.go", "notes.txt"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("files = %v, want one entry for notes.txt", res.Files)
	}
	f := res.Files[0]
	if f.Path != "notes.txt" || f.Lines != 3 || f.Headings != 0 {
		t.Errorf("file = %+v, want notes.txt 3 lines 0 headings", f)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if strings.Contains(string(b), `"headings"`) {
		t.Error("non-markdown owns_checked event JSON carries a headings field, want it omitted")
	}
}

// TestValidateFilesSkipsOutsideOwns checks a changed file outside owns is not
// measured: it fails the owns check but never appears in Files (issue #130).
func TestValidateFilesSkipsOutsideOwns(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stray.md"), []byte("# H\nbody\n"), 0o644); err != nil {
		t.Fatalf("write stray.md: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK {
		t.Error("OwnsOK = true, want false (stray.md is outside owns)")
	}
	if len(res.Files) != 0 {
		t.Errorf("files = %v, want nothing outside owns", res.Files)
	}
}

// TestValidateFilesATXHeadingRule checks the simple, documented ATX rule: a
// '#' inside a fenced code block still counts only when the whole line is a
// real ATX heading (first non-space characters are one to six '#' followed by
// a space); a mid-line '#' never counts. No fenced-code tracking (issue #130).
func TestValidateFilesATXHeadingRule(t *testing.T) {
	dir, err := initTaskOwns(t, []string{"a.go", "code.md"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	doc := "```go\n# comment in fence\nx := 1 // # not a heading\n```\n# Real heading\n"
	if err := os.WriteFile(filepath.Join(dir, "code.md"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write code.md: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Files) != 1 {
		t.Fatalf("files = %v, want one entry for code.md", res.Files)
	}
	f := res.Files[0]
	if f.Path != "code.md" || f.Lines != 5 || f.Headings != 2 {
		t.Errorf("file = %+v, want code.md 5 lines 2 headings (the fenced # line counts, the mid-line # does not)", f)
	}
}

// TestValidateFilesSkipsUnreadable checks a changed owned file that can no
// longer be read (deleted) is skipped, not measured as zero lines (issue
// #130).
func TestValidateFilesSkipsUnreadable(t *testing.T) {
	dir, err := initTaskOwns(t, []string{"a.go", "gone.txt"}, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTaskOwns() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gone.txt"), []byte("byebye\n"), 0o644); err != nil {
		t.Fatalf("write gone.txt: %v", err)
	}
	git(t, dir, []string{"add", "gone.txt"})
	git(t, dir, []string{"commit", "-m", "add gone.txt"})
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatalf("remove gone.txt: %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Files) != 0 {
		t.Errorf("files = %v, want nothing (gone.txt is deleted, cannot be read)", res.Files)
	}
}
