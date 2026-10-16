package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// observeSetup makes task T1 (owns a.go, gate "true") whose brief declares the
// given observe: lines, planned and, when dispatch is set, dispatched to
// worker session "worker-1" and finished; it returns the flywheel dir.
func observeSetup(t *testing.T, observes []string, dispatch bool) string {
	t.Helper()
	dir, err := initTask(t, []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	brief := "owns: a.go\nneeds: none\ngate: true\n"
	for _, o := range observes {
		brief += "observe: " + o + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief+"\n# TASK: observe\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:30Z", Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	if dispatch {
		if err := AppendEvent(dir, Event{TS: "2026-09-12T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "worker-1"}); err != nil {
			t.Fatalf("append dispatched: %v", err)
		}
		logFinished(t, dir, "T1", "worker-1")
	}
	return dir
}

// observeGateReadings appends a passing gate 1 reading and a clean
// owns_checked on tree after the finished event.
func observeGateReadings(t *testing.T, dir, tree string) {
	t.Helper()
	rc := 0
	batch := []Event{
		{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "validated", Attempt: "r1", Gate: "1", Command: "true", Tree: tree, RC: &rc, Persona: "supervisor"},
		{TS: "2026-09-12T02:00:01Z", Task: "T1", Kind: "owns_checked", Attempt: "r1", Tree: tree, Persona: "supervisor"},
	}
	if err := AppendEvents(dir, batch); err != nil {
		t.Fatalf("append readings: %v", err)
	}
}

func TestObserveBriefHeaderAndDelta(t *testing.T) {
	t.Parallel()
	h, err := ParseBriefHeaderBytes([]byte("owns: a.go\ngate: true\nobserve: the clock shows 12:00\nobserve: the tone is 440 Hz\n\n# T\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"the clock shows 12:00", "the tone is 440 Hz"}; !slices.Equal(h.Observes, want) {
		t.Errorf("Observes = %q, want %q", h.Observes, want)
	}
	plain, _ := ParseBriefHeaderBytes([]byte("owns: a.go\ngate: true\n\n# T\n"))
	if plain.Observes != nil {
		t.Errorf("a brief with no observe line has Observes = %q, want nil", plain.Observes)
	}
	dir := observeSetup(t, []string{"screen is red"}, true)
	if err := os.WriteFile(filepath.Join(dir, "delta.txt"), []byte("owns: a.go\ngate: go vet ./...\n\n# delta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T03:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "c1", Brief: "delta.txt", Session: "worker-2"}); err != nil {
		t.Fatal(err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	eff, _, err := AttemptBrief(dir, events, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(eff.Gates, []string{"go vet ./..."}) || !slices.Equal(eff.Observes, []string{"screen is red"}) {
		t.Errorf("correction header gates %q observes %q; want the delta's gate and the base's observe", eff.Gates, eff.Observes)
	}
}

func TestObserveLintEmptyLine(t *testing.T) {
	t.Parallel()
	res := lintCheck(t, t.TempDir(), []string{"a.go"}, "owns: a.go\nneeds: none\ngate: true\nobserve:\n\n# TASK\nAt most one write per response\n")
	if !slices.ContainsFunc(res.Problems, func(p string) bool { return strings.Contains(p, "observe: line is empty") }) {
		t.Errorf("problems = %v, want an empty observe: problem", res.Problems)
	}
	res = lintCheck(t, t.TempDir(), []string{"a.go"}, "owns: a.go\nneeds: none\ngate: true\nobserve: the screen is red\n\n# TASK\nAt most one write per response\n")
	if slices.ContainsFunc(res.Problems, func(p string) bool { return strings.Contains(p, "observe") }) {
		t.Errorf("problems = %v, want none about observe", res.Problems)
	}
}

func TestObserveRefusals(t *testing.T) {
	t.Parallel()
	dir := observeSetup(t, []string{"screen is red"}, true)
	var r *RuleRefusal
	if _, err := Observe(dir, "T1", 1, true, "", "lead-1"); err == nil || errors.As(err, &r) {
		t.Errorf("Observe() without evidence error = %v, want a plain error", err)
	}
	if _, err := Observe(dir, "T1", 1, true, "photo.jpg", ""); !errors.As(err, &r) || r.Rule != "T4" {
		t.Errorf("Observe() without a session error = %v, want T4", err)
	}
	if _, err := Observe(dir, "T1", 1, true, "photo.jpg", "worker-1"); !errors.As(err, &r) || r.Rule != "T4" {
		t.Errorf("Observe() from a worker session error = %v, want T4", err)
	}
	for _, n := range []int{0, 2} {
		if _, err := Observe(dir, "T1", n, true, "photo.jpg", "lead-1"); err == nil || errors.As(err, &r) || !strings.Contains(err.Error(), "1..1") {
			t.Errorf("Observe(check %d) error = %v, want an error naming 1..1", n, err)
		}
	}
	undispatched := observeSetup(t, []string{"screen is red"}, false)
	if _, err := Observe(undispatched, "T1", 1, true, "photo.jpg", "lead-1"); !errors.As(err, &r) || r.Rule != "T5" {
		t.Errorf("Observe() before dispatch error = %v, want T5", err)
	}
}

func TestObserveRecordsReadingAndGatesPass(t *testing.T) {
	t.Parallel()
	dir := observeSetup(t, []string{"screen is red"}, true)
	tree, err := treeHash(absPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	observeGateReadings(t, dir, tree)
	refusal := func() RuleRefusal {
		t.Helper()
		events, err := ReadEvents(dir)
		if err != nil {
			t.Fatal(err)
		}
		res := requireReadings(dir, dir, "T1", events, tree)
		if res.err != nil {
			t.Fatal(res.err)
		}
		return res.refusal
	}
	if r := refusal(); r.Rule != "T3" || !strings.Contains(r.Fix, "flywheel observe T1 --check 1") || !strings.Contains(r.Fix, "screen is red") {
		t.Errorf("pass with no observation = %+v, want T3 naming flywheel observe", r)
	}
	if _, err := Observe(dir, "T1", 1, false, "photo-1.jpg", "lead-1"); err != nil {
		t.Fatalf("Observe(fail) error = %v", err)
	}
	if r := refusal(); r.Rule != "T3" {
		t.Errorf("pass with a failing observation = %+v, want T3", r)
	}
	got, err := Observe(dir, "T1", 1, true, "photo-2.jpg", "lead-1")
	if err != nil {
		t.Fatalf("Observe(pass) error = %v", err)
	}
	if got != tree {
		t.Errorf("Observe() tree = %q, want %q", got, tree)
	}
	events, _ := ReadEvents(dir)
	last := events[len(events)-1]
	if last.Kind != "validated" || last.Gate != "obs1" || last.Command != "screen is red" || last.Source != "observed" ||
		last.Evidence != "photo-2.jpg" || last.Session != "lead-1" || last.Persona != "supervisor" || last.Attempt != "r1" || last.RC == nil || *last.RC != 0 {
		t.Errorf("observation event = %+v", last)
	}
	if r := refusal(); r.Rule != "" {
		t.Errorf("pass with a passing observation refused: %+v", r)
	}
	// A newer worker finish makes the observation stale.
	rc := 0
	if err := AppendEvent(dir, Event{TS: "2099-01-01T00:00:00Z", Task: "T1", Kind: "finished", Attempt: "r1", Session: "worker-1", RC: &rc}); err != nil {
		t.Fatal(err)
	}
	if r := refusal(); r.Rule != "T3" {
		t.Errorf("pass after a newer finished event = %+v, want T3", r)
	}
}

func TestObserveNoObserveLineUnchanged(t *testing.T) {
	t.Parallel()
	dir := observeSetup(t, nil, true)
	tree, err := treeHash(absPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	observeGateReadings(t, dir, tree)
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res := requireReadings(dir, dir, "T1", events, tree); res.err != nil || res.refusal.Rule != "" {
		t.Errorf("a brief with no observe line: %+v, want a pass", res)
	}
}

func TestObserveEventNeedsSession(t *testing.T) {
	t.Parallel()
	rc := 0
	e := Event{Task: "T1", Kind: "validated", Gate: "obs1", Tree: "abc", RC: &rc, Source: "observed", Evidence: "p.jpg"}
	if err := Validate(e); err == nil {
		t.Error("Validate() accepted an observed reading with no session")
	}
	e.Session = "lead-1"
	if err := Validate(e); err != nil {
		t.Errorf("Validate() observed reading error = %v", err)
	}
}
