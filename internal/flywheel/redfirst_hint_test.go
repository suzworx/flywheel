package flywheel

import (
	"strings"
	"testing"
)

// TestRedFirstWorkerWrittenTest checks that both red-first refusals tell a lead
// what to gate when the worker writes the regression test (issue #889).
func TestRedFirstWorkerWrittenTest(t *testing.T) {
	t.Parallel()
	const gate = "go test ./x"
	fix := BriefHeader{Kind: "fix", Gates: []string{gate}}
	dispatched := Event{TS: "2026-09-12T02:00:00Z", Task: "T1", Kind: "dispatched"}
	cases := []struct {
		name   string
		events []Event
	}{
		{"no probes", nil},
		{"every probe passed", []Event{redFirstProbe("2026-09-12T01:00:00Z", gate, 0), dispatched}},
	}
	for _, c := range cases {
		r := redFirstRefusal(c.events, "T1", fix, true)
		if r == nil {
			t.Errorf("%s: redFirstRefusal() = nil, want a red-first refusal", c.name)
			continue
		}
		for _, want := range []string{"reproduces the defect", "test -f", "--probe --task T1"} {
			if !strings.Contains(r.Fix, want) {
				t.Errorf("%s: redFirstRefusal().Fix = %q, want it to contain %q", c.name, r.Fix, want)
			}
		}
	}
}
