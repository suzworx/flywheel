package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hdgDir is a temp dir with a go.mod, so the default go full-suite pattern
// applies.
func hdgDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeGh returns a gh func answering out/err and counting its calls.
func fakeGh(out string, err error, calls *int) func(args ...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
		*calls++
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}
}

// hdgClean runs hostDependentGateFindings with a clean tree.
func hdgClean(dir string, lc *LintConfig, owns, gates []string, probes []GateProbe, commit string, on bool, gh func(args ...string) ([]byte, error)) (problems, warnings []string) {
	return hostDependentGateFindings(dir, lc, owns, gates, probes, commit, on, gh, func(string) ([]string, error) { return nil, nil })
}

func TestHostDependentGateDirtyTreeWarns(t *testing.T) {
	t.Parallel()
	calls := 0
	dirty := func(string) ([]string, error) { return []string{"internal/x/x_test.go", "README.md"}, nil }
	probs, warns := hostDependentGateFindings(hdgDir(t), nil, []string{hdgOwnsPth}, []string{hdgFull},
		[]GateProbe{{N: 1, RC: 1}}, hdgSHA, true, fakeGh(ciGreen, nil, &calls), dirty)
	if len(probs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "uncommitted") ||
		!strings.Contains(warns[0], "2 uncommitted path(s), e.g. internal/x/x_test.go") || !strings.Contains(warns[0], hdgSHA[:12]) {
		t.Errorf("problems %q, warnings %q; want no problem and one uncommitted-tree warning", probs, warns)
	}
	if calls != 0 {
		t.Errorf("gh calls = %d, want 0", calls)
	}
}

func TestHostDependentGateDirtyErrorWarns(t *testing.T) {
	t.Parallel()
	calls := 0
	dirty := func(string) ([]string, error) { return nil, errors.New("git status failed") }
	probs, warns := hostDependentGateFindings(hdgDir(t), nil, []string{hdgOwnsPth}, []string{hdgFull},
		[]GateProbe{{N: 1, RC: 1}}, hdgSHA, true, fakeGh(ciGreen, nil, &calls), dirty)
	if len(probs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "git status failed") {
		t.Errorf("problems %q, warnings %q; want no problem and one warning naming the error", probs, warns)
	}
	if calls != 0 {
		t.Errorf("gh calls = %d, want 0", calls)
	}
}

const (
	hdgSHA     = "0123456789abcdef0123456789abcdef01234567"
	ciGreen    = `{"check_runs":[{"name":"test","status":"completed","conclusion":"success"}]}`
	ciPending  = `{"check_runs":[{"name":"test","status":"in_progress","conclusion":null}]}`
	ciNoRuns   = `{"check_runs":[]}`
	ciRed      = `{"check_runs":[{"name":"test","status":"completed","conclusion":"success"},{"name":"lint","status":"completed","conclusion":"failure"}]}`
	hdgFull    = "go test -count=1 ./..."
	hdgNarrow  = "go test -run TestX ./internal/x/"
	hdgOwnsPth = "internal/x/x.go"
)

func TestHostDependentGateFullSuiteGreen(t *testing.T) {
	t.Parallel()
	calls := 0
	gates := []string{"go build ./...", hdgFull}
	probes := []GateProbe{{N: 1}, {N: 2, RC: 1}}
	probs, warns := hdgClean(hdgDir(t), nil, []string{hdgOwnsPth}, gates, probes, hdgSHA, true, fakeGh(ciGreen, nil, &calls))
	if len(probs) != 1 || !strings.Contains(probs[0], "host-dependent-gate") || !strings.Contains(probs[0], "gate 2 ") ||
		!strings.Contains(probs[0], hdgSHA[:12]) || strings.Contains(probs[0], hdgSHA[:13]) {
		t.Errorf("problems = %q, want one host-dependent-gate problem for gate 2", probs)
	}
	if len(warns) != 0 || calls != 1 {
		t.Errorf("warnings = %q, gh calls = %d; want none and 1", warns, calls)
	}
}

func TestHostDependentGateTargetedGateIgnored(t *testing.T) {
	t.Parallel()
	calls := 0
	probs, warns := hdgClean(hdgDir(t), nil, []string{hdgOwnsPth}, []string{hdgNarrow, hdgFull},
		[]GateProbe{{N: 1, RC: 1}, {N: 2}}, hdgSHA, true, fakeGh(ciGreen, nil, &calls))
	if len(probs) != 0 || len(warns) != 0 || calls != 0 {
		t.Errorf("problems %q, warnings %q, gh calls %d; want none, none, 0", probs, warns, calls)
	}
}

