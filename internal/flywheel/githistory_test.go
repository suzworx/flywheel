package flywheel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGitWriteStateDescribesHead checks gitHistoryState of a repo with one
// commit contains ok=true and the expected format with HEAD, branch and stash.
func TestGitWriteStateDescribesHead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cmd := exec.Command("git", append(gitInitFlags(), "init", "-q")...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	// Create initial commit.
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	state, ok := gitHistoryState(dir)
	if !ok {
		t.Fatalf("gitHistoryState() ok = false, want true")
	}
	if !strings.Contains(state, "HEAD=") {
		t.Errorf("state = %q, want to contain HEAD=", state)
	}
	if !strings.Contains(state, "branch=refs/heads/") {
		t.Errorf("state = %q, want to contain branch=refs/heads/", state)
	}
	if !strings.Contains(state, "stash=(none)") {
		t.Errorf("state = %q, want to contain stash=(none)", state)
	}
}

// TestGitWriteStateNotARepo checks gitHistoryState of a plain temp dir returns
// ok=false when GIT_CEILING_DIRECTORIES prevents git from finding a repo.
func TestGitWriteStateNotARepo(t *testing.T) {
	// not parallel: t.Setenv GIT_CEILING_DIRECTORIES
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))

	state, ok := gitHistoryState(dir)
	if ok {
		t.Fatalf("gitHistoryState() ok = true, want false; state = %q", state)
	}
	if state != "" {
		t.Errorf("state = %q, want empty", state)
	}
}

// TestGitWriteCommitDuringRunRecordsSignal checks that when commandHook makes
// an empty commit during Run, a git-write signal is recorded and the finished
// event's note mentions the change.
func TestGitWriteCommitDuringRunRecordsSignal(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := setupTask(t)

	// Initialize as a git repo with one commit.
	cmd := exec.Command("git", append(gitInitFlags(), "init", "-q")...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit init: %v", err)
	}

	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Hook that makes a commit during the run.
	commandHook = func(r RunRequest) {
		guardLogWrite(dir, r.Attempt, "refused commit")
		cmd := exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "worker-commit")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	defer func() { commandHook = nil }()

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	hasGitWriteSignal := false
	hasNoteChange := false
	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" && e.Task == "T1" {
			hasGitWriteSignal = true
		}
		if e.Kind == "finished" && e.Task == "T1" && strings.Contains(e.Note, "git history changed") {
			hasNoteChange = true
		}
	}

	if !hasGitWriteSignal {
		t.Errorf("no git-write signal found for T1")
	}
	if !hasNoteChange {
		t.Errorf("finished event note does not mention git history changed")
	}
}

// TestGitWriteCleanRunRecordsNoSignal checks that when no hook runs and git
// history is unchanged, no git-write signal is recorded.
func TestGitWriteCleanRunRecordsNoSignal(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)

	// Initialize as a git repo with one commit.
	cmd := exec.Command("git", append(gitInitFlags(), "init", "-q")...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit init: %v", err)
	}

	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" && e.Task == "T1" {
			t.Errorf("unexpected git-write signal found for T1")
		}
	}
}

// TestGitWriteStashDuringRunRecordsSignal checks that when commandHook runs
// git stash, a git-write signal is recorded.
func TestGitWriteStashDuringRunRecordsSignal(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := setupTask(t)

	// Initialize as a git repo with one commit.
	cmd := exec.Command("git", append(gitInitFlags(), "init", "-q")...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit init: %v", err)
	}

	// Write and commit a tracked file so we can stash it.
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("tracked content\n"), 0o644); err != nil {
		t.Fatalf("write tracked.txt: %v", err)
	}
	cmd = exec.Command("git", "add", "tracked.txt")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	cmd = exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "add tracked")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit tracked: %v", err)
	}

	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Hook that modifies the file and stashes it.
	commandHook = func(r RunRequest) {
		guardLogWrite(dir, r.Attempt, "refused stash")
		if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("modified content\n"), 0o644); err != nil {
			return
		}
		cmd := exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "stash")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	defer func() { commandHook = nil }()

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	hasGitWriteSignal := false
	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" && e.Task == "T1" {
			hasGitWriteSignal = true
		}
	}

	if !hasGitWriteSignal {
		t.Errorf("no git-write signal found for T1 after stash")
	}
}

