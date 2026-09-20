package flywheel

import (
	"strings"
	"testing"
)

func TestStaffingValidateAdapters(t *testing.T) {
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
		Staffing: &StaffingConfig{
			Lead: &RoleConfig{Adapter: "nope", Model: "m1"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid adapter")
	}
	if !strings.Contains(err.Error(), "staffing.lead") {
		t.Fatalf("error should mention staffing.lead: %v", err)
	}

	cfg.Staffing.Lead.Adapter = "cli"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cli adapter should be valid: %v", err)
	}
	cfg.Staffing.Lead.Adapter = "claude"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("claude adapter should be valid: %v", err)
	}
	cfg.Staffing.Lead.Adapter = "sim"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("sim adapter should be valid: %v", err)
	}
}

func TestStaffingValidateAuditorIndependence(t *testing.T) {
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
		Staffing: &StaffingConfig{
			Lead:    &RoleConfig{Adapter: "claude", Model: "m1"},
			Auditor: &RoleConfig{Adapter: "claude", Model: "m1"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when auditor and lead are the same")
	}
	if !strings.Contains(err.Error(), "independent") {
		t.Fatalf("error should mention independence: %v", err)
	}

	cfg.Staffing.Auditor.Model = "m2"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("different models should be independent: %v", err)
	}

	cfg.Staffing.Auditor.Model = "m1"
	cfg.Staffing.Inspector = &RoleConfig{Adapter: "claude", Model: "m1"}
	err = cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error when auditor and inspector are the same")
	}
	if !strings.Contains(err.Error(), "independent") {
		t.Fatalf("error should mention independence: %v", err)
	}
}

func TestStaffingValidateEmptyOK(t *testing.T) {
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("no staffing section should be valid: %v", err)
	}

	cfg.Staffing = &StaffingConfig{
		Lead: &RoleConfig{Session: "s1"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("role with only session should be valid: %v", err)
	}

	cfg.Staffing.Auditor = &RoleConfig{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("empty role should be valid: %v", err)
	}
}

func TestStaffingConfigGetSet(t *testing.T) {
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
	}

	keys := []string{
		"staffing.lead.adapter", "staffing.lead.model", "staffing.lead.session",
		"staffing.inspector.adapter", "staffing.inspector.model", "staffing.inspector.session",
		"staffing.auditor.adapter", "staffing.auditor.model", "staffing.auditor.session",
	}

	for _, key := range keys {
		val, err := cfg.Get(key)
		if err != nil {
			t.Fatalf("Get(%s): %v", key, err)
		}
		if val != "" {
			t.Fatalf("Get(%s): expected empty, got %q", key, val)
		}
	}

	if err := cfg.Set("staffing.lead.adapter", "claude"); err != nil {
		t.Fatalf("Set lead adapter: %v", err)
	}
	if v, _ := cfg.Get("staffing.lead.adapter"); v != "claude" {
		t.Fatalf("after Set lead adapter, Get returned %q", v)
	}

	if err := cfg.Set("staffing.inspector.model", "m2"); err != nil {
		t.Fatalf("Set inspector model: %v", err)
	}
	if v, _ := cfg.Get("staffing.inspector.model"); v != "m2" {
		t.Fatalf("after Set inspector model, Get returned %q", v)
	}

	if err := cfg.Set("staffing.auditor.session", "audit-session"); err != nil {
		t.Fatalf("Set auditor session: %v", err)
	}
	if v, _ := cfg.Get("staffing.auditor.session"); v != "audit-session" {
		t.Fatalf("after Set auditor session, Get returned %q", v)
	}

	for _, key := range keys {
		found := false
		for _, k := range cfg.validKeys() {
			if k == key {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("validKeys() missing %s", key)
		}
	}

	for _, key := range keys {
		found := false
		for _, k := range cfg.settableKeys() {
			if k == key {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("settableKeys() missing %s", key)
		}
	}
}

func TestStaffingLine(t *testing.T) {
	tests := []struct {
		name     string
		role     string
		cfg      *RoleConfig
		expected string
	}{
		{"nil role", "lead", nil, ""},
		{"empty role", "lead", &RoleConfig{}, ""},
		{"full role", "lead", &RoleConfig{Adapter: "claude", Model: "m1", Session: "s1"}, "lead: claude m1 (session s1)"},
		{"only adapter", "inspector", &RoleConfig{Adapter: "claude"}, "inspector: claude"},
		{"only model", "auditor", &RoleConfig{Model: "m2"}, "auditor: m2"},
		{"adapter and model", "lead", &RoleConfig{Adapter: "sim", Model: "m3"}, "lead: sim m3"},
		{"only session", "lead", &RoleConfig{Session: "s2"}, "lead: session s2"}, // #354 review: a role known only by its session still shows
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := StaffingLine(tc.role, tc.cfg)
			if got != tc.expected {
				t.Errorf("got %q, expected %q", got, tc.expected)
			}
		})
	}
}

