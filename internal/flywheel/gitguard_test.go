package flywheel

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
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
	tests := []string{"status", "diff", "log", "show", "add", "rev-parse", "ls-files", "grep", "blame", "fetch", "config"}

	for _, subcmd := range tests {
		t.Run(subcmd, func(t *testing.T) {
			args := []string{subcmd}
			if subcmd == "add" {
				args = append(args, ".")
			}
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

	if len(env) != 2 {
		t.Errorf("installGitGuard returned %d env entries, want 2", len(env))
	}

	pathFound := false
	guardFound := false
	for _, e := range env {
		if len(e) > 5 && e[:5] == "PATH=" {
			pathFound = true
		}
		if len(e) > len(GitGuardEnv) && e[:len(GitGuardEnv)+1] == GitGuardEnv+"=" {
			guardFound = true
		}
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
