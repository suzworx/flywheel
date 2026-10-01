package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// skillDirs returns the directories an adapter loads agent skills from, in
// order (issue #695): tree is the worker's tree, home the user's home
// directory. An adapter flywheel cannot check returns nil.
func skillDirs(adapter, tree, home string) []string {
	claude := []string{filepath.Join(tree, ".claude", "skills"), filepath.Join(home, ".claude", "skills")}
	switch adapter {
	case "claude":
		return claude
	case "opencode":
		// opencode also reads the claude skill directories.
		return append([]string{
			filepath.Join(tree, ".opencode", "skill"),
			filepath.Join(tree, ".opencode", "skills"),
			filepath.Join(home, ".config", "opencode", "skill"),
			filepath.Join(home, ".config", "opencode", "skills"),
		}, claude...)
	}
	return nil
}

// missingSkills returns, in order, the names in names that are not installed
// as <dir>/<name>/SKILL.md (a regular file) in any of skillDirs (issue #695).
// A plugin skill such as "engineering:debug" (a name containing ':') lives
// elsewhere and is never reported missing. checkable is false when the
// adapter's skill directories are unknown.
func missingSkills(adapter, tree, home string, names []string) (missing []string, checkable bool) {
	dirs := skillDirs(adapter, tree, home)
	if dirs == nil {
		return nil, false
	}
	for _, n := range names {
		if strings.Contains(n, ":") {
			continue
		}
		found := false
		for _, d := range dirs {
			if fi, err := os.Stat(filepath.Join(d, n, "SKILL.md")); err == nil && fi.Mode().IsRegular() {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, n)
		}
	}
	return missing, true
}

// skillsProblem names the skills a brief's skills: line lists that are not
// installed where w loads skills (issue #695). Empty means nothing is missing
// or the adapter is not checkable. Lint reports it as a problem; Run refuses
// with rule skills.
func skillsProblem(w Worker, tree, home string, names []string) string {
	missing, ok := missingSkills(w.Adapter, tree, home, names)
	if !ok || len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("skills %s not installed for worker %q (%s): looked in %s; install each as <dir>/<name>/SKILL.md or drop it from skills:",
		strings.Join(missing, ", "), w.Name, w.Adapter, strings.Join(skillDirs(w.Adapter, tree, home), ", "))
}

// skillsWarning warns that w's installed skills cannot be checked when the
// brief names skills and w's adapter has no known skill directories (issue #695).
func skillsWarning(w Worker, names []string) string {
	if len(names) == 0 || skillDirs(w.Adapter, "", "") != nil {
		return ""
	}
	return fmt.Sprintf("skills: cannot check installed skills for worker %q (%s)", w.Name, w.Adapter)
}

// dispatchTree is the worker's tree as far as it is known before dispatch
// (issue #695), the way Run picks it: --workdir, the task's worktree under
// --worktree when it already exists, the previous attempt's workdir on a
// correction, else dir.
func dispatchTree(dir string, o RunOptions, events []Event) string {
	switch {
	case o.Worktree:
		p := filepath.Join(absPath(dir), ".flywheel", "worktrees", o.Task)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	case o.Workdir != "":
		return absPath(o.Workdir)
	case o.Resume || o.DeltaPath != "":
		if prev := recordedWorkdir(events, o.Task); prev != "" {
			return prev
		}
	}
	return dir
}

// userHome is os.UserHomeDir, "" when it fails.
func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
