package flywheel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	// Independence is the auditor's independence rule (issue #463): "" (the
	// agent and model must differ from every other role's) or "session" (the
	// same agent and model is allowed; flywheel audit's session check is the
	// independence). Only the auditor may set it.
	Independence string `json:"independence,omitempty"`
}

// StaffingConfig names the factory's roles.
type StaffingConfig struct {
	Lead      *RoleConfig `json:"lead,omitempty"`
	Inspector *RoleConfig `json:"inspector,omitempty"`
	Auditor   *RoleConfig `json:"auditor,omitempty"`
	Reviewer  *RoleConfig `json:"reviewer,omitempty"` // the agent that reads a unit's diff (issue #389)
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
	Review     *ReviewConfig     `json:"review,omitempty"`
	Worktree   *WorktreeConfig   `json:"worktree,omitempty"`
	Lint       *LintConfig       `json:"lint,omitempty"`
}

// LintConfig tunes flywheel lint's warnings (issue #462).
type LintConfig struct {
	// FullSuite is the regular expression a gate must match to count as the
	// full test suite; "" means the default for the directory's toolchain.
	FullSuite string `json:"full_suite,omitempty"`
	// Importers turns the Go importer-coverage warning off when false; nil
	// means on wherever go.mod exists.
	Importers *bool `json:"importers,omitempty"`
}

// WorktreeConfig configures `flywheel run --worktree` (issue #430): a setup
// command run in the task's worktree before every dispatch.
type WorktreeConfig struct {
	// Setup is a shell command run (bash -c, cwd the worktree) before the
	// worker starts; it runs on every dispatch, so it must be idempotent.
	Setup string `json:"setup,omitempty"`
	// SetupTimeout bounds Setup, a Go duration; "" means 10m.
	SetupTimeout string `json:"setup_timeout,omitempty"`
	// StrictLinks refuses a dispatch whose needs-state "(link)" paths hold
	// links into the main checkout (issue #460); false only warns.
	StrictLinks bool `json:"strict_links,omitempty"`
	// Carry lists repo-relative paths copied from the main checkout into the
	// task's worktree on every --worktree dispatch, a git-ignored .env say
	// (issue #471); a brief's needs-state "(copy)" entries are added to it.
	Carry []string `json:"carry,omitempty"`
}

// WorktreeCarry is worktree.carry, nil when unset.
func (c Config) WorktreeCarry() []string {
	if c.Worktree == nil {
		return nil
	}
	return c.Worktree.Carry
}

// StrictLinks is worktree.strict_links, false when unset.
func (c Config) StrictLinks() bool {
	return c.Worktree != nil && c.Worktree.StrictLinks
}

// SetupCommand is worktree.setup, "" when unset.
func (c Config) SetupCommand() string {
	if c.Worktree == nil {
		return ""
	}
	return c.Worktree.Setup
}

// SetupTimeoutDuration parses worktree.setup_timeout ("" means 10 minutes).
func (c Config) SetupTimeoutDuration() (time.Duration, error) {
	if c.Worktree == nil || c.Worktree.SetupTimeout == "" {
		return 10 * time.Minute, nil
	}
	return time.ParseDuration(c.Worktree.SetupTimeout)
}

// ReviewConfig configures the review panel (issue #420): the personas that
// review every unit, one dimension each, and whether a pass needs the panel.
type ReviewConfig struct {
	Panel []PanelMember `json:"panel,omitempty"`
	// Required makes a complete panel a condition of every inspected pass,
	// not only of a task the panel has already reviewed. Default false.
	Required bool `json:"required,omitempty"`
	// GroupGates are shell commands run in a group's integration tree by
	// flywheel review --group (issue #420); each is recorded as a validated
	// event of the group, gate g<n>. Default none.
	GroupGates []string `json:"group_gates,omitempty"`
	// AllowedTools are claude --allowedTools patterns a review agent may use
	// beyond its read-only base, gh issue/pr view and the unit's gate
	// commands (issue #469), e.g. Bash(make lint:*). Default none.
	AllowedTools []string `json:"allowed_tools,omitempty"`
}

// groupGatesSep separates review.group_gates and review.allowed_tools on
// config get and set; set also splits on newlines.
const groupGatesSep = ";;"

