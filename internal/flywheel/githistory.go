package flywheel

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// gitState is the worktree's git state captured at dispatch and compared after
// each attempt: history (issue #314) plus the index and tags (#423).
type gitState struct {
	head, branch, stash string
	index               []string // staged paths (index vs HEAD), sorted
	tags                []string // "refs/tags/<name> <sha>", sorted
}

// String is "HEAD=<sha> branch=<ref or (detached)> stash=<sha or (none)>
// index=<n staged> tags=<n>".
func (s gitState) String() string {
	return "HEAD=" + s.head + " branch=" + s.branch + " stash=" + s.stash +
		" index=" + strconv.Itoa(len(s.index)) + " staged tags=" + strconv.Itoa(len(s.tags))
}

// gitHistoryState is readGitState as its String.
func gitHistoryState(dir string) (state string, ok bool) {
	s, ok := readGitState(dir)
	if !ok {
		return "", false
	}
	return s.String(), true
}

// readGitState reads dir's git state. ok is false when dir is not in a git
// repository, HEAD has no commit yet, or git fails; a caller then skips the
// check. Only an explicit "not there" answer (exit 1 from a -q query) reads
// as detached or no stash; any other failure is not mistaken for state (#318
// review). Read-only git commands only.
func readGitState(dir string) (s gitState, ok bool) {
	head, absent, err := gitQuery(dir, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil || absent || head == "" {
		return gitState{}, false
	}
	branch, absent, err := gitQuery(dir, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return gitState{}, false
	}
	if absent || branch == "" {
		branch = "(detached)"
	}
	stash, absent, err := gitQuery(dir, "rev-parse", "--verify", "-q", "refs/stash")
	if err != nil {
		return gitState{}, false
	}
	if absent || stash == "" {
		stash = "(none)"
	}
	index, err := gitList(dir, "\x00", "diff", "--cached", "--no-renames", "--name-only", "-z")
	if err != nil {
		return gitState{}, false
	}
	tags, err := gitList(dir, "\n", "for-each-ref", "--format=%(refname) %(objectname)", "refs/tags")
	if err != nil {
		return gitState{}, false
	}
	return gitState{head: head, branch: branch, stash: stash, index: index, tags: tags}, true
}

// gitList runs a read-only git query and returns its non-empty output items
// split on sep, sorted. Names are kept exact (no trimming).
func gitList(dir, sep string, args ...string) ([]string, error) {
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return nil, err
	}
	var items []string
	for _, it := range strings.Split(string(b), sep) {
		if sep == "\n" {
			it = strings.TrimSuffix(it, "\r")
		}
		if it != "" {
			items = append(items, it)
		}
	}
	sort.Strings(items)
	return items, nil
}

// gitQuery runs a read-only git query and returns its trimmed output. absent
// is true when git answered "not there" (exit status 1, as -q queries do);
// err is any other failure.
func gitQuery(dir string, args ...string) (out string, absent bool, err error) {
	b, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return "", true, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(b)), false, nil
}

// gitChange is what moved between the dispatch state and the final one.
type gitChange struct {
	changed bool     // anything moved, or the final state is unreadable
	worker  bool     // the index or a local-only/deleted tag moved: the worker's write, guard log or not (#423, #442)
	staged  []string // paths staged during the attempt, for flywheel to unstage
	note    string   // names what changed
}

// gitWriteNote compares dir's git state with before, the state captured at
// dispatch (issue #314, #423). Nothing captured (captured false) or nothing
// moved is no change; a final state that cannot be read counts as a change
// (#318 review). The note names each part: "HEAD: <old> -> <new>", "branch:
// ...", "stash", "index: staged <paths>", "index: unstaged <paths>", "tags:
// +<name>/-<name>", a tag on a remote-tracking commit marked "(on a
// remote-tracking commit)".
//
// It compares end points only: a push, or a write undone before the attempt
// ends, leaves them equal. Concurrent attempts in one worktree share HEAD, and
// every worktree of a repository shares refs/stash and the tags, so a history
// change is seen by each attempt that overlapped it; gitWriteVerdict charges
// HEAD and stash moves, and a tag added or moved onto a commit a
// remote-tracking ref contains (a fetch, #442), to the worker only with guard
// evidence (#361). An index change, a local-only tag or a deleted tag needs
// none: the guard may never be reached (#423). An unstaged path
// counts only while HEAD stayed put (a commit by another process empties the
// staged list too).
func gitWriteNote(dir string, before gitState, captured bool) gitChange {
	if !captured {
		return gitChange{}
	}
	after, ok := readGitState(dir)
	if !ok {
		return gitChange{changed: true, note: "git history changed during the attempt: " + before.String() + " -> (unreadable)"}
	}
	var parts []string
	var ch gitChange
	if after.head != before.head {
		parts = append(parts, "HEAD: "+before.head+" -> "+after.head)
	}
	if after.branch != before.branch {
		parts = append(parts, "branch: "+before.branch+" -> "+after.branch)
	}
	if after.stash != before.stash {
		parts = append(parts, "stash")
	}
	ch.staged = setMinus(after.index, before.index)
	if len(ch.staged) > 0 {
		parts = append(parts, "index: staged "+listClip(ch.staged))
		ch.worker = true
	}
	if unstaged := setMinus(before.index, after.index); len(unstaged) > 0 {
		parts = append(parts, "index: unstaged "+listClip(unstaged))
		ch.worker = ch.worker || after.head == before.head
	}
	if tags := tagDelta(before.tags, after.tags); len(tags) > 0 {
		shared := sharedTags(dir, setMinus(after.tags, before.tags))
		for i, t := range tags {
			if shared[t] {
				tags[i] = t + " (on a remote-tracking commit)"
			} else {
				ch.worker = true
			}
		}
		parts = append(parts, "tags: "+listClip(tags))
	}
	if len(parts) == 0 {
		return gitChange{}
	}
	ch.changed = true
	ch.note = "git history changed during the attempt: " + strings.Join(parts, "; ")
	return ch
}

