package main

import "testing"

// TestAttestFlagsInterleaved checks parseArgs plus attestFlags give the same
// task and flag values in the task-first, task-last and interleaved forms.
func TestAttestFlagsInterleaved(t *testing.T) {
	forms := [][]string{
		{"T1", "--dir", "X", "--commit", "abc1234", "--evidence", "E", "--session", "S"},
		{"--dir", "X", "--commit", "abc1234", "--evidence", "E", "--session", "S", "T1"},
		{"--dir", "X", "T1", "--commit", "abc1234", "--evidence", "E", "--session", "S"},
	}
	for _, args := range forms {
		fs, o := attestFlags()
		pos, err := parseArgs(fs, args)
		if err != nil {
			t.Errorf("attestFlags parseArgs(%v): unexpected error: %v", args, err)
			continue
		}
		if len(pos) != 1 || pos[0] != "T1" {
			t.Errorf("attestFlags parseArgs(%v) positionals = %v, want [T1]", args, pos)
		}
		want := attestOptions{dir: "X", commit: "abc1234", evidence: "E", session: "S"}
		if *o != want {
			t.Errorf("attestFlags parseArgs(%v) = %#v, want %#v", args, *o, want)
		}
	}
}

// TestInspectCommitFlagBinds checks inspect's --commit reaches the bound
// options (issue #367).
func TestInspectCommitFlagBinds(t *testing.T) {
	fs, o := inspectFlags()
	if err := fs.Parse([]string{"--commit", "abc1234"}); err != nil {
		t.Fatalf("inspectFlags: %v", err)
	}
	if o.commit != "abc1234" {
		t.Errorf("commit = %q, want abc1234", o.commit)
	}
}
