package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// vsBaseTask makes a flywheel dir whose brief has one `gate[vs-base]: bash
// t.sh` (issue #788), commits baseScript as t.sh, records a dispatch at that
// commit unless noBase, then leaves unitScript as the unit's uncommitted t.sh.
func vsBaseTask(t *testing.T, baseScript, unitScript, failMatch string, noBase bool) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	brief := "owns: a.go, t.sh\nneeds: none\ngate[vs-base]: bash t.sh\n"
	if failMatch != "" {
		brief += "fail-match: " + failMatch + "\n"
	}
	for name, body := range map[string]string{".gitignore": ".flywheel/\nflywheel.md\n", "brief.txt": brief + "\n# TASK: vs-base\n", "t.sh": baseScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := AppendEvent(dir, Event{Task: "T1", Kind: "planned", Brief: "brief.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "base"})
	if !noBase {
		if err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Base: git(t, dir, []string{"rev-parse", "HEAD"})}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "t.sh"), []byte(unitScript), 0o644); err != nil {
		t.Fatalf("write t.sh: %v", err)
	}
	return dir
}

// vsBaseValidate runs validate and returns its one gate and recorded event.
func vsBaseValidate(t *testing.T, dir string) (GateOut, Event) {
	t.Helper()
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if len(res.Gates) != 1 {
		t.Fatalf("gates = %+v, want one", res.Gates)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var last Event
	for _, e := range events {
		if e.Kind == "validated" && e.Gate == "1" {
			last = e
		}
	}
	return res.Gates[0], last
}

const failA = "echo 'FAIL a'\necho ok\nexit 1\n"

func TestVsBasePassesSameFailures(t *testing.T) {
	t.Parallel()
	dir := vsBaseTask(t, failA, "echo 'FAIL a  '\nexit 1\n", "^FAIL ", false)
	g, ev := vsBaseValidate(t, dir)
	if g.RC != 0 || ev.RC == nil || *ev.RC != 0 || ev.Reason != "vs-base" {
		t.Fatalf("gate rc %d, event rc %v reason %q; want 0, 0, vs-base", g.RC, ev.RC, ev.Reason)
	}
	vs := ev.VsBase
	if vs == nil || vs.RC != 1 || vs.BaseRC != 1 || vs.Failing != 1 || vs.BaseFailing != 1 || vs.Cached || len(vs.New) != 0 {
		t.Fatalf("vs_base = %+v, want rc 1, base_rc 1, 1/1 failing, uncached", vs)
	}
	if !strings.HasPrefix(ev.Note, "passed vs base ") || !strings.HasSuffix(ev.Note, ": 1 failing on base too") {
		t.Errorf("note = %q", ev.Note)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(vs.BaseLog))); err != nil || !strings.HasSuffix(vs.BaseLog, "gate-1.base.log") {
		t.Errorf("base log %q: %v", vs.BaseLog, err)
	}
}

func TestVsBaseNewFailureFails(t *testing.T) {
	t.Parallel()
	dir := vsBaseTask(t, failA, "echo 'FAIL a'\necho 'FAIL b'\nexit 1\n", "^FAIL ", false)
	g, ev := vsBaseValidate(t, dir)
	if g.RC != 1 || ev.Reason == "vs-base" || ev.VsBase == nil || !slices.Equal(ev.VsBase.New, []string{"FAIL b"}) {
		t.Fatalf("gate rc %d, reason %q, vs_base %+v; want a failure naming FAIL b", g.RC, ev.Reason, ev.VsBase)
	}
	if !strings.HasPrefix(g.Note, "1 new failing vs base ") || !strings.HasSuffix(g.Note, ": FAIL b") {
		t.Errorf("note = %q", g.Note)
	}
}

func TestVsBaseNoMatchedLineFails(t *testing.T) {
	t.Parallel()
	dir := vsBaseTask(t, failA, "echo 'build broke'\nexit 1\n", "^FAIL ", false)
	g, _ := vsBaseValidate(t, dir)
	if g.RC != 1 || !strings.HasSuffix(g.Note, "no fail-match line in the output") {
		t.Errorf("gate = %+v, want rc 1 with the no-line note", g)
	}
}

func TestVsBaseBasePasses(t *testing.T) {
	t.Parallel()
	dir := vsBaseTask(t, "echo ok\n", failA, "^FAIL ", false)
	g, ev := vsBaseValidate(t, dir)
	if g.RC != 1 || ev.VsBase == nil || ev.VsBase.BaseRC != 0 || !strings.HasSuffix(g.Note, ": base passes") {
		t.Errorf("gate = %+v, vs_base %+v; want the unit's failure to stand", g, ev.VsBase)
	}
}

