package flywheel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigMissingFileReturnsDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if exists {
		t.Fatal("LoadConfig() exists = true, want false for a missing file")
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Errorf("LoadConfig() = %+v, want default %+v", cfg, DefaultConfig())
	}
}

func TestWriteConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultConfig()
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Error("config.json does not end with a trailing newline")
	}

	got, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !exists {
		t.Fatal("LoadConfig() exists = false, want true after WriteConfig")
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("LoadConfig() = %+v, want %+v", got, cfg)
	}
}

func TestWriteConfigOverwritesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := WriteConfig(dir, DefaultConfig()); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	custom := Config{
		Version: 1,
		Workers: []Worker{{Name: "sim", Adapter: "sim", Model: "fixture.jsonl"}},
	}
	if err := WriteConfig(dir, custom); err != nil {
		t.Fatalf("WriteConfig() overwrite error = %v", err)
	}
	got, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !exists {
		t.Fatal("LoadConfig() exists = false, want true after overwrite")
	}
	if !reflect.DeepEqual(got, custom) {
		t.Errorf("LoadConfig() = %+v, want the overwriting config %+v", got, custom)
	}
}

func TestConfigValidateCollectsProblems(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Version: 2,
		Workers: []Worker{
			{Name: "Bad Name!", Adapter: "nope", MaxParallel: -1,
				Fallbacks: []Fallback{{Model: "fb"}, {Model: ""}}},
			{Name: "Bad Name!", Adapter: "sim", Model: "m",
				Fallbacks: []Fallback{{Model: "m"}}},
		},
		Limits:   Limits{PerHost: -3},
		Feedback: Feedback{Submit: "maybe"},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want every problem reported")
	}
	msg := err.Error()
	for _, want := range []string{
		"version: got 2, want 1",
		`name "Bad Name!" must match`,
		`adapter "nope"`,
		"model must not be empty",
		"max_parallel -1 must be >= 0",
		"duplicate name",
		"fallbacks[1] model must not be empty",
		`fallback model "m" must differ`,
		"limits.per_host -3 must be >= 0",
		`feedback.submit "maybe"`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("Validate() error missing %q; got:\n%s", want, msg)
		}
	}
	if lines := strings.Split(msg, "\n"); len(lines) < 9 {
		t.Errorf("Validate() error has %d lines, want one problem per line:\n%s", len(lines), msg)
	}
}

// TestConfigValidateAcceptsClaudeAdapter checks "claude" joins the valid
// adapter names (issue #49) alongside opencode and sim.
func TestConfigValidateAcceptsClaudeAdapter(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for adapter \"claude\"", err)
	}
}

// TestConfigRouting checks Validate's routing rules (issue #474).
func TestConfigRouting(t *testing.T) {
	t.Parallel()
	valid := func() *Routing {
		return &Routing{Candidates: []string{"a", "b"}, Objective: "accepted_rate", Explore: 0.1, MinAttempts: 2, Seed: "s"}
	}
	cases := []struct {
		name string
		edit func(r *Routing)
		want string // "" means valid
	}{
		{"valid", func(r *Routing) {}, ""},
		{"unknown objective", func(r *Routing) { r.Objective = "speed" }, `routing.objective "speed"`},
		{"explore 1.5", func(r *Routing) { r.Explore = 1.5 }, "routing.explore 1.5"},
		{"empty candidate", func(r *Routing) { r.Candidates = []string{"a", ""} }, "routing.candidates[1] must not be empty"},
		{"duplicate candidate", func(r *Routing) { r.Candidates = []string{"a", "a"} }, `routing.candidates[1] duplicates "a"`},
		{"no candidates", func(r *Routing) { r.Candidates = nil }, "routing.candidates must not be empty"},
		{"negative min_attempts", func(r *Routing) { r.MinAttempts = -1 }, "routing.min_attempts -1"},
	}
	for _, tc := range cases {
		r := valid()
		tc.edit(r)
		cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "m", Routing: r}}}
		err := cfg.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: Validate() error = %v, want nil", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), "workers[0]: "+tc.want)):
			t.Errorf("%s: Validate() error = %v, want %q", tc.name, err, tc.want)
		}
	}
	if err := (Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "m"}}}).Validate(); err != nil {
		t.Errorf("Validate() without routing error = %v, want nil", err)
	}
}

// TestConfigLintKinds checks Validate's lint.kinds rules and the default list
// (issue #475).
func TestConfigLintKinds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kinds []string
		want  string // "" means valid
	}{
		{[]string{"docs", "infra"}, ""},
		{[]string{"docs", " "}, "lint.kinds[1] must not be empty"},
		{[]string{"docs", "docs"}, `lint.kinds[1] duplicates "docs"`},
	} {
		cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "m"}}, Lint: &LintConfig{Kinds: tc.kinds}}
		err := cfg.Validate()
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("kinds %q: Validate() error = %v, want %q", tc.kinds, err, tc.want)
		}
	}
	if got := (Config{}).LintKinds(); !slices.Equal(got, DefaultKinds) {
		t.Errorf("LintKinds() unset = %v, want %v", got, DefaultKinds)
	}
}

// TestConfigValidateAcceptsCodexAdapter checks "codex" joins the valid
// adapter names (issue #275) alongside opencode, sim, and claude.
func TestConfigValidateAcceptsCodexAdapter(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "codex", Model: "gpt-5-codex"}}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for adapter \"codex\"", err)
	}
}

