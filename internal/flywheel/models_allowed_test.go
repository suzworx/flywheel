package flywheel

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// dispatchedCount returns how many dispatched events dir's ledger holds.
func dispatchedCount(t *testing.T, dir string) int {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "dispatched" {
			n++
		}
	}
	return n
}

// TestModelsAllowedValidate checks Validate refuses a model, a fallback, a
// routing candidate and an empty entry outside models_allowed, one problem
// each, and accepts a listed model or no list at all (issue #746).
func TestModelsAllowedValidate(t *testing.T) {
	t.Parallel()
	w := Worker{Name: "w", Adapter: "claude", Model: "claude-haiku-4-5-20251001",
		ModelsAllowed: []string{"claude-opus-5-5", " "},
		Fallbacks:     []Fallback{{Model: "claude-sonnet-5", Approved: true}},
		Routing:       &Routing{Candidates: []string{"claude-opus-5-5", "claude-fable-5-1"}, Objective: routingObjectives[0]},
	}
	err := Config{Version: 1, Workers: []Worker{w}}.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want models_allowed problems")
	}
	for _, want := range []string{
		`workers[0]: worker "w": model "claude-haiku-4-5-20251001" is not in models_allowed [claude-opus-5-5,  ]`,
		`workers[0]: worker "w": fallbacks[0] model "claude-sonnet-5" is not in models_allowed`,
		`workers[0]: worker "w": routing.candidates[1] "claude-fable-5-1" is not in models_allowed`,
		`workers[0]: worker "w": models_allowed[1] must not be empty`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Validate() error = %v\nwant substring %q", err, want)
		}
	}
	if strings.Contains(err.Error(), `routing.candidates[0] "claude-opus-5-5"`) {
		t.Errorf("Validate() error = %v, refuses a listed candidate", err)
	}

	ok := Worker{Name: "w", Adapter: "claude", Model: "claude-opus-5-5", ModelsAllowed: []string{" claude-opus-5-5 "}}
	if err := (Config{Version: 1, Workers: []Worker{ok}}).Validate(); err != nil {
		t.Errorf("Validate() listed model error = %v, want nil", err)
	}
	ok.ModelsAllowed = nil
	ok.Model = "claude-haiku-4-5-20251001"
	if err := (Config{Version: 1, Workers: []Worker{ok}}).Validate(); err != nil {
		t.Errorf("Validate() no list error = %v, want nil", err)
	}
}

// TestModelsAllowedConfigSet checks workers.<name>.models_allowed sets a
// comma-separated list, Get reads it back, and an empty value clears it.
func TestModelsAllowedConfigSet(t *testing.T) {
	t.Parallel()
	c := Config{Version: 1, Workers: []Worker{{Name: "w", Adapter: "sim", Model: "a"}}}
	if err := c.Set("workers.w.models_allowed", "a, b"); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if want := []string{"a", "b"}; !reflect.DeepEqual(c.Workers[0].ModelsAllowed, want) {
		t.Errorf("ModelsAllowed = %q, want %q", c.Workers[0].ModelsAllowed, want)
	}
	if v, err := c.Get("workers.w.models_allowed"); err != nil || v != "a,b" {
		t.Errorf("Get() = %q, %v, want \"a,b\"", v, err)
	}
	if err := c.Set("workers.w.models_allowed", ""); err != nil {
		t.Fatalf("Set(\"\") error = %v", err)
	}
	if c.Workers[0].ModelsAllowed != nil {
		t.Errorf("ModelsAllowed = %q after an empty Set, want nil", c.Workers[0].ModelsAllowed)
	}
}

// TestModelsAllowedRunRefusesOffList checks --model outside the list is a
// models-allowed RuleRefusal naming model, worker and list, with no
// dispatched event; --model on the list dispatches.
func TestModelsAllowedRunRefusesOffList(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	clean, other := fixturePath("clean.jsonl", t), fixturePath("longcost.jsonl", t)
	cfg := simConfig(clean)
	cfg.Workers[0].ModelsAllowed = []string{clean}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T1", Model: other})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "models-allowed" {
		t.Fatalf("Run() error = %v, want a RuleRefusal models-allowed", err)
	}
	want := "model " + other + ` is not in worker "sim"'s models_allowed [` + clean + "]; dispatch with --model <one of them> or edit models_allowed"
	if r.Fix != want {
		t.Errorf("Fix = %q, want %q", r.Fix, want)
	}
	if n := dispatchedCount(t, dir); n != 0 {
		t.Fatalf("dispatched events = %d after a refusal, want 0", n)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Model: clean}); err != nil {
		t.Fatalf("Run(--model listed) error = %v", err)
	}
	if n := dispatchedCount(t, dir); n != 1 {
		t.Errorf("dispatched events = %d, want 1", n)
	}
}

// TestModelsAllowedRunDefaultModel checks the worker's model on the list
// dispatches with no --model.
func TestModelsAllowedRunDefaultModel(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	clean := fixturePath("clean.jsonl", t)
	cfg := simConfig(clean)
	cfg.Workers[0].ModelsAllowed = []string{fixturePath("longcost.jsonl", t), clean}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if n := dispatchedCount(t, dir); n != 1 {
		t.Errorf("dispatched events = %d, want 1", n)
	}
}

// TestModelsAllowedRunNoListAnyModel checks a worker with no list dispatches
// any --model, as before issue #746.
func TestModelsAllowedRunNoListAnyModel(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	other := fixturePath("longcost.jsonl", t)
	if _, err := Run(dir, RunOptions{Task: "T1", Model: other}); err != nil {
		t.Fatalf("Run(--model) error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var models []string
	for _, e := range evs {
		if e.Kind == "dispatched" {
			models = append(models, e.Model)
		}
	}
	if len(models) != 1 || models[0] != other {
		t.Errorf("dispatched models = %q, want [%q]", models, other)
	}
}

// TestModelsAllowedBreakerFallbackSkipsOffList checks the breaker never
// falls back onto an approved fallback outside the list.
func TestModelsAllowedBreakerFallbackSkipsOffList(t *testing.T) {
	t.Parallel()
	w := Worker{Name: "w", Model: "a", ModelsAllowed: []string{"a", "c"},
		Fallbacks: []Fallback{{Model: "b", Approved: true}, {Model: "c", Approved: true}}}
	if got := breakerFallback(nil, w, Breaker{Errors: 1}, now()); got != "c" {
		t.Errorf("breakerFallback() = %q, want \"c\"", got)
	}
}
