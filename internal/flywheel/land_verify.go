package flywheel

import (
	"fmt"
	"sort"
	"strings"
)

// InconclusiveError is a check that could not decide (the CLI exits 8): the
// evidence it needs is missing, not wrong. Fix names what to do about it.
type InconclusiveError struct {
	Fix string
}

func (e *InconclusiveError) Error() string {
	return "inconclusive: " + e.Fix
}

// verifyLandCommit checks the commit a landing records (issue #673): it
// exists, it is on the integration branch, and it touches at least one of
// the unit's paths. It returns nil, a T5 RuleRefusal, or an InconclusiveError.
func verifyLandCommit(dir, task, commit string, events []Event) error {
	// One git log resolves the commit and reads its parents and subject (few
	// git processes: ship lands through here, issue #619's budget).
	out, err := gitRead(dir, []string{"log", "-1", "--format=%P%x00%s", commit + "^{commit}", "--"})
	if err != nil {
		// Without a repository there is nothing to verify: landing records
		// an empty tree (landedTree), as it always has.
		if _, err := gitRead(dir, []string{"rev-parse", "--is-inside-work-tree"}); err != nil {
			return nil
		}
		return &InconclusiveError{Fix: fmt.Sprintf("commit %s does not resolve in this repository; it may exist only on the remote: run git fetch, then land again", commit)}
	}
	parents, subject, _ := strings.Cut(strings.TrimSpace(out), "\x00")

	// Every integration ref that resolves: any remote's, then the local one.
	refs, looked := landIntegrationRefs(dir)
	if len(refs) == 0 {
		return &InconclusiveError{Fix: fmt.Sprintf("no integration branch resolves to check commit %s against (looked for %s); set integration.branch or run git fetch", short7(commit), looked)}
	}
	on := false
	for _, ref := range refs {
		rc, _, _, err := runCmdSplit(dir, gitArgs([]string{"merge-base", "--is-ancestor", commit, ref}), nil)
		if err == nil && rc == 0 {
			on = true
			break
		}
	}
	if !on {
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("commit %s (%s) is not on the integration branch (checked %s); land the commit that merged %s, or run git fetch if the ref is stale", short7(commit), subject, strings.Join(refs, ", "), task)}
	}

	// The unit's paths come from the ledger, not the worktree, which may be
	// gone: the diff from the last dispatch's base to the last passing
	// inspection's tree, else the owns of the planned header.
	base, tree := "", ""
	for _, e := range events {
		if e.Task != task {
			continue
		}
		if e.Kind == "dispatched" {
			base = e.Base
		}
		if e.Kind == "inspected" && e.Verdict == "pass" && e.Tree != "" {
			tree = e.Tree
		}
	}
	var unit, owns []string
	if base != "" && tree != "" {
		if out, err := gitRead(dir, []string{"diff-tree", "-r", "--no-renames", "--name-only", "-z", base, tree}); err == nil {
			unit = landPaths(out)
		}
	}
	if len(unit) == 0 {
		if h, ok := plannedHeader(events, task); ok {
			owns = h.Owns
		}
	}
	if len(unit) == 0 && len(owns) == 0 {
		// Nothing in the ledger says which files are the unit's: the
		// commit's contents cannot be checked, so they are not.
		return nil
	}

	args := []string{"diff-tree", "-r", "--no-renames", "--name-only", "-z", "--no-commit-id", commit + "^1", commit}
	if parents == "" {
		args = []string{"diff-tree", "-r", "--no-renames", "--name-only", "-z", "--no-commit-id", "--root", commit}
	}
	out, err = gitRead(dir, args)
	if err != nil {
		return &InconclusiveError{Fix: fmt.Sprintf("listing the files of commit %s failed: %v", short7(commit), err)}
	}
	inUnit := map[string]bool{}
	for _, p := range unit {
		inUnit[p] = true
	}
	for _, p := range landPaths(out) {
		if inUnit[p] || len(unit) == 0 && ownsContains(owns, p) {
			return nil
		}
	}
	shown := unit
	if len(shown) == 0 {
		shown = owns
	}
	list := strings.Join(shown, ", ")
	if len(shown) > 5 {
		list = strings.Join(shown[:5], ", ") + ", ..."
	}
	return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("commit %s (%s) touches none of %s's files (%s); land the commit that merged this unit", short7(commit), subject, task, list)}
}

// landIntegrationRefs lists the integration refs integrationRef considers,
// and the same branch under every other remote (ship --remote, correction
// c1), without skipping the ref HEAD is on: a landing checks the
// integration branch even when it is checked out. It returns the refs that
// exist, origin's first, then other remotes' sorted, then the local
// branches, and a description of what was looked for. The branch is
// integration.branch when configured, else main and master. One git
// for-each-ref lists every candidate.
func landIntegrationRefs(dir string) (refs []string, looked string) {
	branches := []string{"main", "master"}
	if c, _, err := LoadConfig(dir); err == nil && c.Integration != nil && c.Integration.Branch != "" {
		branches = []string{c.Integration.Branch}
	}
	listed, _ := gitRead(dir, []string{"for-each-ref", "--format=%(refname)", "refs/remotes", "refs/heads"})
	var origin, others, local []string
	for _, ref := range strings.Split(listed, "\n") {
		ref = strings.TrimSpace(ref)
		for _, b := range branches {
			if ref == "refs/heads/"+b {
				local = append(local, ref)
			}
			// refs/remotes/<remote>/<b>, where <remote> is one path
			// segment: a remote name holding "/" is not matched.
			if r, ok := strings.CutSuffix(strings.TrimPrefix(ref, "refs/remotes/"), "/"+b); ok && strings.HasPrefix(ref, "refs/remotes/") && r != "" && !strings.Contains(r, "/") {
				if r == "origin" {
					origin = append(origin, ref)
				} else {
					others = append(others, ref)
				}
			}
		}
	}
	sort.Strings(others)
	refs = append(append(append(refs, origin...), others...), local...)
	looked = "refs/remotes/<remote>/" + strings.Join(branches, ", refs/remotes/<remote>/") + ", refs/heads/" + strings.Join(branches, ", refs/heads/")
	return refs, looked
}

// landPaths splits git's -z path output, dropping flywheel's own files.
func landPaths(out string) []string {
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" && !isFlywheelOwnPath(p) {
			paths = append(paths, p)
		}
	}
	return paths
}
