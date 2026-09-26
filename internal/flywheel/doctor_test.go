package flywheel

import (
	"os"
	"path/filepath"
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
