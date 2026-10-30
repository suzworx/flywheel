package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestWorkerDenyClaudeAppends checks worker_policy.deny is appended to the
// claude worker's deny list, never replacing it (issue #692): the defaults
// plus the entries, a custom list plus the entries, deduplicated.
func TestWorkerDenyClaudeAppends(t *testing.T) {
	t.Parallel()
	p := &WorkerPolicy{Deny: []string{"node scripts/flash.mjs", "pio  run -t upload", "node scripts/flash.mjs"}}
	extra := []string{"Bash(node scripts/flash.mjs:*)", "Bash(pio run -t upload:*)"}
	want := append(slices.Clone(defaultDisallowedTools), extra...)
	if got := claudeDisallowed(Worker{}, p); !reflect.DeepEqual(got, want) {
		t.Errorf("claudeDisallowed(defaults) = %v, want %v", got, want)
	}
	custom := Worker{DisallowedTools: []string{"Bash(git commit:*)", "Bash(pio run -t upload:*)"}}
	want = []string{"Bash(git commit:*)", "Bash(pio run -t upload:*)", "Bash(node scripts/flash.mjs:*)"}
	if got := claudeDisallowed(custom, p); !reflect.DeepEqual(got, want) {
		t.Errorf("claudeDisallowed(custom) = %v, want %v", got, want)
	}
	if got := claudeDisallowed(Worker{}, nil); !reflect.DeepEqual(got, defaultDisallowedTools) {
		t.Errorf("claudeDisallowed(no policy) = %v, want the defaults", got)
	}
}

// TestWorkerPolicyDenyColon checks a colon inside a prefix names a colon-named
// script on claude and opencode, while a trailing colon or :* stays refused
// (issue #951), and that a claude prefix ends at a word boundary.
func TestWorkerPolicyDenyColon(t *testing.T) {
	t.Parallel()
	const entry = "npm run validate:non-device"
	const pattern = "Bash(npm run validate:non-device:*)"
	p := &WorkerPolicy{Deny: []string{entry}}
	if got := p.problems(); len(got) != 0 {
		t.Errorf("problems(%q) = %v, want none", entry, got)
	}
	for _, bad := range []string{"make:", "pio run:*"} {
		if got := (&WorkerPolicy{Deny: []string{bad}}).problems(); len(got) != 1 {
			t.Errorf("problems(%q) = %v, want one problem", bad, got)
		}
	}

	disallowed := claudeDisallowed(Worker{}, p)
	if !slices.Contains(disallowed, pattern) {
		t.Errorf("claudeDisallowed() = %v, want it to contain %q", disallowed, pattern)
	}
	want := "Bash: " + pattern + " (" + entry + " 2>&1)"
	if got := attributeDenial(entry+" 2>&1 | tail -5", disallowed); got != want {
		t.Errorf("attributeDenial() = %q, want %q", got, want)
	}
	if denyMatches(pattern, "npm run validate") {
		t.Errorf("denyMatches(%q, %q) = true, want false", pattern, "npm run validate")
	}
	if denyMatches("Bash(npm run validate:*)", entry) {
		t.Errorf("denyMatches(%q, %q) = true, want false", "Bash(npm run validate:*)", entry)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"permission": {"bash": {"*": "allow", "npm run validate:non-device*": "deny"}}}`
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "opencode-worker.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkWorkerPolicy(dir, "opencode", p); err != nil {
		t.Errorf("checkWorkerPolicy() = %v, want nil", err)
	}
}

// TestWorkerDenyOpencodeCheck checks the opencode policy file must deny every
// worker_policy.deny entry (issue #692), and that no entries means no check.
func TestWorkerDenyOpencodeCheck(t *testing.T) {
	t.Parallel()
	write := func(t *testing.T, body string) string {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".flywheel", "opencode-worker.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	p := &WorkerPolicy{Deny: []string{"pio run -t upload"}}

	dir := write(t, `{"permission": {"bash": {"*": "allow", "git push*": "deny"}}}`)
	err := checkWorkerPolicy(dir, "opencode", p)
	if !IsWorkerPolicyRefusal(err) || !strings.Contains(err.Error(), `"pio run -t upload*": "deny"`) ||
		!strings.Contains(err.Error(), "opencode-worker.json") || !strings.Contains(err.Error(), `after the "*": "allow" line`) {
		t.Errorf("missing pattern: checkWorkerPolicy() = %v, want a refusal naming the pattern and the file", err)
	}

	dir = write(t, `{"permission": {"bash": {"*": "allow", "pio run -t upload*": "deny"}}}`)
	if err := checkWorkerPolicy(dir, "opencode", p); err != nil {
		t.Errorf("pattern present: checkWorkerPolicy() = %v, want nil", err)
	}

	dir = write(t, `{"instructions": ["worker-rules.md"]}`)
	for _, none := range []*WorkerPolicy{nil, {}} {
		if err := checkWorkerPolicy(dir, "opencode", none); err != nil {
			t.Errorf("no entries: checkWorkerPolicy() = %v, want nil", err)
		}
	}

	// A missing file is checked as the embedded default flywheel would write.
	if err := checkWorkerPolicy(t.TempDir(), "opencode", p); !IsWorkerPolicyRefusal(err) {
		t.Errorf("missing file: checkWorkerPolicy() = %v, want a refusal", err)
	}
}

// TestWorkerDenyUnenforceableAdapters checks codex and pi are refused while
// worker_policy.deny is set, and claude and sim pass (issue #692).
func TestWorkerDenyUnenforceableAdapters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := &WorkerPolicy{Deny: []string{"make deploy"}}
	for _, a := range []string{"codex", "pi"} {
		want := "worker_policy.deny is set but the " + a + " adapter cannot enforce a command deny list; use a claude or opencode worker"
		if err := checkWorkerPolicy(dir, a, p); !IsWorkerPolicyRefusal(err) || err.Error() != want {
			t.Errorf("%s: checkWorkerPolicy() = %v, want %q", a, err, want)
		}
		if err := checkWorkerPolicy(dir, a, nil); err != nil {
			t.Errorf("%s without entries: checkWorkerPolicy() = %v, want nil", a, err)
		}
	}
	for _, a := range []string{"claude", "sim"} {
		if err := checkWorkerPolicy(dir, a, p); err != nil {
			t.Errorf("%s: checkWorkerPolicy() = %v, want nil", a, err)
		}
	}
}
