package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func TestStaffingStaffWarnsOnDifferentSession(t *testing.T) {
	dir := t.TempDir()

	cfg := flywheel.Config{
		Version: 1,
		Workers: []flywheel.Worker{{Name: "default", Adapter: "claude", Model: "claude-opus-5"}},
		Staffing: &flywheel.StaffingConfig{
			Lead: &flywheel.RoleConfig{Session: "configured-session"},
		},
	}
	if err := flywheel.WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	old := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	runStaff([]string{"--dir", dir, "--role", "lead", "--session", "different-session", "--model", "m1"})

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "warning:") {
		t.Fatalf("expected warning in stderr, got: %s", output)
	}
	if !strings.Contains(output, "configured-session") || !strings.Contains(output, "different-session") {
		t.Fatalf("warning should name both sessions: %s", output)
	}
}
