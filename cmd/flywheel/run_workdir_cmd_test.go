package main

import (
	"strings"
	"testing"
)

// TestRunWorkdirFlag checks run's --workdir binds to the option and is in the
// usage line (issue #545).
func TestRunWorkdirFlag(t *testing.T) {
	t.Parallel()
	fs, o := runFlags()
	if err := fs.Parse([]string{"--workdir", "X"}); err != nil || o.workdir != "X" {
		t.Fatalf("--workdir parse: %v, workdir=%q", err, o.workdir)
	}
	var b strings.Builder
	runUsage(&b)
	if !strings.Contains(b.String(), "[--workdir PATH]") {
		t.Errorf("usage = %q, want [--workdir PATH]", b.String())
	}
}
