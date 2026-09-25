package flywheel

import (
	"errors"
	"strings"
	"testing"
)

func TestT7UnauditedLineRefused(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
	}
	r := T7Refusal(events, "T1")
	if r == nil {
		t.Fatal("T7Refusal() returned nil, want refusal")
	}
	if r.Rule != "T7" {
		t.Errorf("Rule = %q, want T7", r.Rule)
	}
	if !containsSubstring(r.Fix, "claude/m1") {
		t.Errorf("Fix does not contain 'claude/m1': %s", r.Fix)
	}
	if !containsSubstring(r.Fix, "first-article") {
		t.Errorf("Fix does not contain 'first-article': %s", r.Fix)
	}
}

func TestT7ConformingAuditClearsLine(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-18T10:03:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:04:00Z", Task: "T2", Kind: "dispatched", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:05:00Z", Task: "T2", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-18T10:06:00Z", Task: "T2", Kind: "audited", Verdict: "conforms", Session: "aud-1"},
	}
	r := T7Refusal(events, "T1")
	if r != nil {
		t.Errorf("T7Refusal() returned refusal, want nil: %v", r)
	}
}

func TestT7OpenNonconformanceStops(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-18T10:03:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:04:00Z", Task: "T2", Kind: "dispatched", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:05:00Z", Task: "T2", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-18T10:06:00Z", Task: "T2", Kind: "audited", Verdict: "conforms", Session: "aud-1"},
		{TS: "2026-09-18T10:07:00Z", Task: "T3", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:08:00Z", Task: "T3", Kind: "dispatched", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:09:00Z", Task: "T3", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-18T10:10:00Z", Task: "T3", Kind: "audited", Verdict: "nonconformance", Session: "aud-1"},
	}
	r := T7Refusal(events, "T1")
	if r == nil {
		t.Fatal("T7Refusal() returned nil, want refusal")
	}
	if r.Rule != "T7" {
		t.Errorf("Rule = %q, want T7", r.Rule)
	}
	if !containsSubstring(r.Fix, "nonconformance") {
		t.Errorf("Fix does not contain 'nonconformance': %s", r.Fix)
	}
	if !containsSubstring(r.Fix, "T3") {
		t.Errorf("Fix does not contain 'T3': %s", r.Fix)
	}
}

func TestT7RetryAuditStaysOnItsLine(t *testing.T) {
	t.Parallel()
	events := []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-18T10:03:00Z", Task: "T1", Kind: "audited", Verdict: "conforms", Session: "aud-1"},
		{TS: "2026-09-18T10:04:00Z", Task: "T1", Kind: "dispatched", Adapter: "claude", Model: "m2"},
		{TS: "2026-09-18T10:05:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
	}
	r := T7Refusal(events, "T1")
	if r == nil {
		t.Fatal("T7Refusal() returned nil, want refusal")
	}
	if r.Rule != "T7" {
		t.Errorf("Rule = %q, want T7", r.Rule)
	}
	if !containsSubstring(r.Fix, "claude/m2") {
		t.Errorf("Fix does not contain 'claude/m2': %s", r.Fix)
	}
}

func TestT7LandRefusedWhenEnabled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "m1"}},
		Audit:   &AuditPolicy{FirstArticle: true},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Adapter: "claude", Model: "m1"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	err := LandTask(dir, "T1", "abc1234", "", false, "")
	if err == nil {
		t.Fatal("LandTask() succeeded, want refusal")
	}
	var r *RuleRefusal
	if !errors.As(err, &r) {
		t.Fatalf("LandTask() returned non-RuleRefusal error: %v", err)
	}
	if r.Rule != "T7" {
		t.Errorf("Rule = %q, want T7", r.Rule)
	}

	// Same events without Audit config should succeed
	if err := WriteConfig(dir, Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "m1"}},
	}); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if err := LandTask(dir, "T1", "abc1234", "", false, ""); err != nil {
		t.Fatalf("LandTask() error = %v", err)
	}
}

func TestT7ExceptionBypasses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "m1"}},
		Audit:   &AuditPolicy{FirstArticle: true},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Adapter: "claude", Model: "m1"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	err := LandTaskWithException(dir, "T1", "abc1234", "", false, "", "evidence", "lead-2", "")
	if err != nil {
		t.Fatalf("LandTaskWithException() error = %v", err)
	}
}

func containsSubstring(s, substr string) bool {
	for i := 0; i < len(s); i++ {
		if i+len(substr) <= len(s) && s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestT7LaterConformingAuditReopensLine checks that a line stopped by a
// nonconformance is reopened by a later conforming audit on that line.
func TestT7LaterConformingAuditReopensLine(t *testing.T) {
	t.Parallel()
	events := []Event{
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T1", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{Task: "T2", Kind: "inspected", Verdict: "pass"},
		{Task: "T2", Kind: "audited", Verdict: "nonconformance"},
		{Task: "T2", Kind: "audited", Verdict: "conforms"},
	}
	if r := T7Refusal(events, "T1"); r != nil {
		t.Errorf("T7Refusal = %v, want nil (the latest audit on the line conforms)", r)
	}
}

// TestT7ExceptionCoversPassedUnit checks that a lead exception can land a
// passed unit T7 refuses, recorded as such, while an exception on a passed
// unit nothing refuses is still rejected (#317 review).
func TestT7ExceptionCoversPassedUnit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	enabled := Config{Version: 1, Workers: []Worker{{Name: "default", Adapter: "claude", Model: "m1"}}, Audit: &AuditPolicy{FirstArticle: true}}
	if err := WriteConfig(dir, enabled); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	for _, e := range []Event{
		{TS: "2026-09-18T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Adapter: "claude", Model: "m1"},
		{TS: "2026-09-18T10:02:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
		{TS: "2026-09-18T10:03:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-18T10:04:00Z", Task: "T2", Kind: "inspected", Verdict: "pass", Session: "lead-1"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	if err := LandTaskWithException(dir, "T1", "abc1234", "", false, "", "audited by hand", "lead-2", ""); err != nil {
		t.Fatalf("exception landing of a T7-refused passed unit: %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "excepted" && strings.Contains(e.Reason, "T7") {
			found = true
		}
	}
	if !found {
		t.Error("no excepted event naming T7 for T1")
	}
	// T2 has no dispatch, so no line and no T7: an exception is unnecessary.
	err = LandTaskWithException(dir, "T2", "abc1234", "", false, "", "evidence", "lead-2", "")
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T5" {
		t.Errorf("exception on a passed unit T7 does not refuse = %v, want T5 refusal", err)
	}
}
