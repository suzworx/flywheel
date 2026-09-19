package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigMissingFileReturnsDefault(t *testing.T) {
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
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for adapter \"claude\"", err)
	}
}

// TestConfigValidateRejectsUnknownAdapter checks an adapter outside
// opencode/sim/claude is still rejected.
func TestConfigValidateRejectsUnknownAdapter(t *testing.T) {
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "nope", Model: "m"}}}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `adapter "nope"`) {
		t.Errorf("Validate() = %v, want an error naming adapter \"nope\"", err)
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
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

func TestConfigSetIntegerParseError(t *testing.T) {
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

func TestConfigSetInvalidValueLeavesFileUnchanged(t *testing.T) {
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
	cfg := DefaultConfig()
	cfg.Lease = &LeaseConfig{RenewInterval: "30s", TTL: "90s"}
	renew, ttl := cfg.leaseTimings()
	if renew != 30*time.Second || ttl != 90*time.Second {
		t.Errorf("leaseTimings() = %s/%s, want 30s/90s", renew, ttl)
	}
}

func TestConfigLeaseValidation(t *testing.T) {
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
	cfg := DefaultConfig()
	cfg.Controller = &ControllerConfig{Interval: "20s", LockTTL: "60s", IntentTimeout: "5m"}
	interval, ttl, intent := cfg.controllerTimings()
	if interval != 20*time.Second || ttl != 60*time.Second || intent != 5*time.Minute {
		t.Errorf("controllerTimings() = %s/%s/%s, want 20s/60s/5m", interval, ttl, intent)
	}
}

func TestConfigControllerValidation(t *testing.T) {
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

// TestConfigBaselineValidate checks baseline validation: a valid baseline
// passes, empty model fails, negative prices fail (issue #59).
func TestConfigBaselineValidate(t *testing.T) {
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
