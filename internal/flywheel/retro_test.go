package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestRetroRecords: Retro refuses a task that never landed (rule
// retro-not-landed), errors naming a missing changed path, and records
// --changed as Paths and --none as empty Paths (issue #854).
func TestRetroRecords(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "skills", "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skills", "a", "SKILL.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if err := Retro(dir, RetroOptions{Task: "T1", Session: "lead", None: true, Note: "n"}); !errors.As(err, &rf) || rf.Rule != "retro-not-landed" {
		t.Fatalf("Retro() on a task not landed error = %v, want rule retro-not-landed", err)
	}
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "landed"}); err != nil {
		t.Fatal(err)
	}
	if err := Retro(dir, RetroOptions{Task: "T1", Session: "lead", Changed: []string{"skills/gone.md"}, Note: "n"}); err == nil ||
		errors.As(err, &rf) || !strings.Contains(err.Error(), "skills/gone.md") {
		t.Fatalf("Retro() with a missing path error = %v, want a plain error naming skills/gone.md", err)
	}
	for _, o := range []RetroOptions{
		{Task: "T1", Session: "lead", Note: "n"},
		{Task: "T1", Session: "lead", Note: "n", None: true, Changed: []string{"skills/a/SKILL.md"}},
		{Task: "T1", Session: "lead", Note: "n", Changed: []string{"../x"}},
	} {
		if err := Retro(dir, o); err == nil {
			t.Errorf("Retro(%+v) accepted, want an error", o)
		}
	}
	if err := Retro(dir, RetroOptions{Task: "T1", Session: "lead", Changed: []string{"skills/a/SKILL.md"}, Note: "tightened"}); err != nil {
		t.Fatalf("Retro() --changed error = %v", err)
	}
	if err := Retro(dir, RetroOptions{Task: "T1", Session: "lead", None: true, Note: "nothing to change"}); err != nil {
		t.Fatalf("Retro() --none error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var retros []Event
	for _, e := range events {
		if e.Kind == "retro" {
			retros = append(retros, e)
		}
	}
	if len(retros) != 2 || !slices.Equal(retros[0].Paths, []string{"skills/a/SKILL.md"}) || len(retros[1].Paths) != 0 ||
		retros[0].Session != "lead" || retros[1].Note != "nothing to change" {
		t.Fatalf("retro events = %+v, want one with the changed path and one with no paths", retros)
	}
}

// TestRetroDueSince: a landing before since is not due, one at or after it is
// due until a retro follows it, and a re-landing after the retro is due again
// (issue #854). land_corrected is not a landing.
func TestRetroDueSince(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ev := []Event{
		{TS: "2026-09-30T00:00:00Z", Task: "old", Kind: "landed"},
		{TS: "2026-10-01T00:00:00Z", Task: "B", Kind: "landed"},
		{TS: "2026-10-02T00:00:00Z", Task: "A", Kind: "landed"},
		{TS: "2026-10-02T00:00:01Z", Task: "old", Kind: "land_corrected"},
	}
	if got := RetroDue(ev, since); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("RetroDue() = %v, want [A B]", got)
	}
	ev = append(ev, Event{TS: "2026-10-03T00:00:00Z", Task: "A", Kind: "retro", Session: "lead", Note: "n"})
	if got := RetroDue(ev, since); !slices.Equal(got, []string{"B"}) {
		t.Fatalf("RetroDue() after A's retro = %v, want [B]", got)
	}
	ev = append(ev, Event{TS: "2026-10-04T00:00:00Z", Task: "A", Kind: "landed"})
	if got := RetroDue(ev, since); !slices.Equal(got, []string{"A", "B"}) {
		t.Fatalf("RetroDue() after A re-landed = %v, want [A B]", got)
	}
}

// TestRetroRunRefuses: with lint.retro_required_since set, run refuses while
// a landed unit has no retro (rule retro-missing, its Fix naming the unit and
// the command), and dispatches past the rule when the setting is "" (issue
// #854).
func TestRetroRunRefuses(t *testing.T) {
	t.Parallel()
	dir := refusedTask(t, "owns: a.go\nneeds: none\ngate: echo x\n\n# TASK x\n")
	if err := AppendEvent(dir, Event{Task: "T0", Kind: "landed"}); err != nil {
		t.Fatal(err)
	}
	cfg := simConfig(fixturePath("clean.jsonl", t))
	cfg.Lint = &LintConfig{RetroRequiredSince: "2026-01-01T00:00:00Z"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var rf *RuleRefusal
	if _, err := Run(dir, RunOptions{Task: "T1"}); !errors.As(err, &rf) || rf.Rule != "retro-missing" ||
		!strings.Contains(rf.Fix, "T0") || !strings.Contains(rf.Fix, "flywheel retro <task> --session S (--changed P,... | --none) --note TEXT") {
		t.Fatalf("Run() error = %v, want a retro-missing refusal naming T0 and the command", err)
	}
	wantRefusedAppended(t, dir, before, "T1", "retro-missing")
	cfg.Lint = &LintConfig{}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); errors.As(err, &rf) && rf.Rule == "retro-missing" {
		t.Fatalf("Run() with the setting off error = %v, want no retro-missing refusal", err)
	}
	after, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(after, func(e Event) bool { return e.Task == "T1" && e.Kind == "dispatched" }) {
		t.Fatal("Run() with the setting off did not dispatch T1")
	}
}

// TestRetroCorrectionOfDueTask: a correction of the unit that is due is not
// refused by retro-missing; any other dispatch still is (issue #854).
func TestRetroCorrectionOfDueTask(t *testing.T) {
	t.Parallel()
	cfg := Config{Lint: &LintConfig{RetroRequiredSince: "2026-01-01T00:00:00Z"}}
	ev := []Event{{TS: "2026-10-01T00:00:00Z", Task: "T1", Kind: "landed"}}
	if r := retroRefusal(cfg, ev, "T1", true); r != nil {
		t.Errorf("retroRefusal() for a correction of T1 = %v, want nil", r)
	}
	if r := retroRefusal(cfg, ev, "T1", false); r == nil || r.Rule != "retro-missing" {
		t.Errorf("retroRefusal() for a fresh T1 = %v, want retro-missing", r)
	}
	if r := retroRefusal(cfg, ev, "T2", true); r == nil || r.Rule != "retro-missing" {
		t.Errorf("retroRefusal() for a correction of T2 = %v, want retro-missing", r)
	}
}

// TestRetroConfigInvalid: a lint.retro_required_since that is not RFC 3339
// is a config problem naming the key (issue #854).
func TestRetroConfigInvalid(t *testing.T) {
	t.Parallel()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "claude", Model: "m"}}, Lint: &LintConfig{RetroRequiredSince: "yesterday"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "lint.retro_required_since") {
		t.Errorf("Validate() error = %v, want one naming lint.retro_required_since", err)
	}
	cfg.Lint.RetroRequiredSince = "2026-10-01T00:00:00Z"
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() rejected an RFC 3339 lint.retro_required_since: %v", err)
	}
}