// TestVsBaseCache checks a second validate reads the cached base reading and
// never reruns the base script, which counts its runs outside the tree.
func TestVsBaseCache(t *testing.T) {
	t.Parallel()
	count := filepath.Join(t.TempDir(), "count")
	dir := vsBaseTask(t, "echo run >> '"+filepath.ToSlash(count)+"'\n"+failA, failA, "^FAIL ", false)
	if g, ev := vsBaseValidate(t, dir); g.RC != 0 || ev.VsBase == nil || ev.VsBase.Cached {
		t.Fatalf("first pass gate %+v, vs_base %+v; want a pass measured live", g, ev.VsBase)
	}
	g, ev := vsBaseValidate(t, dir)
	if g.RC != 0 || ev.VsBase == nil || !ev.VsBase.Cached || ev.VsBase.BaseFailing != 1 {
		t.Fatalf("second pass gate %+v, vs_base %+v; want a cached pass", g, ev.VsBase)
	}
	if b, err := os.ReadFile(count); err != nil || strings.Count(string(b), "run") != 1 {
		t.Errorf("base runs = %q (%v), want exactly one", b, err)
	}
}

func TestVsBaseNoBase(t *testing.T) {
	t.Parallel()
	dir := vsBaseTask(t, failA, failA, "^FAIL ", true)
	g, ev := vsBaseValidate(t, dir)
	if g.RC != 1 || g.Note != "vs-base: no unit base recorded" || ev.VsBase != nil {
		t.Errorf("gate = %+v, vs_base %+v; want the no-base failure", g, ev.VsBase)
	}
}

func TestVsBaseWithoutFailMatch(t *testing.T) {
	t.Parallel()
	dir := vsBaseTask(t, "exit 3\n", "echo 'FAIL new'\nexit 1\n", "", false)
	g, ev := vsBaseValidate(t, dir)
	if g.RC != 0 || ev.Reason != "vs-base" || ev.VsBase == nil || ev.VsBase.Failing != 0 || !strings.HasSuffix(g.Note, ": base fails too (rc 3)") {
		t.Errorf("gate = %+v, vs_base %+v; want a pass on the exit status only", g, ev.VsBase)
	}
}

func TestParseBriefHeaderVsBase(t *testing.T) {
	t.Parallel()
	h, err := ParseBriefHeaderBytes([]byte("gate: true\ngate[quiet,vs-base]: a\nlive-gate[vs-base]: b\nfail-match: x\nfail-match: ^--- FAIL\n\n# TASK x\n"))
	if err != nil {
		t.Fatalf("ParseBriefHeaderBytes() error = %v", err)
	}
	if !slices.Equal(h.VsBaseGates, []int{2}) || !slices.Equal(h.QuietGates, []int{2}) || !slices.Equal(h.VsBaseLiveGates, []int{1}) || h.FailMatch != "^--- FAIL" {
		t.Errorf("header = vs %v quiet %v live vs %v fail-match %q", h.VsBaseGates, h.QuietGates, h.VsBaseLiveGates, h.FailMatch)
	}
}

func TestLintBriefVsBase(t *testing.T) {
	t.Parallel()
	const body = "\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	res := lintCheck(t, t.TempDir(), []string{"a.go"}, "owns: a.go\nneeds: none\ngate[vs-base]: true\nfail-match: ^FAIL \n"+body)
	want(t, res, nil, nil)
	res = lintCheck(t, t.TempDir(), []string{"a.go"}, "owns: a.go\nneeds: none\ngate[vs-base]: true\nfail-match: (\n"+body)
	want(t, res, []string{"fail-match: \"(\" does not compile: error parsing regexp: missing closing ): `(`"}, nil)
	res = lintCheck(t, t.TempDir(), []string{"a.go"}, "owns: a.go\nneeds: none\ngate: true\nfail-match: ^FAIL\n"+body)
	want(t, res, nil, []string{"fail-match: is set and no gate is marked [vs-base]; it is never used"})
	res = lintCheck(t, t.TempDir(), []string{"a.go"}, "owns: a.go\nneeds: none\ngate[vs-base]: true\n"+body)
	want(t, res, nil, []string{"a gate is marked [vs-base] with no fail-match: line: it compares the exit status only: any new failure passes while the base fails"})
}
