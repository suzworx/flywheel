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
// persona.
func RecordAmended(dir, task, brief, note string) error {
	resolved := brief
	if resolved == "" {
		events, err := ReadEvents(dir)
		if err != nil {
			return err
		}
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
	briefsDir := filepath.Join(dir, ".flywheel", "briefs")
	name := task + ".prev.brief.txt"
	if err := atomicWrite(briefsDir, name, task+".prev.brief-*.txt", content); err != nil {
		return err
	}
	header, err := ParseBriefHeader(path)
	if err != nil {
		return fmt.Errorf("read brief %s: %w", resolved, err)
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
