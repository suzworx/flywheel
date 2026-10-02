package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const stageLine = "stage: staging/claude/ -> .claude/"

// TestUnstage checks flywheel unstage (issue #781): it removes the staging
// source and its now-empty directories, keeps the destination and records one
// unstaged event; a second call records nothing more; a destination changed
// since validate refuses with rule stage and removes nothing.
func TestUnstage(t *testing.T) {
	t.Parallel()
	t.Run("removes", func(t *testing.T) {
		t.Parallel()
		dir := stageTask(t, "staging/claude/", stageLine)
		if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		files, err := Unstage(dir, "T1", UnstageOptions{})
		if err != nil || len(files) != 1 || files[0].To != ".claude/agents/x.md" {
			t.Fatalf("Unstage() = %+v, %v; want the staged file", files, err)
		}
		for _, p := range []string{"staging/claude/agents/x.md", "staging/claude/agents", "staging/claude"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); !os.IsNotExist(err) {
				t.Errorf("%s still exists (%v), want it removed", p, err)
			}
		}
		if b, err := os.ReadFile(filepath.Join(dir, ".claude", "agents", "x.md")); err != nil || string(b) != "agent\n" {
			t.Errorf(".claude/agents/x.md = %q, %v; want it kept", b, err)
		}
		if ev := kindEvents(t, dir, "unstaged"); len(ev) != 1 || !slices.Equal(ev[0].Staged, files) {
			t.Errorf("unstaged events = %+v, want one carrying %+v", ev, files)
		}
		if again, err := Unstage(dir, "T1", UnstageOptions{}); err != nil || !slices.Equal(again, files) {
			t.Errorf("second Unstage() = %+v, %v; want the files and no error", again, err)
		}
		if ev := kindEvents(t, dir, "unstaged"); len(ev) != 1 {
			t.Errorf("unstaged events after a second call = %d, want 1", len(ev))
		}
	})
	t.Run("changed", func(t *testing.T) {
		t.Parallel()
		dir := stageTask(t, "staging/claude/", stageLine)
		if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude", "agents", "x.md"), []byte("edited\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Unstage(dir, "T1", UnstageOptions{})
		var rr *RuleRefusal
		if !errors.As(err, &rr) || rr.Rule != "stage" {
			t.Fatalf("Unstage() error = %v, want a stage RuleRefusal", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "staging", "claude", "agents", "x.md")); err != nil {
			t.Errorf("source removed on refusal: %v", err)
		}
		if ev := kindEvents(t, dir, "unstaged"); len(ev) != 0 {
			t.Errorf("unstaged events = %+v, want none", ev)
		}
	})
	t.Run("resumes", func(t *testing.T) {
		t.Parallel()
		dir := stageTask(t, "staging/claude/", stageLine)
		if err := os.WriteFile(filepath.Join(dir, "staging", "claude", "agents", "y.md"), []byte("other\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
			t.Fatalf("ValidateTask() error = %v", err)
		}
		// An earlier run removed x.md and stopped before recording anything.
		if err := os.Remove(filepath.Join(dir, "staging", "claude", "agents", "x.md")); err != nil {
			t.Fatal(err)
		}
		files, err := Unstage(dir, "T1", UnstageOptions{})
		if err != nil || len(files) != 2 {
			t.Fatalf("Unstage() = %+v, %v; want both staged files", files, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "staging", "claude", "agents", "y.md")); !os.IsNotExist(err) {
			t.Errorf("staging/claude/agents/y.md still exists (%v), want it removed", err)
		}
		for _, p := range []string{"x.md", "y.md"} {
			if _, err := os.Stat(filepath.Join(dir, ".claude", "agents", p)); err != nil {
				t.Errorf(".claude/agents/%s: %v; want it kept", p, err)
			}
		}
		if ev := kindEvents(t, dir, "unstaged"); len(ev) != 1 || !slices.Equal(ev[0].Staged, files) {
			t.Errorf("unstaged events = %+v, want one carrying %+v", ev, files)
		}
	})
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		dir := stageTask(t, "staging/claude/", stageLine)
		if _, err := Unstage(dir, "T1", UnstageOptions{}); err == nil {
			t.Error("Unstage() with no staged event succeeded, want an error")
		}
	})
}

// TestStagedOwnsAfterUnstage checks ownership survives unstage (issue #781):
// the next pass keeps the destination owned through the recorded source and
// notes no staging left; without the owns entry the destination is outside.
func TestStagedOwnsAfterUnstage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		owns    string
		outside bool
	}{{"staging/claude/", false}, {"a.go", true}} {
		t.Run(tc.owns, func(t *testing.T) {
			t.Parallel()
			dir := stageTask(t, tc.owns, stageLine)
			if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir}); err != nil {
				t.Fatalf("ValidateTask() error = %v", err)
			}
			if _, err := Unstage(dir, "T1", UnstageOptions{}); err != nil {
				t.Fatalf("Unstage() error = %v", err)
			}
			res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
			if err != nil {
				t.Fatalf("second ValidateTask() error = %v", err)
			}
			if got := slices.Contains(res.Outside, ".claude/agents/x.md"); got != tc.outside {
				t.Errorf("Outside = %q, want .claude/agents/x.md outside %v", res.Outside, tc.outside)
			}
			if len(res.StagingLeft) != 0 {
				t.Errorf("StagingLeft = %q, want none after unstage", res.StagingLeft)
			}
		})
	}
}

// TestValidateStageWarning checks a pass that staged a file names its From in
// StagingLeft (issue #781).
func TestValidateStageWarning(t *testing.T) {
	t.Parallel()
	dir := stageTask(t, "staging/claude/", stageLine)
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if want := []string{"staging/claude/agents/x.md"}; !slices.Equal(res.StagingLeft, want) || !res.OK() {
		t.Errorf("StagingLeft = %q, OK %v; want %q and a passing reading", res.StagingLeft, res.OK(), want)
	}
}
