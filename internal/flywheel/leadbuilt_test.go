package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLeadBuiltDerivation(t *testing.T) {
	t.Parallel()
	planned := Event{Task: "T1", Kind: "planned", Base: "p1"}
	dispatched := Event{Task: "T1", Kind: "dispatched", Base: "d1"}
	cases := []struct {
		name   string
		events []Event
		want   bool
		base   string
	}{
		{"no dispatched", []Event{planned}, true, "p1"},
		{"dispatched after planned", []Event{planned, dispatched}, false, "d1"},
		{"re-planned after dispatch", []Event{planned, dispatched, {Task: "T1", Kind: "planned", Base: "p2"}}, true, "p2"},
		{"other task dispatched", []Event{planned, {Task: "T2", Kind: "dispatched"}}, true, "p1"},
	}
	for _, c := range cases {
		if got := leadBuilt(c.events, "T1"); got != c.want {
			t.Errorf("%s: leadBuilt() = %v, want %v", c.name, got, c.want)
		}
		if got := leadBuiltBase(c.events, "T1"); got != c.base {
			t.Errorf("%s: leadBuiltBase() = %q, want %q", c.name, got, c.base)
		}
	}
}

// leadRepo is a flywheel root and git repository whose a.go ("package x")
// is committed with a brief owning it, and T1 planned through RecordPlannedBy.
func leadRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initGitRepoAt(t, dir)
	files := map[string]string{
		".gitignore": ".flywheel/\nflywheel.md\n",
		"a.go":       "package x\n",
		"brief.txt":  "owns: a.go\nneeds: none\nkind: feature\ngate: exit 0\n\n# TASK: label\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-q", "-m", "base"})
	if err := RecordPlannedBy(dir, "T1", "brief.txt", PlanMeta{Session: "lead"}); err != nil {
		t.Fatalf("RecordPlannedBy() error = %v", err)
	}
	return dir
}

// leadChange appends n comment lines to a.go.
func leadChange(t *testing.T, dir string, n int) {
	t.Helper()
	body := "package x\n" + strings.Repeat("// line\n", n)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
}

// lastEvent is the ledger's last event.
func lastEvent(t *testing.T, dir string) Event {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil || len(events) == 0 {
		t.Fatalf("ReadEvents() = %d events, %v", len(events), err)
	}
	return events[len(events)-1]
}

func TestLeadBuiltPlannedBase(t *testing.T) {
	t.Parallel()
	dir := leadRepo(t)
	head := git(t, dir, []string{"rev-parse", "HEAD"})
	if e := lastEvent(t, dir); e.Kind != "planned" || e.Base != head {
		t.Errorf("planned event = %s base %q, want planned base %q", e.Kind, e.Base, head)
	}
	plain := t.TempDir()
	if _, err := Init(plain, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(plain, "brief.txt"), []byte("owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK: x\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := RecordPlannedBy(plain, "T1", "brief.txt", PlanMeta{}); err != nil {
		t.Fatalf("RecordPlannedBy() outside a repository error = %v", err)
	}
	if e := lastEvent(t, plain); e.Kind != "planned" || e.Base != "" {
		t.Errorf("planned event outside a repository = %s base %q, want planned base \"\"", e.Kind, e.Base)
	}
}

// leadInspect changes a.go by n lines in a fresh leadRepo (committing it when
// commit), optionally dispatches T1 first, validates and inspects a pass.
func leadInspect(t *testing.T, n int, commit, dispatch bool, exception string) (string, error) {
	t.Helper()
	dir := leadRepo(t)
	if dispatch {
		if err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Session: "w1"}); err != nil {
			t.Fatalf("AppendEvent(dispatched) error = %v", err)
		}
	}
	leadChange(t, dir, n)
	if commit {
		git(t, dir, []string{"commit", "-q", "-am", "lead change"})
	}
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	return dir, InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1", Exception: exception})
}

