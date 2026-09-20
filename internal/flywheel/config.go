package flywheel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const configFileName = "config.json"

var workerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Line is a product line (issue #69): a named part of the product, the paths
// it covers, and the worker that builds it.
type Line struct {
	Name   string   `json:"name"`
	Worker string   `json:"worker"`         // a worker name in Workers
	Owns   []string `json:"owns,omitempty"` // path entries, matched like a brief's owns:
	WIP    int      `json:"wip,omitempty"`  // units in flight this line may hold; 0 is unlimited (issue #69 follow-up)
}

// RoleConfig is who holds one factory role (issue #69): the agent CLI and model
// that act as lead, inspector or auditor. Session is optional: the name the
// holder registers with flywheel staff.
type RoleConfig struct {
	Adapter string `json:"adapter,omitempty"` // opencode, claude, codex, sim, or "cli" for a person
	Model   string `json:"model,omitempty"`
	Session string `json:"session,omitempty"`
}

// StaffingConfig names the factory's roles.
type StaffingConfig struct {
	Lead      *RoleConfig `json:"lead,omitempty"`
	Inspector *RoleConfig `json:"inspector,omitempty"`
	Auditor   *RoleConfig `json:"auditor,omitempty"`
}

// Config is the project configuration stored in .flywheel/config.json.
type Config struct {
	Version    int               `json:"version"`
	Workers    []Worker          `json:"workers"`
	Lines      []Line            `json:"lines,omitempty"`
	Limits     Limits            `json:"limits,omitempty"`
	Feedback   Feedback          `json:"feedback,omitempty"`
	Lease      *LeaseConfig      `json:"lease,omitempty"`
	Controller *ControllerConfig `json:"controller,omitempty"`
	Baseline   *Baseline         `json:"baseline,omitempty"`
	Audit      *AuditPolicy      `json:"audit,omitempty"`
	Log        *LogConfig        `json:"log,omitempty"`
	Staffing   *StaffingConfig   `json:"staffing,omitempty"`
}

// Worker configures a single CLI worker.
type Worker struct {
	Name         string     `json:"name"`
	Adapter      string     `json:"adapter"` // "opencode", "sim", "claude", or "codex"
	Model        string     `json:"model"`
	Variant      string     `json:"variant,omitempty"`
	MaxParallel  int        `json:"max_parallel,omitempty"`  // 0 means 1
	StallTimeout int        `json:"stall_timeout,omitempty"` // whole seconds; 0 means the default 600
	Fallbacks    []Fallback `json:"fallbacks,omitempty"`
	// AllowedTools lists the claude adapter's --allowedTools patterns. When
	// empty, allowedTools() resolves to ["Bash"] so a worker can run its own
	// gates (issue #192). An explicitly configured list REPLACES that default;
	// it is not merged with it.
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// DisallowedTools lists the claude adapter's --disallowedTools patterns.
	// When empty, disallowedTools() resolves to the git-write family
	// ("Bash(git commit:*)", "Bash(git push:*)", ...), enforcing the worker
	// permission policy ("workers never commit, stash, reset, checkout or
	// push") at the permission layer (issue #192). An explicitly configured
	// list REPLACES that default; it is not merged with it.
	DisallowedTools []string `json:"disallowed_tools,omitempty"`
}

// defaultStallTimeout applies when a worker's StallTimeout is unset (0).
const defaultStallTimeout = 600 * time.Second

// stallTimeoutDuration returns the worker's configured stall timeout, or the
// default 600s when unset (issue #85).
func (w Worker) stallTimeoutDuration() time.Duration {
	if w.StallTimeout <= 0 {
		return defaultStallTimeout
	}
	return time.Duration(w.StallTimeout) * time.Second
}

// defaultAllowedTools lets a worker run its own gate lines out of the box.
var defaultAllowedTools = []string{"Bash"}

// defaultDisallowedTools implements the worker permission policy at the
// dispatch's permission layer: workers never commit, stash, reset, checkout,
// rebase or merge, whatever the brief says.
var defaultDisallowedTools = []string{
	"Bash(git commit:*)", "Bash(git push:*)", "Bash(git stash:*)",
	"Bash(git reset:*)", "Bash(git checkout:*)", "Bash(git rebase:*)", "Bash(git merge:*)",
}

// allowedTools returns the worker's allowed tool patterns, or the default
// ["Bash"] when unset; an explicitly configured list replaces, never merges
// (issue #192).
func (w Worker) allowedTools() []string {
	if len(w.AllowedTools) == 0 {
		return defaultAllowedTools
	}
	return w.AllowedTools
}

// disallowedTools returns the worker's disallowed tool patterns, or the
// default git-write family when unset; an explicitly configured list
// replaces, never merges (issue #192).
func (w Worker) disallowedTools() []string {
	if len(w.DisallowedTools) == 0 {
		return defaultDisallowedTools
	}
	return w.DisallowedTools
}

// Fallback is a model to fall back to when the worker's model is unavailable.
type Fallback struct {
	Model    string `json:"model"`
	Approved bool   `json:"approved,omitempty"` // standing OK to switch without asking
}

// Limits caps shared resource use across workers.
type Limits struct {
	PerHost       int      `json:"per_host,omitempty"`
	Budget        *Budget  `json:"budget,omitempty"`
	Breaker       *Breaker `json:"breaker,omitempty"`
	RatePerMinute int      `json:"rate_per_minute,omitempty"` // at most this many dispatches of one model in any 60 seconds; 0 means no limit
}

// Budget caps spending and tokens per wave.
type Budget struct {
	WaveCostUSD float64 `json:"wave_cost_usd,omitempty"`
	WaveTokens  int     `json:"wave_tokens,omitempty"` // once recorded input+output+reasoning tokens reach it, new dispatches are refused
}

// Breaker opens the circuit for a model after Errors consecutive provider
// errors (finished attempts with reason "error") and keeps it open for
// Cooldown after the newest one; then one dispatch is let through as a probe
// (issue #46).
type Breaker struct {
	Errors   int    `json:"errors"`   // consecutive provider errors that open it; 0 disables
	Cooldown string `json:"cooldown"` // how long it stays open, a Go duration such as "10m"
}

// CooldownDuration parses Cooldown ("" means 10 minutes).
func (b Breaker) CooldownDuration() (time.Duration, error) {
	if b.Cooldown == "" {
		return 10 * time.Minute, nil
	}
	return time.ParseDuration(b.Cooldown)
}

// Feedback configures how results flow upstream.
type Feedback struct {
	Upstream string `json:"upstream,omitempty"` // owner/repo
	Submit   string `json:"submit,omitempty"`   // "ask" (default) or "never"
}

// LeaseConfig tunes the worker lease that flywheel run writes while a worker
// runs. Both values are Go duration strings; when the block is absent the
// defaults apply: renew_interval 15s, ttl 45s.
type LeaseConfig struct {
	RenewInterval string `json:"renew_interval,omitempty"`
	TTL           string `json:"ttl,omitempty"`
}

const (
	defaultLeaseRenewInterval = 15 * time.Second
	defaultLeaseTTL           = 45 * time.Second
)

// ControllerConfig tunes the controller loop: the tick interval, the lock
// ttl and the intent timeout (unused until the dispatch phase). All three
// are Go duration strings; when the block is absent the defaults apply:
// interval 10s, lock_ttl 30s, intent_timeout 2m.
type ControllerConfig struct {
	Interval      string `json:"interval,omitempty"`
	LockTTL       string `json:"lock_ttl,omitempty"`
	IntentTimeout string `json:"intent_timeout,omitempty"`
}

const (
	defaultControllerInterval      = 10 * time.Second
	defaultControllerLockTTL       = 30 * time.Second
	defaultControllerIntentTimeout = 2 * time.Minute
)

// Baseline prices a frontier model per million tokens, for the frontier-only
// cost comparison in flywheel stats (issue #59): the same tokens the workers
// used, priced as if the frontier model had done the work.
type Baseline struct {
	Model             string  `json:"model"`
	InputPerMTok      float64 `json:"input_per_mtok"`
	OutputPerMTok     float64 `json:"output_per_mtok"`
	CacheReadPerMTok  float64 `json:"cache_read_per_mtok"`
	CacheWritePerMTok float64 `json:"cache_write_per_mtok"`
}

// AuditPolicy opts a factory into audit gates (issue #61).
type AuditPolicy struct {
	// FirstArticle makes flywheel land refuse a unit (rule T7) until its
	// worker line's first article is audited conforming, and while the
	// line's latest audit is a nonconformance.
	FirstArticle bool `json:"first_article,omitempty"`
}

// LogConfig configures the event log's layout (issue #47).
type LogConfig struct {
	// Shards records that this repository uses per-task shards under
	// .flywheel/events/. It is written by flywheel log --shard; the layout
	// itself is decided by that directory, never by this key, which also
	// fences out binaries too old to read shards (they reject unknown
	// config fields).
	Shards bool `json:"shards,omitempty"`
}

// Cost prices t at the baseline: reasoning is billed at the output price.
func (b Baseline) Cost(t Tokens) float64 {
	return (float64(t.Input)*b.InputPerMTok +
		float64(t.Output+t.Reasoning)*b.OutputPerMTok +
		float64(t.CacheRead)*b.CacheReadPerMTok +
		float64(t.CacheWrite)*b.CacheWritePerMTok) / 1e6
}

// controllerTimings returns the controller interval, lock ttl and intent
// timeout; an absent controller block (or unparseable values, which Validate
// rejects) means the defaults.
func (c Config) controllerTimings() (interval, ttl, intent time.Duration) {
	interval, ttl, intent = defaultControllerInterval, defaultControllerLockTTL, defaultControllerIntentTimeout
	if c.Controller == nil {
		return interval, ttl, intent
	}
	if i, err := time.ParseDuration(c.Controller.Interval); err == nil {
		interval = i
	}
	if t, err := time.ParseDuration(c.Controller.LockTTL); err == nil {
		ttl = t
	}
	if m, err := time.ParseDuration(c.Controller.IntentTimeout); err == nil {
		intent = m
	}
	return interval, ttl, intent
}

// ControllerTimings is the exported form of controllerTimings for the
// command entry points.
func ControllerTimings(cfg Config) (interval, ttl, intent time.Duration) {
	return cfg.controllerTimings()
}

// leaseTimings returns the lease renew interval and ttl; an absent lease
// block (or unparseable values, which Validate rejects) means the defaults.
func (c Config) leaseTimings() (renew, ttl time.Duration) {
	renew, ttl = defaultLeaseRenewInterval, defaultLeaseTTL
	if c.Lease == nil {
		return renew, ttl
	}
	if r, err := time.ParseDuration(c.Lease.RenewInterval); err == nil {
		renew = r
	}
	if t, err := time.ParseDuration(c.Lease.TTL); err == nil {
		ttl = t
	}
	return renew, ttl
}

// DefaultConfig returns the built-in configuration used when no
// .flywheel/config.json exists.
func DefaultConfig() Config {
	return Config{
		Version: 1,
		Workers: []Worker{{
			Name:        "default",
			Adapter:     "opencode",
			Model:       "openrouter/deepseek/deepseek-v4-flash-0731",
			MaxParallel: 4,
		}},
		Feedback: Feedback{
			Upstream: "suzworx/flywheel",
			Submit:   "ask",
		},
	}
}

// LoadConfig reads <dir>/.flywheel/config.json. A missing file returns the
// default configuration and exists=false. Malformed JSON or unknown fields
// is an error naming the file; a successfully parsed configuration is then
// validated.
func LoadConfig(dir string) (Config, bool, error) {
	path := filepath.Join(dir, ".flywheel", configFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), false, nil
		}
		return Config{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, false, err
	}
	return c, true, nil
}

