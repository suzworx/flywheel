package flywheel

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestGitGuardLinkRetry checks linkGuardBinary waits out a flywheel binary
// missing mid-upgrade (issue #461): a source that appears after two tries is
// linked, and one that never appears fails after five tries naming the path.
func TestGitGuardLinkRetry(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "flywheel")
	var sleeps int
	appear := func(time.Duration) {
		sleeps++
		if sleeps == 2 {
			if err := os.WriteFile(src, []byte("binary"), 0o755); err != nil {
				t.Fatalf("write src: %v", err)
			}
		}
	}
	dst := filepath.Join(dir, "git")
	if err := linkGuardBinary(src, dst, 5, time.Millisecond, appear); err != nil {
		t.Fatalf("linkGuardBinary with a source appearing after two tries: %v", err)
	}
	if sleeps != 2 {
		t.Errorf("slept %d times, want 2", sleeps)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "binary" {
		t.Errorf("dst = %q, %v; want %q", got, err, "binary")
	}

	missing := filepath.Join(dir, "gone")
	var tries int
	err := linkGuardBinary(missing, filepath.Join(dir, "git2"), 5, time.Millisecond, func(time.Duration) { tries++ })
	if err == nil {
		t.Fatal("linkGuardBinary with a source that never appears succeeded")
	}
	if tries != 4 {
		t.Errorf("slept %d times, want 4 (5 tries)", tries)
	}
	if !strings.Contains(err.Error(), missing) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want it to name %s and wrap not-exist", err, missing)
	}
}

func TestGitGuardRefusesHistoryWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"commit", "-m", "test"}, "commit"},
		{[]string{"push", "origin", "main"}, "push"},
		{[]string{"stash"}, "stash"},
		{[]string{"reset", "--hard"}, "reset"},
		{[]string{"checkout", "main"}, "checkout"},
		{[]string{"switch", "x"}, "switch"},
		{[]string{"rebase", "main"}, "rebase"},
		{[]string{"merge", "x"}, "merge"},
		{[]string{"tag", "v1"}, "tag"},
		{[]string{"pull"}, "pull"},
		{[]string{"-C", "/x", "commit"}, "commit"},
		{[]string{"-c", "a=b", "push"}, "push"},
		{[]string{"branch", "-D", "x"}, "branch"},
		{[]string{"branch", "new"}, "branch"},
	}

	for _, tt := range tests {
		t.Run(tt.args[0], func(t *testing.T) {
			refused, sub := GitGuardRefused(tt.args)
			if !refused || sub != tt.want {
				t.Errorf("GitGuardRefused(%v) = (%v, %q), want (true, %q)", tt.args, refused, sub, tt.want)
			}
		})
	}
}

func TestGitGuardAllowsReads(t *testing.T) {
	t.Parallel()
	tests := []string{"status", "diff", "log", "show", "rev-parse", "ls-files", "grep", "blame", "config"}

	for _, subcmd := range tests {
		t.Run(subcmd, func(t *testing.T) {
			args := []string{subcmd}
			if subcmd == "rev-parse" {
				args = append(args, "HEAD")
			}
			if subcmd == "config" {
				args = append(args, "--get", "user.name")
			}

			refused, _ := GitGuardRefused(args)
			if refused {
				t.Errorf("GitGuardRefused(%v) refused read command", args)
			}
		})
	}

	allowedBranchOpts := [][]string{
		{"branch"},
		{"branch", "--list"},
		{"branch", "--show-current"},
		{"branch", "-a"},
		{"branch", "-r"},
		{"branch", "-v"},
	}

	for _, args := range allowedBranchOpts {
		t.Run("branch-"+args[len(args)-1], func(t *testing.T) {
			refused, _ := GitGuardRefused(args)
			if refused {
				t.Errorf("GitGuardRefused(%v) refused allowed branch option", args)
			}
		})
	}

	globalOptTests := []struct {
		args []string
		name string
	}{
		{[]string{"-C", "/x", "status"}, "global-C"},
		{[]string{"--no-pager", "log"}, "global-no-pager"},
	}

	for _, tt := range globalOptTests {
		t.Run(tt.name, func(t *testing.T) {
			refused, _ := GitGuardRefused(tt.args)
			if refused {
				t.Errorf("GitGuardRefused(%v) refused with global options", tt.args)
			}
		})
	}
}