func TestLeadBuiltInspect(t *testing.T) {
	t.Parallel()
	dir, err := leadInspect(t, 3, false, false, "")
	if err != nil {
		t.Fatalf("InspectTask() of a 3-line lead-built unit error = %v", err)
	}
	if e := lastEvent(t, dir); e.Kind != "inspected" || !e.LeadBuilt || e.ChangedLines != 3 || e.Exception != "" {
		t.Errorf("inspected = %+v, want lead_built with 3 changed lines", e)
	}
	for _, commit := range []bool{false, true} {
		_, err := leadInspect(t, 15, commit, false, "")
		if err == nil {
			t.Fatalf("commit %v: InspectTask() passed a 15-line lead-built unit", commit)
		}
		if got := refusalRule(t, err); got != "lead-built" || !strings.Contains(err.Error(), "flywheel run T1") ||
			!strings.Contains(err.Error(), "changes 15 lines, over lead_built.max_changed_lines 10") {
			t.Errorf("commit %v: refusal = %v, want rule lead-built naming flywheel run", commit, err)
		}
	}
	dir, err = leadInspect(t, 15, false, false, "label table")
	if err != nil {
		t.Fatalf("InspectTask() with an exception error = %v", err)
	}
	if e := lastEvent(t, dir); !e.LeadBuilt || e.ChangedLines != 15 || e.Exception != "label table" {
		t.Errorf("inspected = %+v, want lead_built, 15 lines, exception \"label table\"", e)
	}
	dir, err = leadInspect(t, 15, false, true, "")
	if err != nil {
		t.Fatalf("InspectTask() of a dispatched unit over the cap error = %v", err)
	}
	if e := lastEvent(t, dir); e.Kind != "inspected" || e.LeadBuilt || e.ChangedLines != 0 {
		t.Errorf("inspected = %+v, want a worker pass with no lead-built record", e)
	}
	if _, err = leadInspect(t, 3, false, true, "why"); err == nil || IsRuleRefusal(err) ||
		!strings.Contains(err.Error(), "--exception applies only to a lead-built unit") {
		t.Errorf("--exception on a dispatched unit = %v, want the lead-built-only error", err)
	}
}

func TestLeadBuiltConfig(t *testing.T) {
	t.Parallel()
	var cfg Config
	if got := cfg.LeadBuiltMaxChangedLines(); got != 10 {
		t.Errorf("unset LeadBuiltMaxChangedLines() = %d, want 10", got)
	}
	if v, err := cfg.Get("lead_built.max_changed_lines"); err != nil || v != "10" {
		t.Errorf("Get(lead_built.max_changed_lines) = %q, %v; want 10", v, err)
	}
	if err := cfg.Set("lead_built.max_changed_lines", "0"); err != nil || cfg.LeadBuiltMaxChangedLines() != 0 {
		t.Errorf("Set(0) = %v, max = %d; want 0", err, cfg.LeadBuiltMaxChangedLines())
	}
	if err := cfg.Set("lead_built.max_changed_lines", "-1"); err == nil || !strings.Contains(err.Error(), "lead_built.max_changed_lines") {
		t.Errorf("Set(-1) = %v, want refused naming the key", err)
	}
	neg := -1
	bad := DefaultConfig()
	bad.LeadBuilt = &LeadBuiltConfig{MaxChangedLines: &neg}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "lead_built.max_changed_lines: -1 must not be negative") {
		t.Errorf("Validate() with -1 = %v, want the negative error", err)
	}
}

func TestLeadBuiltRuleL1(t *testing.T) {
	t.Parallel()
	pass := func(n int, exc string) Event {
		return Event{Task: "T1", Kind: "inspected", Verdict: "pass", Session: "lead", LeadBuilt: true, ChangedLines: n, Exception: exc}
	}
	over := ruleL1("T1", []Event{pass(15, "")}, 10)
	if len(over) != 1 || over[0].Pass || !strings.Contains(over[0].Reason, `"lead" changes 15 lines, over lead_built.max_changed_lines 10`) {
		t.Errorf("over the cap = %+v, want one failing L1", over)
	}
	exc := ruleL1("T1", []Event{pass(15, "label table")}, 10)
	if len(exc) != 1 || !exc[0].Pass || exc[0].Reason != "lead-built by lead: 15 changed lines; exception: label table" {
		t.Errorf("with exception = %+v, want one passing L1", exc)
	}
	under := ruleL1("T1", []Event{pass(3, "")}, 10)
	if len(under) != 1 || !under[0].Pass || under[0].Reason != "lead-built by lead: 3 changed lines" {
		t.Errorf("under the cap = %+v, want one passing L1", under)
	}
	none := ruleL1("T1", []Event{{Task: "T1", Kind: "inspected", Verdict: "pass", ChangedLines: 0}}, 10)
	if len(none) != 1 || !none[0].Pass || none[0].Reason != "no lead-built pass over the cap" || none[0].Rule != "L1" {
		t.Errorf("no lead-built pass = %+v, want the single passing item", none)
	}
}

func TestLeadBuiltEventValidate(t *testing.T) {
	t.Parallel()
	ok := Event{Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", LeadBuilt: true, ChangedLines: 15, Exception: "why"}
	if err := Validate(ok); err != nil {
		t.Fatalf("Validate() of a lead-built inspected event = %v, want nil", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "planned", LeadBuilt: true}); err == nil || !strings.Contains(err.Error(), "only an inspected event") {
		t.Errorf("Validate() of lead_built on planned = %v, want refused", err)
	}
	if err := Validate(Event{Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Exception: "why"}); err == nil ||
		!strings.Contains(err.Error(), "only a lead_built inspected event") {
		t.Errorf("Validate() of exception without lead_built = %v, want refused", err)
	}
}