// TestConfigValidateRejectsUnknownAdapter checks an adapter outside
// opencode/sim/claude/codex is still rejected.
func TestConfigValidateRejectsUnknownAdapter(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "nope", Model: "m"}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `adapter "nope"`) {
		t.Errorf("Validate() = %v, want an error naming adapter \"nope\"", err)
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".flywheel", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"version":1,"workers":[{"name":"default","adapter":"opencode","model":"m","bogus":1}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	_, _, err := LoadConfig(dir)
	if err == nil {
		t.Fatal("LoadConfig() = nil error, want rejection of an unknown field")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("LoadConfig() error = %v, want it to name the file %s", err, path)
	}
}

func TestConfigGet(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "default", Adapter: "opencode", Model: "m0", Variant: "v0", MaxParallel: 4,
				Fallbacks: []Fallback{{Model: "f1", Approved: true}, {Model: "f2"}}},
			{Name: "extra", Adapter: "sim", Model: "m1",
				Fallbacks: []Fallback{{Model: "f3", Approved: true}}},
		},
		Limits:   Limits{PerHost: 7},
		Feedback: Feedback{Upstream: "owner/repo", Submit: "never"},
	}
	cases := []struct {
		key, want string
	}{
		{"model", "m0"},
		{"variant", "v0"},
		{"adapter", "opencode"},
		{"max_parallel", "4"},
		{"fallbacks", "f1"},
		{"fallbacks.all", "f1,f2"},
		{"workers.default.model", "m0"},
		{"workers.default.fallbacks", "f1"},
		{"workers.extra.model", "m1"},
		{"workers.extra.adapter", "sim"},
		{"workers.extra.max_parallel", "0"},
		{"workers.extra.fallbacks", "f3"},
		{"workers.extra.fallbacks.all", "f3"},
		{"feedback.upstream", "owner/repo"},
		{"feedback.submit", "never"},
		{"limits.per_host", "7"},
	}
	for _, tc := range cases {
		got, err := cfg.Get(tc.key)
		if err != nil {
			t.Errorf("Get(%q) error = %v", tc.key, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Get(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestConfigGetUnknownKeyListsValidKeys(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	_, err := cfg.Get("bogus")
	if err == nil {
		t.Fatal("Get(\"bogus\") = nil error, want error listing valid keys")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bogus") {
		t.Errorf("Get() error = %q, want mention of the unknown key", msg)
	}
	for _, k := range []string{
		"model", "variant", "adapter", "max_parallel", "fallbacks", "fallbacks.all",
		"workers.default.model", "feedback.upstream", "feedback.submit", "limits.per_host",
	} {
		if !strings.Contains(msg, k) {
			t.Errorf("Get() error missing valid key %q; got:\n%s", k, msg)
		}
	}
}

func TestConfigWorkerLookup(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if w, ok := cfg.Worker("default"); !ok || w.Model != "openrouter/deepseek/deepseek-v4-flash-0731" {
		t.Errorf("Worker(default) = %+v, %v, want the default worker", w, ok)
	}
	if _, ok := cfg.Worker("nope"); ok {
		t.Error("Worker(nope) = found, want not found")
	}
	if got := cfg.DefaultWorker(); got.Name != "default" {
		t.Errorf("DefaultWorker() = %+v, want the first worker", got)
	}
}

func TestConfigSetWritesAndReadsBack(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err := cfg.Set("variant", "low"); err != nil {
		t.Fatalf("Set(variant, low) error = %v", err)
	}
	if err := cfg.Set("workers.default.max_parallel", "2"); err != nil {
		t.Fatalf("Set(workers.default.max_parallel, 2) error = %v", err)
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "config.json")); err != nil {
		t.Fatalf("config.json not created: %v", err)
	}
	got, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() after write error = %v", err)
	}
	if !exists {
		t.Fatal("LoadConfig() exists = false, want true after set + write")
	}
	for key, want := range map[string]string{
		"variant":                      "low",
		"workers.default.max_parallel": "2",
	} {
		if v, err := got.Get(key); err != nil || v != want {
			t.Errorf("Get(%q) = %q, %v; want %q", key, v, err, want)
		}
	}
}

// TestConfigIntegrationBranch checks integration.branch is settable and
// readable through config set/get (issue #529): it round-trips in memory and
// on disk, an empty value clears it, and a bad name is refused by WriteConfig.
func TestConfigIntegrationBranch(t *testing.T) {
	t.Parallel()
	t.Run("set then get", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		if v, err := cfg.Get("integration.branch"); err != nil || v != "" {
			t.Errorf("unset Get(integration.branch) = %q, %v; want \"\", nil", v, err)
		}
		if err := cfg.Set("integration.branch", " develop "); err != nil {
			t.Fatalf("Set(integration.branch, develop) error = %v", err)
		}
		if v, err := cfg.Get("integration.branch"); err != nil || v != "develop" {
			t.Errorf("Get(integration.branch) = %q, %v; want develop", v, err)
		}
	})
	t.Run("written and read back", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfg, _, err := LoadConfig(dir)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if err := cfg.Set("integration.branch", "develop"); err != nil {
			t.Fatalf("Set(integration.branch, develop) error = %v", err)
		}
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		got, _, err := LoadConfig(dir)
		if err != nil {
			t.Fatalf("LoadConfig() after write error = %v", err)
		}
		if v, err := got.Get("integration.branch"); err != nil || v != "develop" {
			t.Errorf("Get(integration.branch) after write = %q, %v; want develop", v, err)
		}
	})
	t.Run("empty clears", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		if err := cfg.Set("integration.branch", "develop"); err != nil {
			t.Fatalf("Set(integration.branch, develop) error = %v", err)
		}
		if err := cfg.Set("integration.branch", ""); err != nil {
			t.Fatalf("Set(integration.branch, \"\") error = %v", err)
		}
		if cfg.Integration != nil {
			t.Errorf("Integration = %+v after clearing, want nil", cfg.Integration)
		}
	})
	t.Run("bad name refused", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{"-x", "a b"} {
			dir := t.TempDir()
			if err := WriteConfig(dir, DefaultConfig()); err != nil {
				t.Fatalf("WriteConfig() error = %v", err)
			}
			path := filepath.Join(dir, ".flywheel", "config.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config.json: %v", err)
			}
			cfg, _, err := LoadConfig(dir)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			if err := cfg.Set("integration.branch", bad); err != nil {
				t.Fatalf("Set(integration.branch, %q) error = %v (validation is WriteConfig's job)", bad, err)
			}
			if err := WriteConfig(dir, cfg); err == nil {
				t.Errorf("WriteConfig() with integration.branch %q = nil error, want a rejection", bad)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config.json after failed write: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Errorf("config.json changed despite a refused integration.branch %q", bad)
			}
		}
	})
	t.Run("listed", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultConfig()
		if !slices.Contains(cfg.settableKeys(), "integration.branch") {
			t.Error("integration.branch missing from settableKeys")
		}
		if !slices.Contains(cfg.validKeys(), "integration.branch") {
			t.Error("integration.branch missing from validKeys")
		}
		if err := cfg.Set("bogus", "x"); err == nil || !strings.Contains(err.Error(), "integration.branch") {
			t.Errorf("Set(bogus) error = %v, want integration.branch in the settable list", err)
		}
	})
}

func TestConfigSetIntegerParseError(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if err := cfg.Set("max_parallel", "abc"); err == nil {
		t.Fatal("Set(max_parallel, abc) = nil error, want a parse error")
	} else if !strings.Contains(err.Error(), "integer") {
		t.Errorf("Set(max_parallel, abc) error = %q, want mention of integer", err)
	}
	if got := cfg.DefaultWorker().MaxParallel; got != 4 {
		t.Errorf("MaxParallel = %d, want unchanged 4 after failed Set", got)
	}
}