func TestHostDependentGateCIUnknownWarns(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]string{"pending": ciPending, "no runs": ciNoRuns} {
		calls := 0
		probs, warns := hdgClean(hdgDir(t), nil, []string{hdgOwnsPth}, []string{hdgFull},
			[]GateProbe{{N: 1, RC: 1}}, hdgSHA, true, fakeGh(out, nil, &calls))
		if len(probs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "CI could not confirm") {
			t.Errorf("%s: problems %q, warnings %q; want no problem and one CI warning", name, probs, warns)
		}
	}
}

func TestHostDependentGateGhErrorWarns(t *testing.T) {
	t.Parallel()
	calls := 0
	probs, warns := hdgClean(hdgDir(t), nil, []string{hdgOwnsPth}, []string{hdgFull},
		[]GateProbe{{N: 1, RC: 1}}, hdgSHA, true, fakeGh("", errors.New("not logged in"), &calls))
	if len(probs) != 0 || len(warns) != 1 || !strings.Contains(warns[0], "not logged in") || !strings.Contains(warns[0], "gh api") {
		t.Errorf("problems %q, warnings %q; want no problem and one warning naming the gh error", probs, warns)
	}
}

func TestHostDependentGateCIRedAddsNothing(t *testing.T) {
	t.Parallel()
	calls := 0
	probs, warns := hdgClean(hdgDir(t), nil, []string{hdgOwnsPth}, []string{hdgFull},
		[]GateProbe{{N: 1, RC: 1}}, hdgSHA, true, fakeGh(ciRed, nil, &calls))
	if len(probs) != 0 || len(warns) != 0 || calls != 1 {
		t.Errorf("problems %q, warnings %q, gh calls %d; want none, none, 1", probs, warns, calls)
	}
}

func TestHostDependentGateOffOrCannotStart(t *testing.T) {
	t.Parallel()
	calls := 0
	dir := hdgDir(t)
	probs, warns := hdgClean(dir, nil, []string{hdgOwnsPth}, []string{hdgFull},
		[]GateProbe{{N: 1, RC: 1}}, hdgSHA, false, fakeGh(ciGreen, nil, &calls))
	if len(probs) != 0 || len(warns) != 0 || calls != 0 {
		t.Errorf("off: problems %q, warnings %q, gh calls %d; want none, none, 0", probs, warns, calls)
	}
	probs, warns = hdgClean(dir, nil, []string{hdgOwnsPth}, []string{hdgFull},
		[]GateProbe{{N: 1, RC: 127, CannotStart: true}}, hdgSHA, true, fakeGh(ciGreen, nil, &calls))
	if len(probs) != 0 || len(warns) != 0 || calls != 0 {
		t.Errorf("cannot start: problems %q, warnings %q, gh calls %d; want none, none, 0", probs, warns, calls)
	}
}

func TestHostDependentGateCICommitStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, out    string
		green, known bool
	}{
		{"empty list", ciNoRuns, false, false},
		{"all success", `{"check_runs":[{"name":"a","status":"completed","conclusion":"success"},{"name":"b","status":"completed","conclusion":"success"}]}`, true, true},
		{"one in_progress", `{"check_runs":[{"name":"a","status":"completed","conclusion":"success"},{"name":"b","status":"in_progress","conclusion":null}]}`, false, false},
		{"one failure", ciRed, false, true},
		{"skipped and success", `{"check_runs":[{"name":"a","status":"completed","conclusion":"skipped"},{"name":"b","status":"completed","conclusion":"success"}]}`, true, true},
	}
	for _, c := range cases {
		var gotArgs []string
		gh := func(args ...string) ([]byte, error) { gotArgs = args; return []byte(c.out), nil }
		green, known, err := CICommitStatus(hdgSHA, gh)
		if err != nil || green != c.green || known != c.known {
			t.Errorf("%s: CICommitStatus() = %v, %v, %v; want %v, %v, nil", c.name, green, known, err, c.green, c.known)
		}
		if len(gotArgs) != 2 || gotArgs[0] != "api" || !strings.Contains(gotArgs[1], "commits/"+hdgSHA+"/check-runs?per_page=100") {
			t.Errorf("%s: gh args = %q", c.name, gotArgs)
		}
	}
	sentinel := errors.New("boom")
	if _, _, err := CICommitStatus(hdgSHA, func(...string) ([]byte, error) { return nil, sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("CICommitStatus() gh error = %v, want it wrapping %v", err, sentinel)
	}
}
