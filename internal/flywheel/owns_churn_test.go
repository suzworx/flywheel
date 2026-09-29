package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// TestChurnClass is issue #647: an outside path that differs from the base
// only in line endings or only in whitespace is labelled; a real edit, an
// added or a deleted file, and an empty base are not.
func TestChurnClass(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	initGitRepoAt(t, wd)
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(wd, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wd, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("lf.txt", "one\ntwo\n")
	write("sub/ws.go", "func f() {\n\treturn\n}\n")
	write("edit.txt", "one\ntwo\n")
	write("del.txt", "gone\n")
	write("same.txt", "same\n")
	git(t, wd, []string{"add", "-A"})
	git(t, wd, []string{"commit", "-m", "base"})
	base := git(t, wd, []string{"rev-parse", "HEAD"})

	write("lf.txt", "one\r\ntwo\r\n")
	write("sub/ws.go", "func f() {\n    return\n}\n\n")
	write("edit.txt", "one\nthree\n")
	write("new.txt", "fresh\n")
	if err := os.Remove(filepath.Join(wd, "del.txt")); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, base, path, want string
	}{
		{"LF to CRLF", base, "lf.txt", churnLineEndings},
		{"reindent", base, "sub/ws.go", churnWhitespace},
		{"real edit", base, "edit.txt", ""},
		{"new file", base, "new.txt", ""},
		{"deleted file", base, "del.txt", ""},
		{"unchanged", base, "same.txt", ""},
		{"empty base", "", "lf.txt", ""},
		{"bad base", "0000000000000000000000000000000000000000", "lf.txt", ""},
	}
	for _, c := range cases {
		if got := churnClass(wd, c.base, c.path); got != c.want {
			t.Errorf("%s: churnClass(%q) = %q, want %q", c.name, c.path, got, c.want)
		}
	}
}

// TestValidateLabelsLineEndingChurn is issue #647 end to end: an outside file
// rewritten with CRLF is still outside and fails the owns check, but Churn
// (and the owns_checked event) labels it; a real outside edit is not
// labelled.
func TestValidateLabelsLineEndingChurn(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"true"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	for _, p := range []string{"b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x\ny\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "outside files"})
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	if err := AppendEvent(dir, Event{TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Base: base}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("x\r\ny\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x\nz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if res.OwnsOK {
		t.Errorf("OwnsOK = true, want false: the CRLF bytes changed")
	}
	if !slices.Contains(res.Outside, "b.txt") || !slices.Contains(res.Outside, "c.txt") {
		t.Errorf("Outside = %v, want b.txt and c.txt as bare paths", res.Outside)
	}
	want := map[string]string{"b.txt": churnLineEndings}
	if !reflect.DeepEqual(res.Churn, want) {
		t.Errorf("Churn = %v, want %v", res.Churn, want)
	}
	if res.ChurnBase != base {
		t.Errorf("ChurnBase = %q, want %q", res.ChurnBase, base)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var got map[string]string
	for _, e := range events {
		if e.Kind == "owns_checked" {
			got = e.Churn
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("owns_checked churn = %v, want %v", got, want)
	}
}
