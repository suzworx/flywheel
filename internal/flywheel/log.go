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
// persona. The dispatch lock is held across the read, the inert-amendment
// check and the append (issue #272): the same lock file, ordering and
// timings Run uses, so an amendment and a dispatch serialise instead of each
// deciding from a ledger missing the other's event. When the task's current
// attempt was already dispatched and the amendment would change the gate set
// the attempt is effectively measured against without succeeding in changing
// it, the amendment is refused as a RuleRefusal before anything is written:
// a pass is measured against the attempt's effective header, so the
// amendment would be inert. The fix is a correction delta —
// `flywheel run <task> --delta <file>` — while amending before dispatch, or
// changing anything but the gates of a dispatched attempt (widening owns:,
// fixing prose), still works exactly as before.
func RecordAmended(dir, task, brief, note string) error {
	// The dispatch lock (issue #272): the log is read, the inert-amendment
	// check is run and the amended event is appended under
	// .flywheel/dispatch.lock — the same lock file, the same ordering and
	// the same timings as Run's dispatch — so the check can never decide
	// from a ledger a concurrent dispatch is mid-way through changing, and a
	// dispatch can never land between the check and the append.
	release, err := acquireRepoLock(dir, "dispatch.lock", defaultRepoLockTimings())
	if err != nil {
		return err
	}
	defer release()
	return recordAmendedLocked(dir, task, brief, note)
}

// recordAmendedLocked is RecordAmended's critical section, run under the
// dispatch lock: resolve the brief path, read and parse the brief, refuse an
// inert gate amendment, snapshot the current content, and append the amended
// event.
func recordAmendedLocked(dir, task, brief, note string) error {
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
	if r := inertAmendRefusal(dir, task, resolved, events, header); r != nil {
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

// AppendAmendedEvent appends one JSON-ingested amended event through the
// same inert-amendment check and the same dispatch lock as RecordAmended
// (issue #272): a `flywheel log --json` amended must never bypass the
// refusal the flag path goes through. The new header is the event's recorded
// Header when it carries one (issue #259), otherwise the file at the event's
// Brief path — resolved to the task's latest planned-or-amended brief when
// the event names none, and recorded on the appended event — parsed exactly
// as RecordAmended parses it. An inert amendment is refused before anything
// is written; the snapshot RecordAmended takes is a flag-command artifact and
// is not taken here.
func AppendAmendedEvent(dir string, e Event) error {
	release, err := acquireRepoLock(dir, "dispatch.lock", defaultRepoLockTimings())
	if err != nil {
		return err
	}
	defer release()
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	resolved := e.Brief
	if resolved == "" {
		resolved, _, _ = latestBaseBriefAndAttempt(events, e.Task)
		if resolved == "" {
			return fmt.Errorf("task %q has no recorded brief path to amend; pass --brief", e.Task)
		}
		e.Brief = resolved
	}
	header, err := amendedHeaderAt(dir, resolved, e.Header)
	if err != nil {
		return err
	}
	if r := inertAmendRefusal(dir, e.Task, resolved, events, header); r != nil {
		return r
	}
	return AppendEvent(dir, e)
}

// amendedHeaderAt returns the header an amendment carries: the event's
// recorded header when it has one, otherwise the file at path parsed the way
// RecordAmended parses the brief it amends (issue #259).
func amendedHeaderAt(dir, path string, h *BriefHeader) (BriefHeader, error) {
	if h != nil {
		return *h, nil
	}
	header, err := ParseBriefHeader(resolveBriefPath(dir, path))
	if err != nil {
		return BriefHeader{}, fmt.Errorf("read brief %s: %w", path, err)
	}
	return header, nil
}

// inertAmendRefusal returns a RuleRefusal when the amendment carried by
// header would change the gate set — gate: and live-gate: lines, compared as
// parsed lists, not file bytes — that task's current attempt is effectively
// measured against, without succeeding in changing it, and nil otherwise
// (issue #272). The comparison is between the amendment and the attempt's
// EFFECTIVE gate set, AttemptBrief's resolution, not the dispatched header
// alone: a correction whose delta declares no gate: lines inherits the base
// gates, so amending them does change what validation would run and is
// allowed; a fresh attempt's dispatched header, or a correction delta's own
// gates, win over the base, so amending the base gates there changes nothing
// measurable — an operation that reports success and changes nothing is
// worse than one that fails. The effective set is computed twice, before and
// after the proposed amendment (the amendment appended to the same events),
// both through AttemptBrief itself, so the merge rule is shared and can
// never drift. Ordinary gates and live gates are covered separately: the
// base's live gates are always inherited — a correction's delta never
// replaces them — so an amendment touching only live-gate: lines takes
// effect on a correction attempt while it stays inert on a fresh dispatched
// one. Amending before dispatch, or changing anything but the gates of a
// dispatched attempt, is not inert and is not refused. An attempt whose
// effective set cannot be resolved (a legacy delta file that is gone, for
// instance) is never refused: the check must not block on evidence it
// cannot read.
func inertAmendRefusal(dir, task, resolved string, events []Event, header BriefHeader) error {
	_, attempt, _ := latestBaseBriefAndAttempt(events, task)
	if attempt == "" {
		return nil
	}
	before, _, err := AttemptBrief(dir, events, task)
	if err != nil {
		return nil
	}
	afterEvents := make([]Event, len(events), len(events)+1)
	copy(afterEvents, events)
	afterEvents = append(afterEvents, Event{Task: task, Kind: "amended", Brief: resolved, Header: &header})
	after, _, err := AttemptBrief(dir, afterEvents, task)
	if err != nil {
		return nil
	}
	// Refuse only when the amendment would not move the effective gate set:
	// the set after the amendment is what it was before (the dispatched
	// header or the delta's own gates win), and the amendment's gates differ
	// from that set (the amendment tries to change them).
	if !gatesEqual(before.Gates, after.Gates) || !gatesEqual(before.LiveGates, after.LiveGates) {
		return nil
	}
	if gatesEqual(before.Gates, header.Gates) && gatesEqual(before.LiveGates, header.LiveGates) {
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
