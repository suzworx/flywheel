package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countRepo is a git repository with a committed a.txt ("one") and a
// committed .flywheel/events.jsonl (one line); it returns the commit.
func countRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	countWrite(t, dir, "a.txt", "one\n")
	countWrite(t, dir, ".flywheel/events.jsonl", "{}\n")
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-q", "-m", "init"})
	return dir, git(t, dir, []string{"rev-parse", "HEAD"})
}

// countWrite writes body to the slash path name under dir.
func countWrite(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestLeadBuiltCountSkipsFlywheelFiles(t *testing.T) {
	t.Parallel()
	dir, initial := countRepo(t)
	countWrite(t, dir, "a.txt", "two\n")
	countWrite(t, dir, ".flywheel/events.jsonl", "{}\n"+strings.Repeat("{}\n", 5))
	countWrite(t, dir, ".flywheel/briefs/t1.txt", strings.Repeat("brief\n", 6))
	countWrite(t, dir, "flywheel.md", strings.Repeat("# fw\n", 4))
	if n, err := unitChangedLines(dir, "", "t1"); err != nil || n != 2 {
		t.Errorf("unitChangedLines(no base) = %d, %v; want 2, nil", n, err)
	}
	git(t, dir, []string{"add", "a.txt"})
	git(t, dir, []string{"commit", "-q", "-m", "change a"})
	if n, err := unitChangedLines(dir, initial, "t1"); err != nil || n != 2 {
		t.Errorf("unitChangedLines(base %s) = %d, %v; want 2, nil", initial, n, err)
	}
}

func TestLeadBuiltCountCommitSkipsFlywheelFiles(t *testing.T) {
	t.Parallel()
	dir, _ := countRepo(t)
	countWrite(t, dir, "a.txt", "one\nx\ny\nz\n")
	countWrite(t, dir, ".flywheel/briefs/t1.txt", strings.Repeat("brief\n", 6))
	countWrite(t, dir, "flywheel.md", "# fw\n")
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-q", "-m", "change"})
	commit := git(t, dir, []string{"rev-parse", "HEAD"})
	if n, err := commitChangedLines(dir, "", commit); err != nil || n != 3 {
		t.Errorf("commitChangedLines() = %d, %v; want 3, nil", n, err)
	}
}