// Validate checks the configuration and reports every problem found, one per
// line, in a single error.
func (c Config) Validate() error {
	var problems []string
	if c.Version != 1 {
		problems = append(problems, fmt.Sprintf("version: got %d, want 1", c.Version))
	}
	if len(c.Workers) == 0 {
		problems = append(problems, "at least one worker is required")
	}
	seen := make(map[string]bool)
	for i, w := range c.Workers {
		where := fmt.Sprintf("workers[%d]", i)
		if w.Name == "" {
			problems = append(problems, where+": name must not be empty")
		} else {
			if !workerNameRe.MatchString(w.Name) {
				problems = append(problems, fmt.Sprintf("%s: name %q must match ^[a-z0-9][a-z0-9-]*$", where, w.Name))
			}
			if seen[w.Name] {
				problems = append(problems, fmt.Sprintf("%s: duplicate name %q", where, w.Name))
			}
			seen[w.Name] = true
		}
		if !adapterKnown(w.Adapter, false) {
			problems = append(problems, fmt.Sprintf("%s: adapter %q must be \"opencode\", \"sim\", \"claude\", or \"codex\"", where, w.Adapter))
		}
		if w.Model == "" {
			problems = append(problems, where+": model must not be empty")
		}
		if w.MaxParallel < 0 {
			problems = append(problems, fmt.Sprintf("%s: max_parallel %d must be >= 0", where, w.MaxParallel))
		}
		if w.StallTimeout < 0 {
			problems = append(problems, fmt.Sprintf("%s: stall_timeout %d must be >= 0", where, w.StallTimeout))
		}
		for j, f := range w.Fallbacks {
			switch {
			case f.Model == "":
				problems = append(problems, fmt.Sprintf("%s: fallbacks[%d] model must not be empty", where, j))
			case f.Model == w.Model:
				problems = append(problems, fmt.Sprintf("%s: fallback model %q must differ from the worker's model", where, f.Model))
			}
		}
	}
	seenLines := make(map[string]bool)
	for i, l := range c.Lines {
		where := fmt.Sprintf("lines[%d]", i)
		if l.Name == "" {
			problems = append(problems, where+": name must not be empty")
		} else {
			if !workerNameRe.MatchString(l.Name) {
				problems = append(problems, fmt.Sprintf("%s: name %q must match ^[a-z0-9][a-z0-9-]*$", where, l.Name))
			}
			if seenLines[l.Name] {
				problems = append(problems, fmt.Sprintf("%s: duplicate name %q", where, l.Name))
			}
			seenLines[l.Name] = true
		}
		if _, ok := c.Worker(l.Worker); !ok {
			problems = append(problems, fmt.Sprintf("line %q: worker %q is not in workers[]", l.Name, l.Worker))
		}
		if l.WIP < 0 {
			problems = append(problems, fmt.Sprintf("line %q: wip must not be negative", l.Name))
		}
	}
	if c.Limits.PerHost < 0 {
		problems = append(problems, fmt.Sprintf("limits.per_host %d must be >= 0", c.Limits.PerHost))
	}
	if c.Limits.RatePerMinute < 0 {
		problems = append(problems, fmt.Sprintf("limits.rate_per_minute %d must be >= 0", c.Limits.RatePerMinute))
	}
	if c.Limits.Budget != nil && c.Limits.Budget.WaveTokens < 0 {
		problems = append(problems, fmt.Sprintf("limits.budget.wave_tokens %d must be >= 0", c.Limits.Budget.WaveTokens))
	}
	if c.Limits.Breaker != nil {
		if c.Limits.Breaker.Errors < 0 {
			problems = append(problems, fmt.Sprintf("limits.breaker.errors %d must be >= 0", c.Limits.Breaker.Errors))
		}
		if _, err := c.Limits.Breaker.CooldownDuration(); err != nil {
			problems = append(problems, fmt.Sprintf("limits.breaker.cooldown %q: %v", c.Limits.Breaker.Cooldown, err))
		} else {
			dur, _ := c.Limits.Breaker.CooldownDuration()
			if dur <= 0 {
				problems = append(problems, fmt.Sprintf("limits.breaker.cooldown %q must be > 0", c.Limits.Breaker.Cooldown))
			}
		}
	}
	switch c.Feedback.Submit {
	case "", "ask", "never":
	default:
		problems = append(problems, fmt.Sprintf("feedback.submit %q must be empty, \"ask\", or \"never\"", c.Feedback.Submit))
	}
	if c.Lease != nil {
		renew, rerr := time.ParseDuration(c.Lease.RenewInterval)
		if rerr != nil {
			problems = append(problems, fmt.Sprintf("lease.renew_interval %q is not a valid duration", c.Lease.RenewInterval))
		} else if renew <= 0 {
			problems = append(problems, fmt.Sprintf("lease.renew_interval %s must be positive", c.Lease.RenewInterval))
		}
		ttl, terr := time.ParseDuration(c.Lease.TTL)
		if terr != nil {
			problems = append(problems, fmt.Sprintf("lease.ttl %q is not a valid duration", c.Lease.TTL))
		} else if ttl <= 0 {
			problems = append(problems, fmt.Sprintf("lease.ttl %s must be positive", c.Lease.TTL))
		}
		if rerr == nil && terr == nil && ttl <= renew {
			problems = append(problems, fmt.Sprintf("lease.ttl %s must be greater than renew_interval %s", c.Lease.TTL, c.Lease.RenewInterval))
		}
	}
	if c.Controller != nil {
		interval, ierr := time.ParseDuration(c.Controller.Interval)
		if ierr != nil {
			problems = append(problems, fmt.Sprintf("controller.interval %q is not a valid duration", c.Controller.Interval))
		} else if interval <= 0 {
			problems = append(problems, fmt.Sprintf("controller.interval %s must be positive", c.Controller.Interval))
		}
		lockTTL, terr := time.ParseDuration(c.Controller.LockTTL)
		if terr != nil {
			problems = append(problems, fmt.Sprintf("controller.lock_ttl %q is not a valid duration", c.Controller.LockTTL))
		} else if lockTTL <= 0 {
			problems = append(problems, fmt.Sprintf("controller.lock_ttl %s must be positive", c.Controller.LockTTL))
		}
		intent, merr := time.ParseDuration(c.Controller.IntentTimeout)
		if merr != nil {
			problems = append(problems, fmt.Sprintf("controller.intent_timeout %q is not a valid duration", c.Controller.IntentTimeout))
		} else if intent <= 0 {
			problems = append(problems, fmt.Sprintf("controller.intent_timeout %s must be positive", c.Controller.IntentTimeout))
		}
		if ierr == nil && terr == nil && lockTTL <= interval {
			problems = append(problems, fmt.Sprintf("controller.lock_ttl %s must be greater than interval %s", c.Controller.LockTTL, c.Controller.Interval))
		}
	}
	if c.Baseline != nil {
		if c.Baseline.Model == "" {
			problems = append(problems, "baseline.model must not be empty")
		}
		// A slice, not a map, so the problems come out in a stable order.
		for _, p := range []struct {
			field string
			val   float64
		}{
			{"baseline.input_per_mtok", c.Baseline.InputPerMTok},
			{"baseline.output_per_mtok", c.Baseline.OutputPerMTok},
			{"baseline.cache_read_per_mtok", c.Baseline.CacheReadPerMTok},
			{"baseline.cache_write_per_mtok", c.Baseline.CacheWritePerMTok},
		} {
			if p.val < 0 {
				problems = append(problems, fmt.Sprintf("%s %g must be >= 0", p.field, p.val))
			}
		}
	}
	if c.Staffing != nil {
		for _, role := range c.Staffing.roles() {
			if role.Cfg != nil && role.Cfg.Adapter != "" && !adapterKnown(role.Cfg.Adapter, true) {
				problems = append(problems, fmt.Sprintf("staffing.%s: adapter %q must be %s, or \"cli\"", role.Name, role.Cfg.Adapter, strings.Join(quoted(workerAdapters), ", ")))
			}
		}
		// An audit is independent only when the auditor is neither the same
		// session nor the same agent and model as the lead or the inspector
		// (#354 review: one session holding both roles is the plainer case).
		if a := c.Staffing.Auditor; a != nil {
			for _, role := range c.Staffing.roles() {
				if role.Name == "auditor" || role.Cfg == nil {
					continue
				}
				if a.Session != "" && role.Cfg.Session == a.Session {
					problems = append(problems, fmt.Sprintf("staffing.auditor: session %q also holds the %s role (an audit is only independent when it is)", a.Session, role.Name))
				}
				if a.Adapter != "" && a.Model != "" && role.Cfg.Adapter == a.Adapter && role.Cfg.Model == a.Model {
					problems = append(problems, fmt.Sprintf("staffing.auditor: must not be the same agent and model as the %s (an audit is only independent when it is)", role.Name))
				}
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "\n"))
}

// Worker returns the worker with the given name.
func (c Config) Worker(name string) (Worker, bool) {
	for _, w := range c.Workers {
		if w.Name == name {
			return w, true
		}
	}
	return Worker{}, false
}

// Line returns the line with the given name.
func (c Config) Line(name string) (Line, bool) {
	for _, l := range c.Lines {
		if l.Name == name {
			return l, true
		}
	}
	return Line{}, false
}

// DefaultWorker returns the first worker, which owns the bare keys in Get.
func (c Config) DefaultWorker() Worker {
	if len(c.Workers) == 0 {
		return Worker{}
	}
	return c.Workers[0]
}

// Get returns a configuration value for scripts and skills. Bare keys apply
// to the default worker; every worker key is also addressable as
// workers.<name>.<key>.
func (c Config) Get(key string) (string, error) {
	if v, ok := c.defaultKey(key); ok {
		return v, nil
	}
	if rest, ok := strings.CutPrefix(key, "workers."); ok {
		if dot := strings.IndexByte(rest, '.'); dot > 0 {
			if w, ok := c.Worker(rest[:dot]); ok {
				if v, ok := workerValue(w, rest[dot+1:]); ok {
					return v, nil
				}
			}
		}
	}
	if rest, ok := strings.CutPrefix(key, "staffing."); ok {
		if name, field, found := strings.Cut(rest, "."); found {
			for _, role := range c.Staffing.roles() {
				if role.Name != name {
					continue
				}
				switch field {
				case "adapter", "model", "session":
					if role.Cfg == nil {
						return "", nil
					}
					switch field {
					case "adapter":
						return role.Cfg.Adapter, nil
					case "model":
						return role.Cfg.Model, nil
					}
					return role.Cfg.Session, nil
				}
			}
		}
	}
	if rest, ok := strings.CutPrefix(key, "lines."); ok {
		if name, field, found := strings.Cut(rest, "."); found {
			if l, ok := c.Line(name); ok {
				switch field {
				case "wip":
					return strconv.Itoa(l.WIP), nil
				}
			}
		}
	}
	switch key {
	case "feedback.upstream":
		return c.Feedback.Upstream, nil
	case "feedback.submit":
		return c.Feedback.Submit, nil
	case "limits.per_host":
		return strconv.Itoa(c.Limits.PerHost), nil
	case "log.shards":
		if c.Log != nil && c.Log.Shards {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("unknown key %q; valid keys: %s", key, strings.Join(c.validKeys(), ", "))
}

// defaultKey resolves the bare worker keys against the default worker.
func (c Config) defaultKey(key string) (string, bool) {
	if len(c.Workers) == 0 {
		return "", false
	}
	return workerValue(c.Workers[0], key)
}

// workerValue resolves a worker-scoped key.
func workerValue(w Worker, key string) (string, bool) {
	switch key {
	case "model":
		return w.Model, true
	case "variant":
		return w.Variant, true
	case "adapter":
		return w.Adapter, true
	case "max_parallel":
		return strconv.Itoa(w.MaxParallel), true
	case "stall_timeout":
		return strconv.Itoa(w.StallTimeout), true
	case "fallbacks":
		return joinFallbacks(w.Fallbacks, true), true
	case "fallbacks.all":
		return joinFallbacks(w.Fallbacks, false), true
	}
	return "", false
}

// joinFallbacks lists fallback models, comma-separated, optionally restricted
// to approved ones.
func joinFallbacks(fbs []Fallback, approvedOnly bool) string {
	var models []string
	for _, f := range fbs {
		if approvedOnly && !f.Approved {
			continue
		}
		models = append(models, f.Model)
	}
	return strings.Join(models, ",")
}

// validKeys lists every key Get accepts, including each worker's keys.
func (c Config) validKeys() []string {
	keys := []string{
		"adapter", "fallbacks", "fallbacks.all", "feedback.submit",
		"feedback.upstream", "limits.per_host", "log.shards", "max_parallel", "model", "stall_timeout", "variant",
		"staffing.lead.adapter", "staffing.lead.model", "staffing.lead.session",
		"staffing.inspector.adapter", "staffing.inspector.model", "staffing.inspector.session",
		"staffing.auditor.adapter", "staffing.auditor.model", "staffing.auditor.session",
	}
	for _, w := range c.Workers {
		for _, k := range []string{"adapter", "fallbacks", "fallbacks.all", "max_parallel", "model", "stall_timeout", "variant"} {
			keys = append(keys, "workers."+w.Name+"."+k)
		}
	}
	sort.Strings(keys)
	return keys
}

// Set assigns value to the given configuration key. Bare worker keys apply to
// the default worker; every worker key is also addressable as
// workers.<name>.<key>. The settable keys are model, variant, adapter,
// max_parallel, stall_timeout (worker), feedback.upstream, feedback.submit,
// and limits.per_host. Integer keys parse with strconv.Atoi. fallbacks is not
// settable here and directs the caller to edit .flywheel/config.json.
// Validation is left to WriteConfig.
func (c *Config) Set(key, value string) error {
	if rest, ok := strings.CutPrefix(key, "workers."); ok {
		if dot := strings.IndexByte(rest, '.'); dot > 0 {
			for i := range c.Workers {
				if c.Workers[i].Name == rest[:dot] {
					return setWorkerValue(&c.Workers[i], rest[dot+1:], value)
				}
			}
		}
		return c.settableErr(key)
	}
	if rest, ok := strings.CutPrefix(key, "staffing."); ok {
		if name, field, found := strings.Cut(rest, "."); found {
			// Nothing is created before the key is known to be settable, so a
			// rejected Set leaves no empty role behind (#354 review).
			known := false
			for _, role := range (*StaffingConfig)(nil).roles() {
				if role.Name == name {
					known = true
				}
			}
			if !known || (field != "adapter" && field != "model" && field != "session") {
				return c.settableErr(key)
			}
			if c.Staffing == nil {
				c.Staffing = &StaffingConfig{}
			}
			slot := map[string]**RoleConfig{
				"lead": &c.Staffing.Lead, "inspector": &c.Staffing.Inspector, "auditor": &c.Staffing.Auditor,
			}[name]
			if *slot == nil {
				*slot = &RoleConfig{}
			}
			switch field {
			case "adapter":
				(*slot).Adapter = value
			case "model":
				(*slot).Model = value
			default:
				(*slot).Session = value
			}
			return nil
		}
	}
	if rest, ok := strings.CutPrefix(key, "lines."); ok {
		if _, field, found := strings.Cut(rest, "."); found && field == "wip" {
			return fmt.Errorf("lines.<name>.wip is not settable; edit .flywheel/config.json")
		}
	}
	switch key {
	case "model", "variant", "adapter", "max_parallel", "stall_timeout":
		if len(c.Workers) == 0 {
			return c.settableErr(key)
		}
		return setWorkerValue(&c.Workers[0], key, value)
	case "feedback.upstream":
		c.Feedback.Upstream = value
		return nil
	case "feedback.submit":
		c.Feedback.Submit = value
		return nil
	case "limits.per_host":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("limits.per_host: value %q must be an integer", value)
		}
		c.Limits.PerHost = n
		return nil
	case "fallbacks", "fallbacks.all":
		return fmt.Errorf("%s: not settable; edit .flywheel/config.json", key)
	case "log.shards":
		return fmt.Errorf("log.shards is not settable; run flywheel log --shard (the sharded layout is one-way)")
	}
	return c.settableErr(key)
}

// setWorkerValue assigns a worker-scoped value, parsing integer keys.
func setWorkerValue(w *Worker, key, value string) error {
	switch key {
	case "model":
		w.Model = value
	case "variant":
		w.Variant = value
	case "adapter":
		w.Adapter = value
	case "max_parallel":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("max_parallel: value %q must be an integer", value)
		}
		w.MaxParallel = n
	case "stall_timeout":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("stall_timeout: value %q must be an integer", value)
		}
		w.StallTimeout = n
	default:
		return fmt.Errorf("unknown key %q; valid worker keys: model, variant, adapter, max_parallel, stall_timeout", key)
	}
	return nil
}