// isolateGuardEnv points GitGuardEnv at a test-owned bin directory and clears
// GitGuardRepoEnv, so an in-process GitGuard never logs to the guard log of an
// enclosing flywheel run attempt: a gate's go test inherits the worker's
// environment (#442). The enclosing guard directory is dropped from PATH too.
func isolateGuardEnv(t *testing.T) {
	t.Helper()
	if live := os.Getenv(GitGuardEnv); live != "" {
		t.Setenv("PATH", removeGuardDir(os.Getenv("PATH"), filepath.Clean(live)))
	}
	t.Setenv(GitGuardEnv, filepath.Join(t.TempDir(), "test.bin"))
	t.Setenv(GitGuardRepoEnv, "")
}

// TestGitGuardTestsIsolateLiveLog checks that a refusal under isolateGuardEnv
// is logged to the test's own log, never to the enclosing attempt's (#442).
func TestGitGuardTestsIsolateLiveLog(t *testing.T) {
	// not parallel: isolateGuardEnv and t.Setenv change PATH and the guard env
	live := filepath.Join(t.TempDir(), "T1.r1.bin")
	t.Setenv(GitGuardEnv, live)
	t.Run("isolated", func(t *testing.T) {
		isolateGuardEnv(t)
		var out, errb bytes.Buffer
		if rc := GitGuard([]string{"commit", "-m", "test"}, nil, &out, &errb); rc != 1 {
			t.Errorf("GitGuard(commit...) returned %d, want 1", rc)
		}
		if refused, _ := readGitGuardLog(os.Getenv(GitGuardEnv)); strings.Join(refused, ",") != "commit" {
			t.Errorf("isolated log refused %v, want [commit]", refused)
		}
	})
	if _, err := os.Stat(gitGuardLogPath(live)); !os.IsNotExist(err) {
		t.Errorf("the live guard log was written (stat err %v), want none", err)
	}
}

func TestGitGuardPassesThrough(t *testing.T) {
	// not parallel: isolateGuardEnv changes PATH and the guard env
	isolateGuardEnv(t)
	var out, errb bytes.Buffer
	rc := GitGuard([]string{"--version"}, nil, &out, &errb)
	if rc != 0 {
		t.Errorf("GitGuard(--version) returned %d, want 0", rc)
	}
	if !bytes.Contains(out.Bytes(), []byte("git version")) {
		t.Errorf("GitGuard(--version) output %q does not contain 'git version'", out.String())
	}
}

func TestGitGuardRefusalExitsOne(t *testing.T) {
	// not parallel: isolateGuardEnv changes PATH and the guard env
	isolateGuardEnv(t)
	var out, errb bytes.Buffer
	rc := GitGuard([]string{"commit", "-m", "test"}, nil, &out, &errb)
	if rc != 1 {
		t.Errorf("GitGuard(commit...) returned %d, want 1", rc)
	}
	if !bytes.Contains(errb.Bytes(), []byte("workers never")) {
		t.Errorf("GitGuard(commit...) stderr %q does not contain 'workers never'", errb.String())
	}
	if bytes.Contains(errb.Bytes(), []byte("--no-index")) {
		t.Errorf("GitGuard(commit...) stderr %q carries the index clause", errb.String())
	}
}

// TestGitGuardRefusesAddIntentToAdd checks git add -N is refused and the
// refusal names the read-only way to diff a new file (issue #478).
func TestGitGuardRefusesAddIntentToAdd(t *testing.T) {
	// not parallel: isolateGuardEnv and t.Setenv change PATH and the guard env
	isolateGuardEnv(t)
	t.Setenv("GIT_INDEX_FILE", "")
	args := []string{"add", "-N", "x"}
	if refused, sub := GitGuardRefused(args); !refused || sub != "add" {
		t.Fatalf("GitGuardRefused(add -N x) = %v, %q, want true, add", refused, sub)
	}
	var out, errb bytes.Buffer
	if rc := GitGuard(args, nil, &out, &errb); rc != 1 {
		t.Errorf("GitGuard(add -N x) returned %d, want 1", rc)
	}
	if !bytes.Contains(errb.Bytes(), []byte("--no-index")) {
		t.Errorf("GitGuard(add -N x) stderr %q does not name --no-index", errb.String())
	}
	if !bytes.Contains(errb.Bytes(), []byte("3 = whitespace errors")) {
		t.Errorf("GitGuard(add -N x) stderr %q does not state the exit status", errb.String())
	}
}

