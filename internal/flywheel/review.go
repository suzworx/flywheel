package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ReviewOptions configures one review.
type ReviewOptions struct {
	Dir       string // flywheel root; default "."
	Workdir   string // git working tree to review; default Dir
	Verdict   string
	Session   string
	Model     string // the reviewer's model, recorded as its identity (issue #53); optional
	Note      string
	Checklist []string // domain checklist lines the reviewer confirms; optional
}

// ReviewResult reports what a successful review found before recording the
// reviewed event: the workdir's tree hash, the task's latest finished reason
// and report path, when either exists, and the checklist lines confirmed.
type ReviewResult struct {
	Tree           string
	Finished       bool
	FinishedReason string
	Report         bool
	ReportPath     string
	Checklist      []string
}

// ReviewTask re-runs a task's declared gates and owns check on an isolated
// copy of the workdir's current tree (HEAD plus its uncommitted changes), so
// a finished task can be judged without trusting a worktree another
// in-flight unit might still be touching (issue #24: disjoint owns is not a
// disjoint import graph). Refusals mirror inspect.go's style: T8 for a bad
// verdict, T4 for a missing or worker --session, "owns" for a changed path
// outside the brief's owns, and T3 for a gate that fails in the isolated
// tree. On success the reviewed event is recorded (Verdict, Session, Model,
// Note, and Tree = the workdir's tree hash) and derived state is refreshed.
func ReviewTask(dir, task string, o ReviewOptions) (ReviewResult, error) {
	if o.Dir == "" {
		o.Dir = "."
	}
	workdir := wd(o.Workdir, o.Dir)
	switch o.Verdict {
	case "pass", "correct", "reject":
	default:
		return ReviewResult{}, &RuleRefusal{Rule: "T8", Fix: fmt.Sprintf("verdict %q is not pass, correct, or reject", o.Verdict)}
	}
	if o.Session == "" {
		return ReviewResult{}, &RuleRefusal{Rule: "T4", Fix: "a reviewer --session is required"}
	}
	events, err := ReadEvents(o.Dir)
	if err != nil {
		return ReviewResult{}, err
	}
	if r := sessionClash(task, events, o.Session); r != "" {
		return ReviewResult{}, &RuleRefusal{Rule: "T4", Fix: r}
	}
	header, _, err := AttemptBrief(o.Dir, events, task)
	if err != nil {
		return ReviewResult{}, err
	}
	changed, err := changedPaths(workdir)
	if err != nil {
		return ReviewResult{}, err
	}
	var outside []string
	for _, p := range changed {
		if !ownsContains(header.Owns, p) {
			outside = append(outside, p)
		}
	}
	if len(outside) > 0 {
		return ReviewResult{}, &RuleRefusal{Rule: "owns", Fix: fmt.Sprintf("changed paths outside owns: %s", strings.Join(outside, ", "))}
	}
	tmp, err := isolateWorktree(workdir)
	if err != nil {
		return ReviewResult{}, err
	}
	defer os.RemoveAll(tmp)
	for i, gate := range header.Gates {
		n := strconv.Itoa(i + 1)
		rc, _, _, gerr := runGate(tmp, gate)
		if gerr != nil {
			return ReviewResult{}, gerr
		}
		if rc != 0 {
			return ReviewResult{}, &RuleRefusal{Rule: "T3", Fix: fmt.Sprintf("gate %s failed in the isolated worktree: %s", n, gate)}
		}
	}
	tree, err := treeHash(workdir)
	if err != nil {
		return ReviewResult{}, err
	}
	res := ReviewResult{Tree: tree, Checklist: o.Checklist}
	if reason, ok := latestFinishedReason(events, task); ok {
		res.Finished = true
		res.FinishedReason = reason
	}
	if path, ok := latestReportPath(events, task); ok {
		res.Report = true
		res.ReportPath = path
	}
	note := o.Note
	if len(o.Checklist) > 0 {
		suffix := fmt.Sprintf("checklist: %d confirmed", len(o.Checklist))
		if note != "" {
			note = note + "; " + suffix
		} else {
			note = suffix
		}
	}
	if err := AppendEvent(o.Dir, Event{
		TS: "", Task: task, Kind: "reviewed", Verdict: o.Verdict,
		Tree: tree, Session: o.Session, Model: o.Model, Note: note, Persona: "reviewer",
	}); err != nil {
		return ReviewResult{}, err
	}
	_, _ = WriteState(o.Dir)
	return res, nil
}

