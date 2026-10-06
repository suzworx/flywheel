package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ownFilesRepo is initTask with flywheel's own files untracked and NOT
// ignored: an empty .gitignore is committed, info/exclude is emptied, and the
// ledger, config, a brief under .flywheel/ and flywheel.md are on disk. The
// unit has no other change.
func ownFilesRepo(t *testing.T) string {
	t.Helper()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), nil, 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	git(t, dir, []string{"add", ".gitignore"})
	git(t, dir, []string{"commit", "-m", "unignore flywheel"})
	exclude := git(t, dir, []string{"rev-parse", "--git-path", "info/exclude"})
	if !filepath.IsAbs(exclude) {
		exclude = filepath.Join(dir, exclude)
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		t.Fatalf("mkdir info: %v", err)
	}
	if err := os.WriteFile(exclude, nil, 0o644); err != nil {
		t.Fatalf("write info/exclude: %v", err)
	}
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	for name, body := range map[string]string{
		filepath.Join(".flywheel", "briefs", "T1.md"): "owns: a.go\nneeds: none\n",
		"flywheel.md": "# flywheel\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	untracked := git(t, dir, []string{"ls-files", "--others", "--exclude-standard"})
	for _, want := range []string{".flywheel/events.jsonl", ".flywheel/config.json", ".flywheel/briefs/T1.md", "flywheel.md"} {
		if !strings.Contains(untracked, want) {
			t.Fatalf("%s is not untracked and unignored; untracked:\n%s", want, untracked)
		}
	}
	return dir
}

// TestReviewOwnPaths is issue #793: flywheel's own files are never the
// unit's change, so they neither make a review's diff non-empty nor reach
// the reviewer's prompt.
func TestReviewOwnPaths(t *testing.T) {
	// not parallel: fakeClaudeAnswer sets PATH and the fake stream env
	t.Run("only own files", func(t *testing.T) {
		dir := ownFilesRepo(t)
		fakeClaudeAnswer(t, cleanAnswer)
		n := len(mustEvents(t, dir))
		_, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
		if err == nil || refusalRule(t, err) != "review-empty" {
			t.Fatalf("ReviewAgent() error = %v, want a review-empty refusal", err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".flywheel", "reviews", "T1.1.prompt.md")); err == nil {
			t.Errorf("T1.1.prompt.md exists: the reviewer was prepared on flywheel's own files")
		}
		if got := len(mustEvents(t, dir)); got != n {
			t.Errorf("events: %d after the refusal, want %d", got, n)
		}
	})
	t.Run("one real change", func(t *testing.T) {
		dir := ownFilesRepo(t)
		fakeClaudeAnswer(t, cleanAnswer)
		if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("real change\n"), 0o644); err != nil {
			t.Fatalf("write x.txt: %v", err)
		}
		res, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
		if err != nil {
			t.Fatalf("ReviewAgent() error = %v", err)
		}
		data, err := os.ReadFile(res.Prompt)
		if err != nil {
			t.Fatalf("read prompt: %v", err)
		}
		prompt := string(data)
		if !strings.Contains(prompt, "x.txt") {
			t.Errorf("prompt %s does not name x.txt", res.Prompt)
		}
		for _, own := range []string{".flywheel/", "flywheel.md"} {
			if strings.Contains(prompt, own) {
				t.Errorf("prompt %s names %s", res.Prompt, own)
			}
		}
	})
	t.Run("panel", func(t *testing.T) {
		dir := ownFilesRepo(t)
		n := len(mustEvents(t, dir))
		calls := 0
		member := func(string, string, ReviewAgentOptions) (ReviewAgentResult, error) {
			calls++
			return ReviewAgentResult{}, nil
		}
		_, err := ReviewPanel(dir, "T1", ReviewPanelOptions{Panel: []PanelMember{{Persona: "correctness"}, {Persona: "tests"}}, Session: "rev-1", review: member})
		if err == nil || refusalRule(t, err) != "review-empty" {
			t.Fatalf("ReviewPanel() error = %v, want a review-empty refusal", err)
		}
		if calls != 0 {
			t.Errorf("%d member(s) ran on flywheel's own files", calls)
		}
		if got := len(mustEvents(t, dir)); got != n {
			t.Errorf("events: %d after the refusal, want %d", got, n)
		}
	})
}
