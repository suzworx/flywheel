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
