package flywheel

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDoctorFixture writes a sim fixture: a step_start carrying sessionID,
// then either a step_finish{reason:"stop"} (errMsg == "") or a top-level
// error.data.message line, matching the shape adapter.go's errorMessage
// reads.
func writeDoctorFixture(t *testing.T, dir, name, errMsg string) {
	t.Helper()
	line2 := `{"type":"step_finish","sessionID":"s1","part":{"type":"step_finish","reason":"stop"}}`
	if errMsg != "" {
		line2 = `{"type":"error","sessionID":"s1","error":{"data":{"message":"` + errMsg + `"}}}`
	}
	content := `{"type":"step_start","sessionID":"s1","part":{"type":"step_start"}}` + "\n" + line2 + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

// TestDoctorShellWarning checks the WSL-launcher line (issue #471) names both
// the launcher and the shell used instead, and is empty otherwise.
func TestDoctorShellWarning(t *testing.T) {
	t.Parallel()
	const wsl = `C:\Windows\System32\bash.exe`
	const git = `C:\Program Files\Git\bin\bash.exe`
	h := fakeShellHost("windows", map[string]string{"bash": wsl, "git": `C:\Program Files\Git\cmd\git.exe`}, git)
	want := "bash on PATH is the WSL launcher (" + wsl + "); gates and worktree.setup use " + git
	if got := shellWarning(h); got != want {
		t.Errorf("shellWarning = %q, want %q", got, want)
	}
	if got := shellWarning(fakeShellHost("windows", map[string]string{"bash": git})); got != "" {
		t.Errorf("shellWarning(Git bash) = %q, want empty", got)
	}
	if got := shellWarning(fakeShellHost("linux", map[string]string{"bash": "/bin/bash"})); got != "" {
		t.Errorf("shellWarning(linux) = %q, want empty", got)
	}
}

func TestDoctorClassification(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name, errMsg, want string
	}{
		{"ok.jsonl", "", ClassOK},
		{"consent.jsonl", "requires explicit opt in", ClassConsent},
		{"limit.jsonl", "rate limit exceeded", ClassLimit},
		{"credits.jsonl", "insufficient credits", ClassCredits},
		{"auth.jsonl", "invalid api key", ClassAuth},
		{"unknown.jsonl", "something went sideways", ClassError},
	}
	var fallbacks []Fallback
	for _, c := range cases[1:] {
		writeDoctorFixture(t, dir, c.name, c.errMsg)
		fallbacks = append(fallbacks, Fallback{Model: c.name, Approved: true})
	}
	writeDoctorFixture(t, dir, cases[0].name, cases[0].errMsg)
	cfg := Config{Version: 1, Workers: []Worker{{Name: "sim", Adapter: "sim", Model: cases[0].name, Fallbacks: fallbacks}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	probes, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if len(probes) != len(cases) {
		t.Fatalf("probes = %d, want %d", len(probes), len(cases))
	}
	for i, c := range cases {
		if probes[i].Model != c.name || probes[i].Class != c.want {
			t.Errorf("probes[%d] = %+v, want {%s %s}", i, probes[i], c.name, c.want)
		}
	}
	if DoctorAllOK(probes) {
		t.Error("DoctorAllOK() = true, want false (unknown.jsonl is not ok)")
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("events after Doctor() = %d, want 0 (no events recorded)", len(evs))
	}
}

// TestDoctorMissingFixture checks a fallback naming a nonexistent fixture
// classifies as ClassError instead of panicking, and every other probe still
// runs.
func TestDoctorMissingFixture(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDoctorFixture(t, dir, "ok.jsonl", "")
	cfg := Config{Version: 1, Workers: []Worker{{Name: "sim", Adapter: "sim", Model: "ok.jsonl",
		Fallbacks: []Fallback{{Model: "missing.jsonl", Approved: true}}}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	probes, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	if len(probes) != 2 || probes[0].Class != ClassOK || probes[1].Class != ClassError {
		t.Errorf("probes = %+v, want [ok.jsonl:ok missing.jsonl:error]", probes)
	}
	if DoctorAllOK(probes) {
		t.Error("DoctorAllOK() = true, want false")
	}
}

// ledgerRepo creates a git repository in a temp dir holding the ledger files
// (paths relative to it, each written with one line), commits the ones in
// commit, and returns the dir.
func ledgerRepo(t *testing.T, files, commit []string) string {
	t.Helper()
	dir := t.TempDir()
	initGitRepoAt(t, dir)
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(commit) > 0 {
		git(t, dir, append([]string{"add", "--"}, commit...))
	}
	git(t, dir, []string{"commit", "-q", "--allow-empty", "-m", "seed"})
	return dir
}

// TestLedgerTrackedWarningEvents: a committed events.jsonl is named, with the
// fix (#464).
func TestLedgerTrackedWarningEvents(t *testing.T) {
	t.Parallel()
	dir := ledgerRepo(t, []string{".flywheel/events.jsonl"}, []string{".flywheel/events.jsonl"})
	got := DoctorLedgerWarning(dir)
	for _, want := range []string{"the ledger is tracked by git (.flywheel/events.jsonl)", "git rm --cached -r .flywheel/events.jsonl", ".gitignore", "merge=union", "(#436)"} {
		if !strings.Contains(got, want) {
			t.Errorf("DoctorLedgerWarning = %q, want it to contain %q", got, want)
		}
	}
}

// TestLedgerTrackedWarningShard: a tracked shard file is named; past three
// paths the rest are counted, and the untrack command names the directory.
func TestLedgerTrackedWarningShard(t *testing.T) {
	t.Parallel()
	dir := ledgerRepo(t, []string{".flywheel/events/T1.jsonl"}, []string{".flywheel/events/T1.jsonl"})
	got := DoctorLedgerWarning(dir)
	for _, want := range []string{"(.flywheel/events/T1.jsonl)", "git rm --cached -r .flywheel/events)"} {
		if !strings.Contains(got, want) {
			t.Errorf("DoctorLedgerWarning = %q, want it to contain %q", got, want)
		}
	}
	shards := []string{".flywheel/events/T1.jsonl", ".flywheel/events/T2.jsonl", ".flywheel/events/T3.jsonl", ".flywheel/events/T4.jsonl"}
	dir = ledgerRepo(t, shards, shards)
	got = DoctorLedgerWarning(dir)
	for _, want := range []string{"(.flywheel/events/T1.jsonl, .flywheel/events/T2.jsonl, .flywheel/events/T3.jsonl and 1 more)", "git rm --cached -r .flywheel/events)"} {
		if !strings.Contains(got, want) {
			t.Errorf("DoctorLedgerWarning = %q, want it to contain %q", got, want)
		}
	}
}

// TestLedgerTrackedWarningUntracked: a ledger that is present but ignored and
// untracked gives no warning.
func TestLedgerTrackedWarningUntracked(t *testing.T) {
	t.Parallel()
	dir := ledgerRepo(t, []string{".gitignore", ".flywheel/events.jsonl", ".flywheel/events/T1.jsonl"}, nil)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", ".gitignore"})
	git(t, dir, []string{"commit", "-q", "-m", "ignore"})
	if got := DoctorLedgerWarning(dir); got != "" {
		t.Errorf("DoctorLedgerWarning(untracked) = %q, want empty", got)
	}
	if got := DoctorLedgerWarning(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("DoctorLedgerWarning(missing dir) = %q, want empty", got)
	}
}

// TestLedgerTrackedWarningNotRepo: outside a repository there is no warning.
func TestLedgerTrackedWarningNotRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DoctorLedgerWarning(dir); got != "" {
		t.Errorf("DoctorLedgerWarning(not a repo) = %q, want empty", got)
	}
}

// TestLedgerTrackedWarningIndexUntouched: the check never writes the index,
// even with a dirty tracked ledger that a refreshing command would restat.
func TestLedgerTrackedWarningIndexUntouched(t *testing.T) {
	t.Parallel()
	dir := ledgerRepo(t, []string{".flywheel/events.jsonl"}, []string{".flywheel/events.jsonl"})
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), []byte("{}\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := filepath.Join(dir, ".git", "index")
	before, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if got := DoctorLedgerWarning(dir); got == "" {
		t.Error("DoctorLedgerWarning = \"\", want the tracked-ledger warning")
	}
	after, err := os.ReadFile(idx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error(".git/index changed across DoctorLedgerWarning")
	}
}
