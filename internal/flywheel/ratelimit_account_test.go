package flywheel

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	acctNow   = time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	acctReset = acctNow.Add(2 * time.Hour)
)

// acctLog is an Opus unit dispatched on the claude adapter whose finish
// reported util of window, then any extra events.
func acctLog(util float64, window string, extra ...Event) []Event {
	return append([]Event{
		{Kind: "dispatched", Task: "T1", Model: "opus", Adapter: "claude"},
		{Kind: "finished", Task: "T1", Model: "opus", Reason: "stop", LimitUtilization: util, LimitWindow: window, LimitResetAt: acctReset.Format(time.RFC3339)},
	}, extra...)
}

func TestAccountWideSevenDayPausesSameAdapter(t *testing.T) {
	t.Parallel()
	events := acctLog(0.96, "seven_day", Event{Kind: "dispatched", Task: "T2", Model: "sonnet", Adapter: "claude"})
	p, ok := rateLimitPausedOn(events, "sonnet", "claude", acctNow, 0.95, nil)
	if !ok || p.Model != "opus" || !p.Until.Equal(acctReset) {
		t.Fatalf("sonnet pause = %+v, %v; want paused by opus until %v", p, ok, acctReset)
	}
	if got, want := p.reason(), " (opus reported 96% of the seven_day window, account-wide)"; got != want {
		t.Errorf("reason() = %q, want %q", got, want)
	}
	if models, _ := pausedModels(events, acctNow, 0.95); !slices.Equal(models, []string{"opus", "sonnet"}) {
		t.Errorf("pausedModels = %v, want [opus sonnet]", models)
	}
}

func TestAccountWideModelScopedWindows(t *testing.T) {
	t.Parallel()
	for _, w := range []string{"seven_day_opus", "seven_day_sonnet", "unknown", ""} {
		events := acctLog(0.96, w)
		if p, ok := rateLimitPausedOn(events, "sonnet", "claude", acctNow, 0.95, nil); ok {
			t.Errorf("%q: sonnet paused %+v, want only opus", w, p)
		}
		if _, ok := rateLimitPausedOn(events, "opus", "claude", acctNow, 0.95, nil); !ok {
			t.Errorf("%q: opus not paused by its own window", w)
		}
	}
}

// TestAccountWideTableEntries: every account-wide window pauses a model on the
// same adapter, one subtest per entry.
func TestAccountWideTableEntries(t *testing.T) {
	t.Parallel()
	if len(accountWideWindows) != 2 || !accountWideWindows["five_hour"] || !accountWideWindows["seven_day"] {
		t.Fatalf("accountWideWindows = %v, want five_hour and seven_day", accountWideWindows)
	}
	for w := range accountWideWindows {
		t.Run(w, func(t *testing.T) {
			t.Parallel()
			if _, ok := rateLimitPausedOn(acctLog(0.97, w), "sonnet", "claude", acctNow, 0.95, nil); !ok {
				t.Errorf("%s: sonnet not paused", w)
			}
		})
	}
}

func TestAccountWideReleases(t *testing.T) {
	t.Parallel()
	if _, ok := rateLimitPausedOn(acctLog(0.96, "five_hour"), "sonnet", "opencode", acctNow, 0.95, nil); ok {
		t.Errorf("a model on another adapter is paused")
	}
	stop := acctLog(0.96, "seven_day", Event{Kind: "dispatched", Task: "T2", Model: "sonnet", Adapter: "claude"},
		Event{Kind: "finished", Task: "T2", Model: "sonnet", Reason: "stop", LimitUtilization: 0.4, LimitWindow: "seven_day"})
	for _, m := range []string{"opus", "sonnet"} {
		if p, ok := rateLimitPausedOn(stop, m, "claude", acctNow, 0.95, nil); ok {
			t.Errorf("%s after sonnet's clean stop: paused %+v, want released", m, p)
		}
	}
	if _, ok := rateLimitPausedOn(acctLog(0.96, "seven_day"), "sonnet", "claude", acctReset.Add(time.Second), 0.95, nil); ok {
		t.Errorf("sonnet paused after the reset passed")
	}
	hit := acctLog(0, "seven_day")
	hit[1].Reason, hit[1].ResetAt = "rate-limited", acctReset.Format(time.RFC3339)
	if p, ok := rateLimitPausedOn(hit, "sonnet", "claude", acctNow, 0.95, nil); !ok || !strings.Contains(p.reason(), "opus hit the seven_day window, account-wide") {
		t.Errorf("hit seven_day limit: sonnet pause = %+v, %v (%q)", p, ok, p.reason())
	}
	hit[1].LimitWindow = ""
	if _, ok := rateLimitPausedOn(hit, "sonnet", "claude", acctNow, 0.95, nil); ok {
		t.Errorf("hit limit with no window paused sonnet")
	}
}

// TestAccountWideFallbackFromConfig: a fallback with no events takes its
// adapter from the config, so TokensExhausted freezes on the opus report.
func TestAccountWideFallbackFromConfig(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "opus", Fallbacks: []Fallback{{Model: "sonnet", Approved: true}}}}}
	events := acctLog(0.96, "seven_day")
	if p, ok := rateLimitPausedFor(events, "sonnet", acctNow, 0.95, configAdapter(cfg)); !ok || p.Model != "opus" {
		t.Errorf("fallback sonnet pause = %+v, %v; want paused by opus", p, ok)
	}
	if ex, until, models := TokensExhausted(events, cfg, acctNow); !ex || !until.Equal(acctReset) || !slices.Equal(models, []string{"opus", "sonnet"}) {
		t.Errorf("TokensExhausted = %v %v %v, want true %v [opus sonnet]", ex, until, models, acctReset)
	}
}

// TestAccountWideRunRefusesModelOverride: run --model sonnet on the adapter
// opus reported 96% of seven_day on is refused with rule rate-limit.
func TestAccountWideRunRefusesModelOverride(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(noPlanFixture(t, 5, false))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	reset := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for _, e := range []Event{
		{Task: "T2", Kind: "dispatched", Attempt: "r1", Model: "opus", Adapter: "sim"},
		{Task: "T2", Kind: "finished", Attempt: "r1", Model: "opus", Reason: "stop", LimitUtilization: 0.96, LimitWindow: "seven_day", LimitResetAt: reset.Format(time.RFC3339)},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	_, err := Run(dir, RunOptions{Task: "T1", Model: "sonnet"})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "rate-limit" || !strings.Contains(rf.Fix, "sonnet is paused") || !strings.Contains(rf.Fix, "opus reported 96% of the seven_day window, account-wide") {
		t.Fatalf("Run(--model sonnet) error = %v, want a rate-limit refusal naming opus", err)
	}
}