// latestFinishedReason returns the latest finished event's reason for task,
// and whether any finished event exists.
func latestFinishedReason(events []Event, task string) (string, bool) {
	reason := ""
	ok := false
	for _, e := range events {
		if e.Task == task && e.Kind == "finished" {
			reason = e.Reason
			ok = true
		}
	}
	return reason, ok
}

// latestReportPath returns the latest report event's path for task, and
// whether any report event exists.
func latestReportPath(events []Event, task string) (string, bool) {
	path := ""
	ok := false
	for _, e := range events {
		if e.Task == task && e.Kind == "report" {
			path = e.Path
			ok = true
		}
	}
	return path, ok
}

// isolateWorktree materializes workdir's current tree — HEAD's tracked
// files (the gauges temp-index trick: git read-tree HEAD then git
// checkout-index into a fresh directory, so the shared index is never
// touched) plus workdir's own uncommitted changes (the tracked diff applied
// with git apply, untracked files copied by hand) — into a new temp
// directory for gates to run against in isolation. The caller removes the
// returned directory; the shared repo is never written except through the
// throwaway index.
func isolateWorktree(workdir string) (string, error) {
	tmp, err := os.MkdirTemp("", "fw-review-*")
	if err != nil {
		return "", fmt.Errorf("create temp worktree: %w", err)
	}
	idxFile, err := os.CreateTemp("", "fw-review-index-*")
	if err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("create temp index: %w", err)
	}
	idx := idxFile.Name()
	idxFile.Close()
	os.Remove(idx)
	defer os.Remove(idx)

	if _, err := gitRun(workdir, idx, []string{"read-tree", "HEAD"}); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	prefix := filepath.ToSlash(tmp) + "/"
	if _, err := gitRun(workdir, idx, []string{"checkout-index", "-a", "-f", "--prefix=" + prefix}); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if _, err := gitRead(tmp, []string{"init", "-q"}); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := applyTrackedDiff(workdir, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	if err := copyUntracked(workdir, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	return tmp, nil
}

// applyTrackedDiff writes workdir's tracked changes against HEAD to a patch
// file and applies it inside tmp; an empty diff (nothing changed) is a
// no-op.
func applyTrackedDiff(workdir, tmp string) error {
	diff, err := gitRead(workdir, []string{"diff", "--binary", "HEAD"})
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	patch, err := os.CreateTemp("", "fw-review-patch-*.diff")
	if err != nil {
		return fmt.Errorf("create patch file: %w", err)
	}
	path := patch.Name()
	defer os.Remove(path)
	if _, err := patch.WriteString(diff); err != nil {
		patch.Close()
		return fmt.Errorf("write patch file: %w", err)
	}
	if err := patch.Close(); err != nil {
		return fmt.Errorf("close patch file: %w", err)
	}
	if _, err := gitRead(tmp, []string{"apply", path}); err != nil {
		return fmt.Errorf("apply tracked diff: %w", err)
	}
	return nil
}

// copyUntracked copies every untracked file workdir's git status lists into
// the same relative path under tmp, creating parent directories as needed.
func copyUntracked(workdir, tmp string) error {
	untracked, err := gitRead(workdir, []string{"ls-files", "--others", "--exclude-standard"})
	if err != nil {
		return err
	}
	for _, line := range strings.Split(untracked, "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		p = filepath.ToSlash(p)
		src := filepath.Join(workdir, filepath.FromSlash(p))
		dst := filepath.Join(tmp, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("read %s: %w", src, err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
	}
	return nil
}
