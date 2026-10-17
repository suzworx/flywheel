package flywheel

import "testing"

// TestLintGateSequence is issue #857: a command followed by ; has its exit
// status discarded, so an earlier suite's failure passes the gate.
func TestLintGateSequence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, gate, word string
	}{
		{"semicolon", "npm run test:unit; npm run test:ui", "npm"},
		{"newline", "go vet ./...\ngo test ./...", "go"},
		{"cd counts", "cd web; npm test", "cd"},
		{"set -e", "set -e; a; b", ""},
		{"errexit", "set -o errexit; a; b", ""},
		{"and chain", "a && b", ""},
		{"or handled", "a || true; b", ""},
		{"echo first", "echo hi; go test ./...", ""},
		{"trailing semicolon", "go test ./...;", ""},
		{"gofmt loop", `for f in $(git ls-files -m); do [ -z "$(tr -d '\r' < "$f" | go run cmd/gofmt -l)" ] || { echo "UNFORMATTED $f"; exit 1; }; done`, ""},
		{"quoted semicolon", `echo "a; b"`, ""},
		{"inside substitution", "x=$(a; b) && c", ""},
		{"after and chain", "a && b; c", "b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := gateDiscardedStatus(c.gate); got != c.word {
				t.Errorf("gateDiscardedStatus(%q) = %q, want %q", c.gate, got, c.word)
			}
		})
	}
}

// TestLintGateSequenceWarning checks lint warns on a gate whose ; discards an
// exit status and not on the same gate chained with &&.
func TestLintGateSequenceWarning(t *testing.T) {
	t.Parallel()
	const warning = `gate 2 runs npm then continues after ';': npm's exit status is discarded, so its failure passes the gate; chain with && or start the gate with "set -e;"`
	brief := func(gate string) string {
		return "owns: a.go\nneeds: none\ngate: true\ngate: " + gate + "\n\n# TASK: x\n## Checks\nAt most one write per response\n"
	}
	res := lintCheck(t, t.TempDir(), []string{"a.go"}, brief("npm run test:unit; npm run test:ui"))
	want(t, res, nil, []string{warning})
	res = lintCheck(t, t.TempDir(), []string{"a.go"}, brief("npm run test:unit && npm run test:ui"))
	want(t, res, nil, nil)
}
