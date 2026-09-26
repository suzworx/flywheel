package flywheel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TaskWorktree must be called under the dispatch lock (Run does): its
// existence check, branch lookup and creation are separate git commands.
//
// TaskWorktree returns the absolute path of task's worktree under dir,
// <dir>/.flywheel/worktrees/<task>, creating it when missing with
// `git -C <dir> worktree add -b fw/<task> <path> <start>`, start as
// defaultTaskStart picks it (issue #550) (or, when branch
// fw/<task> already exists, `git -C <dir> worktree add <path> fw/<task>`).
// An existing directory that is a git worktree is reused as is. Errors name
// the git command and its output.
func TaskWorktree(dir, task string) (string, error) {
	return TaskWorktreeFrom(dir, task, "")
}

// TaskWorktreeFrom is TaskWorktreeFromNote without the note.
func TaskWorktreeFrom(dir, task, base string) (string, error) {
	path, _, err := TaskWorktreeFromNote(dir, task, base)
	return path, err
}

// TaskWorktreeFromNote is TaskWorktree branching a new fw/<task> from base
// (issue #456). A non-empty base is resolved to its commit first and must be
// an ancestor of an existing fw/<task>: a branch that does not contain it is
// refused with the rebase hint rather than silently running the unit on the
// wrong base. Base "" picks the start of a new fw/<task> with
// defaultTaskStart (issue #550) and returns its note for the operator.
func TaskWorktreeFromNote(dir, task, base string) (path, note string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	worktreesDir := filepath.Join(abs, ".flywheel", "worktrees")
	path = filepath.Join(worktreesDir, task)
	branchName := "fw/" + task

	start := "HEAD"
	if base != "" {
		out, err := exec.Command("git", "-C", abs, "rev-parse", "--verify", "-q", base+"^{commit}").Output()
		if err != nil {
			return "", "", fmt.Errorf("base %q does not resolve to a commit", base)
		}
		start = strings.TrimSpace(string(out))
		if exec.Command("git", "-C", abs, "rev-parse", "--verify", "-q", "refs/heads/"+branchName).Run() == nil &&
			exec.Command("git", "-C", abs, "merge-base", "--is-ancestor", start, "refs/heads/"+branchName).Run() != nil {
			return "", "", fmt.Errorf("%s already exists and does not contain %s; move it with flywheel rebase %s --onto %s", branchName, base, task, base)
		}
	}

	// An existing path is reused only when it is a worktree of this same
	// repository with fw/<task> checked out; anything else is refused rather
	// than silently running a worker in the wrong tree (#333 review).
	if _, err := os.Stat(path); err == nil {
		if err := checkTaskWorktree(abs, path, branchName); err != nil {
			return "", "", err
		}
		return path, "", nil
	}

	// Check if branch fw/<task> already exists
	checkBranchCmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "-q", "refs/heads/"+branchName)
	branchExists := checkBranchCmd.Run() == nil
	if !branchExists && base == "" {
		if start, note, err = defaultTaskStart(abs, task); err != nil {
			return "", "", err
		}
	}

	// Create the worktrees directory if it doesn't exist
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create %s: %w", worktreesDir, err)
	}

	// Build the worktree add command
	var args []string
	if branchExists {
		// Branch exists, use it
		args = []string{"worktree", "add", path, branchName}
	} else {
		// Branch doesn't exist, create it
		args = []string{"worktree", "add", "-b", branchName, path, start}
	}

	// Run git worktree add
	cmd := exec.Command("git", "-C", dir)
	cmd.Args = append([]string{"git", "-C", dir}, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		gitErr := fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
		// Another worktree holding the branch is usually another flywheel
		// root that claimed the id (issue #479): say so, whatever language
		// git reported it in.
		if branchExists {
			if _, other := TaskBranch(abs, task); other != "" && !samePath(other, path) {
				return "", "", fmt.Errorf("%s is checked out in another worktree (%s): another flywheel root may own task %s; plan under a new id or withdraw it there: %w", branchName, other, task, gitErr)
			}
		}
		return "", "", gitErr
	}

	return path, note, nil
}

