package flywheel

import (
	"strings"
	"testing"
)

// TestGateStateful checks which gates count as end-to-end or integration
// checks (issue #636).
func TestGateStateful(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		gate string
		want bool
	}{
		{"npx playwright test", true},
		{"npm run test:e2e", true},
		{"go test -tags=integration ./...", true},
		{"node_modules/.bin/cypress run", true},
		{"npx wdio run wdio.conf.js", true},
		{`bash -c "detox test"`, true},
		{"vitest run --project=e2e", true},
		{"go test ./...", false},
		{"npm test", false},
		{"vitest run src/", false},
	} {
		t.Run(tc.gate, func(t *testing.T) {
			t.Parallel()
			if got := gateStateful(tc.gate); got != tc.want {
				t.Errorf("gateStateful(%q) = %v, want %v", tc.gate, got, tc.want)
			}
		})
	}
}

// TestGateResetsState checks which gates mark a fresh stack (issue #636).
func TestGateResetsState(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		gate string
		want bool
	}{
		{"docker compose down -v && docker compose up -d && npm run test:e2e", true},
		{"npm run db:reset && npx playwright test", true},
		{"docker run --rm pg", true},
		{"DROPDB test && createdb test", true},
		{"npx playwright test", false},
	} {
		t.Run(tc.gate, func(t *testing.T) {
			t.Parallel()
			if got := gateResetsState(tc.gate); got != tc.want {
				t.Errorf("gateResetsState(%q) = %v, want %v", tc.gate, got, tc.want)
			}
		})
	}
}

// TestLintFreshState checks lint warns once on an end-to-end gate with no gate
// that resets state, and not when one does or no gate is end-to-end.
func TestLintFreshState(t *testing.T) {
	t.Parallel()
	const warning = `gate 2 runs an end-to-end or integration check and no gate resets its state: a spec that passes on the worker's seeded data can fail on CI's fresh stack; add a gate that runs it against a fresh stack (e.g. "docker compose down -v && docker compose up -d && <spec>") and have the spec seed its own data`
	brief := func(gates ...string) string {
		b := "owns: a.go\nneeds: none\n"
		for _, g := range gates {
			b += "gate: " + g + "\n"
		}
		return b + "\n# TASK: x\n## Checks\nAt most one write per response\n"
	}
	fresh := func(res LintResult) []string {
		var out []string
		for _, w := range res.Warnings {
			if strings.Contains(w, "runs an end-to-end or integration check") {
				out = append(out, w)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		gates []string
		want  []string
	}{
		{"e2e alone", []string{"go build ./...", "npx playwright test"}, []string{warning}},
		{"with reset", []string{"go build ./...", "npx playwright test", "npm run db:reset"}, nil},
		{"no e2e", []string{"go test ./..."}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := fresh(lintCheck(t, t.TempDir(), []string{"a.go"}, brief(tc.gates...)))
			if len(got) != len(tc.want) || len(got) == 1 && got[0] != tc.want[0] {
				t.Errorf("fresh-state warnings = %q, want %q", got, tc.want)
			}
		})
	}
}
