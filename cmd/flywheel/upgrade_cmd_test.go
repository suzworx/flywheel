package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// runUpgradeHelperEnv carries the upgrade arguments into the child process
// TestUpgradeRefusesLiveLease re-execs, since runUpgrade exits the process.
const runUpgradeHelperEnv = "FLYWHEEL_TEST_RUN_UPGRADE_ARGS"

// runUpgradeProcess runs runUpgrade(args) in a child test process and
// returns its stderr and exit code.
func runUpgradeProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestUpgradeRefusesLiveLease$")
	cmd.Env = append(os.Environ(), runUpgradeHelperEnv+"="+strings.Join(args, "\x1f"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var e *exec.ExitError
	if errors.As(err, &e) {
		return stderr.String(), e.ExitCode()
	}
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	return stderr.String(), 0
}

// TestUpgradeRefusesLiveLease checks upgrade refuses with exit 6, naming the
// run, while a lease is live in --dir and installs nothing; with --force it
// warns and goes on to the release lookup (issue #461).
func TestUpgradeRefusesLiveLease(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runUpgradeHelperEnv); ok {
		runUpgrade(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	if err := flywheel.WriteLease(dir, flywheel.Lease{
		Task: "U7", Attempt: "a3", PID: 4242,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("WriteLease: %v", err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "flywheel")
	if err := os.WriteFile(dest, []byte("old binary"), 0o755); err != nil {
		t.Fatalf("write dest: %v", err)
	}
	base := []string{"--dir", dir, "--dest", dest, "--api", srv.URL, "--download", srv.URL, "--goos", "linux", "--goarch", "amd64"}

	stderr, code := runUpgradeProcess(t, base...)
	if code != 6 {
		t.Fatalf("exit = %d, want 6; stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"refused: 1 run(s) live in " + dir, "U7 a3 (pid 4242, expires ", "--force"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("refusal made %d release requests, want 0", n)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "old binary" {
		t.Errorf("dest after refusal = %q, %v; want it untouched", got, err)
	}

	stderr, code = runUpgradeProcess(t, append(base, "--force")...)
	if code == 6 || strings.Contains(stderr, "refused") {
		t.Fatalf("--force still refused (exit %d):\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "warning: --force") || !strings.Contains(stderr, "U7 a3") {
		t.Errorf("--force stderr missing the warning naming U7 a3:\n%s", stderr)
	}
	if hits.Load() == 0 {
		t.Errorf("--force made no release request; it should proceed past the lease check")
	}
}