// gitWriteVerdict decides the git-write signal from gitWriteNote's result and
// the write subcommands the git guard refused during the attempt (#361): a
// moved HEAD or stash, or a tag on a remote-tracking commit (tags are shared
// refs, #442), is charged to the worker only with evidence that it tried a
// write; otherwise another process moved it and the note says so. An index
// write, a local-only tag or a deleted tag is the worker's without that
// evidence (#423).
func gitWriteVerdict(ch gitChange, refused []string) (signal bool, outNote string) {
	if !ch.changed {
		return false, ""
	}
	if len(refused) > 0 {
		return true, ch.note + "; the worker tried: " + strings.Join(refused, ", ")
	}
	if ch.worker {
		return true, ch.note + "; an index or tag write is the worker's whether or not the guard saw it (#423)"
	}
	return false, ch.note + "; no worker git write was recorded by the guard (another process moved it, e.g. the lead committing in a shared worktree)"
}

// restoreIndex unstages paths in wt (#423) with flywheel's own git, never the
// worker's guard: `git reset -q -- <paths>` keeps the content in the working
// tree.
func restoreIndex(wt string, paths []string) error {
	args := append([]string{"--literal-pathspecs", "reset", "-q", "--"}, paths...)
	rc, _, stderr, err := runCmdSplit(wt, gitArgs(args), nil)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("git reset failed (rc=%d): %s", rc, strings.TrimSpace(string(stderr)))
	}
	return nil
}

// setMinus returns the items of a not in b, in a's order.
func setMinus(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	return out
}

// tagDelta names tag changes as +<name> (new or moved) and -<name> (deleted).
func tagDelta(before, after []string) []string {
	var out []string
	for _, t := range setMinus(after, before) {
		out = append(out, "+"+strings.TrimPrefix(strings.Fields(t)[0], "refs/tags/"))
	}
	for _, t := range setMinus(before, after) {
		name := strings.Fields(t)[0]
		if !strings.Contains("\n"+strings.Join(after, "\n"), "\n"+name+" ") {
			out = append(out, "-"+strings.TrimPrefix(name, "refs/tags/"))
		}
	}
	return out
}

// sharedTags returns the "+<name>" entries of tagDelta, among the added or
// moved tags ("refs/tags/<name> <sha>"), whose commit (annotated tags peeled)
// a remote-tracking ref contains: a fetch brings such a tag in (#442). A query
// that fails leaves the tag out, so it stays charged (#423). Read-only git.
func sharedTags(dir string, added []string) map[string]bool {
	shared := map[string]bool{}
	for _, t := range added {
		f := strings.Fields(t)
		if len(f) < 2 {
			continue
		}
		out, absent, err := gitQuery(dir, "for-each-ref", "--contains", f[1]+"^{commit}", "--format=%(refname)", "refs/remotes")
		if err == nil && !absent && out != "" {
			shared["+"+strings.TrimPrefix(f[0], "refs/tags/")] = true
		}
	}
	return shared
}

// listClip joins paths with ", ", naming at most ten.
func listClip(items []string) string {
	if len(items) > 10 {
		return strings.Join(items[:10], ", ") + fmt.Sprintf(" (+%d more)", len(items)-10)
	}
	return strings.Join(items, ", ")
}

// joinNote appends extra to note with "; " when both are non-empty.
func joinNote(note, extra string) string {
	switch {
	case extra == "":
		return note
	case note == "":
		return extra
	}
	return note + "; " + extra
}
