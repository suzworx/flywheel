package flywheel

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const needsEnvVar = "FW_TEST_NEEDS_ENV_X"

// TestNeedsEnvHeader checks needs-env: parsing (repeat lines accumulate, a
// name is kept once, none is empty, invalid names stay out of NeedsEnv) and
// MissingEnv's unset, empty, whitespace and set cases (issue #534).
func TestNeedsEnvHeader(t *testing.T) {
	t.Parallel()
	h, err := ParseBriefHeaderBytes([]byte("needs-env: A_KEY, _b1\nneeds-env: A_KEY, 9BAD, C-D\nneeds-env: none\n\n# TASK x\n"))
	if err != nil {
		t.Fatalf("ParseBriefHeaderBytes() error = %v", err)
	}
	if want := []string{"A_KEY", "_b1"}; !reflect.DeepEqual(h.NeedsEnv, want) {
		t.Errorf("NeedsEnv = %q, want %q", h.NeedsEnv, want)
	}
	if want := []string{"9BAD", "C-D"}; !reflect.DeepEqual(h.needsEnvInvalid, want) {
		t.Errorf("needsEnvInvalid = %q, want %q", h.needsEnvInvalid, want)
	}
	if h.needsEnvEmpty {
		t.Error("needsEnvEmpty = true, want false")
	}
	none, _ := ParseBriefHeaderBytes([]byte("needs-env: none\n"))
	if len(none.NeedsEnv) != 0 || len(none.needsEnvInvalid) != 0 || none.needsEnvEmpty {
		t.Errorf("needs-env: none parsed as %+v, want an empty list", none)
	}
	env := map[string]string{"SET": "v", "EMPTY": "", "BLANK": " \t"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	got := MissingEnv([]string{"UNSET", "SET", "EMPTY", "BLANK"}, lookup)
	if want := []string{"UNSET", "EMPTY", "BLANK"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MissingEnv() = %q, want %q", got, want)
	}
	if got := MissingEnv([]string{"SET"}, lookup); got != nil {
		t.Errorf("MissingEnv(SET) = %q, want nil", got)
	}
}

// needsEnvTask sets up a planned task whose brief is text.
func needsEnvTask(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(text), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-26T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	return dir
}

// unsetEnv unsets name for the test and restores it after; t.Setenv makes the
// test non-parallel, which it must be since it changes the process env.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
}

// TestNeedsEnvRunRefusal checks run refuses a brief whose needs-env: variable
// is unset, recording no dispatched event, and with the variable set runs and
// never writes its value under .flywheel (issue #534). Not parallel: it sets
// the process environment.
func TestNeedsEnvRunRefusal(t *testing.T) {
	dir := needsEnvTask(t, "needs-env: "+needsEnvVar+"\n\n# TASK x\n")
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	unsetEnv(t, needsEnvVar)
	_, err := Run(dir, RunOptions{Task: "T1"})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "needs-env" || !strings.Contains(rf.Error(), needsEnvVar) {
		t.Fatalf("Run() error = %v, want a needs-env RuleRefusal naming %s", err, needsEnvVar)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "dispatched" {
			t.Fatalf("dispatched event recorded despite the refusal: %+v", e)
		}
	}

	const secret = "s3cr3t-needs-env-value-534"
	t.Setenv(needsEnvVar, secret)
	if _, err := Run(dir, RunOptions{Task: "T1"}); errors.As(err, &rf) && rf.Rule == "needs-env" {
		t.Fatalf("Run() with %s set refused: %v", needsEnvVar, err)
	}
	if evs, _ = ReadEvents(dir); !hasEventKind(evs, "dispatched") {
		t.Fatal("no dispatched event recorded with the variable set")
	}
	_ = filepath.WalkDir(filepath.Join(dir, ".flywheel"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), secret) {
			t.Errorf("%s holds the variable's value", p)
		}
		return nil
	})
}

// hasEventKind reports whether an event of kind is in evs.
func hasEventKind(evs []Event, kind string) bool {
	for _, e := range evs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// TestNeedsEnvValidateRefusal checks ValidateTask refuses with a needs-env
// RuleRefusal while the variable is unset and runs no gate (issue #534). Not
// parallel: it sets the process environment.
func TestNeedsEnvValidateRefusal(t *testing.T) {
	dir := needsEnvTask(t, "needs-env: "+needsEnvVar+"\ngate: echo x > marker.txt\n\n# TASK x\n")
	unsetEnv(t, needsEnvVar)
	_, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "needs-env" || !strings.Contains(rf.Error(), needsEnvVar) {
		t.Fatalf("ValidateTask() error = %v, want a needs-env RuleRefusal naming %s", err, needsEnvVar)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("gate ran despite the refusal: stat marker.txt = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "evidence")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("evidence dir created despite the refusal: %v", err)
	}
}

// TestNeedsEnvLint checks lint reports an invalid name and an empty
// needs-env: line, and a valid line gives neither (issue #534).
func TestNeedsEnvLint(t *testing.T) {
	t.Parallel()
	noList := func(string) (string, error) { return "", nil }
	lint := func(header string) []string {
		dir := t.TempDir()
		p := filepath.Join(dir, "b.md")
		if err := os.WriteFile(p, []byte(header+"gate: go test ./...\n\n# TASK x\n"), 0o644); err != nil {
			t.Fatalf("write brief: %v", err)
		}
		res, err := lintBrief(dir, p, noList)
		if err != nil {
			t.Fatalf("lintBrief() error = %v", err)
		}
		var got []string
		for _, pr := range res.Problems {
			if strings.Contains(pr, "needs-env") {
				got = append(got, pr)
			}
		}
		return got
	}
	if got, want := lint("needs-env: OK_NAME, 9BAD\n"), []string{`needs-env name "9BAD" is not a valid environment variable name`}; !reflect.DeepEqual(got, want) {
		t.Errorf("invalid name problems = %q, want %q", got, want)
	}
	if got, want := lint("needs-env:\n"), []string{"needs-env: line is empty; name variables or write needs-env: none"}; !reflect.DeepEqual(got, want) {
		t.Errorf("empty line problems = %q, want %q", got, want)
	}
	if got := lint("needs-env: OK_NAME, _OTHER2\n"); len(got) != 0 {
		t.Errorf("valid line problems = %q, want none", got)
	}
}
