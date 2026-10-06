package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// vsBaseLeadBuiltTask makes a lead-built unit (issue #798): a flywheel dir
// whose brief has one `gate[vs-base]: sh t.sh`, baseScript committed as t.sh,
// a planned event recording that commit as its base (none when noBase) and no
// dispatched event, then unitScript committed as the unit's change. It
// returns the dir and the planned base.
func vsBaseLeadBuiltTask(t *testing.T, baseScript, unitScript string, noBase bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)
	brief := "owns: a.go, t.sh\nneeds: none\ngate[vs-base]: sh t.sh\nfail-match: ^FAIL\n\n# TASK: vs-base lead-built\n"
	for name, body := range map[string]string{".gitignore": ".flywheel/\nflywheel.md\n", "brief.txt": brief, "t.sh": baseScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "base"})
	base := git(t, dir, []string{"rev-parse", "HEAD"})
	planned := Event{Task: "T1", Kind: "planned", Brief: "brief.txt", Base: base}
	if noBase {
		planned.Base = ""
	}
	if err := AppendEvent(dir, planned); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t.sh"), []byte(unitScript), 0o644); err != nil {
		t.Fatalf("write t.sh: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "unit"})
	return dir, base
}

func TestVsBaseLeadBuilt(t *testing.T) {
	t.Parallel()
	t.Run("same failures pass vs the planned base", func(t *testing.T) {
		t.Parallel()
		dir, base := vsBaseLeadBuiltTask(t, failA, "echo 'FAIL a'\nexit 1\n", false)
		g, ev := vsBaseValidate(t, dir)
		if g.RC != 0 || ev.Reason != "vs-base" || ev.VsBase == nil || ev.VsBase.Base != base {
			t.Fatalf("gate %+v, reason %q, vs_base %+v; want a vs-base pass against the planned base %s", g, ev.Reason, ev.VsBase, base)
		}
	})
	t.Run("a new failure fails", func(t *testing.T) {
		t.Parallel()
		dir, _ := vsBaseLeadBuiltTask(t, failA, "echo 'FAIL a'\necho 'FAIL b'\nexit 1\n", false)
		g, ev := vsBaseValidate(t, dir)
		if g.RC != 1 || ev.VsBase == nil || !slices.Equal(ev.VsBase.New, []string{"FAIL b"}) || !strings.Contains(g.Note, "FAIL b") {
			t.Fatalf("gate %+v, vs_base %+v; want a failure naming FAIL b", g, ev.VsBase)
		}
	})
	t.Run("no planned base", func(t *testing.T) {
		t.Parallel()
		dir, _ := vsBaseLeadBuiltTask(t, failA, "echo 'FAIL a'\nexit 1\n", true)
		g, ev := vsBaseValidate(t, dir)
		if g.RC != 1 || !strings.Contains(g.Note, "no unit base recorded") || ev.VsBase != nil {
			t.Fatalf("gate %+v, vs_base %+v; want the no-base failure", g, ev.VsBase)
		}
	})
}
