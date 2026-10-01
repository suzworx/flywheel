package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outsideBrief is a lintable brief whose text carries body; gate is its one
// gate: line.
func outsideBrief(gate, body string) string {
	return "owns: a.go\nneeds: none\ngate: " + gate + "\n\n# TASK: x\n" + body + "\n## Checks\nAt most one write per response\nreport\n"
}

// outsideSeed writes seed.json in a second temp dir, outside any checkout,
// and returns its absolute path with forward slashes.
func outsideSeed(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seed.json")
	if err := os.WriteFile(p, []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	return filepath.ToSlash(p)
}

// outsideProblems is the problems of res that name a path outside the
// checkout.
func outsideProblems(res LintResult) []string {
	var out []string
	for _, p := range res.Problems {
		if strings.Contains(p, "outside the checkout") {
			out = append(out, p)
		}
	}
	return out
}

func outsideWant(tok string) string {
	return "brief names " + tok + ", outside the checkout; a worker cannot read it (#746): copy it into the repository, or carry repo-relative state with needs-state: <path> (copy)"
}

func TestLintOutsidePathAbsoluteReported(t *testing.T) {
	t.Parallel()
	co := t.TempDir()
	seed := outsideSeed(t)
	res := lintCheck(t, co, []string{"a.go"}, outsideBrief("true", "Read the seed at "+seed+" first; again: "+seed))
	got := outsideProblems(res)
	if len(got) != 1 || got[0] != outsideWant(seed) {
		t.Errorf("outside problems = %q, want exactly [%q]", got, outsideWant(seed))
	}
}

func TestLintOutsidePathBacktickedWithPeriodReported(t *testing.T) {
	t.Parallel()
	co := t.TempDir()
	seed := outsideSeed(t)
	res := lintCheck(t, co, []string{"a.go"}, outsideBrief("true", "Compare with `"+seed+"`."))
	got := outsideProblems(res)
	if len(got) != 1 || got[0] != outsideWant(seed) {
		t.Errorf("outside problems = %q, want exactly [%q]", got, outsideWant(seed))
	}
}

func TestLintOutsidePathInsideOrMissingQuiet(t *testing.T) {
	t.Parallel()
	co := t.TempDir()
	inside := filepath.ToSlash(filepath.Join(co, "a.go"))
	missing := filepath.ToSlash(filepath.Join(filepath.Dir(outsideSeed(t)), "nope.json"))
	res := lintCheck(t, co, []string{"a.go"}, outsideBrief("true", "Edit "+inside+" and never "+missing+", nor /dev/null or 1/2."))
	if got := outsideProblems(res); len(got) != 0 {
		t.Errorf("outside problems = %q, want none", got)
	}
	want(t, res, nil, nil)
}

func TestLintOutsidePathSiblingRelativeReported(t *testing.T) {
	t.Parallel()
	co := t.TempDir()
	seed := outsideSeed(t)
	rel, err := filepath.Rel(co, filepath.FromSlash(seed))
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "../") {
		t.Fatalf("seed %s is not a sibling of %s (rel %s)", seed, co, rel)
	}
	res := lintCheck(t, co, []string{"a.go"}, outsideBrief("true", "Load ("+rel+") as the seed."))
	got := outsideProblems(res)
	if len(got) != 1 || got[0] != outsideWant(rel) {
		t.Errorf("outside problems = %q, want exactly [%q]", got, outsideWant(rel))
	}
}

func TestLintOutsidePathGateLineQuiet(t *testing.T) {
	t.Parallel()
	co := t.TempDir()
	seed := outsideSeed(t)
	res := lintCheck(t, co, []string{"a.go"}, outsideBrief("test -s "+seed, "Nothing outside here."))
	if got := outsideProblems(res); len(got) != 0 {
		t.Errorf("outside problems = %q, want none", got)
	}
}
