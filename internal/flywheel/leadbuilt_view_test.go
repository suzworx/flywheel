package flywheel

import (
	"bytes"
	"strings"
	"testing"
)

// leadBuiltViewLedger writes a lead-built unit L (planned, validated,
// inspected with 3 changed lines) beside a dispatched unit W to a fresh dir.
func leadBuiltViewLedger(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	recoverLedger(t, dir,
		Event{Task: "L", Kind: "planned", Brief: "brief.txt"},
		Event{Task: "L", Kind: "validated", Gate: "go test ./...", Tree: "t1"},
		Event{Task: "L", Kind: "inspected", Verdict: "pass", Session: "lead", LeadBuilt: true, ChangedLines: 3},
		Event{Task: "W", Kind: "planned", Brief: "brief.txt"},
		Event{Task: "W", Kind: "dispatched", Attempt: "r1", Model: "m"},
		Event{Task: "W", Kind: "finished", Attempt: "r1", Model: "m", Session: "ses_w", Reason: "stop"},
	)
	return dir
}

// TestLeadBuiltViewMark checks leadBuiltMark on each kind of unit.
func TestLeadBuiltViewMark(t *testing.T) {
	t.Parallel()
	planned := Event{Task: "T", Kind: "planned"}
	validated := Event{Task: "T", Kind: "validated", Gate: "g", Tree: "t"}
	inspected := Event{Task: "T", Kind: "inspected", Verdict: "pass", LeadBuilt: true, ChangedLines: 3}
	excepted := inspected
	excepted.Exception = "generated file"
	dispatched := Event{Task: "T", Kind: "dispatched", Attempt: "r1"}
	for _, c := range []struct {
		name   string
		events []Event
		want   string
		ok     bool
	}{
		{"inspected", []Event{planned, validated, inspected}, "built by lead, 3 changed lines", true},
		{"exception", []Event{planned, validated, excepted}, "built by lead, 3 changed lines, exception: generated file", true},
		{"validated only", []Event{planned, validated}, "built by lead", true},
		{"planned only", []Event{planned}, "", false},
		{"dispatched", []Event{planned, dispatched, validated, {Task: "T", Kind: "inspected", Verdict: "pass"}}, "", false},
		{"re-planned", []Event{planned, dispatched, planned, validated, inspected}, "built by lead, 3 changed lines", true},
		{"other task", []Event{planned, validated, {Task: "U", Kind: "inspected", LeadBuilt: true, ChangedLines: 9}}, "built by lead", true},
	} {
		if got, ok := leadBuiltMark(c.events, "T"); got != c.want || ok != c.ok {
			t.Errorf("%s: leadBuiltMark = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestLeadBuiltViewFactory checks the factory floor and its rendered rows:
// the lead-built unit carries the mark where the dispatched one shows its
// session and model.
func TestLeadBuiltViewFactory(t *testing.T) {
	t.Parallel()
	dir := leadBuiltViewLedger(t)
	w := NewWatcher()
	fl, err := w.Refresh(dir, recoverNow)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	units := map[string]Unit{}
	for _, u := range fl.Units {
		units[u.Task] = u
	}
	if got := units["L"].LeadBuilt; got != "built by lead, 3 changed lines" {
		t.Errorf("L LeadBuilt = %q, want the mark", got)
	}
	if got := units["W"].LeadBuilt; got != "" {
		t.Errorf("W LeadBuilt = %q, want empty", got)
	}
	var b bytes.Buffer
	RenderText(&b, fl, 120, false)
	rows := map[string]string{}
	for _, line := range strings.Split(b.String(), "\n") {
		// The first row per task is its unit row; the andon may list it again.
		if f := strings.Fields(line); len(f) > 0 && (f[0] == "L" || f[0] == "W") && rows[f[0]] == "" {
			rows[f[0]] = line
		}
	}
	if !strings.Contains(rows["L"], "built by lead") {
		t.Errorf("L row = %q, want built by lead\n%s", rows["L"], b.String())
	}
	if rows["W"] == "" || strings.Contains(rows["W"], "built by lead") {
		t.Errorf("W row = %q, want a row without built by lead", rows["W"])
	}
	if len(rows["L"]) > 0 && strings.Index(rows["L"], "built by lead") != strings.Index(rows["W"], "ses_w") {
		t.Errorf("mark column %d != session column %d\n%s", strings.Index(rows["L"], "built by lead"), strings.Index(rows["W"], "ses_w"), b.String())
	}
	var j bytes.Buffer
	RenderJSON(&j, fl)
	if !strings.Contains(j.String(), `"lead_built": "built by lead, 3 changed lines"`) {
		t.Errorf("JSON lacks lead_built:\n%s", j.String())
	}
}

// TestLeadBuiltViewRecover checks recover's lead_built field and its text.
func TestLeadBuiltViewRecover(t *testing.T) {
	t.Parallel()
	dir := leadBuiltViewLedger(t)
	rep, err := Recover(dir, recoverNow, RecoverOptions{All: true})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	got := map[string]string{}
	for _, task := range rep.Tasks {
		got[task.Task] = task.LeadBuilt
	}
	if got["L"] != "built by lead, 3 changed lines" || got["W"] != "" {
		t.Errorf("LeadBuilt = %v, want L marked and W empty", got)
	}
	text := rep.Text()
	if !strings.Contains(text, "  built by lead: 3 changed lines\n") {
		t.Errorf("text lacks built by lead: 3 changed lines:\n%s", text)
	}
	if strings.Count(text, "built by lead") != 1 {
		t.Errorf("text marks more than the lead-built unit:\n%s", text)
	}
}
