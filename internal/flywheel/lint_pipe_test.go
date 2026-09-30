package flywheel

import "testing"

// TestGateFilterMasks is issue #704: gates run under bash without pipefail,
// so a pipeline ending in a filter takes the filter's exit status.
func TestGateFilterMasks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, gate, filter string
	}{
		{"tail", "go test ./... | tail -5", "tail"},
		{"quoted pipe is not a pipe", `runner | grep -E 'PASS|FAIL'`, "grep"},
		{"pipefail", "set -o pipefail; go test ./... | tail -5", ""},
		{"or", "a || b", ""},
		{"pipe stderr", "a |& grep x", "grep"},
		{"inside substitution", "out=$(go test ./... | tail -1); [ $? = 0 ]", ""},
		{"inside backticks", "out=`go test ./... | tail -1`; [ $? = 0 ]", ""},
		{"echo captured", `echo "$out" | grep -c ok`, ""},
		{"printf captured", `printf '%s\n' "$out" | wc -l`, ""},
		{"and chain", "go build ./... && go vet ./...", ""},
		{"sort redirected", "x | sort -u > f.txt", "sort"},
		{"assignment and path", "FOO=1 make | /usr/bin/tee log", "tee"},
		{"pipestatus", `go test ./... | tail -5; test "${PIPESTATUS[0]}" = 0`, ""},
		{"quoted script", "node -e 'a|b'", ""},
		{"double quoted pipe", `bash -c "a | grep x"`, ""},
		{"later command", "go build ./... && go test ./... 2>&1 | grep -v cached", "grep"},
		{"filter not last", "grep x f | node check.js", ""},
		{"backgrounded redirect", "go test ./... >&2 | head", "head"},
		{"windows filter", `go test ./... | findstr.exe ok`, "findstr"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := gateMaskingFilter(c.gate); got != c.filter {
				t.Errorf("gateMaskingFilter(%q) = %q, want %q", c.gate, got, c.filter)
			}
			if got := gateFilterMasks(c.gate); got != (c.filter != "") {
				t.Errorf("gateFilterMasks(%q) = %v, want %v", c.gate, got, c.filter != "")
			}
		})
	}
}

// TestLintGateFilterMasks checks lint warns on a gate piped into a filter and
// not on the same gate started with set -o pipefail.
func TestLintGateFilterMasks(t *testing.T) {
	t.Parallel()
	const warning = `gate 2 pipes into tail: the gate's exit status is tail's, not the command's; start it with "set -o pipefail;" or drop the filter`
	brief := func(gate string) string {
		return "owns: a.go\nneeds: none\ngate: true\ngate: " + gate + "\n\n# TASK: x\n## Checks\nAt most one write per response\n"
	}
	res := lintCheck(t, t.TempDir(), []string{"a.go"}, brief("go test ./... | tail -5"))
	want(t, res, nil, []string{warning})
	res = lintCheck(t, t.TempDir(), []string{"a.go"}, brief("set -o pipefail; go test ./... | tail -5"))
	want(t, res, nil, nil)
}
