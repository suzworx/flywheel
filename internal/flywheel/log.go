package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
)

// RecordPlanned parses the brief header at brief (resolved against dir when
// relative) and appends a planned event for task carrying the brief path
// exactly as given, the whole parsed header, the header's owns and needs
// arrays (paths stored exactly as given in the header), and the planner
// persona.
func RecordPlanned(dir, task, brief string) error {
	header, err := ParseBriefHeader(resolveBriefPath(dir, brief))
	if err != nil {
		return fmt.Errorf("read brief %s: %w", brief, err)
	}
	return AppendEvent(dir, Event{
		Task:    task,
		Kind:    "planned",
		Brief:   brief,
		Owns:    header.Owns,
		Needs:   header.Needs,
		Header:  &header,
		Persona: "planner",
	})
}

// RecordAmended resolves the brief path to amend — the one given, or when
// empty, task's latest planned-or-amended event's Brief (an error naming the
// task when neither exists) — reads that file (an error naming it when
// missing or unreadable), snapshots its current content to
// .flywheel/briefs/<task>.prev.brief.txt (atomic temp file + rename,
// replacing an earlier copy), the previous brief text the amendment
// replaces, then appends the amended event carrying the brief path, the
// whole parsed header, the header's owns and needs, note, and the planner
// persona. When the task's current attempt was already dispatched with a
// header whose gate set the new brief would change, the amendment is refused
// as a RuleRefusal before anything is written (issue #272): a pass is
// measured against the dispatched header, so the amendment would be inert.
// The fix is a correction delta — `flywheel run <task> --delta <file>` —
// while amending before dispatch, or changing anything but the gates of a
// dispatched attempt (widening owns:, fixing prose), still works exactly as
// before.
func RecordAmended(dir, task, brief, note string) error {
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	resolved := brief
	if resolved == "" {
		resolved, _, _ = latestBaseBriefAndAttempt(events, task)
		if resolved == "" {
			return fmt.Errorf("task %q has no recorded brief path to amend; pass --brief", task)
		}
	}
	path := resolveBriefPath(dir, resolved)
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read brief %s: %w", resolved, err)
	}
	header, err := ParseBriefHeader(path)
	if err != nil {
		return fmt.Errorf("read brief %s: %w", resolved, err)
	}
	if r := inertAmendRefusal(task, events, header); r != nil {
		return r
	}
	briefsDir := filepath.Join(dir, ".flywheel", "briefs")
	name := task + ".prev.brief.txt"
	if err := atomicWrite(briefsDir, name, task+".prev.brief-*.txt", content); err != nil {
		return err
	}
	return AppendEvent(dir, Event{
		Task:    task,
		Kind:    "amended",
		Brief:   resolved,
		Owns:    header.Owns,
		Needs:   header.Needs,
		Note:    note,
		Header:  &header,
		Persona: "planner",
	})
}

// inertAmendRefusal returns a RuleRefusal when task's current attempt was
// already dispatched with a header whose gate set (gate: and live-gate:
// lines, compared as parsed lists, not file bytes) the amendment would
// change, and nil otherwise (issue #272). A pass is measured against the
// dispatched header, so such an amendment would record nothing measurable —
// an operation that reports success and changes nothing is worse than one
// that fails. Amending before dispatch, or changing anything but the gates
// of a dispatched attempt, is not inert and is not refused.
func inertAmendRefusal(task string, events []Event, header BriefHeader) error {
	_, attempt, _ := latestBaseBriefAndAttempt(events, task)
	if attempt == "" {
		return nil
	}
	dispatched := freshDispatchedHeader(events, task, attempt)
	if dispatched == nil {
		return nil
	}
	if gatesEqual(dispatched.Gates, header.Gates) && gatesEqual(dispatched.LiveGates, header.LiveGates) {
		return nil
	}
	return &RuleRefusal{
		Rule: "amended",
		Fix: fmt.Sprintf(
			"attempt %s of task %s was already dispatched with these gates, and a pass is measured against the dispatched header, so an amendment cannot change them; dispatch a correction delta instead: flywheel run %s --delta <file>",
			attempt, task, task),
	}
}

// gatesEqual reports whether two gate lists are the same commands in the
// same order (validate records gate indices, so order is part of the set).
func gatesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
