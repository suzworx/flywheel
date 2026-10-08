package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// nestedEscapeFixture builds a worktree wt (outside root) for issue #829: a
// workspace package's real apps/web/node_modules holds a scoped link into
// root/packages/ui (escapes) and one into wt/packages/own; apps/linked's
// node_modules is itself a link, apps/lnk is a linked directory, a/b/c sits
// three levels down and node_modules/pkg/node_modules is under a top
// node_modules, each holding a link into root/packages/ui that is never read.
func nestedEscapeFixture(t *testing.T) (root, wt string) {
	t.Helper()
	root = t.TempDir()
	wt = t.TempDir()
	ui := filepath.Join(root, "packages", "ui")
	web := filepath.Join(wt, "apps", "web", "node_modules")
	for _, d := range []string{
		ui, filepath.Join(root, "linked", "node_modules"), filepath.Join(root, "outside", "node_modules"),
		filepath.Join(wt, "packages", "own"), filepath.Join(web, "@acme"),
		filepath.Join(wt, "apps", "linked"), filepath.Join(wt, "a", "b", "c", "node_modules"),
		filepath.Join(wt, "node_modules", "pkg", "node_modules"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testLink(t, ui, filepath.Join(web, "@acme", "ui"))
	testLink(t, filepath.Join(wt, "packages", "own"), filepath.Join(web, "@acme", "own"))
	testLink(t, ui, filepath.Join(root, "linked", "node_modules", "esc"))
	testLink(t, filepath.Join(root, "linked", "node_modules"), filepath.Join(wt, "apps", "linked", "node_modules"))
	testLink(t, ui, filepath.Join(root, "outside", "node_modules", "esc"))
	testLink(t, filepath.Join(root, "outside"), filepath.Join(wt, "apps", "lnk"))
	testLink(t, ui, filepath.Join(wt, "a", "b", "c", "node_modules", "deep"))
	testLink(t, ui, filepath.Join(wt, "node_modules", "pkg", "node_modules", "inner"))
	testLink(t, ui, filepath.Join(wt, "node_modules", "top"))
	return root, wt
}

// TestNestedEscapeLinks checks ownEscapingLinks' nested scan (issue #829):
// only apps/web/node_modules/@acme/ui and the top node_modules/top are
// reported; a link inside wt, a linked node_modules, a linked directory,
// depth 3 and a node_modules under node_modules are not.
func TestNestedEscapeLinks(t *testing.T) {
	t.Parallel()
	root, wt := nestedEscapeFixture(t)
	got, err := ownEscapingLinks(root, wt)
	want := []string{"apps/web/node_modules/@acme/ui", "node_modules/top"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("ownEscapingLinks() = %q, %v, want %q", got, err, want)
	}
}

// TestNestedEscapeNoTopModules checks that a worktree with no node_modules at
// its top still has its workspace packages' node_modules scanned (issue #829).
func TestNestedEscapeNoTopModules(t *testing.T) {
	t.Parallel()
	root, wt := nestedEscapeFixture(t)
	if err := os.RemoveAll(filepath.Join(wt, "node_modules")); err != nil {
		t.Fatal(err)
	}
	got, err := ownEscapingLinks(root, wt)
	if want := []string{"apps/web/node_modules/@acme/ui"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("ownEscapingLinks() = %q, %v, want %q", got, err, want)
	}
}
