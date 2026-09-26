package main

import (
	"bytes"
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
