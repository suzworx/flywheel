package flywheel

import (
	"os/exec"
	"strings"
	"testing"
)

// TestPRMergeHint pins issue #883: a commit whose subject carries a PR
// number and whose Flywheel-Task trailers name the task yields the
// `land --pr N` hint; anything else yields "".
func TestPRMergeHint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		full := append([]string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)
		cmd := exec.Command("git", full...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg string) string {
		t.Helper()
		git("commit", "--allow-empty", "-q", "-m", msg)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q")
	squash := commit("feat: x (#12)\n\nFlywheel-Task: A\nFlywheel-Task: B\n")
	merge := commit("Merge pull request #7 from o/b\n\nFlywheel-Task: A\n")
	plain := commit("feat: y\n\nFlywheel-Task: A\n")

	want := func(sha, pr, task string) string {
		return "hint: " + sha[:7] + " is PR #" + pr + "'s merge commit and names " + task +
			"; flywheel land --pr " + pr + " --session <session> lands every unit it names, with the PR's CI as evidence"
	}
	cases := []struct {
		name, task, commit, want string
	}{
		{"squash names task", "B", squash, want(squash, "12", "B")},
		{"merge commit names task", "A", merge, want(merge, "7", "A")},
		{"task not in trailers", "C", squash, ""},
		{"no PR number", "A", plain, ""},
		{"commit does not resolve", "A", "deadbeef", ""},
	}
	for _, c := range cases {
		if got := PRMergeHint(dir, c.task, c.commit); got != c.want {
			t.Errorf("%s: PRMergeHint(%q, %q) = %q, want %q", c.name, c.task, c.commit, got, c.want)
		}
	}
}
