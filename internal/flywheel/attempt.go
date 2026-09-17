package flywheel

import (
	"errors"
	"fmt"
	"path/filepath"
)

// errNoPlannedBrief is AttemptBrief's error when a task has no planned or
// amended event carrying a brief path.
var errNoPlannedBrief = errors.New("no planned event")

// AttemptBrief resolves the brief header to measure for a task's current
// attempt, and the repo-relative paths it read, in order.
//
// The base brief is the latest `planned` or `amended` event's `brief` for
// task, whichever comes later; when neither exists, the error wraps
// errNoPlannedBrief with the same message gauges.go gave before AttemptBrief
// existed. Each brief (base and delta) is measured from the event's recorded
// `header` when it carries one, falling back to parsing the file at the
// recorded path only when it does not (issue #259): a verifier needs the log
// and nothing else, and a brief edited on disk after the fact does not
// rewrite what a pass is measured against. A fresh attempt (id `r<N>`, see
// events.go's attemptOK) always uses the base brief alone, whatever path its
// `flywheel run` copies an external brief into .flywheel/briefs/<task>.txt
// (#87), which differs in path but not content from the base brief, and
// that copy is not a correction. Only a correction attempt (id `c<N>`) can
// carry a delta: the `brief` of the latest `dispatched` event for that
// attempt is parsed when its path differs from the base brief, and when its
// content does too (compared by the parsed headers' SHA256 — a
// byte-identical copy adds nothing). When it does, gate: lines come from
// the delta when it declares any, otherwise from the base brief; owns is
// the union of both, base first, without duplicates; needs stays the base
// brief's. A delta file that is missing or unreadable is an error naming
// it. Relative paths resolve against dir.
func AttemptBrief(dir string, events []Event, task string) (BriefHeader, []string, error) {
	basePath, attempt, baseEv := latestBaseBriefAndAttempt(events, task)
	if basePath == "" {
		return BriefHeader{}, nil, fmt.Errorf("task %q has %w; record one with: flywheel log --task %s --kind planned --brief <path>", task, errNoPlannedBrief, task)
	}
	header, err := briefHeaderAt(dir, basePath, baseEv)
	if err != nil {
		return BriefHeader{}, nil, fmt.Errorf("parse brief %s: %w", basePath, err)
	}
	paths := []string{basePath}

	if attempt == "" || attempt[0] != 'c' {
		return header, paths, nil
	}

	promptPath := ""
	var promptEv *Event
	for i := range events {
		e := &events[i]
		if e.Task == task && e.Kind == "dispatched" && e.Attempt == attempt && e.Brief != "" {
			promptPath = e.Brief
			promptEv = e
		}
	}
	if promptPath == "" || promptPath == basePath {
		return header, paths, nil
	}

	prompt, err := briefHeaderAt(dir, promptPath, promptEv)
	if err != nil {
		return BriefHeader{}, nil, fmt.Errorf("read prompt %s: %w", promptPath, err)
	}
	if prompt.SHA256 == header.SHA256 {
		return header, paths, nil
	}
	paths = append(paths, promptPath)

	merged := header
	if len(prompt.Gates) > 0 {
		merged.Gates = prompt.Gates
	}
	merged.Owns = unionStrings(header.Owns, prompt.Owns)
	merged.Exclusive = unionStrings(header.Exclusive, prompt.Exclusive)
	return merged, paths, nil
}

// latestBaseBriefAndAttempt scans events in order for task's latest
// planned-or-amended brief path (and the event carrying it), and the last
// nonempty attempt seen.
func latestBaseBriefAndAttempt(events []Event, task string) (brief, attempt string, ev *Event) {
	for i := range events {
		e := &events[i]
		if e.Task != task {
			continue
		}
		if (e.Kind == "planned" || e.Kind == "amended") && e.Brief != "" {
			brief = e.Brief
			ev = e
		}
		if e.Attempt != "" {
			attempt = e.Attempt
		}
	}
	return brief, attempt, ev
}

// briefHeaderAt returns the brief header to measure for a path: the event's
// recorded header when it carries one (issue #259), otherwise the file at
// path parsed as before. The fallback is required: ledgers written before
// the header field existed have no header on their events, and they must
// keep resolving by reading the file exactly as today.
func briefHeaderAt(dir, path string, ev *Event) (BriefHeader, error) {
	if ev != nil && ev.Header != nil {
		return *ev.Header, nil
	}
	return ParseBriefHeader(resolveBriefPath(dir, path))
}

// resolveBriefPath joins a repo-relative brief path against dir, leaving an
// absolute path unchanged.
func resolveBriefPath(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// unionStrings returns a's elements followed by b's elements not already
// present, preserving order and dropping duplicates.
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
