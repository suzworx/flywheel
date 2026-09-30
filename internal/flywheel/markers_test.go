package flywheel

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestConflictMarkersLFCRLFBinaryDeleted: markers are found in LF and CRLF
// files alike, a bare "<<<<<<<" counts, near-misses do not, and binary and
// missing files are skipped (issue #698).
func TestConflictMarkersLFCRLFBinaryDeleted(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	conflict := "a\n<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> other\n"
	files := map[string]string{
		"lf.txt":       conflict,
		"sub/crlf.txt": strings.ReplaceAll(conflict, "\n", "\r\n"),
		"alone.txt":    "<<<<<<<\r\n>>>>>>>\n",
		"near.txt":     "<<<<<<<x\n==========\n=======x\n >>>>>>> a\n>>>>>>>x\n",
		"bin.dat":      "\x00\n<<<<<<< HEAD\n",
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(wd, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wd, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := conflictMarkers(wd, []string{"lf.txt", "sub/crlf.txt", "alone.txt", "near.txt", "bin.dat", "gone.txt", "lf.txt"})
	want := []string{"alone.txt:1", "alone.txt:2", "lf.txt:2", "lf.txt:4", "lf.txt:6", "sub/crlf.txt:2", "sub/crlf.txt:4", "sub/crlf.txt:6"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("conflictMarkers = %q, want %q", got, want)
	}
}

// TestConflictMarkersCap: every marker line counts, but the list stops at 20
// entries plus one "... and N more".
func TestConflictMarkersCap(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "many.txt"), []byte(strings.Repeat("=======\n", 25)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := conflictMarkers(wd, []string{"many.txt"})
	if len(got) != 21 || got[0] != "many.txt:1" || got[19] != "many.txt:20" || got[20] != "... and 5 more" {
		t.Errorf("conflictMarkers = %q, want 20 entries and \"... and 5 more\"", got)
	}
}

// TestParseEventsMarkerCRLF: the event log shares the line predicate, so a
// CRLF marker line is still refused as a conflict.
func TestParseEventsMarkerCRLF(t *testing.T) {
	t.Parallel()
	for _, line := range []string{"=======\r\n", "<<<<<<< HEAD\r\n", ">>>>>>>\n"} {
		_, err := ParseEvents(strings.NewReader(line), false)
		if err == nil || !strings.Contains(err.Error(), "unresolved merge conflict") {
			t.Errorf("ParseEvents(%q) error = %v, want an unresolved merge conflict", line, err)
		}
	}
}

// TestValidateMarkerCRLFFailsAndInspectRefused: a validate on a tree whose
// owned file holds CRLF conflict markers fails with Markers set, the
// owns_checked event carries them, and a pass on that reading is refused
// naming them.
func TestValidateMarkerCRLFFailsAndInspectRefused(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")
	body := "package x\r\n<<<<<<< HEAD\r\nvar a = 1\r\n=======\r\nvar a = 2\r\n>>>>>>> other\r\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	want := []string{"a.go:2", "a.go:4", "a.go:6"}
	if res.OK() || !res.GatesOK || !res.OwnsOK || !reflect.DeepEqual(res.Markers, want) {
		t.Fatalf("ValidateTask() = OK %v gates %v owns %v markers %q, want a failure on markers %q", res.OK(), res.GatesOK, res.OwnsOK, res.Markers, want)
	}
	var owns *Event
	for _, e := range mustEvents(t, dir) {
		if e.Kind == "owns_checked" {
			e := e
			owns = &e
		}
	}
	if owns == nil || !reflect.DeepEqual(owns.Markers, want) || ownsReadingClean(*owns) {
		t.Fatalf("owns_checked = %+v, want markers %q and not clean", owns, want)
	}
	err = InspectTask(dir, "T1", InspectOptions{Dir: dir, Verdict: "pass", Session: "i1"})
	if err == nil {
		t.Fatal("InspectTask() accepted a pass on a reading with conflict markers")
	}
	if got := refusalRule(t, err); got != "T3" || !strings.Contains(err.Error(), "a.go:2") {
		t.Errorf("InspectTask() = %v (rule %s), want a T3 refusal naming a.go:2", err, got)
	}
}