// gitRepoWithCommit makes dir a git repository with one empty commit.
func gitRepoWithCommit(t *testing.T, dir string) {
	t.Helper()
	initGitRepoAt(t, dir)
	for _, args := range [][]string{
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestGitWriteSilentRunStillFlagged checks that the check runs on the silent
// (timed-out) exit path too, not only after a normal finish (#318 review).
func TestGitWriteSilentRunStillFlagged(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := setupTask(t)
	gitRepoWithCommit(t, dir)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	commandHook = func(r RunRequest) {
		guardLogWrite(dir, r.Attempt, "refused commit")
		cmd := exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "worker-commit")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	t.Cleanup(func() { commandHook = nil })
	res, err := Run(dir, RunOptions{Task: "T1", StartTimeout: 50 * time.Millisecond, SimDelay: time.Second})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "silent" {
		t.Fatalf("reason = %q, want silent", res.Reason)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" && e.Task == "T1" {
			return
		}
	}
	t.Error("no git-write signal after a silent run whose worker committed")
}

// TestGitWriteUnreadableFinalStateCounts checks that a final state git can no
// longer read is a change, not a clean attempt (#318 review).
func TestGitWriteUnreadableFinalStateCounts(t *testing.T) {
	// not parallel: t.Setenv GIT_CEILING_DIRECTORIES
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	gitRepoWithCommit(t, dir)
	before, ok := readGitState(dir)
	if !ok {
		t.Fatal("readGitState of a fresh repo: not ok")
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git")); err != nil {
		t.Fatal(err)
	}
	ch := gitWriteNote(dir, before, true)
	if !ch.changed || ch.worker || !strings.Contains(ch.note, "(unreadable)") {
		t.Errorf("gitWriteNote = %+v, want a non-worker change to (unreadable)", ch)
	}
	if ch := gitWriteNote(dir, before, false); ch.changed {
		t.Error("gitWriteNote with nothing captured reported a change")
	}
}

// TestGitStateDetectsIndexWrite checks a staged file and a new tag are each
// named as what changed and charged to the worker (#423).
func TestGitStateDetectsIndexWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitRepoWithCommit(t, dir)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	before, ok := readGitState(dir)
	if !ok {
		t.Fatal("readGitState: not ok")
	}
	if err := os.WriteFile(filepath.Join(dir, "a b.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a b.txt")
	ch := gitWriteNote(dir, before, true)
	if !ch.changed || !ch.worker || !strings.Contains(ch.note, "index: staged a b.txt") || strings.Contains(ch.note, "HEAD:") {
		t.Errorf("after git add: %+v, want a worker change naming index: staged a b.txt", ch)
	}
	if len(ch.staged) != 1 || ch.staged[0] != "a b.txt" {
		t.Errorf("staged = %q, want [a b.txt]", ch.staged)
	}
	if signal, _ := gitWriteVerdict(ch, nil); !signal {
		t.Error("index write without a guard record raised no signal")
	}

	before, _ = readGitState(dir)
	run("tag", "v9")
	ch = gitWriteNote(dir, before, true)
	if !ch.changed || !ch.worker || !strings.Contains(ch.note, "tags: +v9") || strings.Contains(ch.note, "index:") {
		t.Errorf("after git tag: %+v, want a worker change naming tags: +v9", ch)
	}
	before, _ = readGitState(dir)
	run("tag", "-d", "v9")
	if ch = gitWriteNote(dir, before, true); !strings.Contains(ch.note, "tags: -v9") {
		t.Errorf("after git tag -d: %q, want tags: -v9", ch.note)
	}
}

// TestGitStateFetchedTagShared checks that a tag on a commit a remote-tracking
// ref contains (a fetch by another process, #442) needs guard evidence, while
// a local-only tag and a deleted tag stay the worker's (#423).
func TestGitStateFetchedTagShared(t *testing.T) {
	t.Parallel()
	dir, origin := t.TempDir(), t.TempDir()
	gitRepoWithCommit(t, dir)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "--bare", origin)
	run("remote", "add", "origin", origin)
	run("commit", "-q", "--allow-empty", "-m", "released")
	run("push", "-q", "origin", "HEAD:refs/heads/main")
	run("fetch", "-q", "origin")
	run("commit", "-q", "--allow-empty", "-m", "local only")

	tag := func(args ...string) gitChange {
		t.Helper()
		before, ok := readGitState(dir)
		if !ok {
			t.Fatal("readGitState: not ok")
		}
		run(append([]string{"tag"}, args...)...)
		return gitWriteNote(dir, before, true)
	}

	ch := tag("v0.21.1", "HEAD~1")
	if !ch.changed || ch.worker || !strings.Contains(ch.note, "tags: +v0.21.1 (on a remote-tracking commit)") {
		t.Errorf("fetched tag: %+v, want a shared change naming +v0.21.1 on a remote-tracking commit", ch)
	}
	if signal, note := gitWriteVerdict(ch, nil); signal {
		t.Errorf("fetched tag without guard evidence raised a signal: %q", note)
	}
	if signal, _ := gitWriteVerdict(ch, []string{"tag"}); !signal {
		t.Error("fetched tag with a refused git tag raised no signal")
	}

	if ch = tag("-a", "-m", "release", "v0.21.2", "HEAD~1"); !ch.changed || ch.worker || !strings.Contains(ch.note, "+v0.21.2 (on a remote-tracking commit)") {
		t.Errorf("annotated fetched tag: %+v, want a shared change", ch)
	}

	if ch = tag("v9"); !ch.changed || !ch.worker || !strings.Contains(ch.note, "tags: +v9") || strings.Contains(ch.note, "remote-tracking") {
		t.Errorf("local-only tag: %+v, want a worker change naming +v9", ch)
	}
	if signal, _ := gitWriteVerdict(ch, nil); !signal {
		t.Error("local-only tag without a guard record raised no signal")
	}

	if ch = tag("-d", "v0.21.1"); !ch.changed || !ch.worker || !strings.Contains(ch.note, "tags: -v0.21.1") {
		t.Errorf("deleted tag: %+v, want a worker change naming -v0.21.1", ch)
	}
}