func TestGitGuardSkipsGuardDir(t *testing.T) {
	// not parallel: isolateGuardEnv and t.Setenv change PATH and the guard env
	isolateGuardEnv(t)
	guardDir := t.TempDir()
	t.Setenv(GitGuardEnv, guardDir)

	dummyFile := filepath.Join(guardDir, "git")
	if runtime.GOOS == "windows" {
		dummyFile = filepath.Join(guardDir, "git.exe")
	}

	f, err := os.Create(dummyFile)
	if err != nil {
		t.Fatalf("create dummy git: %v", err)
	}
	f.Close()

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", guardDir+string(os.PathListSeparator)+oldPath)

	var out, errb bytes.Buffer
	rc := GitGuard([]string{"--version"}, nil, &out, &errb)
	if rc != 0 {
		t.Errorf("GitGuard(--version) with guard dir on PATH returned %d, want 0", rc)
	}
	if !bytes.Contains(out.Bytes(), []byte("git version")) {
		t.Errorf("GitGuard(--version) output %q does not contain 'git version'", out.String())
	}
}

func TestGitGuardInstall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bin, env, err := installGitGuard(dir, "T1", "r1")
	if err != nil {
		t.Fatalf("installGitGuard: %v", err)
	}

	if _, err := os.Stat(bin); err != nil {
		t.Errorf("bin directory %s: %v", bin, err)
	}

	gitName := "git"
	if runtime.GOOS == "windows" {
		gitName = "git.exe"
	}
	gitPath := filepath.Join(bin, gitName)
	if _, err := os.Stat(gitPath); err != nil {
		t.Errorf("%s not found in bin dir: %v", gitName, err)
	}

	pathFound := false
	guardFound := false
	realFound := false
	for _, e := range env {
		if len(e) > 5 && e[:5] == "PATH=" {
			pathFound = true
		}
		if len(e) > len(GitGuardEnv) && e[:len(GitGuardEnv)+1] == GitGuardEnv+"=" {
			guardFound = true
		}
		if strings.HasPrefix(e, GitRealEnv+"=") {
			realFound = true
		}
	}
	if !realFound {
		t.Error(GitRealEnv + "= entry not found in env")
	}

	if !pathFound {
		t.Error("PATH= entry not found in env")
	}
	if !guardFound {
		t.Error(GitGuardEnv + "= entry not found in env")
	}

	if err := os.RemoveAll(bin); err != nil {
		t.Errorf("cleanup bin: %v", err)
	}
}

func TestGitGuardBranchListing(t *testing.T) {
	t.Parallel()
	tests := [][]string{
		{"branch", "-a"},
		{"branch", "-r"},
		{"branch", "--list"},
	}

	for _, args := range tests {
		t.Run(args[len(args)-1], func(t *testing.T) {
			refused, _ := GitGuardRefused(args)
			if refused {
				t.Errorf("GitGuardRefused(%v) should allow branch listing", args)
			}
		})
	}
}

// TestGitGuardSeparateValueOptions checks that a global option given its value
// as the next argument (--git-dir /x) is skipped, not taken as the subcommand.
func TestGitGuardSeparateValueOptions(t *testing.T) {
	t.Parallel()
	if refused, sub := GitGuardRefused([]string{"--git-dir", "/x/.git", "commit", "-m", "m"}); !refused || sub != "commit" {
		t.Errorf("--git-dir /x commit = %v %q, want refused commit", refused, sub)
	}
	if refused, _ := GitGuardRefused([]string{"--work-tree", "/x", "status"}); refused {
		t.Error("--work-tree /x status refused, want allowed")
	}
}

// TestGitGuardAllowListRefusesTheRest checks the #325 review cases: an alias,
// and commands that mutate the index or working tree, are refused; branch,
// tag, config, stash and worktree pass only in their read-only forms.
func TestGitGuardAllowListRefusesTheRest(t *testing.T) {
	t.Parallel()
	refused := [][]string{
		{"-c", "alias.publish=push", "publish", "origin", "main"},
		{"publish"},
		{"clean", "-fd"}, {"add", "."}, {"rm", "a.go"}, {"mv", "a", "b"},
		{"update-index", "--add", "a"}, {"read-tree", "HEAD"}, {"checkout-index", "-a"},
		{"fetch"}, {"apply", "p.diff"}, {"gc"},
		{"branch", "new"}, {"branch", "-D", "x"}, {"branch", "--set-upstream-to=origin/x"},
		{"tag", "v1"}, {"config", "user.name", "x"}, {"config", "--unset", "a.b"},
		{"stash"}, {"stash", "push"}, {"worktree", "add", "../x"}, {"remote", "add", "o", "u"},
		{"reflog", "expire", "--all"},
	}
	for _, args := range refused {
		if r, _ := GitGuardRefused(args); !r {
			t.Errorf("GitGuardRefused(%v) allowed, want refused", args)
		}
	}
	allowed := [][]string{
		{}, {"--version"}, {"help", "log"},
		{"branch", "--list", "release/*"}, {"branch", "--contains", "abc"}, {"branch", "--merged", "main"},
		{"tag"}, {"tag", "-l", "v1.*"}, {"config", "--get", "user.email"}, {"config", "--list"},
		{"stash", "list"}, {"worktree", "list"}, {"remote", "-v"}, {"reflog"}, {"reflog", "show", "HEAD"},
		{"-C", "/x", "log", "--oneline"}, {"diff-tree", "-r", "HEAD"}, {"cat-file", "-p", "HEAD"},
	}
	for _, args := range allowed {
		if r, _ := GitGuardRefused(args); r {
			t.Errorf("GitGuardRefused(%v) refused, want allowed", args)
		}
	}
}

