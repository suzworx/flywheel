package flywheel

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestResourceWaitRecorded checks a gate finding its resource held appends
// exactly one resource_wait event, before the gate's validated event, and a
// free lock appends none (issue #697).
func TestResourceWaitRecorded(t *testing.T) {
	t.Parallel()
	gate := "touch '" + filepath.ToSlash(filepath.Join(t.TempDir(), "ran")) + "'"
	dir, lockDir := resourcesTask(t, "resources: e2e\ngate: "+gate+"\n")
	p := resourceLockPath(lockDir, "e2e")
	holdResource(t, p)
	polls := 0
	tune := fakeLockClock(30*time.Second, func(c time.Time) {
		polls++
		if polls == 5 {
			_ = os.Remove(p)
		} else {
			_ = os.Chtimes(p, c, c)
		}
	})
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, resourceTimings: tune}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	waitAt, validatedAt := -1, -1
	waits := 0
	for i, e := range evs {
		switch e.Kind {
		case "resource_wait":
			waits++
			waitAt = i
		case "validated":
			validatedAt = i
		}
	}
	if waits != 1 || validatedAt < 0 {
		t.Fatalf("resource_wait events = %d, validated at %d; want exactly one wait and a reading", waits, validatedAt)
	}
	w, v := evs[waitAt], evs[validatedAt]
	if waitAt > validatedAt {
		t.Errorf("resource_wait at %d after validated at %d, want before", waitAt, validatedAt)
	}
	want := "waiting for resource e2e (validate o9 gate 1 (pid 4242))"
	if w.Task != "T1" || w.Gate != "1" || w.Gate != v.Gate || w.Attempt != v.Attempt || w.Command != gate || w.Persona != "supervisor" || w.Note != want {
		t.Errorf("resource_wait = %+v, want task T1, gate 1, attempt %q, command %q, persona supervisor, note %q", w, v.Attempt, gate, want)
	}

	// a free lock records nothing.
	free, _ := resourcesTask(t, "resources: e2e\ngate: "+gate+"\n")
	if _, err := ValidateTask(free, "T1", ValidateOptions{Dir: free, resourceTimings: fakeLockClock(30*time.Second, nil)}); err != nil {
		t.Fatalf("ValidateTask(free) error = %v", err)
	}
	fevs, err := ReadEvents(free)
	if err != nil {
		t.Fatalf("ReadEvents(free) error = %v", err)
	}
	for _, e := range fevs {
		if e.Kind == "resource_wait" {
			t.Errorf("free lock recorded %+v, want no resource_wait", e)
		}
	}
}

// resourceWaitFloor writes unit E (planned, dispatched r1) and extra to a
// fresh ledger and returns E's unit and its rendered row at recoverNow.
func resourceWaitFloor(t *testing.T, extra ...Event) (Unit, string) {
	t.Helper()
	dir := t.TempDir()
	recoverLedger(t, dir, append([]Event{
		{Task: "E", Kind: "planned", Brief: "brief.txt"},
		{Task: "E", Kind: "dispatched", Attempt: "r1", Model: "m"},
	}, extra...)...)
	w := NewWatcher()
	fl, err := w.Refresh(dir, recoverNow)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var u Unit
	for _, x := range fl.Units {
		if x.Task == "E" {
			u = x
		}
	}
	var b bytes.Buffer
	RenderText(&b, fl, 200, false)
	for _, line := range strings.Split(b.String(), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "E" {
			return u, line
		}
	}
	t.Fatalf("no row for E:\n%s", b.String())
	return u, ""
}

// TestResourceWaitFactory checks the factory's live-wait rule (issue #697).
func TestResourceWaitFactory(t *testing.T) {
	t.Parallel()
	at := func(ago time.Duration) string { return recoverNow.Add(-ago).Format(time.RFC3339) }
	wait := func(ago time.Duration, attempt, gate string) Event {
		return Event{TS: at(ago), Task: "E", Kind: "resource_wait", Attempt: attempt, Gate: gate, Command: "go test", Persona: "supervisor", Note: "waiting for resource e2e (validate o12 gate 1)"}
	}
	validated := func(ago time.Duration, gate string) Event {
		return Event{TS: at(ago), Task: "E", Kind: "validated", Attempt: "r1", Gate: gate, Command: "go test", Tree: "t1", Reason: "inconclusive", Note: "resource busy: e2e"}
	}
	for _, c := range []struct {
		name  string
		extra []Event
		want  string
	}{
		{"live", []Event{wait(2*time.Minute, "r1", "1")}, "resource e2e (validate o12 gate 1) 2m"},
		{"ended", []Event{wait(2*time.Minute, "r1", "1"), validated(time.Minute, "1")}, ""},
		{"other gate", []Event{wait(2*time.Minute, "r1", "1"), validated(time.Minute, "2")}, "resource e2e (validate o12 gate 1) 2m"},
		{"inside budget", []Event{wait(30*time.Minute+30*time.Second, "r1", "1")}, "resource e2e (validate o12 gate 1) 30m"},
		{"too old", []Event{wait(31*time.Minute+time.Second, "r1", "1")}, ""},
		{"older attempt", []Event{wait(3*time.Minute, "r1", "1"), {TS: at(2 * time.Minute), Task: "E", Kind: "dispatched", Attempt: "c1", Model: "m"}}, ""},
	} {
		u, row := resourceWaitFloor(t, c.extra...)
		if u.ResourceWait != c.want {
			t.Errorf("%s: ResourceWait = %q, want %q", c.name, u.ResourceWait, c.want)
		}
		if c.want != "" && !strings.Contains(row, "waiting for "+c.want) {
			t.Errorf("%s: row = %q, want the RUN cell %q", c.name, row, "waiting for "+c.want)
		}
		if c.want == "" && strings.Contains(row, "waiting for resource") {
			t.Errorf("%s: row = %q, want no live wait", c.name, row)
		}
	}
}

// TestResourceWaitValidate checks Validate requires a task, gate and note.
func TestResourceWaitValidate(t *testing.T) {
	t.Parallel()
	full := Event{Task: "T1", Kind: "resource_wait", Attempt: "r1", Gate: "1", Command: "go test", Persona: "supervisor", Note: "waiting for resource e2e (another command)"}
	if err := Validate(full); err != nil {
		t.Errorf("Validate(full) = %v, want nil", err)
	}
	noGate, noNote, noTask := full, full, full
	noGate.Gate, noNote.Note, noTask.Task = "", "", ""
	for name, e := range map[string]Event{"no gate": noGate, "no note": noNote, "no task": noTask} {
		if err := Validate(e); err == nil {
			t.Errorf("%s: Validate = nil, want a refusal", name)
		}
	}
	if err := Validate(noGate); err == nil || err.Error() != "resource_wait event must carry a task, a gate and a note" {
		t.Errorf("no gate: Validate = %v, want the resource_wait refusal", err)
	}
}
