package flywheel

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrAlreadyLanded is wrapped in the error LandTask returns when the task was
// already landed with the same commit: landing is a no-op and the caller
// reports success.
var ErrAlreadyLanded = errors.New("already landed")

// CommitOK reports whether s looks like a commit id: 7 to 40 hex characters.
func CommitOK(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// LandTask records a landed event for task after enforcing T5: only a task
// whose derived status is passed may land, and an already-landed task may only
// repeat its recorded commit. On success the landed event is appended and the
// derived state refreshed.
//
// When leadImplemented is set, the landing is recorded as lead-implemented
// (the flag on the landed event) and the reason is put in the event's note,
// prefixed "lead-implemented: "; an operator-supplied note, when present, is
// kept first and the reason appended after "; " (issue #198).
func LandTask(dir, task, commit, note string, leadImplemented bool, reason string) error {
	if dir == "" {
		dir = "."
	}
	if !CommitOK(commit) {
		return fmt.Errorf("commit %q is not 7 to 40 hex characters", commit)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	landed := ""
	for _, e := range events {
		if e.Task == task && e.Kind == "landed" {
			landed = e.Commit
		}
	}
	if landed != "" {
		if landed == commit {
			return fmt.Errorf("%s already landed %s: %w", task, commit, ErrAlreadyLanded)
		}
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s already landed with commit %s; refusing to re-land with %s", task, landed, commit)}
	}
	status := ""
	for _, t := range Derive(events).Tasks {
		if t.ID == task {
			status = t.Status
			break
		}
	}
	if status != "passed" {
		return &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("task %s is not passed (status %q); land only after a passing inspection: flywheel inspect %s --verdict pass --session <session>", task, status, task)}
	}
	if leadImplemented {
		suffix := "lead-implemented: " + reason
		if note != "" {
			note = note + "; " + suffix
		} else {
			note = suffix
		}
	}
	// Resolve the tree of the commit being landed with the same
	// normalisation treeHash applies to a worktree — flywheel's own
	// bookkeeping (.flywheel/ and flywheel.md) removed — so the landed tree
	// equals the tree a validate pass inspected for the same content. It is
	// computed in a throwaway index seeded from the commit; the shared index
	// is never touched. A commit that is not in this repository cannot
	// resolve; the landed event then records an empty tree rather than
	// failing, because landing must not become refusable for a reporting
	// field.
	tree := landedTree(dir, commit)
	if err := AppendEvent(dir, Event{Task: task, Kind: "landed", Commit: commit, Tree: tree, Note: note, LeadImplemented: leadImplemented}); err != nil {
		return fmt.Errorf("append landed for %s: %w", task, err)
	}
	if _, err := WriteState(dir); err != nil {
		return fmt.Errorf("refresh state for %s: %w", task, err)
	}
	return nil
}

// landedTree returns the tree id of commit with flywheel's own bookkeeping
// (.flywheel/ and flywheel.md) removed, mirroring treeHash's normalisation so
// the landed tree is comparable with the tree a validate pass measured. It is
// computed in a temporary GIT_INDEX_FILE, the way treeHash creates and
// disposes of it: git read-tree the commit, git rm --cached the bookkeeping,
// git write-tree; the shared git index is never touched. A commit that cannot
// be read into the index (not in this repository) yields "" rather than an
// error: the landed event then records an empty tree.
func landedTree(dir, commit string) string {
	tmp, err := os.CreateTemp("", "fw-index-*")
	if err != nil {
		return ""
	}
	idx := tmp.Name()
	tmp.Close()
	os.Remove(idx)
	defer os.Remove(idx)
	if _, err := gitRun(dir, idx, []string{"read-tree", commit}); err != nil {
		return ""
	}
	if _, err := gitRun(dir, idx, []string{"rm", "-r", "--cached", "--ignore-unmatch", "--", ".flywheel", "flywheel.md"}); err != nil {
		return ""
	}
	out, err := gitRun(dir, idx, []string{"write-tree"})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}
