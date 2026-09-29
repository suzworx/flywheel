package flywheel

import (
	"runtime"
	"strconv"
	"strings"
	"time"
)

// claudeShellEnv returns env with the claude Bash tool's foreground limit set
// to limit (issue #678): BASH_MAX_TIMEOUT_MS is appended, so it wins over an
// inherited value (os/exec keeps the last duplicate key), and
// BASH_DEFAULT_TIMEOUT_MS is appended only when env carries none, so a user's
// own default is kept. Keys match case-insensitively on Windows only.
func claudeShellEnv(env []string, limit time.Duration) []string {
	ms := strconv.FormatInt(limit.Milliseconds(), 10)
	out := append(env[:len(env):len(env)], "BASH_MAX_TIMEOUT_MS="+ms)
	if !envHasKey(env, "BASH_DEFAULT_TIMEOUT_MS") {
		out = append(out, "BASH_DEFAULT_TIMEOUT_MS="+ms)
	}
	return out
}

// envHasKey reports whether env has an entry for key.
func envHasKey(env []string, key string) bool {
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == key || (runtime.GOOS == "windows" && strings.EqualFold(k, key)) {
			return true
		}
	}
	return false
}