// TestConfigRateLimitKeys checks limits.rate_limit_retries and
// limits.rate_limit_max_wait: their defaults (3, 5h), Get/Set, 0 disabling
// retries, and Validate refusing a negative count or a bad duration (issue
// #380).
func TestConfigRateLimitKeys(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	for key, want := range map[string]string{"limits.rate_limit_retries": "3", "limits.rate_limit_max_wait": "5h"} {
		if v, err := cfg.Get(key); err != nil || v != want {
			t.Errorf("default Get(%q) = %q, %v; want %q", key, v, err, want)
		}
	}
	if d, err := cfg.Limits.RateLimitMaxWaitDuration(); err != nil || d != 5*time.Hour {
		t.Errorf("default RateLimitMaxWaitDuration() = %v, %v; want 5h", d, err)
	}
	if err := cfg.Set("limits.rate_limit_retries", "0"); err != nil {
		t.Fatalf("Set(limits.rate_limit_retries, 0) error = %v", err)
	}
	if err := cfg.Set("limits.rate_limit_max_wait", "90m"); err != nil {
		t.Fatalf("Set(limits.rate_limit_max_wait, 90m) error = %v", err)
	}
	if cfg.Limits.RateLimitRetryCount() != 0 {
		t.Errorf("RateLimitRetryCount() = %d after setting 0, want 0 (disabled)", cfg.Limits.RateLimitRetryCount())
	}
	if v, _ := cfg.Get("limits.rate_limit_max_wait"); v != "90m" {
		t.Errorf("Get(limits.rate_limit_max_wait) = %q, want 90m", v)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if err := cfg.Set("limits.rate_limit_retries", "x"); err == nil || !strings.Contains(err.Error(), "integer") {
		t.Errorf("Set(limits.rate_limit_retries, x) error = %v, want an integer error", err)
	}
	for _, tc := range []struct{ key, value, want string }{
		{"limits.rate_limit_retries", "-1", "limits.rate_limit_retries -1 must be >= 0"},
		{"limits.rate_limit_max_wait", "soon", "limits.rate_limit_max_wait"},
		{"limits.rate_limit_max_wait", "-5m", "must be > 0"},
	} {
		bad := DefaultConfig()
		if err := bad.Set(tc.key, tc.value); err != nil {
			t.Fatalf("Set(%q, %q) error = %v", tc.key, tc.value, err)
		}
		if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate() with %s=%s = %v, want %q", tc.key, tc.value, err, tc.want)
		}
	}
	for _, k := range []string{"limits.rate_limit_retries", "limits.rate_limit_max_wait"} {
		if !slices.Contains(cfg.validKeys(), k) || !slices.Contains(cfg.settableKeys(), k) {
			t.Errorf("%s missing from validKeys or settableKeys", k)
		}
	}
}

func TestConfigSetInvalidValueLeavesFileUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := WriteConfig(dir, DefaultConfig()); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	path := filepath.Join(dir, ".flywheel", "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err := cfg.Set("max_parallel", "-1"); err != nil {
		t.Fatalf("Set(max_parallel, -1) error = %v (parse succeeds; validation is WriteConfig's job)", err)
	}
	if err := WriteConfig(dir, cfg); err == nil {
		t.Fatal("WriteConfig() = nil error, want validation rejection of max_parallel -1")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json after failed write: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Error("config.json changed despite a failed write")
	}
}

func TestConfigSetUnknownKeyListsSettableKeys(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	err := cfg.Set("bogus", "x")
	if err == nil {
		t.Fatal("Set(bogus) = nil error, want error listing settable keys")
	}
	msg := err.Error()
	if !strings.Contains(msg, "bogus") {
		t.Errorf("Set(bogus) error = %q, want mention of the unknown key", msg)
	}
	for _, k := range []string{
		"model", "variant", "adapter", "max_parallel",
		"workers.default.model", "feedback.upstream", "feedback.submit", "limits.per_host",
	} {
		if !strings.Contains(msg, k) {
			t.Errorf("Set(bogus) error missing settable key %q; got:\n%s", k, msg)
		}
	}
	if strings.Contains(msg, "fallbacks") {
		t.Error("Set(bogus) error lists fallbacks, which is not settable")
	}
}

func TestConfigSetFallbacksUnsupported(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	for _, key := range []string{"fallbacks", "fallbacks.all"} {
		err := cfg.Set(key, "m")
		if err == nil {
			t.Fatalf("Set(%q) = nil error, want rejection", key)
		}
		if !strings.Contains(err.Error(), "config.json") {
			t.Errorf("Set(%q) error = %q, want mention of .flywheel/config.json", key, err)
		}
	}
}

func TestConfigLeaseDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	renew, ttl := cfg.leaseTimings()
	if renew != 15*time.Second || ttl != 45*time.Second {
		t.Errorf("leaseTimings() = %s/%s, want 15s/45s", renew, ttl)
	}
	if cfg.Lease != nil {
		t.Errorf("DefaultConfig().Lease = %+v, want nil (an absent block must stay absent)", cfg.Lease)
	}
}

func TestConfigLeaseTimingsFromBlock(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Lease = &LeaseConfig{RenewInterval: "30s", TTL: "90s"}
	renew, ttl := cfg.leaseTimings()
	if renew != 30*time.Second || ttl != 90*time.Second {
		t.Errorf("leaseTimings() = %s/%s, want 30s/90s", renew, ttl)
	}
}

func TestConfigLeaseValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		lc   LeaseConfig
		want string
	}{
		{"renew zero", LeaseConfig{RenewInterval: "0s", TTL: "45s"}, "lease.renew_interval 0s must be positive"},
		{"renew negative", LeaseConfig{RenewInterval: "-5s", TTL: "45s"}, "lease.renew_interval -5s must be positive"},
		{"renew unparseable", LeaseConfig{RenewInterval: "soon", TTL: "45s"}, `lease.renew_interval "soon" is not a valid duration`},
		{"ttl zero", LeaseConfig{RenewInterval: "15s", TTL: "0s"}, "lease.ttl 0s must be positive"},
		{"ttl unparseable", LeaseConfig{RenewInterval: "15s", TTL: "later"}, `lease.ttl "later" is not a valid duration`},
		{"ttl equal", LeaseConfig{RenewInterval: "15s", TTL: "15s"}, "lease.ttl 15s must be greater than renew_interval 15s"},
		{"ttl shorter", LeaseConfig{RenewInterval: "15s", TTL: "10s"}, "lease.ttl 10s must be greater than renew_interval 15s"},
	}
	for _, tc := range cases {
		cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}}, Lease: &tc.lc}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
	ok := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
		Lease: &LeaseConfig{RenewInterval: "15s", TTL: "45s"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid lease block: Validate() error = %v, want nil", err)
	}
}

func TestConfigControllerDefaults(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	interval, ttl, intent := cfg.controllerTimings()
	if interval != 10*time.Second || ttl != 30*time.Second || intent != 2*time.Minute {
		t.Errorf("controllerTimings() = %s/%s/%s, want 10s/30s/2m", interval, ttl, intent)
	}
	if cfg.Controller != nil {
		t.Errorf("DefaultConfig().Controller = %+v, want nil (an absent block must stay absent)", cfg.Controller)
	}
}

func TestConfigControllerTimingsFromBlock(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	cfg.Controller = &ControllerConfig{Interval: "20s", LockTTL: "60s", IntentTimeout: "5m"}
	interval, ttl, intent := cfg.controllerTimings()
	if interval != 20*time.Second || ttl != 60*time.Second || intent != 5*time.Minute {
		t.Errorf("controllerTimings() = %s/%s/%s, want 20s/60s/5m", interval, ttl, intent)
	}
}

// TestControllerConfigAutoResume: controller.auto_resume and controller.notify
// round-trip through the config file; an absent block or key means on.
func TestControllerConfigAutoResume(t *testing.T) {
	t.Parallel()
	if !DefaultConfig().controllerAutoResume() || DefaultConfig().controllerNotify() != "" {
		t.Errorf("absent controller block: auto_resume must default on and notify empty")
	}
	if !(Config{Controller: &ControllerConfig{Interval: "10s"}}).controllerAutoResume() {
		t.Errorf("nil controller.auto_resume must mean on")
	}
	for _, on := range []bool{false, true} {
		dir := t.TempDir()
		cfg := DefaultConfig()
		// No timings: a block holding only auto_resume and notify is valid.
		cfg.Controller = &ControllerConfig{AutoResume: &on, Notify: "echo resumed"}
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig: %v", err)
		}
		got, _, err := LoadConfig(dir)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if got.controllerAutoResume() != on || got.controllerNotify() != "echo resumed" {
			t.Errorf("auto_resume %v: loaded auto_resume=%v notify=%q", on, got.controllerAutoResume(), got.controllerNotify())
		}
	}
}

func TestConfigControllerValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cc   ControllerConfig
		want string
	}{
		{"interval zero", ControllerConfig{Interval: "0s", LockTTL: "30s"}, "controller.interval 0s must be positive"},
		{"interval negative", ControllerConfig{Interval: "-5s", LockTTL: "30s"}, "controller.interval -5s must be positive"},
		{"interval unparseable", ControllerConfig{Interval: "soon", LockTTL: "30s"}, `controller.interval "soon" is not a valid duration`},
		{"lock_ttl zero", ControllerConfig{Interval: "10s", LockTTL: "0s"}, "controller.lock_ttl 0s must be positive"},
		{"lock_ttl unparseable", ControllerConfig{Interval: "10s", LockTTL: "later"}, `controller.lock_ttl "later" is not a valid duration`},
		{"lock_ttl equal", ControllerConfig{Interval: "10s", LockTTL: "10s"}, "controller.lock_ttl 10s must be greater than interval 10s"},
		{"lock_ttl shorter", ControllerConfig{Interval: "10s", LockTTL: "5s"}, "controller.lock_ttl 5s must be greater than interval 10s"},
		{"intent zero", ControllerConfig{Interval: "10s", LockTTL: "30s", IntentTimeout: "0s"}, "controller.intent_timeout 0s must be positive"},
		{"intent unparseable", ControllerConfig{Interval: "10s", LockTTL: "30s", IntentTimeout: "soon"}, `controller.intent_timeout "soon" is not a valid duration`},
	}
	for _, tc := range cases {
		cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}}, Controller: &tc.cc}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate() = %v, want error containing %q", tc.name, err, tc.want)
		}
	}
	ok := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
		Controller: &ControllerConfig{Interval: "10s", LockTTL: "30s", IntentTimeout: "2m"}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid controller block: Validate() error = %v, want nil", err)
	}
}

// TestWorkerStallTimeoutDefault checks an unset (zero) stall_timeout resolves
// to the 600s default, and a set value resolves to itself (issue #85).
func TestWorkerStallTimeoutDefault(t *testing.T) {
	t.Parallel()
	if got := (Worker{}).stallTimeoutDuration(); got != 600*time.Second {
		t.Errorf("stallTimeoutDuration() = %s, want 600s for an unset stall_timeout", got)
	}
	if got := (Worker{StallTimeout: 45}).stallTimeoutDuration(); got != 45*time.Second {
		t.Errorf("stallTimeoutDuration() = %s, want 45s", got)
	}
}

// TestConfigValidateRejectsNegativeStallTimeout checks a negative
// stall_timeout is reported by Validate (issue #85).
func TestConfigValidateRejectsNegativeStallTimeout(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m", StallTimeout: -5}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "stall_timeout -5 must be >= 0") {
		t.Errorf("Validate() = %v, want an error naming stall_timeout -5", err)
	}
}

// TestConfigStallTimeoutGetSetRoundTrip checks stall_timeout parses via Set,
// survives a get/set round trip through WriteConfig/LoadConfig for both the
// bare and workers.<name> forms, and a valid (non-negative) value passes
// Validate (issue #85).
func TestConfigStallTimeoutGetSetRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if err := cfg.Set("stall_timeout", "45"); err != nil {
		t.Fatalf("Set(stall_timeout, 45) error = %v", err)
	}
	if err := cfg.Set("workers.default.stall_timeout", "90"); err != nil {
		t.Fatalf("Set(workers.default.stall_timeout, 90) error = %v", err)
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	got, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() after write error = %v", err)
	}
	for key, want := range map[string]string{
		"stall_timeout":                 "90",
		"workers.default.stall_timeout": "90",
	} {
		if v, err := got.Get(key); err != nil || v != want {
			t.Errorf("Get(%q) = %q, %v; want %q", key, v, err, want)
		}
	}
}

func TestConfigWithoutLeaseRoundTripsUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "default", Adapter: "opencode", Model: "m"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	path := filepath.Join(dir, ".flywheel", "config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	got, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !exists {
		t.Fatal("LoadConfig() exists = false, want true")
	}
	if err := WriteConfig(dir, got); err != nil {
		t.Fatalf("WriteConfig() after load error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.json after rewrite: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("old config without a lease block changed when written back:\nbefore: %s\nafter:  %s", before, after)
	}
	if got.Lease != nil {
		t.Errorf("loaded config Lease = %+v, want nil", got.Lease)
	}
}

// TestWorkerToolDefaults checks an unset worker resolves to the documented
// allowed/disallowed defaults, and an explicitly set list replaces rather
// than merges with them (issue #192).
func TestWorkerToolDefaults(t *testing.T) {
	t.Parallel()
	if got := (Worker{}).allowedTools(); !reflect.DeepEqual(got, defaultAllowedTools) {
		t.Errorf("allowedTools() = %v, want the default %v", got, defaultAllowedTools)
	}
	if got := (Worker{}).disallowedTools(); !reflect.DeepEqual(got, defaultDisallowedTools) {
		t.Errorf("disallowedTools() = %v, want the default %v", got, defaultDisallowedTools)
	}
	w := Worker{
		AllowedTools:    []string{"Bash(go:*)", "Edit"},
		DisallowedTools: []string{"Bash(git commit:*)"},
	}
	if got := w.allowedTools(); !reflect.DeepEqual(got, []string{"Bash(go:*)", "Edit"}) {
		t.Errorf("allowedTools() = %v, want the explicit list to replace the default", got)
	}
	if got := w.disallowedTools(); !reflect.DeepEqual(got, []string{"Bash(git commit:*)"}) {
		t.Errorf("disallowedTools() = %v, want the explicit list to replace the default", got)
	}
}

// TestWorkerMCPOptIn checks the worker's mcp key (issue #425): unset and a
// valid {"mcpServers": {...}} object pass and compact to a JSON string; a
// non-object, a missing mcpServers or a non-object mcpServers fail Validate.
func TestWorkerMCPOptIn(t *testing.T) {
	t.Parallel()
	valid := map[string]string{
		"unset": "",
		"empty": `{"mcpServers": {}}`,
		"one":   "{\n  \"mcpServers\": {\"fs\": {\"command\": \"mcp-fs\", \"args\": [\"/tmp\"]}}\n}",
	}
	for name, raw := range valid {
		w := Worker{Name: "w", Adapter: "claude", Model: "m", MCP: json.RawMessage(raw)}
		if err := (Config{Version: 1, Workers: []Worker{w}}).Validate(); err != nil {
			t.Errorf("%s: Validate() = %v, want nil", name, err)
		}
	}
	if got := (Worker{}).mcpConfig(); got != "" {
		t.Errorf("unset mcpConfig() = %q, want empty", got)
	}
	w := Worker{MCP: json.RawMessage(valid["one"])}
	if got, want := w.mcpConfig(), `{"mcpServers":{"fs":{"command":"mcp-fs","args":["/tmp"]}}}`; got != want {
		t.Errorf("mcpConfig() = %q, want compact %q", got, want)
	}
	invalid := map[string]string{
		"array":          `[1]`,
		"string":         `"x"`,
		"no mcpServers":  `{"servers": {}}`,
		"list servers":   `{"mcpServers": []}`,
		"null servers":   `{"mcpServers": null}`,
		"not json":       `{mcpServers`,
		"top-level null": `null`,
	}
	for name, raw := range invalid {
		w := Worker{Name: "w", Adapter: "claude", Model: "m", MCP: json.RawMessage(raw)}
		err := (Config{Version: 1, Workers: []Worker{w}}).Validate()
		if err == nil || !strings.Contains(err.Error(), "workers[0]: mcp") {
			t.Errorf("%s: Validate() = %v, want a workers[0]: mcp error", name, err)
		}
	}
}

