package flywheel

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGitGuardRefusesHistoryWrites(t *testing.T) {
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

func TestGitGuardPassesThrough(t *testing.T) {
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
	var out, errb bytes.Buffer
	rc := GitGuard([]string{"commit", "-m", "test"}, nil, &out, &errb)
	if rc != 1 {
		t.Errorf("GitGuard(commit...) returned %d, want 1", rc)
	}
	if !bytes.Contains(errb.Bytes(), []byte("workers never")) {
		t.Errorf("GitGuard(commit...) stderr %q does not contain 'workers never'", errb.String())
	}
}

func TestGitGuardSkipsGuardDir(t *testing.T) {
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
