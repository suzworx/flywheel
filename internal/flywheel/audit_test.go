package flywheel

import (
	"testing"
)

func TestAuditRequiresSession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	_, err = AuditTask(dir, "T1", AuditOptions{Dir: dir, Session: ""})
	if err == nil {
		t.Fatal("AuditTask() accepted empty session")
	}
	if got := refusalRule(t, err); got != "T4" {
		t.Errorf("rule = %q, want T4", got)
	}
}

func TestAuditRefusesWorkerSession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	_, err = AuditTask(dir, "T1", AuditOptions{Dir: dir, Session: "w1"})
	if err == nil {
		t.Fatal("AuditTask() accepted worker session")
	}
	if got := refusalRule(t, err); got != "T4" {
		t.Errorf("rule = %q, want T4", got)
	}
}

func TestAuditRefusesInspectorSession(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if err := AppendEvent(dir, Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "insp-1", Tree: "t", Persona: "inspector"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	_, err = AuditTask(dir, "T1", AuditOptions{Dir: dir, Session: "insp-1"})
	if err == nil {
		t.Fatal("AuditTask() accepted inspector session")
	}
	if got := refusalRule(t, err); got != "T4" {
		t.Errorf("rule = %q, want T4", got)
	}
}

func TestAuditConformsRecordsEvent(t *testing.T) {
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	res, err := AuditTask(dir, "T1", AuditOptions{Dir: dir, Session: "aud-1"})
	if err != nil {
		t.Fatalf("AuditTask() error = %v", err)
	}
	if res.Verdict == "" {
		t.Error("Verdict is empty")
	}
	if res.Tree == "" {
		t.Error("Tree is empty")
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	found := false
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "audited" && e.Session == "aud-1" && e.Persona == "auditor" {
			found = true
			if e.Verdict != res.Verdict {
				t.Errorf("event Verdict %q != result Verdict %q", e.Verdict, res.Verdict)
			}
			if e.Tree != res.Tree {
				t.Errorf("event Tree %q != result Tree %q", e.Tree, res.Tree)
			}
		}
	}
	if !found {
		t.Fatal("audited event not found in log")
	}
}

func TestAuditFailingGateIsNonconformance(t *testing.T) {
	dir, err := initTask(t, []string{"exit 1"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	res, err := AuditTask(dir, "T1", AuditOptions{Dir: dir, Session: "aud-1"})
	if err != nil {
		t.Fatalf("AuditTask() error = %v", err)
	}
	if res.Verdict != "nonconformance" {
		t.Errorf("Verdict = %q, want nonconformance", res.Verdict)
	}
	if len(res.Findings) == 0 {
		t.Error("Findings is empty, want gate 1 failed")
	}
	found := false
	for _, f := range res.Findings {
		if len(f) > 0 && f[:len("gate 1 failed")] == "gate 1 failed" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Findings does not contain gate 1 failed; got %v", res.Findings)
	}
}

func TestAuditValidateRejectsBadVerdict(t *testing.T) {
	err := Validate(Event{Task: "T1", Kind: "audited", Session: "a", Verdict: "maybe"})
	if err == nil {
		t.Fatal("Validate() accepted bad verdict")
	}
}
