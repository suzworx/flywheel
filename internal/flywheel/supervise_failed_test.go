package flywheel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSuperviseFailedRemeasuredAfterOwnedFix checks that supervise re-measures
// a failed unit after an owned file is fixed.
func TestSuperviseFailedRemeasuredAfterOwnedFix(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"grep -q 'package a' a.go"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result1, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() first pass error = %v", err)
	}
	if len(result1.Measured) != 1 {
		t.Fatalf("first pass Measured len = %d, want 1", len(result1.Measured))
	}
	if result1.Measured[0].OK != false {
		t.Errorf("first pass OK = %v, want false (grep should fail)", result1.Measured[0].OK)
	}

	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}

	result2, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() second pass error = %v", err)
	}
	if len(result2.Measured) != 1 {
		t.Errorf("second pass Measured len = %d, want 1", len(result2.Measured))
	}
	if len(result2.Measured) > 0 && result2.Measured[0].Task != "T1" {
		t.Errorf("second pass Task = %q, want T1", result2.Measured[0].Task)
	}
	if len(result2.Measured) > 0 && result2.Measured[0].OK != true {
		t.Errorf("second pass OK = %v, want true (grep matches now)", result2.Measured[0].OK)
	}
}

// TestSuperviseFailedUnchangedNotRemeasured checks that supervise does not
// re-measure a failed unit when its owned files haven't changed.
func TestSuperviseFailedUnchangedNotRemeasured(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"grep -q 'package a' a.go"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result1, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() first pass error = %v", err)
	}
	if len(result1.Measured) != 1 {
		t.Fatalf("first pass Measured len = %d, want 1", len(result1.Measured))
	}
	if result1.Measured[0].OK != false {
		t.Errorf("first pass OK = %v, want false (grep should fail)", result1.Measured[0].OK)
	}

	result2, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() second pass error = %v", err)
	}
	if len(result2.Measured) != 0 {
		t.Errorf("second pass Measured len = %d, want 0 (file unchanged)", len(result2.Measured))
	}
}

// TestSuperviseFailedEditOutsideOwnsNotRemeasured checks that supervise does
// not re-measure a failed unit when only files outside its owns changed.
func TestSuperviseFailedEditOutsideOwnsNotRemeasured(t *testing.T) {
	t.Parallel()
	dir, err := initTask(t, []string{"grep -q 'package a' a.go"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	logFinished(t, dir, "T1", "w1")

	result1, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() first pass error = %v", err)
	}
	if len(result1.Measured) != 1 {
		t.Fatalf("first pass Measured len = %d, want 1", len(result1.Measured))
	}
	if result1.Measured[0].OK != false {
		t.Errorf("first pass OK = %v, want false (grep should fail)", result1.Measured[0].OK)
	}

	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatalf("write other.txt: %v", err)
	}

	result2, err := Supervise(dir)
	if err != nil {
		t.Fatalf("Supervise() second pass error = %v", err)
	}
	if len(result2.Measured) != 0 {
		t.Errorf("second pass Measured len = %d, want 0 (edit outside owns)", len(result2.Measured))
	}
}