// ReviewGroupGates is review.group_gates.
func (c Config) ReviewGroupGates() []string {
	if c.Review == nil {
		return nil
	}
	return c.Review.GroupGates
}

// ReviewAllowedTools is review.allowed_tools.
func (c Config) ReviewAllowedTools() []string {
	if c.Review == nil {
		return nil
	}
	return c.Review.AllowedTools
}

// PanelMember is one reviewer on the panel: its persona (the dimension) and
// optionally who runs it — a worker in config, or an adapter and model.
type PanelMember struct {
	Persona string `json:"persona"`
	Adapter string `json:"adapter,omitempty"`
	Model   string `json:"model,omitempty"`
	Worker  string `json:"worker,omitempty"`
}

// ReviewPanel is the configured panel, or DefaultPanel's members when unset.
func (c Config) ReviewPanel() []PanelMember {
	if c.Review != nil && len(c.Review.Panel) > 0 {
		return c.Review.Panel
	}
	out := make([]PanelMember, len(DefaultPanel))
	for i, p := range DefaultPanel {
		out[i] = PanelMember{Persona: p}
	}
	return out
}

// PanelDimensions is the panel's persona names, in order.
func (c Config) PanelDimensions() []string {
	var out []string
	for _, m := range c.ReviewPanel() {
		out = append(out, m.Persona)
	}
	return out
}

// ReviewRequired reports review.required.
func (c Config) ReviewRequired() bool {
	return c.Review != nil && c.Review.Required
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
	// Routing picks the worker's model per dispatch from the per-model
	// scoreboard (issue #474). Opt-in: no routing block means today's
	// behaviour exactly. Edited in config.json, like fallbacks.
	Routing *Routing `json:"routing,omitempty"`
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
	// MCP is the MCP servers a claude worker may load, in the Claude CLI's
	// --mcp-config JSON shape ({"mcpServers": {...}}). When unset the worker
	// loads no MCP server at all (issue #425).
	MCP json.RawMessage `json:"mcp,omitempty"`
}