func TestStaffingMismatchReportsDifferentSession(t *testing.T) {
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
		Staffing: &StaffingConfig{
			Lead: &RoleConfig{Session: "lead-a"},
		},
	}
	events := []Event{
		{Kind: "staffed", Persona: "lead", Session: "lead-b", Model: "m1"},
	}
	mismatches := StaffingMismatch(cfg, events)
	if len(mismatches) != 1 {
		t.Fatalf("expected 1 mismatch, got %d", len(mismatches))
	}
	if !strings.Contains(mismatches[0], "lead-a") || !strings.Contains(mismatches[0], "lead-b") {
		t.Fatalf("mismatch should name both sessions: %s", mismatches[0])
	}

	events = []Event{
		{Kind: "staffed", Persona: "lead", Session: "lead-a", Model: "m1"},
	}
	mismatches = StaffingMismatch(cfg, events)
	if len(mismatches) != 0 {
		t.Fatalf("matching sessions should produce no mismatch, got %v", mismatches)
	}

	cfg.Staffing.Lead.Session = ""
	events = []Event{
		{Kind: "staffed", Persona: "lead", Session: "anything", Model: "m1"},
	}
	mismatches = StaffingMismatch(cfg, events)
	if len(mismatches) != 0 {
		t.Fatalf("unconfigured session should produce no mismatch, got %v", mismatches)
	}
}

func TestStaffingSummaryShowsRoles(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
		Staffing: &StaffingConfig{
			Lead:    &RoleConfig{Adapter: "claude", Model: "m1"},
			Auditor: &RoleConfig{Adapter: "claude", Model: "m2"},
		},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	summary, err := FactorySummary(dir)
	if err != nil {
		t.Fatalf("FactorySummary: %v", err)
	}
	if !strings.Contains(summary, "lead: claude m1") {
		t.Fatalf("summary missing lead line: %s", summary)
	}
	if !strings.Contains(summary, "auditor: claude m2") {
		t.Fatalf("summary missing auditor line: %s", summary)
	}
}

func TestStaffingSummaryUnchangedWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	summary, err := FactorySummary(dir)
	if err != nil {
		t.Fatalf("FactorySummary: %v", err)
	}
	if strings.Contains(summary, "lead:") {
		t.Fatalf("summary should not add lead line when not configured: %s", summary)
	}
}

// TestStaffingUnknownKeyIsAnError checks that a staffing key nobody defines
// is reported as unknown instead of reading back as empty.
func TestStaffingUnknownKeyIsAnError(t *testing.T) {
	cfg := Config{Version: 1, Workers: []Worker{{Name: "default", Adapter: "sim", Model: "m", MaxParallel: 1}}}
	for _, key := range []string{"staffing.lead.agent", "staffing.foreman.model", "staffing.lead"} {
		if got, err := cfg.Get(key); err == nil {
			t.Errorf("Get(%q) = %q, nil; want an unknown-key error", key, got)
		}
		if err := cfg.Set(key, "x"); err == nil {
			t.Errorf("Set(%q) succeeded; want an unknown-key error", key)
		}
	}
	if got, err := cfg.Get("staffing.lead.model"); err != nil || got != "" {
		t.Errorf("Get(staffing.lead.model) on an unstaffed config = %q, %v; want \"\", nil", got, err)
	}
}

// TestStaffingAuditorSessionIndependence checks that one session cannot hold
// the auditor role and the lead or inspector role, whatever the models are
// (#354 review).
func TestStaffingAuditorSessionIndependence(t *testing.T) {
	base := func(s *StaffingConfig) Config {
		return Config{Version: 1, Workers: []Worker{{Name: "default", Adapter: "sim", Model: "m", MaxParallel: 1}}, Staffing: s}
	}
	same := base(&StaffingConfig{
		Lead:    &RoleConfig{Adapter: "claude", Model: "m1", Session: "s1"},
		Auditor: &RoleConfig{Adapter: "claude", Model: "m2", Session: "s1"},
	})
	err := same.Validate()
	if err == nil || !strings.Contains(err.Error(), "also holds the lead role") {
		t.Errorf("Validate() = %v, want a refusal naming the shared session", err)
	}
	apart := base(&StaffingConfig{
		Lead:      &RoleConfig{Adapter: "claude", Model: "m1", Session: "s1"},
		Inspector: &RoleConfig{Adapter: "cli", Session: "s2"},
		Auditor:   &RoleConfig{Adapter: "codex", Model: "m2", Session: "s3"},
	})
	if err := apart.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for three separate sessions", err)
	}
}

// TestStaffingSetLeavesNothingBehindOnError checks that a rejected Set does
// not create an empty staffing section (#354 review).
func TestStaffingSetLeavesNothingBehindOnError(t *testing.T) {
	cfg := Config{Version: 1, Workers: []Worker{{Name: "default", Adapter: "sim", Model: "m", MaxParallel: 1}}}
	if err := cfg.Set("staffing.lead.agent", "claude"); err == nil {
		t.Fatal("Set(staffing.lead.agent) succeeded, want an unknown-key error")
	}
	if cfg.Staffing != nil {
		t.Errorf("Staffing = %+v after a rejected Set, want nil", cfg.Staffing)
	}
}