// TestDefaultDisallowedIndexWrites checks the default deny list covers every
// index, ref and history write (#423) and leaves read-only git allowed.
func TestDefaultDisallowedIndexWrites(t *testing.T) {
	t.Parallel()
	has := map[string]bool{}
	for _, d := range defaultDisallowedTools {
		has[d] = true
	}
	for _, sub := range []string{"commit", "push", "stash", "reset", "checkout", "rebase", "merge",
		"add", "rm", "mv", "restore", "update-index", "apply", "tag", "branch", "switch",
		"cherry-pick", "revert", "am", "worktree", "clean", "notes", "replace", "update-ref", "gc"} {
		if !has["Bash(git "+sub+":*)"] {
			t.Errorf("defaultDisallowedTools lacks Bash(git %s:*)", sub)
		}
	}
	for _, sub := range []string{"status", "diff", "log"} {
		if has["Bash(git "+sub+":*)"] {
			t.Errorf("defaultDisallowedTools denies read-only git %s", sub)
		}
	}
}

// TestConfigBaselineValidate checks baseline validation: a valid baseline
// passes, empty model fails, negative prices fail (issue #59).
func TestConfigBaselineValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		base    Baseline
		wantErr string
	}{
		{"empty model", Baseline{Model: "", InputPerMTok: 5}, "baseline.model must not be empty"},
		{"negative input", Baseline{Model: "m", InputPerMTok: -1}, "baseline.input_per_mtok -1 must be >= 0"},
		{"negative output", Baseline{Model: "m", OutputPerMTok: -0.5}, "baseline.output_per_mtok -0.5 must be >= 0"},
		{"negative cache_read", Baseline{Model: "m", CacheReadPerMTok: -1}, "baseline.cache_read_per_mtok -1 must be >= 0"},
		{"negative cache_write", Baseline{Model: "m", CacheWritePerMTok: -1}, "baseline.cache_write_per_mtok -1 must be >= 0"},
	}
	for _, tc := range cases {
		cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}}, Baseline: &tc.base}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: Validate() = %v, want error containing %q", tc.name, err, tc.wantErr)
		}
	}
	ok := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
		Baseline: &Baseline{Model: "frontier-x", InputPerMTok: 5, OutputPerMTok: 25, CacheReadPerMTok: 0.5, CacheWritePerMTok: 6.25}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid baseline: Validate() error = %v, want nil", err)
	}
}

