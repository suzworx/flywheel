package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// appendHandoffEvents records the given events, each with the pinned TS the
// fixture sets so derivation is deterministic.
func appendHandoffEvents(t *testing.T, dir string, events []Event) {
	t.Helper()
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
}

func TestHandoffFixtureDerivesSections(t *testing.T) {
	dir := t.TempDir()
	appendHandoffEvents(t, dir, []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "t1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "t1", Kind: "dispatched", Attempt: "r1", Model: "m1"},
		{TS: "2026-09-14T10:02:00Z", Task: "t1", Kind: "started", Attempt: "r1", Model: "m1", Session: "ses_1"},
		{TS: "2026-09-14T10:00:00Z", Task: "t2", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "t2", Kind: "blocked"},
		{TS: "2026-09-14T10:00:00Z", Task: "t3", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:00:00Z", Task: "t4", Kind: "planned", Brief: "b.txt", Needs: []string{"t5"}},
		{TS: "2026-09-14T10:01:00Z", Task: "t5", Kind: "landed"},
	})
	h, err := HandoffSummary(dir)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if len(h.InFlight) != 1 {
		t.Fatalf("in-flight = %+v, want 1 task", h.InFlight)
	}
	if h.InFlight[0].ID != "t1" || h.InFlight[0].Status != "running" ||
		h.InFlight[0].Session != "ses_1" || h.InFlight[0].Model != "m1" {
		t.Errorf("in-flight task = %+v, want t1 running, session ses_1, model m1", h.InFlight[0])
	}
	if want := []string{"t2"}; !slices.Equal(h.Blocked, want) {
		t.Errorf("blocked = %q, want %q", h.Blocked, want)
	}
	if want := []string{"t3", "t4"}; !slices.Equal(h.Ready, want) {
		t.Errorf("ready = %q, want %q", h.Ready, want)
	}
	wantText := "in-flight: t1 (running, session ses_1, model m1)\n" +
		"blocked: t2\n" +
		"ready: t3 t4\n" +
		"untriaged signals: none\n" +
		"model: openrouter/deepseek/deepseek-v4-flash-0731"
	if got := HandoffText(h); got != wantText {
		t.Errorf("HandoffText() =\n%q\nwant\n%q", got, wantText)
	}
}

func TestHandoffUnmetNeedKeepsTaskOutOfReady(t *testing.T) {
	dir := t.TempDir()
	appendHandoffEvents(t, dir, []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "t1", Kind: "planned", Needs: []string{"t2"}},
		{TS: "2026-09-14T10:00:00Z", Task: "t2", Kind: "finished"},
	})
	h, err := HandoffSummary(dir)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if len(h.Ready) != 0 {
		t.Errorf("ready = %q, want [] (t1's need t2 never landed or passed)", h.Ready)
	}

	// A passed task satisfies the need.
	dir2 := t.TempDir()
	appendHandoffEvents(t, dir2, []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "t1", Kind: "planned", Needs: []string{"t2"}},
		{TS: "2026-09-14T10:00:00Z", Task: "t2", Kind: "inspected", Verdict: "pass"},
	})
	h2, err := HandoffSummary(dir2)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if want := []string{"t1"}; !slices.Equal(h2.Ready, want) {
		t.Errorf("ready = %q, want %q (a passed task satisfies the need)", h2.Ready, want)
	}
}

func TestHandoffEmptyLogPrintsNoneSections(t *testing.T) {
	dir := t.TempDir()
	h, err := HandoffSummary(dir)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if len(h.InFlight) != 0 || len(h.Blocked) != 0 || len(h.Ready) != 0 {
		t.Errorf("empty log handoff = %+v, want empty lists", h)
	}
	if h.Model != "openrouter/deepseek/deepseek-v4-flash-0731" {
		t.Errorf("model = %q, want the built-in default worker model", h.Model)
	}
	text := HandoffText(h)
	for _, want := range []string{"in-flight: none", "blocked: none", "ready: none", "untriaged signals: none"} {
		if !strings.Contains(text, want) {
			t.Errorf("HandoffText() missing %q:\n%q", want, text)
		}
	}
	if !strings.HasSuffix(text, "model: openrouter/deepseek/deepseek-v4-flash-0731") {
		t.Errorf("HandoffText() = %q, want it to end with the config model line", text)
	}
}

func TestHandoffModelComesFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Workers[0].Model = "openrouter/other/flash-1"
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	appendHandoffEvents(t, dir, []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "t1", Kind: "planned"},
	})
	h, err := HandoffSummary(dir)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if h.Model != "openrouter/other/flash-1" {
		t.Errorf("model = %q, want the configured default worker model", h.Model)
	}
	if !strings.Contains(HandoffText(h), "model: openrouter/other/flash-1") {
		t.Errorf("HandoffText() missing the configured model:\n%q", HandoffText(h))
	}
}

func TestHandoffBlockReplacedOnSecondRun(t *testing.T) {
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "flywheel.md")
	md := "before\n<!-- flywheel:handoff:start -->\nOLD SUMMARY\n<!-- flywheel:handoff:end -->\nafter\n"
	if err := os.WriteFile(mdPath, []byte(md), 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}
	appendHandoffEvents(t, dir, []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "t1", Kind: "started", Session: "ses_1", Model: "m1"},
	})
	h, err := HandoffSummary(dir)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if err := WriteHandoff(dir, h); err != nil {
		t.Fatalf("WriteHandoff() error = %v", err)
	}
	b, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	content := string(b)
	if !strings.HasPrefix(content, "before\n<!-- flywheel:handoff:start -->\n") {
		t.Errorf("text before the marked block changed:\n%q", content)
	}
	if !strings.HasSuffix(content, "\n<!-- flywheel:handoff:end -->\nafter\n") {
		t.Errorf("text after the marked block changed:\n%q", content)
	}
	if strings.Contains(content, "OLD SUMMARY") {
		t.Error("old handoff body was not replaced")
	}
	if !strings.Contains(content, "in-flight: t1 (running, session ses_1, model m1)") {
		t.Errorf("handoff block missing the in-flight line:\n%q", content)
	}

	// Same input gives byte-identical output on the second run.
	if err := WriteHandoff(dir, h); err != nil {
		t.Fatalf("WriteHandoff() second run error = %v", err)
	}
	b2, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("re-read flywheel.md: %v", err)
	}
	if string(b2) != string(b) {
		t.Error("WriteHandoff() output changed between runs")
	}
}

func TestHandoffCarriesUntriagedSignals(t *testing.T) {
	dir := t.TempDir()
	appendHandoffEvents(t, dir, []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "signal", Attempt: "r1", Signal: "no-plan"},
	})
	h, err := HandoffSummary(dir)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if len(h.Untriaged) != 1 {
		t.Fatalf("Untriaged = %+v, want 1 signal", h.Untriaged)
	}
	if h.Untriaged[0].Task != "T1" || h.Untriaged[0].Attempt != "r1" || h.Untriaged[0].Signal != "no-plan" {
		t.Errorf("Untriaged[0] = %+v, want Task T1, Attempt r1, Signal no-plan", h.Untriaged[0])
	}
	text := HandoffText(h)
	if !strings.Contains(text, "untriaged signals: T1 r1 no-plan") {
		t.Errorf("HandoffText() missing untriaged signal entry:\n%q", text)
	}
}

func TestHandoffUntriagedNoneWhenTriaged(t *testing.T) {
	dir := t.TempDir()
	appendHandoffEvents(t, dir, []Event{
		{TS: "2026-09-14T10:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"},
		{TS: "2026-09-14T10:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1"},
		{TS: "2026-09-14T10:02:00Z", Task: "T1", Kind: "signal", Attempt: "r1", Signal: "no-plan"},
		{TS: "2026-09-14T10:03:00Z", Task: "T1", Kind: "learning", Severity: "P2", Title: "t", Observed: "o", Evidence: "e", Ask: "a", Signals: []string{"no-plan"}},
	})
	h, err := HandoffSummary(dir)
	if err != nil {
		t.Fatalf("HandoffSummary() error = %v", err)
	}
	if len(h.Untriaged) != 0 {
		t.Errorf("Untriaged = %+v, want empty after triaging", h.Untriaged)
	}
	text := HandoffText(h)
	if !strings.Contains(text, "untriaged signals: none") {
		t.Errorf("HandoffText() missing 'untriaged signals: none':\n%q", text)
	}
}