// defaultTaskStart picks the start of a new fw/<task> when no base is given
// (issue #550). With integration.branch B configured it is origin/B when
// that resolves, else B, else an error. Otherwise it is HEAD, with a warning
// note when HEAD carries commits that origin/main (else origin/master, else
// the local main or master) does not contain. The start is a commit, so a
// remote-tracking start sets no upstream.
func defaultTaskStart(abs, task string) (start, note string, err error) {
	commit := func(ref string) string {
		out, err := exec.Command("git", "-C", abs, "rev-parse", "--verify", "-q", ref+"^{commit}").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	b, configured := IntegrationBranch(abs)
	if configured {
		ref, c := "origin/"+b, commit("refs/remotes/origin/"+b)
		if c == "" {
			ref, c = b, commit("refs/heads/"+b)
		}
		if c == "" {
			return "", "", fmt.Errorf("integration.branch %s resolves neither as origin/%s nor as a local branch: fetch it, or pass --base", b, b)
		}
		return c, fmt.Sprintf("new task branch fw/%s starts from %s (integration.branch)", task, ref), nil
	}
	refs := []string{"origin/main", "origin/master"}
	if b != "" {
		refs = append(refs, b)
	}
	for _, ref := range refs {
		full := "refs/heads/" + ref
		if strings.HasPrefix(ref, "origin/") {
			full = "refs/remotes/" + ref
		}
		if commit(full) == "" {
			continue
		}
		out, err := exec.Command("git", "-C", abs, "rev-list", "--count", full+"..HEAD").Output()
		if err != nil {
			return "", "", fmt.Errorf("git rev-list --count %s..HEAD: %w", full, err)
		}
		if n := strings.TrimSpace(string(out)); n != "0" {
			note = fmt.Sprintf("warning: HEAD carries %s commit(s) not on %s; fw/%s will include them (pass --base %s to start clean)", n, ref, task, ref)
		}
		return "HEAD", note, nil
	}
	return "HEAD", "", nil
}

// TaskBranch reports whether branch fw/<task> exists in the repository dir
// belongs to and, when a worktree of that repository has it checked out, that
// worktree's path (issue #479). Branches are shared by every worktree of a
// repository, so another flywheel root that claimed the id shows here. It
// runs read-only git only (rev-parse --verify, worktree list --porcelain),
// and none at all when no .git entry sits at or above dir; any git failure
// reads as no branch.
func TaskBranch(dir, task string) (exists bool, worktree string) {
	abs, err := filepath.Abs(dir)
	if err != nil || !underGit(abs) {
		return false, ""
	}
	ref := "refs/heads/fw/" + task
	if exec.Command("git", "-C", abs, "rev-parse", "--verify", "-q", ref).Run() != nil {
		return false, ""
	}
	out, err := exec.Command("git", "-C", abs, "worktree", "list", "--porcelain").Output()
	if err != nil {
		return true, ""
	}
	path := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			path = filepath.Clean(filepath.FromSlash(p))
		} else if line == "branch "+ref {
			return true, path
		}
	}
	return true, ""
}

// underGit reports whether a .git entry (a directory, or a worktree's .git
// file) sits at abs or any of its parents.
func underGit(abs string) bool {
	for p := abs; ; {
		if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// recordedWorkdir returns the Workdir of the latest dispatched event of
// task's current attempt (as currentAttempt resolves it), or "" when none.
func recordedWorkdir(events []Event, task string) string {
	state := Derive(events)
	var ts TaskState
	found := false
	for _, t := range state.Tasks {
		if t.ID == task {
			ts = t
			found = true
			break
		}
	}
	if !found {
		return ""
	}
	attempt := currentAttempt(ts, events)
	if attempt == "" {
		return ""
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.Task == task && e.Attempt == attempt && e.Kind == "dispatched" {
			// A worktree removed after landing no longer holds the unit:
			// measure the flywheel root instead, as before --worktree.
			if e.Workdir != "" {
				// Only a worktree that is gone falls back; any other error
				// keeps the recorded path so validation fails loudly there
				// (#333 review).
				if _, err := os.Stat(e.Workdir); os.IsNotExist(err) {
					return ""
				}
			}
			return e.Workdir
		}
	}
	return ""
}

// gitCommonDir returns the absolute git common directory of the repository
// that d belongs to.
func gitCommonDir(d string) (string, error) {
	out, err := exec.Command("git", "-C", d, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(string(out))), nil
}

// LedgerRoot maps dir to the flywheel root whose ledger it must use. When dir
// is a task worktree <X>/.flywheel/worktrees/<T> (or inside one) that shares
// <X>'s repository, it returns (X, T): the worktree holds a stale copy of the
// ledger, and commands run there must read and write the main one (#395).
// Anything else, a non-repository included, returns (dir, "") with dir made
// absolute and clean.
func LedgerRoot(dir string) (root string, task string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	abs = filepath.Clean(abs)
	for p := abs; ; {
		worktrees := filepath.Dir(p)
		fw := filepath.Dir(worktrees)
		if filepath.Base(worktrees) == "worktrees" && filepath.Base(fw) == ".flywheel" {
			x := filepath.Dir(fw)
			// A plain directory at that path sits inside <X>'s own work
			// tree and shares its common dir too: p must be a top level.
			top, terr := exec.Command("git", "-C", p, "rev-parse", "--show-toplevel").Output()
			want, werr := gitCommonDir(x)
			got, gerr := gitCommonDir(p)
			if terr == nil && werr == nil && gerr == nil &&
				samePath(filepath.Clean(strings.TrimSpace(string(top))), p) && samePath(got, want) {
				return x, filepath.Base(p), nil
			}
		}
		parent := filepath.Dir(p)
		if parent == p {
			return abs, "", nil
		}
		p = parent
	}
}

// checkTaskWorktree verifies that path is a git worktree sharing dir's
// repository (the same git common directory) with branch checked out.
func checkTaskWorktree(dir, path, branch string) error {
	want, err := gitCommonDir(dir)
	if err != nil {
		return fmt.Errorf("resolve the repository of %s: %w", dir, err)
	}
	got, err := gitCommonDir(path)
	if err != nil || !samePath(got, want) {
		return fmt.Errorf("%s exists but is not a worktree of this repository; remove it (git worktree prune) and rerun", path)
	}
	head, err := exec.Command("git", "-C", path, "symbolic-ref", "-q", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(head)) != "refs/heads/"+branch {
		return fmt.Errorf("%s does not have %s checked out; switch it back or remove the worktree and rerun", path, branch)
	}
	return nil
}
