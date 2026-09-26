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

// TestDoctorRoutingCandidates checks the routing candidates are probed after
// the fallbacks, and a candidate that repeats the model is probed once (#474).
func TestDoctorRoutingCandidates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeDoctorFixture(t, dir, "ok.jsonl", "")
	writeDoctorFixture(t, dir, "fb.jsonl", "")
	writeDoctorFixture(t, dir, "cand.jsonl", "")
	cfg := Config{Version: 1, Workers: []Worker{{Name: "sim", Adapter: "sim", Model: "ok.jsonl",
		Fallbacks: []Fallback{{Model: "fb.jsonl", Approved: true}},
		Routing:   &Routing{Candidates: []string{"ok.jsonl", "cand.jsonl"}, Objective: "accepted_rate"}}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	probes, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor() error = %v", err)
	}
	want := []string{"ok.jsonl", "fb.jsonl", "cand.jsonl"}
	if len(probes) != len(want) {
		t.Fatalf("probes = %+v, want models %v", probes, want)
	}
	for i, m := range want {
		if probes[i].Model != m || probes[i].Class != ClassOK {
			t.Errorf("probes[%d] = %+v, want {%s ok}", i, probes[i], m)
		}
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

// TestDoctorLedger: the ledger is committed (owner decision 2026-09-26,
// #464), so doctor warns when it is untracked or ignored, or tracked without
// merge=union (#436), and is silent when it is tracked with merge=union.
func TestDoctorLedger(t *testing.T) {
	t.Parallel()
	const ev = ".flywheel/events.jsonl"
	wantAll := func(t *testing.T, got string, wants ...string) {
		t.Helper()
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("DoctorLedgerWarning = %q, want it to contain %q", got, want)
			}
		}
	}
	commitFile := func(t *testing.T, dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, []string{"add", "--", name})
		git(t, dir, []string{"commit", "-q", "-m", name})
	}
	t.Run("untracked", func(t *testing.T) {
		t.Parallel()
		dir := ledgerRepo(t, []string{ev}, nil)
		wantAll(t, DoctorLedgerWarning(dir), "not tracked by git (.flywheel/events.jsonl)", "audit", "git add .flywheel/events.jsonl")
	})
	t.Run("ignored", func(t *testing.T) {
		t.Parallel()
		dir := ledgerRepo(t, []string{ev, ".flywheel/events/T1.jsonl"}, nil)
		commitFile(t, dir, ".gitignore", ".flywheel/\n")
		wantAll(t, DoctorLedgerWarning(dir), "git-ignored", ".gitignore:1:.flywheel/", "remove that ignore rule", "git add .flywheel/events.jsonl .flywheel/events")
	})
	t.Run("tracked without merge=union", func(t *testing.T) {
		t.Parallel()
		dir := ledgerRepo(t, []string{ev}, []string{ev})
		wantAll(t, DoctorLedgerWarning(dir), "(.flywheel/events.jsonl)", `".flywheel/events.jsonl merge=union"`, ".gitattributes", "(#436)")
	})
	t.Run("shards without merge=union", func(t *testing.T) {
		t.Parallel()
		shards := []string{".flywheel/events/T1.jsonl", ".flywheel/events/T2.jsonl", ".flywheel/events/T3.jsonl", ".flywheel/events/T4.jsonl"}
		dir := ledgerRepo(t, shards, shards)
		wantAll(t, DoctorLedgerWarning(dir), "(.flywheel/events/T1.jsonl, .flywheel/events/T2.jsonl, .flywheel/events/T3.jsonl and 1 more)", `".flywheel/events/*.jsonl merge=union"`)
	})
	t.Run("tracked with merge=union", func(t *testing.T) {
		t.Parallel()
		dir := ledgerRepo(t, []string{ev}, []string{ev})
		commitFile(t, dir, ".gitattributes", ev+" merge=union\n")
		if got := DoctorLedgerWarning(dir); got != "" {
			t.Errorf("DoctorLedgerWarning(tracked, union) = %q, want empty", got)
		}
	})
	t.Run("init's flywheel gitattributes", func(t *testing.T) {
		t.Parallel()
		files := []string{ev, ".flywheel/events/T1.jsonl"}
		dir := ledgerRepo(t, files, files)
		commitFile(t, dir, ".flywheel/.gitattributes", "events.jsonl merge=union\nevents/*.jsonl merge=union\n")
		if got := DoctorLedgerWarning(dir); got != "" {
			t.Errorf("DoctorLedgerWarning(.flywheel/.gitattributes) = %q, want empty", got)
		}
	})
	t.Run("no ledger or missing dir", func(t *testing.T) {
		t.Parallel()
		dir := ledgerRepo(t, nil, nil)
		if got := DoctorLedgerWarning(dir); got != "" {
			t.Errorf("DoctorLedgerWarning(no ledger) = %q, want empty", got)
		}
		if got := DoctorLedgerWarning(filepath.Join(dir, "missing")); got != "" {
			t.Errorf("DoctorLedgerWarning(missing dir) = %q, want empty", got)
		}
	})
	t.Run("not a repo", func(t *testing.T) {
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
	})
	t.Run("index unchanged", testDoctorLedgerIndexUntouched)
}

// testDoctorLedgerIndexUntouched: the check never writes the index, even with
// a dirty tracked ledger that a refreshing command would restat.
func testDoctorLedgerIndexUntouched(t *testing.T) {
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