// TestConfigToolListsJSONRoundTrip checks allowed_tools/disallowed_tools
// survive a WriteConfig/LoadConfig round trip under their JSON field names
// (issue #192).
func TestConfigToolListsJSONRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := Config{Version: 1, Workers: []Worker{{
		Name:            "claude",
		Adapter:         "claude",
		Model:           "m",
		AllowedTools:    []string{"Bash"},
		DisallowedTools: []string{"Bash(git commit:*)", "Bash(git push:*)"},
	}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	for _, want := range []string{`"allowed_tools"`, `"disallowed_tools"`, `"Bash(git commit:*)"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("config.json missing %s; got:\n%s", want, b)
		}
	}
	got, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !exists {
		t.Fatal("LoadConfig() exists = false, want true")
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Errorf("LoadConfig() = %+v, want %+v", got, cfg)
	}
}

// TestConfigBreakerValidate checks Breaker validation: valid configs pass, invalid ones fail.
func TestConfigBreakerValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		wantMsg string
	}{
		{
			name: "valid breaker",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
				Limits:  Limits{Breaker: &Breaker{Errors: 2, Cooldown: "10m"}},
			},
			wantErr: false,
		},
		{
			name: "negative errors",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
				Limits:  Limits{Breaker: &Breaker{Errors: -1, Cooldown: "10m"}},
			},
			wantErr: true,
			wantMsg: "limits.breaker.errors -1 must be >= 0",
		},
		{
			name: "invalid cooldown",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
				Limits:  Limits{Breaker: &Breaker{Errors: 2, Cooldown: "banana"}},
			},
			wantErr: true,
			wantMsg: "limits.breaker.cooldown",
		},
		{
			name: "zero cooldown",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}},
				Limits:  Limits{Breaker: &Breaker{Errors: 2, Cooldown: "0s"}},
			},
			wantErr: true,
			wantMsg: "limits.breaker.cooldown",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && (err == nil || !strings.Contains(err.Error(), tt.wantMsg)) {
				t.Errorf("Validate() = %v, want error containing %q", err, tt.wantMsg)
			}
		})
	}
}

// TestLostAfterConfig covers limits.lost_after (issue #402): the 24h default,
// Get/Set, and validation refusing an unparseable or non-positive duration.
func TestLostAfterConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if v, err := cfg.Get("limits.lost_after"); err != nil || v != "24h" {
		t.Errorf("default Get(limits.lost_after) = %q, %v; want 24h", v, err)
	}
	if d, err := cfg.Limits.LostAfterDuration(); err != nil || d != 24*time.Hour {
		t.Errorf("default LostAfterDuration() = %v, %v; want 24h", d, err)
	}
	if err := cfg.Set("limits.lost_after", "6h"); err != nil {
		t.Fatalf("Set(limits.lost_after, 6h) error = %v", err)
	}
	if v, _ := cfg.Get("limits.lost_after"); v != "6h" {
		t.Errorf("Get(limits.lost_after) = %q, want 6h", v)
	}
	if d, _ := cfg.Limits.LostAfterDuration(); d != 6*time.Hour {
		t.Errorf("LostAfterDuration() = %v, want 6h", d)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	for _, tc := range []struct{ value, want string }{
		{"soon", "limits.lost_after"},
		{"0s", "must be > 0"},
		{"-1h", "must be > 0"},
	} {
		c := DefaultConfig()
		if err := c.Set("limits.lost_after", tc.value); err != nil {
			t.Fatalf("Set(limits.lost_after, %s) error = %v", tc.value, err)
		}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate() with lost_after %q = %v, want error containing %q", tc.value, err, tc.want)
		}
	}
}

// TestQuietWaitConfig covers limits.quiet_wait (issue #411): the 30m default,
// Get/Set, the key lists, and validation refusing an unparseable or
// non-positive duration.
func TestQuietWaitConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if v, err := cfg.Get("limits.quiet_wait"); err != nil || v != "30m" {
		t.Errorf("default Get(limits.quiet_wait) = %q, %v; want 30m", v, err)
	}
	if d, err := cfg.Limits.QuietWaitDuration(); err != nil || d != 30*time.Minute {
		t.Errorf("default QuietWaitDuration() = %v, %v; want 30m", d, err)
	}
	if err := cfg.Set("limits.quiet_wait", "5m"); err != nil {
		t.Fatalf("Set(limits.quiet_wait, 5m) error = %v", err)
	}
	if v, _ := cfg.Get("limits.quiet_wait"); v != "5m" {
		t.Errorf("Get(limits.quiet_wait) = %q, want 5m", v)
	}
	if d, _ := cfg.Limits.QuietWaitDuration(); d != 5*time.Minute {
		t.Errorf("QuietWaitDuration() = %v, want 5m", d)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if err := cfg.Set("bogus", "x"); err == nil || !strings.Contains(err.Error(), "limits.quiet_wait") {
		t.Errorf("Set(bogus) error = %v, want limits.quiet_wait among settable keys", err)
	}
	if _, err := cfg.Get("bogus"); err == nil || !strings.Contains(err.Error(), "limits.quiet_wait") {
		t.Errorf("Get(bogus) error = %v, want limits.quiet_wait among valid keys", err)
	}
	for _, tc := range []struct{ value, want string }{
		{"soon", "limits.quiet_wait"},
		{"0s", "must be > 0"},
		{"-1m", "must be > 0"},
	} {
		c := DefaultConfig()
		if err := c.Set("limits.quiet_wait", tc.value); err != nil {
			t.Fatalf("Set(limits.quiet_wait, %s) error = %v", tc.value, err)
		}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate() with quiet_wait %q = %v, want error containing %q", tc.value, err, tc.want)
		}
	}
}

// TestRateLimitPauseAtConfig checks limits.rate_limit_pause_at: default 0.95,
// Get/Set, a negative value disabling the pause (threshold 0), and Validate
// refusing a value above 1 (issue #417).
func TestRateLimitPauseAtConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if v, err := cfg.Get("limits.rate_limit_pause_at"); err != nil || v != "0.95" {
		t.Errorf("default Get = %q, %v; want 0.95", v, err)
	}
	if p := cfg.Limits.RateLimitPauseThreshold(); p != 0.95 {
		t.Errorf("default RateLimitPauseThreshold() = %v, want 0.95", p)
	}
	if err := cfg.Set("limits.rate_limit_pause_at", "0.9"); err != nil {
		t.Fatalf("Set(0.9) error = %v", err)
	}
	if v, _ := cfg.Get("limits.rate_limit_pause_at"); v != "0.9" || cfg.Limits.RateLimitPauseThreshold() != 0.9 {
		t.Errorf("after Set(0.9): Get = %q, threshold %v; want 0.9", v, cfg.Limits.RateLimitPauseThreshold())
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if err := cfg.Set("limits.rate_limit_pause_at", "-1"); err != nil {
		t.Fatalf("Set(-1) error = %v", err)
	}
	if p := cfg.Limits.RateLimitPauseThreshold(); p != 0 {
		t.Errorf("RateLimitPauseThreshold() = %v after -1, want 0 (disabled)", p)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() with -1 = %v, want nil", err)
	}
	if err := cfg.Set("limits.rate_limit_pause_at", "most"); err == nil || !strings.Contains(err.Error(), "number") {
		t.Errorf("Set(most) error = %v, want a number error", err)
	}
	bad := DefaultConfig()
	if err := bad.Set("limits.rate_limit_pause_at", "1.2"); err != nil {
		t.Fatalf("Set(1.2) error = %v", err)
	}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "limits.rate_limit_pause_at 1.2 must be <= 1") {
		t.Errorf("Validate() with 1.2 = %v, want the <= 1 problem", err)
	}
	if k := "limits.rate_limit_pause_at"; !slices.Contains(cfg.validKeys(), k) || !slices.Contains(cfg.settableKeys(), k) {
		t.Errorf("%s missing from validKeys or settableKeys", k)
	}
}

// TestReviewAllowedToolsConfig covers review.allowed_tools (issue #469): Set
// and Get round-trip ";;" or newline separated patterns through config.json,
// the key lists name it, an empty value clears it, and an empty entry is
// refused by Set and by Validate.
func TestReviewAllowedToolsConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := DefaultConfig()
	if !slices.Contains(cfg.validKeys(), "review.allowed_tools") || !slices.Contains(cfg.settableKeys(), "review.allowed_tools") {
		t.Error("review.allowed_tools missing from validKeys or settableKeys")
	}
	if err := cfg.Set("review.allowed_tools", " Bash(make lint:*) ;;Bash(cargo test:*)\nWebFetch "); err != nil {
		t.Fatalf("Set(review.allowed_tools) error = %v", err)
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	loaded, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if v, err := loaded.Get("review.allowed_tools"); err != nil || v != "Bash(make lint:*);;Bash(cargo test:*);;WebFetch" {
		t.Errorf("Get(review.allowed_tools) = %q, %v; want the three patterns", v, err)
	}
	if err := loaded.Set("review.allowed_tools", "Read;;;;Grep"); err == nil || !strings.Contains(err.Error(), "empty entry") {
		t.Errorf("Set with an empty entry = %v, want an empty-entry refusal", err)
	}
	if got := loaded.ReviewAllowedTools(); len(got) != 3 {
		t.Errorf("a refused Set changed the patterns to %q", got)
	}
	if err := loaded.Set("review.allowed_tools", ""); err != nil || len(loaded.ReviewAllowedTools()) != 0 {
		t.Errorf("empty Set = %v, patterns %q; want them cleared", err, loaded.ReviewAllowedTools())
	}
	loaded.Review.AllowedTools = []string{"Read", " "}
	if err := loaded.Validate(); err == nil || !strings.Contains(err.Error(), "review.allowed_tools[1]: empty pattern") {
		t.Errorf("Validate() = %v, want review.allowed_tools[1] refused", err)
	}
}

// TestPanelConfig covers review.panel and review.required (issue #420): the
// default panel, Get/Set as a comma-separated persona list that keeps a
// member's worker, the key lists, and validation refusing an unknown or
// duplicate persona and an unknown worker.
func TestPanelConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	if v, err := cfg.Get("review.panel"); err != nil || v != "correctness,tests,errors,contract,docs" {
		t.Errorf("default Get(review.panel) = %q, %v; want the default panel", v, err)
	}
	if v, err := cfg.Get("review.required"); err != nil || v != "false" {
		t.Errorf("default Get(review.required) = %q, %v; want false", v, err)
	}
	for _, k := range []string{"review.panel", "review.required"} {
		if !slices.Contains(cfg.validKeys(), k) || !slices.Contains(cfg.settableKeys(), k) {
			t.Errorf("%s missing from validKeys or settableKeys", k)
		}
	}
	cfg.Review = &ReviewConfig{Panel: []PanelMember{{Persona: "tests", Worker: cfg.DefaultWorker().Name}}}
	if err := cfg.Set("review.panel", " security, tests ,cross-os"); err != nil {
		t.Fatalf("Set(review.panel) error = %v", err)
	}
	if v, _ := cfg.Get("review.panel"); v != "security,tests,cross-os" {
		t.Errorf("Get(review.panel) = %q, want security,tests,cross-os", v)
	}
	if w := cfg.Review.Panel[1].Worker; w != cfg.DefaultWorker().Name {
		t.Errorf("tests member worker = %q, want it kept (%q)", w, cfg.DefaultWorker().Name)
	}
	if err := cfg.Set("review.required", "true"); err != nil || !cfg.ReviewRequired() {
		t.Errorf("Set(review.required, true) = %v, required = %v", err, cfg.ReviewRequired())
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if err := cfg.Set("review.required", "maybe"); err == nil {
		t.Error("Set(review.required, maybe) = nil, want an error")
	}
	if err := cfg.Set("review.panel", " , "); err == nil {
		t.Error("Set(review.panel, empty) = nil, want an error")
	}
	for _, tc := range []struct {
		panel []PanelMember
		want  string
	}{
		{[]PanelMember{{Persona: "style"}}, `persona "style" must be one of`},
		{[]PanelMember{{Persona: "tests"}, {Persona: "tests"}}, `duplicate persona "tests"`},
		{[]PanelMember{{Persona: "docs", Worker: "nobody"}}, `worker "nobody" is not in workers[]`},
		{[]PanelMember{{Persona: "docs", Adapter: "sim"}}, `adapter "sim"`},
	} {
		c := DefaultConfig()
		c.Review = &ReviewConfig{Panel: tc.panel}
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate(%+v) = %v, want error containing %q", tc.panel, err, tc.want)
		}
	}
}

// TestSetupConfig checks worktree.setup and worktree.setup_timeout (issue
// #430): defaults, Set/Get round-trip, and validation of the timeout.
func TestSetupConfig(t *testing.T) {
	t.Parallel()
	var c Config
	if got, _ := c.Get("worktree.setup"); got != "" {
		t.Errorf("Get(worktree.setup) = %q, want empty by default", got)
	}
	if got, _ := c.Get("worktree.setup_timeout"); got != "10m" {
		t.Errorf("Get(worktree.setup_timeout) = %q, want 10m by default", got)
	}
	if d, err := c.SetupTimeoutDuration(); err != nil || d != 10*time.Minute {
		t.Errorf("SetupTimeoutDuration() = %v, %v, want 10m", d, err)
	}
	if err := c.Set("worktree.setup", "npm ci"); err != nil {
		t.Fatalf("Set(worktree.setup) error = %v", err)
	}
	if err := c.Set("worktree.setup_timeout", "90s"); err != nil {
		t.Fatalf("Set(worktree.setup_timeout) error = %v", err)
	}
	if got, _ := c.Get("worktree.setup"); got != "npm ci" {
		t.Errorf("Get(worktree.setup) = %q, want npm ci", got)
	}
	if d, err := c.SetupTimeoutDuration(); err != nil || d != 90*time.Second {
		t.Errorf("SetupTimeoutDuration() = %v, %v, want 90s", d, err)
	}
	if !slices.Contains(c.settableKeys(), "worktree.setup") || !slices.Contains(c.validKeys(), "worktree.setup_timeout") {
		t.Error("worktree keys missing from settableKeys/validKeys")
	}
	for _, bad := range []string{"soon", "-1m", "0s"} {
		c.Worktree.SetupTimeout = bad
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "worktree.setup_timeout") {
			t.Errorf("Validate(setup_timeout %q) = %v, want a worktree.setup_timeout error", bad, err)
		}
	}
}

// TestConfigStrictLinks checks worktree.strict_links (issue #460): false by
// default, a Set/Get round trip, listed as a key, and a non-bool refused.
func TestConfigStrictLinks(t *testing.T) {
	t.Parallel()
	var c Config
	if got, err := c.Get("worktree.strict_links"); err != nil || got != "false" {
		t.Errorf("Get(worktree.strict_links) = %q, %v, want false by default", got, err)
	}
	if err := c.Set("worktree.strict_links", "true"); err != nil {
		t.Fatalf("Set(worktree.strict_links) error = %v", err)
	}
	if got, _ := c.Get("worktree.strict_links"); got != "true" || !c.StrictLinks() {
		t.Errorf("Get(worktree.strict_links) = %q, StrictLinks() = %v, want true", got, c.StrictLinks())
	}
	if err := c.Set("worktree.strict_links", "yes please"); err == nil || !strings.Contains(err.Error(), "true or false") {
		t.Errorf("Set(worktree.strict_links, non-bool) error = %v, want a true-or-false refusal", err)
	}
	if !c.StrictLinks() {
		t.Error("a refused Set changed worktree.strict_links")
	}
	if !slices.Contains(c.settableKeys(), "worktree.strict_links") || !slices.Contains(c.validKeys(), "worktree.strict_links") {
		t.Error("worktree.strict_links missing from settableKeys/validKeys")
	}
}

// TestConfigWorktreeCarry checks worktree.carry (issue #471): a
// comma-separated Set/Get round trip with empty entries trimmed, "" clears,
// listed as a key, and Validate refuses an absolute path, a .. element and an
// empty entry, naming the key.
func TestConfigWorktreeCarry(t *testing.T) {
	t.Parallel()
	var c Config
	if err := c.Set("worktree.carry", " .env, ,config/local.json,"); err != nil {
		t.Fatalf("Set(worktree.carry) error = %v", err)
	}
	if got, err := c.Get("worktree.carry"); err != nil || got != ".env,config/local.json" {
		t.Errorf("Get(worktree.carry) = %q, %v, want .env,config/local.json", got, err)
	}
	if want := []string{".env", "config/local.json"}; !slices.Equal(c.WorktreeCarry(), want) {
		t.Errorf("WorktreeCarry() = %v, want %v", c.WorktreeCarry(), want)
	}
	if err := c.Validate(); err != nil && strings.Contains(err.Error(), "worktree.carry") {
		t.Errorf("Validate() = %v, want no worktree.carry problem", err)
	}
	if err := c.Set("worktree.carry", ""); err != nil || len(c.WorktreeCarry()) != 0 {
		t.Errorf("Set(worktree.carry, \"\") = %v, WorktreeCarry() = %v, want cleared", err, c.WorktreeCarry())
	}
	if !slices.Contains(c.settableKeys(), "worktree.carry") || !slices.Contains(c.validKeys(), "worktree.carry") {
		t.Error("worktree.carry missing from settableKeys/validKeys")
	}
	for _, bad := range []string{"/abs", "a/../b", ""} {
		c := Config{Worktree: &WorktreeConfig{Carry: []string{".env", bad}}}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "worktree.carry") || !strings.Contains(err.Error(), `entry "`+bad+`"`) {
			t.Errorf("Validate(carry %q) = %v, want a worktree.carry refusal naming it", bad, err)
		}
	}
}

// auditorStaffing returns a config whose lead and auditor are both
// claude/claude-opus-5-5, the auditor with the given independence.
func auditorStaffing(independence, leadSession, auditSession string) Config {
	return Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}}, Staffing: &StaffingConfig{
		Lead:    &RoleConfig{Adapter: "claude", Model: "claude-opus-5-5", Session: leadSession},
		Auditor: &RoleConfig{Adapter: "claude", Model: "claude-opus-5-5", Session: auditSession, Independence: independence},
	}}
}

// TestConfigAuditorIndependenceDefaultRefusesSameModel checks the default
// still refuses an auditor on the lead's agent and model, and names the way
// out (issue #463).
func TestConfigAuditorIndependenceDefaultRefusesSameModel(t *testing.T) {
	t.Parallel()
	err := auditorStaffing("", "", "").Validate()
	if err == nil || !strings.Contains(err.Error(), "same agent and model as the lead") || !strings.Contains(err.Error(), `set staffing.auditor.independence to "session"`) {
		t.Errorf("Validate() = %v, want the same-agent-and-model refusal naming independence", err)
	}
}

// TestConfigAuditorIndependenceSessionAccepted checks independence
// "session" lets a single-model factory staff an auditor (issue #463).
func TestConfigAuditorIndependenceSessionAccepted(t *testing.T) {
	t.Parallel()
	if err := auditorStaffing("session", "lead-1", "audit-1").Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for independence \"session\"", err)
	}
}

// TestConfigAuditorIndependenceSessionStillRefusesSameSession checks
// independence "session" never lets the auditor share the lead's session.
func TestConfigAuditorIndependenceSessionStillRefusesSameSession(t *testing.T) {
	t.Parallel()
	err := auditorStaffing("session", "one", "one").Validate()
	if err == nil || !strings.Contains(err.Error(), `session "one" also holds the lead role`) {
		t.Errorf("Validate() = %v, want the same-session refusal", err)
	}
	if err != nil && strings.Contains(err.Error(), "same agent and model") {
		t.Errorf("Validate() = %v, want no same-agent-and-model problem under independence \"session\"", err)
	}
}

// TestConfigAuditorIndependenceBadValue checks an unknown independence is
// refused.
func TestConfigAuditorIndependenceBadValue(t *testing.T) {
	t.Parallel()
	err := auditorStaffing("model", "", "").Validate()
	if err == nil || !strings.Contains(err.Error(), `staffing.auditor: independence "model" must be "session" or empty`) {
		t.Errorf("Validate() = %v, want the bad-independence problem", err)
	}
}

// TestConfigAuditorIndependenceOnlyAuditor checks independence on any role
// but the auditor is refused.
func TestConfigAuditorIndependenceOnlyAuditor(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "m"}}, Staffing: &StaffingConfig{
		Reviewer: &RoleConfig{Adapter: "claude", Model: "claude-opus-5-5", Independence: "session"},
	}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "staffing.reviewer: independence applies only to the auditor") {
		t.Errorf("Validate() = %v, want the auditor-only problem", err)
	}
}

// loadConfigText writes body as a temp dir's .flywheel/config.json and
// returns LoadConfig's error.
func loadConfigText(t *testing.T, body string) error {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", configFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadConfig(dir)
	return err
}

// TestConfigLintSection checks the lint section's keys are known (issue #462)
// and a near miss is hinted to them.
func TestConfigLintSection(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":1,"workers":[{"name":"w","adapter":"sim","model":"m"}],"lint":{"full_suite":"make test","importers":false}}`
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", configFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if c.Lint == nil || c.Lint.FullSuite != "make test" || c.Lint.Importers == nil || *c.Lint.Importers {
		t.Errorf("Lint = %+v, want full_suite \"make test\" and importers false", c.Lint)
	}
	err = loadConfigText(t, `{"version":1,"workers":[{"name":"w","adapter":"sim"}],"lint":{"fullsuite":"x"}}`)
	if err == nil || !strings.Contains(err.Error(), `did you mean "full_suite"?`) {
		t.Errorf("LoadConfig(fullsuite) = %v, want a full_suite hint", err)
	}
}

// TestConfigUnknownKeyHint checks an unknown key names the nearest key Config
// accepts anywhere in its tree, or keeps today's message when none is near
// (issue #463).
func TestConfigUnknownKeyHint(t *testing.T) {
	t.Parallel()
	worker := func(extra string) string {
		return `{"version":1,"workers":[{"name":"w","adapter":"sim",` + extra + `}]}`
	}
	for _, tc := range []struct{ body, key, hint string }{
		{worker(`"disallowedTools":["Bash"]`), "disallowedTools", "disallowed_tools"},
		{worker(`"allowedTools":["Read"]`), "allowedTools", "allowed_tools"},
		{`{"version":1,"workers":[{"name":"w","adapter":"sim"}],"stafing":{}}`, "stafing", "staffing"},
		{`{"version":1,"workers":[{"name":"w","adapter":"sim"}],"zzqqxxvv":1}`, "zzqqxxvv", ""},
	} {
		err := loadConfigText(t, tc.body)
		if err == nil {
			t.Errorf("LoadConfig(%s) = nil, want an unknown-key error", tc.key)
			continue
		}
		var se *json.SyntaxError
		if errors.As(err, &se) {
			t.Errorf("LoadConfig(%s) = syntax error %v", tc.key, err)
		}
		if tc.hint == "" {
			if strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), `json: unknown field "`+tc.key+`"`) {
				t.Errorf("LoadConfig(%s) = %v, want today's message with no hint", tc.key, err)
			}
			continue
		}
		want := `unknown key "` + tc.key + `"; did you mean "` + tc.hint + `"?`
		if !strings.Contains(err.Error(), want) || !strings.HasPrefix(err.Error(), "parse ") || !strings.Contains(err.Error(), "config.json") {
			t.Errorf("LoadConfig(%s) = %v, want %q with the path", tc.key, err, want)
		}
		if errors.Unwrap(err) == nil || !strings.Contains(errors.Unwrap(err).Error(), "unknown field") {
			t.Errorf("LoadConfig(%s) unwraps to %v, want the decoder's error", tc.key, errors.Unwrap(err))
		}
	}
}

