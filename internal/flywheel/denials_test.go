package flywheel

import "testing"

// TestAttributeDenial checks a denied Bash command names the deny pattern and
// the segment that matched, never a leading cd or a quoted operator, and
// falls back to unattributed (issue #497).
func TestAttributeDenial(t *testing.T) {
	t.Parallel()
	deny := defaultDisallowedTools
	cases := []struct {
		name, command string
		deny          []string
		want          string
	}{
		{"cd prefix", `cd "D:/x y" && gh issue view 435; git branch -a --contains HEAD`, deny,
			"Bash: Bash(git branch:*) (git branch -a --contains HEAD)"},
		{"cd and", `cd "D:/x y" && git branch -a --contains HEAD`, deny,
			"Bash: Bash(git branch:*) (git branch -a --contains HEAD)"},
		{"semicolons", `node a.mjs; git add -N docs 2>/dev/null; git status`, deny,
			"Bash: Bash(git add:*) (git add -N docs 2>/dev/null)"},
		{"bare prefix", `git branch`, deny, "Bash: Bash(git branch:*) (git branch)"},
		{"not a word prefix", `git branchx`, deny, "Bash: unattributed (git branchx)"},
		{"quoted operator", `echo "a && git add x"`, deny, `Bash: unattributed (echo "a && git add x")`},
		{"single quoted", `echo 'x; git add y' | wc -l`, deny, `Bash: unattributed (echo 'x; git add y' | wc -l)`},
		{"env assignment", `FOO=1 git push origin`, deny, "Bash: Bash(git push:*) (git push origin)"},
		{"or and pipe", "go vet ./... || git stash\nls", deny, "Bash: Bash(git stash:*) (git stash)"},
		{"exact", `ls -la && make clean`, []string{"Bash(make clean)"}, "Bash: Bash(make clean) (make clean)"},
		{"exact no prefix", `make clean all`, []string{"Bash(make clean)"}, "Bash: unattributed (make clean all)"},
		{"bare Bash skips cd", `cd /w && ls`, []string{"Bash"}, "Bash: Bash (ls)"},
		{"no match", `go test ./...`, deny, "Bash: unattributed (go test ./...)"},
		{"clipped", "echo 0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890", deny,
			"Bash: unattributed (echo 012345678901234567890123456789012345678901234567890123456789012345678901234)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := attributeDenial(c.command, c.deny); got != c.want {
				t.Errorf("attributeDenial(%q) = %q, want %q", c.command, got, c.want)
			}
		})
	}
}
