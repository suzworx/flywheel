package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RetroOptions is one `flywheel retro` record (issue #854): the lead session,
// the agents, briefs or skills the retrospective changed (repo-relative paths
// under dir), or None when it changed nothing, and the note.
type RetroOptions struct {
	Task    string
	Session string
	Changed []string
	None    bool
	Note    string
}

// Retro records a retro event for a landed task (issue #854). The task must
// have a landed event (rule retro-not-landed). Exactly one of Changed and None
// is required; every Changed path must be a local path that exists under dir.
// --none is recorded as empty Paths. A second retro on a task is appended too.
func Retro(dir string, o RetroOptions) error {
	if dir == "" {
		dir = "."
	}
	if o.Session == "" || o.Note == "" {
		return fmt.Errorf("a retro requires a session and a note")
	}
	if (len(o.Changed) > 0) == o.None {
		return fmt.Errorf("a retro requires exactly one of changed paths or none")
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(events, func(e Event) bool { return e.Task == o.Task && e.Kind == "landed" }) {
		return &RuleRefusal{Rule: "retro-not-landed", Fix: fmt.Sprintf("task %s has no landed event; land it first: flywheel land %s --commit <sha>", o.Task, o.Task)}
	}
	var paths []string
	for _, p := range o.Changed {
		if p == "" || !filepath.IsLocal(filepath.FromSlash(p)) {
			return fmt.Errorf("changed path %q is not a relative path inside %s", p, dir)
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			return fmt.Errorf("changed path %q does not exist under %s: %w", p, dir, err)
		}
		paths = append(paths, filepath.ToSlash(p))
	}
	ev := Event{Task: o.Task, Kind: "retro", Session: o.Session, Note: o.Note, Paths: paths}
	if err := AppendEvent(dir, ev); err != nil {
		return fmt.Errorf("append retro for %s: %w", o.Task, err)
	}
	if _, err := WriteState(dir); err != nil {
		return fmt.Errorf("refresh state for %s: %w", o.Task, err)
	}
	return nil
}

// RetroDue returns, sorted, the tasks whose latest landed event is at or
// after since and that have no retro event after it in log order (issue
// #854). Only landed counts as a landing, as in Derive: land_corrected
// changes the commit, not the status. A landed event whose ts does not parse
// is never due.
func RetroDue(events []Event, since time.Time) []string {
	landed := map[string]int{}
	retro := map[string]int{}
	for i, e := range events {
		switch e.Kind {
		case "landed":
			landed[e.Task] = i
		case "retro":
			retro[e.Task] = i
		}
	}
	var due []string
	for task, i := range landed {
		ts, err := time.Parse(time.RFC3339Nano, events[i].TS)
		if err != nil || ts.Before(since) {
			continue
		}
		if r, ok := retro[task]; ok && r > i {
			continue
		}
		due = append(due, task)
	}
	slices.Sort(due)
	return due
}

// retroDue is RetroDue under the config's lint.retro_required_since, nil when
// the setting is off or does not parse (config validation reports that).
func retroDue(cfg Config, events []Event) []string {
	if cfg.Lint == nil || cfg.Lint.RetroRequiredSince == "" {
		return nil
	}
	since, err := time.Parse(time.RFC3339, cfg.Lint.RetroRequiredSince)
	if err != nil {
		return nil
	}
	return RetroDue(events, since)
}

// retroRefusal is the rule retro-missing refusal (issue #854) when retros are
// due, leaving out task when the dispatch is a correction of it; else nil.
func retroRefusal(cfg Config, events []Event, task string, correction bool) *RuleRefusal {
	due := retroDue(cfg, events)
	if correction {
		due = slices.DeleteFunc(due, func(t string) bool { return t == task })
	}
	if len(due) == 0 {
		return nil
	}
	return &RuleRefusal{Rule: "retro-missing", Fix: fmt.Sprintf("landed units have no retro: %s; record each with flywheel retro <task> --session S (--changed P,... | --none) --note TEXT", strings.Join(due, ", "))}
}
