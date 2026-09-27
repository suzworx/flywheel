package flywheel

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planStream is a sim fixture for session: steps step_finish lines, with a
// PLAN check-in first when plan is set.
func planStream(session string, plan bool, steps int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"step_start","sessionID":%q,"part":{"type":"step_start"}}`+"\n", session)
	if plan {
		fmt.Fprintf(&b, `{"type":"text","sessionID":%q,"part":{"type":"text","text":"PLAN read a.go, then edit it."}}`+"\n", session)
	}
	for i := 0; i < steps; i++ {
		fmt.Fprintf(&b, `{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":"stop"}}`+"\n", session)
	}
	return b.String()
}

// runStream writes stream to the sim model file and runs task T1.
func runStream(t *testing.T, dir, model, stream string, resume bool) Result {
	t.Helper()
	if err := os.WriteFile(model, []byte(stream), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if resume {
		writeDelta(t, dir, "T1")
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Resume: resume, Progress: &buf})
	if err != nil {
		t.Fatalf("Run(resume=%v) error = %v", resume, err)
	}
	return res
}

// resumeScenario runs each (session, plan) as a fresh attempt, then resumes
// the last session with a 5-step stream holding no PLAN, and returns the ledger.
func resumeScenario(t *testing.T, attempts []struct {
	session string
	plan    bool
}) []Event {
	t.Helper()
	dir := setupTask(t)
	model := filepath.Join(t.TempDir(), "f.jsonl")
	if err := WriteConfig(dir, simConfig(model)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	for _, a := range attempts {
		runStream(t, dir, model, planStream(a.session, a.plan, 2), false)
	}
	last := attempts[len(attempts)-1].session
	if res := runStream(t, dir, model, planStream(last, false, 5), true); res.Attempt != "c1" {
		t.Fatalf("resume attempt = %q, want c1", res.Attempt)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	return evs
}

// c1NoPlan reports whether c1 recorded a no-plan event and a no-plan signal.
func c1NoPlan(evs []Event) (event, signal bool) {
	for _, e := range evs {
		if e.Task == "T1" && e.Attempt == "c1" && e.Kind == "no-plan" {
			event = true
		}
		if e.Task == "T1" && e.Attempt == "c1" && e.Kind == "signal" && e.Signal == "no-plan" {
			signal = true
		}
	}
	return event, signal
}

// TestRunResumeCarriesPlan checks a --resume of a session that checked in
// carries its PLAN over to c1 and records no no-plan (issue #592). On the
// unfixed code the no-plan and the c1 worker_plan assertions fail; the r1
// Attempt assertion fails there too (recordPlan set no Attempt).
func TestRunResumeCarriesPlan(t *testing.T) {
	t.Parallel()
	evs := resumeScenario(t, []struct {
		session string
		plan    bool
	}{{"ses_resume_plan_001", true}})
	var r1, c1 *Event
	for i, e := range evs {
		if e.Kind != "worker_plan" {
			continue
		}
		switch e.Attempt {
		case "r1":
			r1 = &evs[i]
		case "c1":
			c1 = &evs[i]
		}
	}
	if r1 == nil || r1.Path != ".flywheel/runs/T1.r1.plan.md" {
		t.Fatalf("no r1 worker_plan with Attempt r1 and the r1 path; events = %v", evs)
	}
	if c1 == nil {
		t.Fatalf("no carried-over c1 worker_plan; events = %v", evs)
	}
	if c1.Path != r1.Path || c1.SHA256 != r1.SHA256 {
		t.Errorf("c1 worker_plan = %s %s, want r1's %s %s", c1.Path, c1.SHA256, r1.Path, r1.SHA256)
	}
	if want := "carried over from r1 (resumed session ses_resu)"; c1.Note != want {
		t.Errorf("c1 worker_plan note = %q, want %q", c1.Note, want)
	}
	if ev, sig := c1NoPlan(evs); ev || sig {
		t.Errorf("c1 no-plan event = %v, signal = %v; want neither", ev, sig)
	}
}

// TestRunResumeSessionPlan checks sessionPlan matches a worker_plan to the
// session's attempts by path (old ledgers) or Attempt, and the latest wins
// (issue #592). It fails on the unfixed code: sessionPlan does not exist.
func TestRunResumeSessionPlan(t *testing.T) {
	t.Parallel()
	evs := []Event{
		{Task: "T1", Kind: "finished", Attempt: "r1", Session: "S"},
		{Task: "T1", Kind: "worker_plan", Path: ".flywheel/runs/T1.r1.plan.md", SHA256: "a"},
		{Task: "T1", Kind: "finished", Attempt: "r2", Session: "S2"},
		{Task: "T1", Kind: "worker_plan", Attempt: "r2", Path: ".flywheel/runs/T1.r2.plan.md", SHA256: "b"},
		{Task: "T2", Kind: "finished", Attempt: "r1", Session: "S"},
		{Task: "T2", Kind: "worker_plan", Path: ".flywheel/runs/T2.r1.plan.md", SHA256: "x"},
	}
	if p, ok := sessionPlan(evs, "T1", "S"); !ok || p.Attempt != "r1" || p.SHA256 != "a" {
		t.Errorf("path match = %v %v, want r1 a", p, ok)
	}
	if p, ok := sessionPlan(evs, "T1", "S2"); !ok || p.Attempt != "r2" || p.SHA256 != "b" {
		t.Errorf("Attempt match = %v %v, want r2 b", p, ok)
	}
	if _, ok := sessionPlan(evs, "T1", "S3"); ok {
		t.Error("unknown session matched a plan")
	}
	evs = append(evs,
		Event{Task: "T1", Kind: "finished", Attempt: "c1", Session: "S"},
		Event{Task: "T1", Kind: "worker_plan", Attempt: "c1", Path: ".flywheel/runs/T1.r1.plan.md", SHA256: "a"},
		Event{Task: "T1", Kind: "finished", Attempt: "c2", Session: "S"},
		Event{Task: "T1", Kind: "worker_plan", Attempt: "c2", Path: ".flywheel/runs/T1.c2.plan.md", SHA256: "c"},
	)
	if p, ok := sessionPlan(evs, "T1", "S"); !ok || p.Attempt != "c2" || p.SHA256 != "c" {
		t.Errorf("latest = %v %v, want c2 c", p, ok)
	}
}

// TestRunResumeNoPlanWhenSessionNeverPlanned checks a resumed session that
// never checked in still records no-plan on c1, and a worker_plan of another
// session's attempt does not carry over (issue #592). On the unfixed code both
// pass; they guard against over-reaching carry-over.
func TestRunResumeNoPlanWhenSessionNeverPlanned(t *testing.T) {
	t.Parallel()
	type attempt = struct {
		session string
		plan    bool
	}
	cases := map[string][]attempt{
		"never planned": {{"ses_resume_none_001", false}},
		"other session": {{"ses_resume_s1_001", true}, {"ses_resume_s2_001", false}},
	}
	for name, attempts := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			evs := resumeScenario(t, attempts)
			for _, e := range evs {
				if e.Kind == "worker_plan" && e.Attempt == "c1" {
					t.Errorf("c1 has a worker_plan: %v", e)
				}
			}
			if ev, sig := c1NoPlan(evs); !ev || !sig {
				t.Errorf("c1 no-plan event = %v, signal = %v; want both", ev, sig)
			}
		})
	}
}
