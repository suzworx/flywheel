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
	changed bool      // anything moved, or the final state is unreadable
	worker  bool      // a part is the worker's write, guard log or not (#423)
	staged  []string  // paths staged during the attempt, for flywheel to unstage
	parts   []gitPart // what changed, each attributed (#621)
	note    string    // names what changed, as text(nil)
}

// gitWriteNote compares dir's git state with before, the state captured at
// dispatch (issue #314, #423). Nothing captured (captured false) or nothing
// moved is no change; a final state that cannot be read counts as a change
// (#318 review). The note names each part — "HEAD: <old> -> <new>", "branch:
// ...", "stash", "index: staged <paths>", "index: unstaged <paths>", "tags:
// +<name>" or "tags: -<name>" — and whose it is (#621).
//
// It compares end points only: a push, or a write undone before the attempt
// ends, leaves them equal. Concurrent attempts in one worktree share HEAD, and
// every worktree of a repository shares refs/stash and the tags, so a history
// change is seen by each attempt that overlapped it. Attribution:
//   - the worker's whatever the guard logged (#423: the guard may never be
//     reached): a staged path, an unstaged path while HEAD stayed put, a tag
//     added or moved onto a local-only commit, a deleted tag;
//   - the worker's only with a guard-logged write (#361): a HEAD or stash
//     move, a branch change that moved HEAD (a checkout);
//   - another process's, never charged (#442, #621): a tag on a commit a
//     remote-tracking ref contains (fetched), the worktree's branch renamed
//     with HEAD unchanged, an unstaged path when HEAD moved. Other branches
//     and refs/remotes are not compared at all.
func gitWriteNote(dir string, before gitState, captured bool) gitChange {
	if !captured {
		return gitChange{}
	}
	after, ok := readGitState(dir)
	if !ok {
		return gitChange{changed: true, note: "git history changed during the attempt: " + before.String() + " -> (unreadable)"}
	}
	var parts []gitPart
	var ch gitChange
	headMoved := after.head != before.head
	if headMoved {
		parts = append(parts, gitPart{text: "HEAD: " + before.head + " -> " + after.head})
	}
	if after.branch != before.branch {
		if headMoved {
			parts = append(parts, gitPart{text: "branch: " + before.branch + " -> " + after.branch})
		} else {
			parts = append(parts, gitPart{text: "branch: " + before.branch + " -> " + after.branch, who: gitByOther,
				why: "a rename that keeps HEAD's commit, and the guard refuses a worker's git branch"})
		}
	}
	if after.stash != before.stash {
		parts = append(parts, gitPart{text: "stash"})
	}
	ch.staged = setMinus(after.index, before.index)
	if len(ch.staged) > 0 {
		parts = append(parts, gitPart{text: "index: staged " + listClip(ch.staged), who: gitByWorker, why: "an index write"})
	}
	if unstaged := setMinus(before.index, after.index); len(unstaged) > 0 {
		if headMoved {
			parts = append(parts, gitPart{text: "index: unstaged " + listClip(unstaged), who: gitByOther,
				why: "the commit that moved HEAD emptied the staged list"})
		} else {
			parts = append(parts, gitPart{text: "index: unstaged " + listClip(unstaged), who: gitByWorker, why: "an index write"})
		}
	}
	if tags := tagDelta(before.tags, after.tags); len(tags) > 0 {
		shared := sharedTags(dir, setMinus(after.tags, before.tags))
		for _, t := range tags {
			switch {
			case shared[t]:
				parts = append(parts, gitPart{text: "tags: " + t, who: gitByOther, why: "fetched, its commit is on a remote-tracking ref"})
			case strings.HasPrefix(t, "-"):
				parts = append(parts, gitPart{text: "tags: " + t, who: gitByWorker, why: "a deleted tag"})
			default:
				parts = append(parts, gitPart{text: "tags: " + t, who: gitByWorker, why: "a tag on a local-only commit"})
			}
		}
	}
	if len(parts) == 0 {
		return gitChange{}
	}
	ch.changed = true
	ch.parts = parts
	for _, p := range parts {
		ch.worker = ch.worker || p.who == gitByWorker
	}
	ch.note = ch.text(nil)
	return ch
}

// gitPart is one change gitWriteNote found and whose it is: the worker's
// (gitByWorker), another process's (gitByOther), or, with who empty, the
// worker's only when the guard logged a write it tried (#361).
type gitPart struct {
	text, who, why string
}

const (
	gitByWorker = "worker"
	gitByOther  = "other"
)

// text is the change's note with each part attributed given the writes the
// guard refused: "<part> (the worker's: <why>)" or "<part> (changed by
// another process: <why>)". A change without parts (an unreadable final
// state) is its note alone.
func (ch gitChange) text(refused []string) string {
	if len(ch.parts) == 0 {
		return ch.note
	}
	out := make([]string, 0, len(ch.parts))
	for _, p := range ch.parts {
		who, why := p.who, p.why
		if who == "" {
			who, why = gitByOther, "no worker git write was recorded by the guard"
			if len(refused) > 0 {
				who, why = gitByWorker, "it tried git "+strings.Join(refused, ", ")
			}
		}
		if who == gitByWorker {
			out = append(out, p.text+" (the worker's: "+why+")")
		} else {
			out = append(out, p.text+" (changed by another process: "+why+")")
		}
	}
	return "git history changed during the attempt: " + strings.Join(out, "; ")
}

// gitWriteVerdict decides the git-write signal from gitWriteNote's result and
// the write subcommands the git guard refused during the attempt (#361; the
// guard lets reads such as git config --get through unlogged, #621). It
// signals when a part is the worker's (#423), or when the guard refused a
// write and a part is the worker's only with that evidence (a HEAD, stash or
// branch-with-HEAD move, or a change without parts). A part another process
// made — a fetched tag, a branch rename — never signals (#442, #621). The
// note attributes each part (gitChange.text).
func gitWriteVerdict(ch gitChange, refused []string) (signal bool, outNote string) {
	if !ch.changed {
		return false, ""
	}
	evidenced := len(ch.parts) == 0
	for _, p := range ch.parts {
		evidenced = evidenced || p.who == ""
	}
	note := ch.text(refused)
	switch {
	case len(refused) > 0 && (ch.worker || evidenced):
		return true, note + "; the worker tried: " + strings.Join(refused, ", ")
	case ch.worker:
		return true, note + "; an index or tag write is the worker's whether or not the guard saw it (#423)"
	case len(refused) > 0:
		return false, note + "; the worker tried: " + strings.Join(refused, ", ") + ", but only changes another process makes moved"
	}
	return false, note + "; no worker git write was recorded by the guard (another process moved it, e.g. the lead committing in a shared worktree)"
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