// TestPermissionModeValidate checks worker permission_mode (issue #526): each
// valid mode is accepted on a claude worker, a bad value is rejected naming
// the worker and the allowed values, and any mode on a non-claude worker is
// rejected.
func TestPermissionModeValidate(t *testing.T) {
	t.Parallel()
	cfg := func(adapter, mode string) Config {
		c := DefaultConfig()
		c.Workers = []Worker{{Name: "w1", Adapter: adapter, Model: "m", PermissionMode: mode}}
		return c
	}
	for _, mode := range []string{"", "acceptEdits", "bypassPermissions", "default", "plan", "dontAsk"} {
		if err := cfg("claude", mode).Validate(); err != nil {
			t.Errorf("claude permission_mode %q: Validate() = %v, want nil", mode, err)
		}
	}
	err := cfg("claude", "yolo").Validate()
	if err == nil || !strings.Contains(err.Error(), `worker "w1": permission_mode "yolo" must be one of acceptEdits, bypassPermissions, default, plan, dontAsk`) {
		t.Errorf("bad mode: Validate() = %v, want the worker and allowed values named", err)
	}
	err = cfg("opencode", "bypassPermissions").Validate()
	if err == nil || !strings.Contains(err.Error(), "permission_mode applies to the claude adapter") {
		t.Errorf("opencode mode: Validate() = %v, want permission_mode applies to the claude adapter", err)
	}
	if err := cfg("opencode", "").Validate(); err != nil {
		t.Errorf("opencode without mode: Validate() = %v, want nil", err)
	}
}