// guardLogWrite appends line to the git guard's log for T1's attempt in dir,
// as the guard does when the worker runs a write-class git command (#361);
// the sim adapter installs no guard, so a test hook records it this way.
func guardLogWrite(dir, attempt, line string) {
	bin := filepath.Join(dir, ".flywheel", "runs", "T1."+attempt+".bin")
	f, err := os.OpenFile(gitGuardLogPath(bin), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

// TestGitWriteEvidence checks the three verdicts: no change, a change with a
// write the guard recorded, and a change with none (#361).
func TestGitWriteEvidence(t *testing.T) {
	t.Parallel()
	if signal, note := gitWriteVerdict(gitChange{}, []string{"commit"}); signal || note != "" {
		t.Errorf("unchanged: %v %q, want false \"\"", signal, note)
	}
	moved := gitChange{changed: true, note: "moved"}
	signal, note := gitWriteVerdict(moved, []string{"commit", "stash"})
	if !signal || note != "moved; the worker tried: commit, stash" {
		t.Errorf("changed with refused writes: %v %q", signal, note)
	}
	signal, note = gitWriteVerdict(moved, nil)
	if signal || !strings.HasPrefix(note, "moved; no worker git write was recorded by the guard") {
		t.Errorf("changed without evidence: %v %q", signal, note)
	}
}

// TestGitWriteSharedWorktreeNoSignal checks that HEAD moved by another process
// (the lead committing in a shared worktree) with no write in the guard's log
// raises no git-write signal, and the finished note says why (#361).
func TestGitWriteSharedWorktreeNoSignal(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := setupTask(t)
	gitRepoWithCommit(t, dir)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	commandHook = func(RunRequest) {
		cmd := exec.Command("git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "lead-commit")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	t.Cleanup(func() { commandHook = nil })
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	noted := false
	for _, e := range events {
		if e.Kind == "signal" && e.Signal == "git-write" {
			t.Errorf("git-write signal without a recorded worker write: %+v", e)
		}
		if e.Kind == "finished" && strings.Contains(e.Note, "no worker git write was recorded") {
			noted = true
		}
	}
	if !noted {
		t.Error("finished note does not say no worker git write was recorded")
	}
}
