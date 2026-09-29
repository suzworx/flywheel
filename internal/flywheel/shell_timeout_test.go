package flywheel

import (
	"strings"
	"testing"
	"time"
)

// lastEnv returns the value of the last entry for key in env, as os/exec
// would use it.
func lastEnv(env []string, key string) (string, int) {
	val, n := "", 0
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			val, n = v, n+1
		}
	}
	return val, n
}

// TestShellTimeoutSetsBothWhenAbsent checks both Bash tool variables are set
// to the limit in milliseconds when env carries neither (issue #678).
func TestShellTimeoutSetsBothWhenAbsent(t *testing.T) {
	t.Parallel()
	in := []string{"PATH=/bin", "HOME=/home/w"}
	got := claudeShellEnv(in, 60*time.Minute)
	for _, key := range []string{"BASH_MAX_TIMEOUT_MS", "BASH_DEFAULT_TIMEOUT_MS"} {
		if v, n := lastEnv(got, key); v != "3600000" || n != 1 {
			t.Errorf("%s = %q (%d entries), want 3600000 once", key, v, n)
		}
	}
	if len(in) != 2 {
		t.Errorf("input env modified: %q", in)
	}
}

// TestShellTimeoutKeepsDefaultOverridesMax checks an inherited
// BASH_DEFAULT_TIMEOUT_MS is kept and an inherited BASH_MAX_TIMEOUT_MS is
// overridden: the configured limit is the last entry, the one os/exec uses.
func TestShellTimeoutKeepsDefaultOverridesMax(t *testing.T) {
	t.Parallel()
	in := []string{"BASH_MAX_TIMEOUT_MS=600000", "BASH_DEFAULT_TIMEOUT_MS=120000"}
	got := claudeShellEnv(in, 90*time.Minute)
	if v, _ := lastEnv(got, "BASH_MAX_TIMEOUT_MS"); v != "5400000" {
		t.Errorf("BASH_MAX_TIMEOUT_MS last entry = %q, want 5400000", v)
	}
	if v, n := lastEnv(got, "BASH_DEFAULT_TIMEOUT_MS"); v != "120000" || n != 1 {
		t.Errorf("BASH_DEFAULT_TIMEOUT_MS = %q (%d entries), want the inherited 120000 once", v, n)
	}
}
