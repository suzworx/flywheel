package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// initGroup makes a flywheel dir with a git repo (a committed a.go), and
// planned and finished tasks A and B whose briefs own ownsA and ownsB and
// declare gatesA and gatesB, then writes files (path -> content) into the
// working tree: the combined tree validate --group measures (issue #775).
func initGroup(t *testing.T, ownsA, ownsB string, gatesA, gatesB []string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	write := func(p, s string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	write(".gitignore", ".flywheel/\nflywheel.md\n")
	for _, m := range []struct{ task, owns string }{{"A", ownsA}, {"B", ownsB}} {
		brief := "owns: " + m.owns + "\nneeds: none\n"
		gates := gatesA
		if m.task == "B" {
			gates = gatesB
		}
		for _, g := range gates {
			brief += "gate: " + g + "\n"
		}
		write("brief-"+m.task+".txt", brief+"\n# TASK: group\n")
		if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: m.task, Kind: "planned", Brief: "brief-" + m.task + ".txt"}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "briefs"})
	logFinished(t, dir, "A", "w1")
	logFinished(t, dir, "B", "w2")
	for p, s := range files {
		write(p, s)
	}
	return dir
}

func TestValidateGroupReadings(t *testing.T) {
	t.Parallel()
	dir := initGroup(t, "a.go", "b.go", []string{"exit 0", "test -f b.go"}, []string{"exit 0"},
		map[string]string{"a.go": "package y\n", "b.go": "package y\n"})
	res, err := ValidateGroup(dir, "tasks:A,B", ValidateOptions{Dir: dir, Workdir: dir})
	if err != nil {
		t.Fatalf("ValidateGroup() error = %v", err)
	}
	if !res.OK() || len(res.Gates) != 2 {
		t.Fatalf("result OK=%v gates=%d, want OK and 2 distinct gates: %+v", res.OK(), len(res.Gates), res)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	group := GroupTask("tasks:A,B")
	var validated int
	var logs []string
	owns := map[string]bool{}
	for _, e := range events {
		switch e.Kind {
		case "validated":
			validated++
			if e.Group != group || e.Tree != res.Tree {
				t.Errorf("validated %s gate %s: group %q tree %q, want %q %q", e.Task, e.Gate, e.Group, e.Tree, group, res.Tree)
			}
			if !slices.Contains(logs, e.Path) {
				logs = append(logs, e.Path)
			}
		case "owns_checked":
			if e.Group != group || e.Tree != res.Tree || len(e.Outside) != 0 {
				t.Errorf("owns_checked %s = %+v, want clean on tree %s with group %s", e.Task, e, res.Tree, group)
			}
			owns[e.Task] = true
		}
	}
	if validated != 3 || len(logs) != 2 {
		t.Errorf("validated events = %d with %d logs, want 3 events from 2 runs", validated, len(logs))
	}
	if !owns["A"] || !owns["B"] {
		t.Errorf("owns_checked tasks = %v, want A and B", owns)
	}
	if r := requireReadings(dir, dir, "A", events, ""); r.err != nil || r.refusal.Rule != "" {
		t.Fatalf("requireReadings(A) = %+v", r)
	}
	if err := InspectTask(dir, "A", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"}); err != nil {
		t.Fatalf("InspectTask(A) error = %v", err)
	}
}

func TestValidateGroupOwnsOverlap(t *testing.T) {
	t.Parallel()
	dir := initGroup(t, "a.go", "a.go", []string{"exit 0"}, []string{"exit 0"}, map[string]string{"a.go": "package y\n"})
	_, err := ValidateGroup(dir, "tasks:A,B", ValidateOptions{Dir: dir, Workdir: dir})
	if got := refusalRule(t, err); got != "group-owns" {
		t.Errorf("rule = %q, want group-owns", got)
	}
}

func TestValidateGroupOutside(t *testing.T) {
	t.Parallel()
	dir := initGroup(t, "a.go", "b.go", []string{"exit 0"}, []string{"exit 0"},
		map[string]string{"a.go": "package y\n", "c.go": "package y\n"})
	res, err := ValidateGroup(dir, "tasks:A,B", ValidateOptions{Dir: dir, Workdir: dir})
	if err != nil {
		t.Fatalf("ValidateGroup() error = %v", err)
	}
	if res.OK() || !slices.Contains(res.Outside, "c.go") {
		t.Errorf("OK=%v outside=%v, want not OK with c.go outside", res.OK(), res.Outside)
	}
}

func TestValidateGroupFailingGate(t *testing.T) {
	t.Parallel()
	dir := initGroup(t, "a.go", "b.go", []string{"exit 0", "exit 3"}, []string{"exit 3"},
		map[string]string{"a.go": "package y\n", "b.go": "package y\n"})
	res, err := ValidateGroup(dir, "tasks:A,B", ValidateOptions{Dir: dir, Workdir: dir})
	if err != nil {
		t.Fatalf("ValidateGroup() error = %v", err)
	}
	if res.OK() {
		t.Fatal("OK() = true with a failing gate")
	}
	for _, m := range res.Members {
		failed := false
		for _, g := range m.Gates {
			failed = failed || g.Command == "exit 3" && g.RC == 3
		}
		if !failed {
			t.Errorf("member %s gates = %+v, want exit 3 failed", m.Task, m.Gates)
		}
	}
}

func TestValidateGroupEventField(t *testing.T) {
	t.Parallel()
	if err := Validate(Event{Kind: "note", Note: "n", Group: "group:g1"}); err == nil {
		t.Error("Validate() accepted group on a note event")
	}
	rc := 0
	ok := Event{Task: "A", Kind: "validated", Attempt: "r1", Gate: "1", Tree: "t", RC: &rc, Group: "group:g1"}
	if err := Validate(ok); err != nil {
		t.Errorf("Validate() refused a group:<id> on validated: %v", err)
	}
	ok.Group = "g1"
	if err := Validate(ok); err == nil {
		t.Error("Validate() accepted a group that is not group:<id>")
	}
}
