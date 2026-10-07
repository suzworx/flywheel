package flywheel

import (
	"os"
	"path/filepath"
)

// LintDir resolves the directory flywheel lint checks a brief against when
// --dir is not given (issue #808): the nearest ancestor of cwd (cwd included)
// holding a .flywheel directory, else the git top level of cwd, else cwd
// unchanged. Run from a subdirectory such as .flywheel/briefs, lint then
// resolves repo-relative owns and runs probes from the flywheel root instead
// of reporting every owns path missing.
func LintDir(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}
	for d := abs; ; {
		if st, err := os.Stat(filepath.Join(d, ".flywheel")); err == nil && st.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if root, err := RepoRoot(cwd); err == nil {
		return filepath.Clean(root)
	}
	return cwd
}
