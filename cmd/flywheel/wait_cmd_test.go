package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestWaitCmd checks flywheel wait's exit codes (issue #393): 0 every finish
// clean, 4 any unclean, 8 timeout, 2 usage.
func TestWaitCmd(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := flywheel.AppendEvents(dir, []flywheel.Event{
		{Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{Task: "T1", Kind: "finished", Attempt: "r1", Reason: "stop"},
		{Task: "T2", Kind: "dispatched", Attempt: "r1"},
		{Task: "T2", Kind: "finished", Attempt: "r1", Reason: "error"},
		{Task: "T3", Kind: "dispatched", Attempt: "r1"},
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"clean", []string{"T1", "--dir", dir}, 0},
		{"unclean", []string{"T1", "T2", "--dir", dir}, 4},
		{"timeout", []string{"T3", "--dir", dir, "--timeout", "30ms", "--interval", "10ms"}, 8},
		{"no task", []string{"--dir", dir}, 2},
		{"bad flag", []string{"T1", "--bogus"}, 2},
		{"bad interval", []string{"T1", "--dir", dir, "--interval", "0s"}, 2},
	}
	for _, tc := range cases {
		var out, errb bytes.Buffer
		if got := waitMain(tc.args, &out, &errb); got != tc.want {
			t.Errorf("%s: waitMain(%v) = %d, want %d (stderr %q)", tc.name, tc.args, got, tc.want, errb.String())
		}
	}
}

// TestRunNotify checks run's --notify hook (issue #393): the command runs
// through the shell with FLYWHEEL_FINISHED set, and a failing command only
// warns.
func TestRunNotify(t *testing.T) {
	fs, o := runFlags()
	if err := fs.Parse([]string{"--notify", "echo hi"}); err != nil || o.notify != "echo hi" {
		t.Fatalf("--notify parse: %v, notify=%q", err, o.notify)
	}
	line := finishedLine("T1", "r2", "stop", 0)
	if line != "T1 r2 reason=stop exit=0" {
		t.Errorf("finishedLine = %q", line)
	}
	if got := finishedLine("T1", "", "refused", 6); got != "T1 - reason=refused exit=6" {
		t.Errorf("finishedLine(no attempt) = %q", got)
	}

	out := filepath.ToSlash(filepath.Join(t.TempDir(), "finished.txt"))
	cmd := `echo "$FLYWHEEL_FINISHED" > "` + out + `"`
	if _, err := exec.LookPath("bash"); err != nil && runtime.GOOS == "windows" {
		cmd = `echo %FLYWHEEL_FINISHED%> "` + filepath.FromSlash(out) + `"`
	}
	var errb bytes.Buffer
	runNotify(cmd, line, &errb)
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("notify wrote nothing: %v (stderr %q)", err, errb.String())
	}
	if got := strings.TrimSpace(string(b)); got != line {
		t.Errorf("FLYWHEEL_FINISHED = %q, want %q", got, line)
	}
	if strings.Contains(errb.String(), "warning") {
		t.Errorf("unexpected warning: %q", errb.String())
	}

	errb.Reset()
	runNotify("exit 3", line, &errb)
	if !strings.Contains(errb.String(), "warning: --notify command failed") {
		t.Errorf("failing notify: stderr = %q, want a warning", errb.String())
	}
}
