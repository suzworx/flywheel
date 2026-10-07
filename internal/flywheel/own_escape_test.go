package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ownEscapeFixture builds a worktree wt holding a package.json and a real
// node_modules whose entries are a scoped link into root/packages/web
// (escapes), a scoped link into wt/packages/own, a real directory and a
// broken link. With nested wt is root/.flywheel/worktrees/T1, otherwise a
// directory outside root.
func ownEscapeFixture(t *testing.T, nested bool) (root, wt string) {
	t.Helper()
	root = t.TempDir()
	wt = t.TempDir()
	if nested {
		wt = filepath.Join(root, ".flywheel", "worktrees", "T1")
	}
	nm := filepath.Join(wt, "node_modules")
	for _, d := range []string{
		filepath.Join(root, "packages", "web"), filepath.Join(wt, "packages", "own"),
		filepath.Join(nm, "@acme"), filepath.Join(nm, "left-pad"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	testLink(t, filepath.Join(root, "packages", "web"), filepath.Join(nm, "@acme", "web"))
	testLink(t, filepath.Join(wt, "packages", "own"), filepath.Join(nm, "@acme", "own"))
	testLink(t, filepath.Join(root, "nowhere"), filepath.Join(nm, "broken"))
	return root, wt
}

// TestOwnEscapingLinks checks ownEscapingLinks (issue #819): only the link
// into the main checkout's packages/ is reported; a linked node_modules (left
// to escapingLinks) and a missing one are nil.
func TestOwnEscapingLinks(t *testing.T) {
	t.Parallel()
	root, wt := ownEscapeFixture(t, true)
	got, err := ownEscapingLinks(root, wt)
	if want := []string{"node_modules/@acme/web"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("ownEscapingLinks() = %q, %v, want %q", got, err, want)
	}
	linkRoot := t.TempDir()
	linkWT := filepath.Join(linkRoot, ".flywheel", "worktrees", "T1")
	for _, d := range []string{filepath.Join(linkRoot, "node_modules", "@acme"), filepath.Join(linkRoot, "packages", "web"), linkWT} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testLink(t, filepath.Join(linkRoot, "packages", "web"), filepath.Join(linkRoot, "node_modules", "@acme", "web"))
	testLink(t, filepath.Join(linkRoot, "node_modules"), filepath.Join(linkWT, "node_modules"))
	if got, err := ownEscapingLinks(linkRoot, linkWT); err != nil || got != nil {
		t.Errorf("linked node_modules: ownEscapingLinks() = %q, %v, want nil, nil", got, err)
	}
	bareRoot := t.TempDir()
	bare := filepath.Join(bareRoot, ".flywheel", "worktrees", "T1")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := ownEscapingLinks(bareRoot, bare); err != nil || got != nil {
		t.Errorf("no node_modules: ownEscapingLinks() = %q, %v, want nil, nil", got, err)
	}
}

// TestPrepareWorktreeOwnEscape checks prepareWorktree with nothing configured
// (issue #819): the worktree's own node_modules holding a link into the main
// checkout is warned about and recorded as Escaped, and with
// worktree.strict_links it is a setup RuleRefusal, recorded too; a worktree
// outside the root is reported the same way.
func TestPrepareWorktreeOwnEscape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		strict, nested bool
	}{{false, true}, {true, true}, {false, false}, {true, false}}
	for _, c := range cases {
		root, wt := ownEscapeFixture(t, c.nested)
		cfg := Config{Worktree: &WorktreeConfig{StrictLinks: c.strict}}
		warnings, err := prepareWorktree(root, wt, "T1", "r1", cfg, nil, nil, nil)
		var rr *RuleRefusal
		if c.strict && (!errors.As(err, &rr) || rr.Rule != "setup" || !strings.Contains(rr.Fix, "node_modules/@acme/web") || !strings.Contains(rr.Fix, "(install)") || !strings.Contains(rr.Fix, "#819")) {
			t.Errorf("%+v: prepareWorktree() error = %v, want a setup RuleRefusal naming node_modules/@acme/web, (install) and #819", c, err)
		}
		if !c.strict && err != nil {
			t.Errorf("%+v: prepareWorktree() error = %v, want nil", c, err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "worktree node_modules holds links into the main checkout (1: node_modules/@acme/web)") || !strings.Contains(warnings[0], "#819") {
			t.Errorf("%+v: warnings = %q, want the #819 warning", c, warnings)
		}
		evs, err := ReadEvents(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].Kind != "worktree_setup" || !slices.Equal(evs[0].Escaped, []string{"node_modules/@acme/web"}) {
			t.Fatalf("%+v: events = %+v, want one worktree_setup with Escaped", c, evs)
		}
		if c.strict != strings.Contains(evs[0].Note, "refused") {
			t.Errorf("%+v: note = %q, want refused only when strict", c, evs[0].Note)
		}
	}
}