// settableErr lists the keys Set accepts for an unrecognized key.
func (c Config) settableErr(key string) error {
	return fmt.Errorf("unknown key %q; settable keys: %s", key, strings.Join(c.settableKeys(), ", "))
}

// settableKeys lists every key Set accepts, including each worker's keys.
func (c Config) settableKeys() []string {
	keys := []string{
		"adapter", "feedback.submit", "feedback.upstream", "limits.per_host",
		"max_parallel", "model", "stall_timeout", "variant",
		"staffing.lead.adapter", "staffing.lead.model", "staffing.lead.session",
		"staffing.inspector.adapter", "staffing.inspector.model", "staffing.inspector.session",
		"staffing.auditor.adapter", "staffing.auditor.model", "staffing.auditor.session",
	}
	for _, w := range c.Workers {
		for _, k := range []string{"adapter", "max_parallel", "model", "stall_timeout", "variant"} {
			keys = append(keys, "workers."+w.Name+"."+k)
		}
	}
	sort.Strings(keys)
	return keys
}

// WriteConfig validates c and writes it to <dir>/.flywheel/config.json as
// 2-space-indented JSON with a trailing newline. The write goes through a
// temp file and rename so readers never observe partial output; .flywheel/ is
// created if missing.
func WriteConfig(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	b = append(b, '\n')
	dotFlywheel := filepath.Join(dir, ".flywheel")
	if err := os.MkdirAll(dotFlywheel, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dotFlywheel, err)
	}
	path := filepath.Join(dotFlywheel, configFileName)
	tmp, err := os.CreateTemp(dotFlywheel, ".config.json.tmp*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	return nil
}