// TestGitGuardIndexWrites checks that every index write a worker could leave
// behind (#391: intent-to-add entries it could not undo) is refused.
func TestGitGuardIndexWrites(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"add", "a.go"}, {"add", "-N", "a.go"}, {"add", "--intent-to-add", "a.go"},
		{"rm", "--cached", "a.go"}, {"update-index", "--add", "a.go"},
		{"restore", "--staged", "a.go"}, {"-C", "/x", "add", "-N", "a.go"},
	} {
		if r, _ := GitGuardRefused(args); !r {
			t.Errorf("GitGuardRefused(%v) allowed, want refused", args)
		}
	}
}

// commonDir returns dir's absolute git common directory.
func commonDir(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		t.Fatalf("git rev-parse in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a git repository with one commit in a fresh temp dir.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	for _, args := range [][]string{
		{"-c", "user.name=t", "-c", "user.email=t@e.x", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// TestGitGuardScopedToUnitRepo checks that the guard protects the unit's
// repository only: a gate's tests must be able to init and commit in their own
// temporary repositories (#325).
func TestGitGuardScopedToUnitRepo(t *testing.T) {
	// not parallel: isolateGuardEnv and t.Setenv change PATH and the guard env
	isolateGuardEnv(t)
	unit, other := newRepo(t), newRepo(t)
	t.Setenv(GitGuardRepoEnv, commonDir(t, unit))
	commit := func(dir string) int {
		var out, errb bytes.Buffer
		return GitGuard([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@e.x", "commit", "-q", "--allow-empty", "-m", "x"}, nil, &out, &errb)
	}
	if rc := commit(unit); rc != 1 {
		t.Errorf("commit in the unit's repository: rc %d, want 1 (refused)", rc)
	}
	if rc := commit(other); rc != 0 {
		t.Errorf("commit in another repository: rc %d, want 0 (passed through)", rc)
	}
	fresh := t.TempDir()
	var out, errb bytes.Buffer
	if rc := GitGuard([]string{"-C", fresh, "init", "-q"}, nil, &out, &errb); rc != 0 {
		t.Errorf("git init in a temp dir: rc %d, want 0 (%s)", rc, errb.String())
	}
}

// TestGitGuardLogsWrites checks that the guard logs each write-class call
// beside its bin directory, and nothing for a read (#361).
func TestGitGuardLogsWrites(t *testing.T) {
	// not parallel: isolateGuardEnv and t.Setenv change PATH and the guard env
	isolateGuardEnv(t)
	unit := newRepo(t)
	binDir := filepath.Join(t.TempDir(), "T1.r1.bin")
	t.Setenv(GitGuardEnv, binDir)
	t.Setenv(GitGuardRepoEnv, commonDir(t, unit))
	var out, errb bytes.Buffer
	if rc := GitGuard([]string{"-C", unit, "status"}, nil, &out, &errb); rc != 0 {
		t.Fatalf("status: rc %d, want 0 (%s)", rc, errb.String())
	}
	if _, err := os.Stat(gitGuardLogPath(binDir)); !os.IsNotExist(err) {
		t.Errorf("status wrote a guard log (stat err %v), want none", err)
	}
	if rc := GitGuard([]string{"-C", unit, "commit", "-q", "--allow-empty", "-m", "x"}, nil, &out, &errb); rc != 1 {
		t.Fatalf("commit in the unit's repository: rc %d, want 1 (refused)", rc)
	}
	refused, allowed := readGitGuardLog(binDir)
	if strings.Join(refused, ",") != "commit" || len(allowed) != 0 {
		t.Errorf("log = refused %v allowed %v, want refused [commit], allowed []", refused, allowed)
	}
	b, err := os.ReadFile(gitGuardLogPath(binDir))
	if err != nil || strings.TrimSpace(string(b)) != "refused commit" {
		t.Errorf("log file = %q (%v), want \"refused commit\"", b, err)
	}
}
