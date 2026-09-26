package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestLintOutput pins writeLint: problems then warnings, each labelled, and a
// count line that always ends the output with correct singular and plural.
func TestLintOutput(t *testing.T) {
	t.Parallel()
	const brief = "b.txt"
	cases := []struct {
		name string
		res  flywheel.LintResult
		want string
	}{
		{
			name: "problems only",
			res:  flywheel.LintResult{Problems: []string{"no owns: line", "no gate: line"}},
			want: "lint: b.txt: problem: no owns: line\n" +
				"lint: b.txt: problem: no gate: line\n" +
				"lint: b.txt: 2 problems, 0 warnings\n",
		},
		{
			name: "warnings only",
			res:  flywheel.LintResult{Warnings: []string{"no full-suite gate", "owns misses importer tests"}},
			want: "lint: b.txt: warning: no full-suite gate\n" +
				"lint: b.txt: warning: owns misses importer tests\n" +
				"lint: b.txt: 0 problems, 2 warnings\n",
		},
		{
			name: "both",
			res: flywheel.LintResult{
				Problems: []string{"no gate: line", "no # TASK goal"},
				Warnings: []string{"no full-suite gate", "owns misses importer tests"},
			},
			want: "lint: b.txt: problem: no gate: line\n" +
				"lint: b.txt: problem: no # TASK goal\n" +
				"lint: b.txt: warning: no full-suite gate\n" +
				"lint: b.txt: warning: owns misses importer tests\n" +
				"lint: b.txt: 2 problems, 2 warnings\n",
		},
		{
			name: "none",
			res:  flywheel.LintResult{},
			want: "lint: b.txt: 0 problems, 0 warnings\n",
		},
		{
			name: "singular",
			res:  flywheel.LintResult{Problems: []string{"no gate: line"}, Warnings: []string{"no full-suite gate"}},
			want: "lint: b.txt: problem: no gate: line\n" +
				"lint: b.txt: warning: no full-suite gate\n" +
				"lint: b.txt: 1 problem, 1 warning\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			writeLint(&b, brief, tc.res)
			if got := b.String(); got != tc.want {
				t.Errorf("writeLint output:\n%s\nwant:\n%s", got, tc.want)
			}
			if tc.name == "warnings only" && bytes.Contains(b.Bytes(), []byte("problem:")) {
				t.Errorf("warnings-only output contains problem:\n%s", b.String())
			}
		})
	}
}

// runLintHelperEnv carries runLint's arguments to the re-executed test
// binary, separated by \x1f, so a test can observe its output and exit status.
const runLintHelperEnv = "FLYWHEEL_TEST_RUNLINT_ARGS"

// runLintProcess runs runLint(args) in a child copy of the test binary and
// returns its stderr and exit code.
func runLintProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLintProbe$")
	cmd.Env = append(os.Environ(), runLintHelperEnv+"="+strings.Join(args, "\x1f"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var e *exec.ExitError
	if errors.As(err, &e) {
		return stderr.String(), e.ExitCode()
	}
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	return stderr.String(), 0
}

// lintProbeBrief writes a structurally clean brief with gates to dir/name.
func lintProbeBrief(t *testing.T, dir, name string, gates ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("owns: out.txt (new)\nneeds: none\n")
	for _, g := range gates {
		fmt.Fprintf(&b, "gate: %s\n", g)
	}
	b.WriteString("\n# TASK: probe\n\n## Checks\nrun the gates.\n\n## Rules\n- At most one write per response.\n")
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	return p
}

// TestLintProbe checks lint --probe prints one line per gate, counts a
// failing gate as a warning and a cannot-start gate as a problem (exit 1),
// and that without --probe no gate is ever run (issue #544).
func TestLintProbe(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runLintHelperEnv); ok {
		var args []string
		if v != "" {
			args = strings.Split(v, "\x1f")
		}
		runLint(args)
		os.Exit(0)
	}
	dir := t.TempDir()
	brief := lintProbeBrief(t, dir, "probe.md", "exit 0", "echo oops; exit 3", "definitely-not-a-command-xyz")
	base, code := runLintProcess(t, brief, "--dir", dir)
	if code != 0 || strings.Contains(base, ": gate ") {
		t.Fatalf("lint without --probe: exit %d, stderr %q; want exit 0 and no gate lines", code, base)
	}
	var warn int
	if _, err := fmt.Sscanf(base[strings.LastIndex(strings.TrimSpace(base), "\n")+1:],
		"lint: "+brief+": 0 problems, %d warning", &warn); err != nil {
		t.Fatalf("parse count line of %q: %v", base, err)
	}
	out, code := runLintProcess(t, brief, "--probe", "--dir", dir)
	if code != 1 {
		t.Errorf("lint --probe exit %d, want 1\n%s", code, out)
	}
	p := "lint: " + brief + ": "
	for _, want := range []string{
		p + "gate 1: pass (",
		p + "gate 2: fail (exit 3): oops\n",
		p + "gate 3: error: ",
		p + "warning: gate 2 fails on the base tree (exit 3): oops; a test-first gate is expected to fail, a wrong runner or path is not\n",
		p + "problem: gate 3 cannot start (exit ",
		p + "1 problem, " + plural(warn+1, "warning") + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lint --probe output lacks %q:\n%s", want, out)
		}
	}
	marker := lintProbeBrief(t, dir, "marker.md", "echo x > marker; exit 1")
	if out, code := runLintProcess(t, marker, "--dir", dir); code != 0 || strings.Contains(out, ": gate ") {
		t.Errorf("lint without --probe: exit %d, stderr %q; want exit 0 and no gate lines", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Errorf("lint without --probe ran a gate: marker exists")
	}
}
