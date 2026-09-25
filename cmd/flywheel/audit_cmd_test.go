package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuditReleaseCmdUsageErrors checks `flywheel audit --release`'s usage
// errors (issue #420) exit 2 and record nothing.
func TestAuditReleaseCmdUsageErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"task id", []string{"--release", "0.2.0", "--session", "aud", "T1"}, "cannot be used with"},
		{"sample", []string{"--release", "0.2.0", "--session", "aud", "--sample", "0.5"}, "cannot be used with"},
		{"first article", []string{"--release", "0.2.0", "--session", "aud", "--first-article"}, "cannot be used with"},
		{"wave", []string{"--release", "0.2.0", "--session", "aud", "--wave"}, "cannot be used with"},
		{"list", []string{"--release", "0.2.0", "--session", "aud", "--list"}, "cannot be used with"},
		{"no session", []string{"--release", "0.2.0"}, "--session is required"},
		{"artifact alone", []string{"--release", "0.2.0", "--session", "aud", "--artifact", "a.zip"}, "go together"},
		{"checksums alone", []string{"--release", "0.2.0", "--session", "aud", "--checksums", "c.txt"}, "go together"},
		{"min-recall alone", []string{"--release", "0.2.0", "--session", "aud", "--min-recall", "0.5"}, "needs --calibration"},
		{"bad flag", []string{"--release", "0.2.0", "--session", "aud", "--nope"}, "nope"},
	}
	for _, tc := range cases {
		var out, errb strings.Builder
		args := append(append([]string{}, tc.args...), "--dir", dir)
		if got := auditReleaseMain(args, &out, &errb); got != 2 {
			t.Errorf("%s: exit %d, want 2 (stderr %q)", tc.name, got, errb.String())
		}
		if !strings.Contains(errb.String(), tc.want) || !strings.Contains(errb.String(), "--release VERSION") {
			t.Errorf("%s: stderr %q, want %q and the --release usage", tc.name, errb.String(), tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "events.jsonl")); err == nil {
		t.Error("a usage error recorded an event")
	}
}

// TestAuditReleaseCmdFlags checks the release flags bind, --notes repeats,
// --min-recall defaults to -1 and `flywheel help audit` names every flag.
func TestAuditReleaseCmdFlags(t *testing.T) {
	t.Parallel()
	fs, o := auditFlags()
	if o.minRecall != -1 {
		t.Errorf("--min-recall default = %v, want -1", o.minRecall)
	}
	if err := fs.Parse([]string{"--release", "v0.2.0", "--prev", "v0.1.0", "--artifact", "a.zip", "--checksums", "c.txt",
		"--notes", "n1.md", "--notes", "n2.md", "--calibration", "cal.md", "--min-recall", "0.7"}); err != nil {
		t.Fatal(err)
	}
	if o.release != "v0.2.0" || o.prev != "v0.1.0" || o.artifact != "a.zip" || o.checksums != "c.txt" ||
		len(o.notes) != 2 || o.notes[1] != "n2.md" || o.calibration != "cal.md" || o.minRecall != 0.7 {
		t.Errorf("parsed = %+v", *o)
	}
	help := helpText("audit")
	for _, f := range []string{"-release", "-prev", "-artifact", "-checksums", "-notes", "-calibration", "-min-recall", "flywheel audit --release VERSION"} {
		if !strings.Contains(help, f) {
			t.Errorf("help audit does not name %s:\n%s", f, help)
		}
	}
}