// validateMCP checks that a set MCP value is a JSON object with an
// "mcpServers" object; an unset value is valid (issue #425).
func (w Worker) validateMCP() error {
	if len(bytes.TrimSpace(w.MCP)) == 0 {
		return nil
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(w.MCP, &v); err != nil || v == nil {
		return errors.New("mcp must be a JSON object")
	}
	var servers map[string]json.RawMessage
	raw, ok := v["mcpServers"]
	if !ok {
		return errors.New("mcp must have an \"mcpServers\" object")
	}
	if err := json.Unmarshal(raw, &servers); err != nil || servers == nil {
		return errors.New("mcp.mcpServers must be a JSON object")
	}
	return nil
}

// mcpConfig returns the worker's MCP value as a compact JSON string, or ""
// when unset or invalid (issue #425).
func (w Worker) mcpConfig() string {
	if w.validateMCP() != nil || len(bytes.TrimSpace(w.MCP)) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, w.MCP); err != nil {
		return ""
	}
	return buf.String()
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
// dispatch's permission layer: workers never write the index, a ref or the
// history — no commit, stash, reset, checkout, rebase or merge, and no add,
// rm, restore, tag, branch or other index/ref write — whatever the brief says.
// This deny list is the guarantee: the PATH git guard may never be reached
// (a shell snapshot can put the real git first, #423), and flywheel detects
// any write that slips through after each attempt. Denying `git branch` also
// denies `git branch --show-current`; workers use `git rev-parse
// --abbrev-ref HEAD`. Read-only git (status, diff, log, show) stays allowed.
var defaultDisallowedTools = []string{
	"Bash(git commit:*)", "Bash(git push:*)", "Bash(git stash:*)",
	"Bash(git reset:*)", "Bash(git checkout:*)", "Bash(git rebase:*)", "Bash(git merge:*)",
	"Bash(git add:*)", "Bash(git rm:*)", "Bash(git mv:*)", "Bash(git restore:*)",
	"Bash(git update-index:*)", "Bash(git apply:*)", "Bash(git tag:*)", "Bash(git branch:*)",
	"Bash(git switch:*)", "Bash(git cherry-pick:*)", "Bash(git revert:*)", "Bash(git am:*)",
	"Bash(git worktree:*)", "Bash(git clean:*)", "Bash(git notes:*)", "Bash(git replace:*)",
	"Bash(git update-ref:*)", "Bash(git gc:*)",
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

// Routing configures evidence-based model routing (issue #474): flywheel run
// scores each candidate by Objective from the ledger's per-model scoreboard
// and dispatches the best, exploring another candidate with probability
// Explore by a deterministic draw. The worker's own model need not be a
// candidate; with routing set, a fresh run without --model uses a candidate.
type Routing struct {
	Candidates  []string `json:"candidates"`
	Objective   string   `json:"objective"`              // cost_per_accepted, accepted_rate, gate_pass_rate or clean_rate
	Explore     float64  `json:"explore,omitempty"`      // 0..1, the chance of trying a non-best candidate
	MinAttempts int      `json:"min_attempts,omitempty"` // attempts before a candidate is scored; 0 means StatsMinSample
	Seed        string   `json:"seed,omitempty"`         // mixed into the draw
}

// routingObjectives lists the Routing.Objective values.
var routingObjectives = []string{"cost_per_accepted", "accepted_rate", "gate_pass_rate", "clean_rate"}

// Limits caps shared resource use across workers.
type Limits struct {
	PerHost       int      `json:"per_host,omitempty"`
	Budget        *Budget  `json:"budget,omitempty"`
	Breaker       *Breaker `json:"breaker,omitempty"`
	RatePerMinute int      `json:"rate_per_minute,omitempty"` // at most this many dispatches of one model in any 60 seconds; 0 means no limit
	// RateLimitRetries is how many times flywheel run resumes a worker cut
	// off by a provider rate limit; nil means 3, 0 disables (issue #380).
	RateLimitRetries *int `json:"rate_limit_retries,omitempty"`
	// RateLimitMaxWait is the longest flywheel run waits for a rate limit to
	// reset, a Go duration; "" means 5h (issue #380).
	RateLimitMaxWait string `json:"rate_limit_max_wait,omitempty"`
	// RateLimitPauseAt is the share of a rate-limit window (0..1) at which a
	// model is paused until the window resets, before the limit hits: 0 means
	// 0.95, a negative value disables it (issue #417).
	RateLimitPauseAt float64 `json:"rate_limit_pause_at,omitempty"`
	// LostAfter is how long an attempt with no live lease may sit idle (its
	// run file untouched, or no run file since dispatch) before it is marked
	// lost, a Go duration; "" means 24h (issue #402).
	LostAfter string `json:"lost_after,omitempty"`
	// QuietWait is how long a quiet gate waits for the host to go idle (and
	// an ordinary gate waits for a running quiet gate to end), a Go
	// duration; "" means 30m (issue #411).
	QuietWait string `json:"quiet_wait,omitempty"`
}

// QuietWaitDuration parses QuietWait ("" means 30 minutes).
func (l Limits) QuietWaitDuration() (time.Duration, error) {
	if l.QuietWait == "" {
		return 30 * time.Minute, nil
	}
	return time.ParseDuration(l.QuietWait)
}

// LostAfterDuration parses LostAfter ("" means 24 hours).
func (l Limits) LostAfterDuration() (time.Duration, error) {
	if l.LostAfter == "" {
		return 24 * time.Hour, nil
	}
	return time.ParseDuration(l.LostAfter)
}

// RateLimitRetryCount is RateLimitRetries, 3 when unset.
func (l Limits) RateLimitRetryCount() int {
	if l.RateLimitRetries == nil {
		return 3
	}
	return *l.RateLimitRetries
}

// RateLimitPauseThreshold is RateLimitPauseAt, 0.95 when unset and 0 (never
// pause) when negative.
func (l Limits) RateLimitPauseThreshold() float64 {
	switch {
	case l.RateLimitPauseAt == 0:
		return 0.95
	case l.RateLimitPauseAt < 0:
		return 0
	}
	return l.RateLimitPauseAt
}

// RateLimitMaxWaitDuration parses RateLimitMaxWait ("" means 5 hours).
func (l Limits) RateLimitMaxWaitDuration() (time.Duration, error) {
	if l.RateLimitMaxWait == "" {
		return 5 * time.Hour, nil
	}
	return time.ParseDuration(l.RateLimitMaxWait)
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
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			if name, uerr := strconv.Unquote(name); uerr == nil {
				if hint := nearestConfigKey(name); hint != "" {
					return Config{}, false, unknownKeyError{fmt.Sprintf("parse %s: unknown key %q; did you mean %q?", path, name, hint), err}
				}
			}
		}
		return Config{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, false, err
	}
	return c, true, nil
}

// unknownKeyError is a config parse error for an unknown key that names the
// nearest known key (issue #463); it unwraps to the decoder's error.
type unknownKeyError struct {
	msg string
	err error
}

func (e unknownKeyError) Error() string { return e.msg }
func (e unknownKeyError) Unwrap() error { return e.err }

// nearestConfigKey returns the json key Config accepts anywhere in its tree
// nearest to name (issue #463): an exact match once both are lowercased with
// '_' and '-' removed, else the smallest Levenshtein distance <= 2 on the
// lowercased names, else "".
func nearestConfigKey(name string) string {
	fold := func(s string) string {
		return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(s))
	}
	keys := configKeys(reflect.TypeOf(Config{}), map[reflect.Type]bool{}, map[string]bool{})
	for _, k := range keys {
		if fold(k) == fold(name) {
			return k
		}
	}
	best, bestD := "", 3
	for _, k := range keys {
		if d := levenshtein(strings.ToLower(name), strings.ToLower(k)); d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

// configKeys collects, sorted, every json tag name in t's struct tree,
// following pointers, slices and map values.
func configKeys(t reflect.Type, seen map[reflect.Type]bool, keys map[string]bool) []string {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && !seen[t] {
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag != "" && tag != "-" {
				keys[tag] = true
			}
			configKeys(f.Type, seen, keys)
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// levenshtein returns the edit distance between a and b, by rune.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
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
		if err := w.validateMCP(); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", where, err))
		}
		for j, f := range w.Fallbacks {
			switch {
			case f.Model == "":
				problems = append(problems, fmt.Sprintf("%s: fallbacks[%d] model must not be empty", where, j))
			case f.Model == w.Model:
				problems = append(problems, fmt.Sprintf("%s: fallback model %q must differ from the worker's model", where, f.Model))
			}
		}
		if r := w.Routing; r != nil {
			if len(r.Candidates) == 0 {
				problems = append(problems, where+": routing.candidates must not be empty")
			}
			seenCand := map[string]bool{}
			for j, m := range r.Candidates {
				switch {
				case m == "":
					problems = append(problems, fmt.Sprintf("%s: routing.candidates[%d] must not be empty", where, j))
				case seenCand[m]:
					problems = append(problems, fmt.Sprintf("%s: routing.candidates[%d] duplicates %q", where, j, m))
				}
				seenCand[m] = true
			}
			known := false
			for _, o := range routingObjectives {
				known = known || r.Objective == o
			}
			if !known {
				problems = append(problems, fmt.Sprintf("%s: routing.objective %q must be %s", where, r.Objective, strings.Join(quoted(routingObjectives), ", ")))
			}
			if r.Explore < 0 || r.Explore > 1 {
				problems = append(problems, fmt.Sprintf("%s: routing.explore %g must be between 0 and 1", where, r.Explore))
			}
			if r.MinAttempts < 0 {
				problems = append(problems, fmt.Sprintf("%s: routing.min_attempts %d must be >= 0", where, r.MinAttempts))
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
	if n := c.Limits.RateLimitRetryCount(); n < 0 {
		problems = append(problems, fmt.Sprintf("limits.rate_limit_retries %d must be >= 0", n))
	}
	if d, err := c.Limits.RateLimitMaxWaitDuration(); err != nil {
		problems = append(problems, fmt.Sprintf("limits.rate_limit_max_wait %q: %v", c.Limits.RateLimitMaxWait, err))
	} else if d <= 0 {
		problems = append(problems, fmt.Sprintf("limits.rate_limit_max_wait %q must be > 0", c.Limits.RateLimitMaxWait))
	}
	if p := c.Limits.RateLimitPauseAt; p > 1 {
		problems = append(problems, fmt.Sprintf("limits.rate_limit_pause_at %v must be <= 1", p))
	}
	if d, err := c.Limits.LostAfterDuration(); err != nil {
		problems = append(problems, fmt.Sprintf("limits.lost_after %q: %v", c.Limits.LostAfter, err))
	} else if d <= 0 {
		problems = append(problems, fmt.Sprintf("limits.lost_after %q must be > 0", c.Limits.LostAfter))
	}
	if d, err := c.Limits.QuietWaitDuration(); err != nil {
		problems = append(problems, fmt.Sprintf("limits.quiet_wait %q: %v", c.Limits.QuietWait, err))
	} else if d <= 0 {
		problems = append(problems, fmt.Sprintf("limits.quiet_wait %q must be > 0", c.Limits.QuietWait))
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
	if d, err := c.SetupTimeoutDuration(); err != nil {
		problems = append(problems, fmt.Sprintf("worktree.setup_timeout %q: %v", c.Worktree.SetupTimeout, err))
	} else if d <= 0 {
		problems = append(problems, fmt.Sprintf("worktree.setup_timeout %q must be > 0", c.Worktree.SetupTimeout))
	}
	for _, p := range c.WorktreeCarry() {
		if unsafeRelPath(p) {
			problems = append(problems, fmt.Sprintf("worktree.carry entry %q must be a non-empty repo-relative path (no absolute path, no .. element)", p))
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
			if role.Cfg != nil && role.Cfg.Independence != "" {
				if role.Cfg.Independence != "session" {
					problems = append(problems, fmt.Sprintf("staffing.%s: independence %q must be \"session\" or empty", role.Name, role.Cfg.Independence))
				}
				if role.Name != "auditor" {
					problems = append(problems, fmt.Sprintf("staffing.%s: independence applies only to the auditor", role.Name))
				}
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
				// independence "session" relies on flywheel audit's
				// fresh-session check instead (issue #463).
				if a.Independence != "session" && a.Adapter != "" && a.Model != "" && role.Cfg.Adapter == a.Adapter && role.Cfg.Model == a.Model {
					problems = append(problems, fmt.Sprintf("staffing.auditor: must not be the same agent and model as the %s (an audit is only independent when it is; set staffing.auditor.independence to \"session\" to rely on a fresh session instead)", role.Name))
				}
			}
		}
		// A review is independent only when the reviewer does not share a
		// session with the lead or the inspector (issue #389).
		if r := c.Staffing.Reviewer; r != nil && r.Session != "" {
			for _, role := range c.Staffing.roles() {
				if (role.Name == "lead" || role.Name == "inspector") && role.Cfg != nil && role.Cfg.Session == r.Session {
					problems = append(problems, fmt.Sprintf("staffing.reviewer: session %q also holds the %s role (a review is only independent when it is)", r.Session, role.Name))
				}
			}
		}
	}
	if c.Review != nil {
		seenPersona := map[string]bool{}
		for i, m := range c.Review.Panel {
			where := fmt.Sprintf("review.panel[%d]", i)
			switch {
			case !personaKnown(m.Persona):
				problems = append(problems, fmt.Sprintf("%s: persona %q must be one of %s", where, m.Persona, strings.Join(PanelPersonas(), ", ")))
			case seenPersona[m.Persona]:
				problems = append(problems, fmt.Sprintf("%s: duplicate persona %q", where, m.Persona))
			}
			seenPersona[m.Persona] = true
			if m.Worker != "" {
				if _, ok := c.Worker(m.Worker); !ok {
					problems = append(problems, fmt.Sprintf("%s: worker %q is not in workers[]", where, m.Worker))
				}
				if m.Adapter != "" {
					problems = append(problems, fmt.Sprintf("%s: set worker or adapter, not both", where))
				}
			}
			if m.Adapter != "" && (m.Adapter == "sim" || !adapterKnown(m.Adapter, false)) {
				problems = append(problems, fmt.Sprintf("%s: adapter %q must be \"claude\", \"opencode\" or \"codex\" (a review agent)", where, m.Adapter))
			}
		}
		for i, p := range c.Review.AllowedTools {
			if strings.TrimSpace(p) == "" {
				problems = append(problems, fmt.Sprintf("review.allowed_tools[%d]: empty pattern", i))
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
	case "limits.rate_limit_retries":
		return strconv.Itoa(c.Limits.RateLimitRetryCount()), nil
	case "limits.rate_limit_pause_at":
		if c.Limits.RateLimitPauseAt == 0 {
			return "0.95", nil
		}
		return strconv.FormatFloat(c.Limits.RateLimitPauseAt, 'g', -1, 64), nil
	case "limits.rate_limit_max_wait":
		if c.Limits.RateLimitMaxWait == "" {
			return "5h", nil
		}
		return c.Limits.RateLimitMaxWait, nil
	case "limits.lost_after":
		if c.Limits.LostAfter == "" {
			return "24h", nil
		}
		return c.Limits.LostAfter, nil
	case "limits.quiet_wait":
		if c.Limits.QuietWait == "" {
			return "30m", nil
		}
		return c.Limits.QuietWait, nil
	case "log.shards":
		if c.Log != nil && c.Log.Shards {
			return "true", nil
		}
		return "false", nil
	case "review.panel":
		return strings.Join(c.PanelDimensions(), ","), nil
	case "review.required":
		return strconv.FormatBool(c.ReviewRequired()), nil
	case "review.group_gates":
		return strings.Join(c.ReviewGroupGates(), groupGatesSep), nil
	case "review.allowed_tools":
		return strings.Join(c.ReviewAllowedTools(), groupGatesSep), nil
	case "worktree.setup":
		return c.SetupCommand(), nil
	case "worktree.setup_timeout":
		if c.Worktree == nil || c.Worktree.SetupTimeout == "" {
			return "10m", nil
		}
		return c.Worktree.SetupTimeout, nil
	case "worktree.strict_links":
		return strconv.FormatBool(c.StrictLinks()), nil
	case "worktree.carry":
		return strings.Join(c.WorktreeCarry(), ","), nil
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
		"feedback.upstream", "limits.lost_after", "limits.per_host", "limits.quiet_wait", "limits.rate_limit_max_wait", "limits.rate_limit_pause_at", "limits.rate_limit_retries",
		"log.shards", "max_parallel", "model", "review.allowed_tools", "review.group_gates", "review.panel", "review.required", "stall_timeout", "variant",
		"worktree.carry", "worktree.setup", "worktree.setup_timeout", "worktree.strict_links",
		"staffing.lead.adapter", "staffing.lead.model", "staffing.lead.session",
		"staffing.inspector.adapter", "staffing.inspector.model", "staffing.inspector.session",
		"staffing.auditor.adapter", "staffing.auditor.model", "staffing.auditor.session",
		"staffing.reviewer.adapter", "staffing.reviewer.model", "staffing.reviewer.session",
	}
	for _, l := range c.Lines {
		keys = append(keys, "lines."+l.Name+".wip")
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
// limits.per_host, limits.rate_limit_retries, limits.rate_limit_max_wait,
// limits.rate_limit_pause_at, limits.lost_after, limits.quiet_wait, review.panel
// (a comma-separated persona list), review.required (true or false) and
// review.group_gates (commands separated by ";;" or newlines) and
// review.allowed_tools (claude patterns, separated the same way; an empty
// entry is refused).
// Integer keys parse with strconv.Atoi. fallbacks is not
// settable here and directs the caller to edit .flywheel/config.json; a
// worker's routing block (issue #474) is edited there too and is no key here.
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
				"reviewer": &c.Staffing.Reviewer,
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
	case "limits.rate_limit_retries":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("limits.rate_limit_retries: value %q must be an integer", value)
		}
		c.Limits.RateLimitRetries = &n
		return nil
	case "limits.rate_limit_pause_at":
		p, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("limits.rate_limit_pause_at: value %q must be a number", value)
		}
		c.Limits.RateLimitPauseAt = p
		return nil
	case "limits.rate_limit_max_wait":
		c.Limits.RateLimitMaxWait = value
		return nil
	case "limits.lost_after":
		c.Limits.LostAfter = value
		return nil
	case "limits.quiet_wait":
		c.Limits.QuietWait = value
		return nil
	case "fallbacks", "fallbacks.all":
		return fmt.Errorf("%s: not settable; edit .flywheel/config.json", key)
	case "log.shards":
		return fmt.Errorf("log.shards is not settable; run flywheel log --shard (the sharded layout is one-way)")
	case "review.panel":
		// A comma-separated persona list; each member keeps the worker,
		// adapter and model it already had. Validation is WriteConfig's.
		had := map[string]PanelMember{}
		if c.Review != nil {
			for _, m := range c.Review.Panel {
				had[m.Persona] = m
			}
		}
		var panel []PanelMember
		for _, p := range strings.Split(value, ",") {
			if p = strings.TrimSpace(p); p == "" {
				continue
			}
			m, ok := had[p]
			if !ok {
				m = PanelMember{Persona: p}
			}
			panel = append(panel, m)
		}
		if len(panel) == 0 {
			return fmt.Errorf("review.panel: value %q names no persona; known: %s", value, strings.Join(PanelPersonas(), ", "))
		}
		if c.Review == nil {
			c.Review = &ReviewConfig{}
		}
		c.Review.Panel = panel
		return nil
	case "review.required":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("review.required: value %q must be true or false", value)
		}
		if c.Review == nil {
			c.Review = &ReviewConfig{}
		}
		c.Review.Required = b
		return nil
	case "review.group_gates":
		// Commands separated by ";;" or newlines; an empty value clears them.
		var gates []string
		for _, line := range strings.Split(value, "\n") {
			for _, g := range strings.Split(line, groupGatesSep) {
				if g = strings.TrimSpace(g); g != "" {
					gates = append(gates, g)
				}
			}
		}
		if c.Review == nil {
			c.Review = &ReviewConfig{}
		}
		c.Review.GroupGates = gates
		return nil
	case "review.allowed_tools":
		// Patterns separated by ";;" or newlines; an empty value clears them,
		// an empty entry among others is refused.
		var tools []string
		if value = strings.TrimSpace(value); value != "" {
			for _, line := range strings.Split(value, "\n") {
				for _, p := range strings.Split(line, groupGatesSep) {
					if p = strings.TrimSpace(p); p == "" {
						return fmt.Errorf("review.allowed_tools: value %q has an empty entry; separate patterns with %q or newlines", value, groupGatesSep)
					}
					tools = append(tools, p)
				}
			}
		}
		if c.Review == nil {
			c.Review = &ReviewConfig{}
		}
		c.Review.AllowedTools = tools
		return nil
	case "worktree.setup", "worktree.setup_timeout":
		if c.Worktree == nil {
			c.Worktree = &WorktreeConfig{}
		}
		if key == "worktree.setup" {
			c.Worktree.Setup = value
		} else {
			c.Worktree.SetupTimeout = value
		}
		return nil
	case "worktree.strict_links":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("worktree.strict_links: value %q must be true or false", value)
		}
		if c.Worktree == nil {
			c.Worktree = &WorktreeConfig{}
		}
		c.Worktree.StrictLinks = b
		return nil
	case "worktree.carry":
		var carry []string
		for _, p := range strings.Split(value, ",") {
			if p = strings.TrimSpace(p); p != "" {
				carry = append(carry, p)
			}
		}
		if c.Worktree == nil {
			c.Worktree = &WorktreeConfig{}
		}
		c.Worktree.Carry = carry
		return nil
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
		"adapter", "feedback.submit", "feedback.upstream", "limits.lost_after", "limits.per_host",
		"limits.quiet_wait", "limits.rate_limit_max_wait", "limits.rate_limit_pause_at", "limits.rate_limit_retries",
		"max_parallel", "model", "review.allowed_tools", "review.group_gates", "review.panel", "review.required", "stall_timeout", "variant",
		"worktree.carry", "worktree.setup", "worktree.setup_timeout", "worktree.strict_links",
		"staffing.lead.adapter", "staffing.lead.model", "staffing.lead.session",
		"staffing.inspector.adapter", "staffing.inspector.model", "staffing.inspector.session",
		"staffing.auditor.adapter", "staffing.auditor.model", "staffing.auditor.session",
		"staffing.reviewer.adapter", "staffing.reviewer.model", "staffing.reviewer.session",
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
