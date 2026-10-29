package flywheel

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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
// that copy is not a correction. When the fresh attempt's own `dispatched`
// event records a `header`, that header is what a pass is measured against
// (issue #259): it is the prompt the worker was actually given, so a brief
// edited on disk after the dispatch — one `Run` tolerates as drift without
// --strict-brief — cannot hide owns and gates the worker never saw. A
// dispatched event with no header (every pre-existing ledger) falls back to
// the base planned/amended header exactly as before. Only a correction
// attempt (id `c<N>`) can
// carry deltas: the `brief` of every correction dispatched after the task's
// latest fresh `r<N>` dispatch, up to and including the current attempt's
// latest `dispatched` event (see correctionDeltas; issue #912 — the tree is
// cumulative, so a later correction keeps what an earlier one widened). A
// delta counts when its path differs from the base brief and its content
// does too (compared by the parsed headers' SHA256 — a byte-identical copy
// adds nothing). gate: lines come from the current attempt's delta when it
// declares any, otherwise from the base brief, never from an earlier delta;
// owns, exclusive, needs-env and stage are the union of the base and every
// delta, base first, in log order, without duplicates; needs stays the base
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
		// A fresh attempt (r*) is measured against the header its own
		// dispatched event recorded when it carries one — the prompt actually
		// sent, not the base planned/amended header — and against the base
		// header only when the dispatched event has none (every ledger written
		// before the header field existed). An amended event recorded after
		// that dispatch widens its owns and exclusive (issue #281).
		if attempt != "" && attempt[0] == 'r' {
			if h := freshDispatchedHeader(events, task, attempt); h != nil {
				eff := *h
				// The recorded header is JSON: an invalid stage: line (issue
				// #781) is not in it, so the base brief's parse supplies them.
				eff.stageInvalid = header.stageInvalid
				for _, a := range amendedAfterDispatch(events, task, attempt) {
					eff.Owns = unionStrings(eff.Owns, a.Owns)
					eff.Exclusive = unionStrings(eff.Exclusive, a.Exclusive)
				}
				return eff, paths, nil
			}
		}
		return header, paths, nil
	}

	deltas := correctionDeltas(events, task, attempt)
	if len(deltas) == 0 {
		return header, paths, nil
	}
	merged := header
	for i, ev := range deltas {
		latest := i == len(deltas)-1
		if ev.Brief == basePath {
			continue
		}
		prompt, err := briefHeaderAt(dir, ev.Brief, ev)
		if err != nil {
			return BriefHeader{}, nil, fmt.Errorf("read prompt %s: %w", ev.Brief, err)
		}
		if prompt.SHA256 == header.SHA256 {
			continue
		}
		if !slices.Contains(paths, ev.Brief) {
			paths = append(paths, ev.Brief)
		}
		// Gates come from the latest attempt's delta only: an earlier
		// delta's gates were that attempt's (issue #912).
		if latest && len(prompt.Gates) > 0 {
			merged.Gates = prompt.Gates
		}
		merged.Owns = unionStrings(merged.Owns, prompt.Owns)
		merged.Exclusive = unionStrings(merged.Exclusive, prompt.Exclusive)
		// A correction cannot drop an environment requirement (issue #534).
		merged.NeedsEnv = unionStrings(merged.NeedsEnv, prompt.NeedsEnv)
		// Stage lines union as owns do (issue #781).
		for _, m := range prompt.Stage {
			if !slices.Contains(merged.Stage, m) {
				merged.Stage = append(merged.Stage, m)
			}
		}
		merged.stageInvalid = unionStrings(merged.stageInvalid, prompt.stageInvalid)
	}
	return merged, paths, nil
}

// correctionDeltas returns the dispatched events, in log order, whose briefs
// are the correction deltas in force for task's correction attempt `attempt`
// (issue #912): every correction (`c<N>`) dispatched after the task's latest
// fresh (`r<N>`) dispatched event, up to and including attempt's own latest
// dispatched event, one event per attempt (its latest). With no fresh
// dispatch (a legacy ledger) every correction of the task counts. When
// attempt has no dispatched event with a brief there is no delta at all, and
// the result is nil; the last element is always attempt's own.
func correctionDeltas(events []Event, task, attempt string) []*Event {
	start, end := 0, -1
	for i := range events {
		e := &events[i]
		if e.Task != task || e.Kind != "dispatched" || e.Attempt == "" {
			continue
		}
		if e.Attempt[0] == 'r' {
			start = i + 1
		}
		if e.Attempt == attempt && e.Brief != "" {
			end = i
		}
	}
	if end < start {
		return nil
	}
	var out []*Event
	for i := start; i <= end; i++ {
		e := &events[i]
		if e.Task != task || e.Kind != "dispatched" || e.Brief == "" || e.Attempt == "" || e.Attempt[0] != 'c' {
			continue
		}
		if e.Attempt == attempt && i != end {
			continue
		}
		// A re-dispatched correction counts once, at its latest dispatch.
		out = slices.DeleteFunc(out, func(o *Event) bool { return o.Attempt == e.Attempt })
		out = append(out, e)
	}
	return out
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

// freshDispatchedHeader returns the latest dispatched event's recorded header
// for task's fresh attempt `attempt`, or nil when that attempt has none. A
// task's attempt is dispatched at most once, so the latest matching event is
// the one; matching on the attempt keeps a later correction's dispatch out of
// a fresh attempt's resolution, and slicing the events to a prefix keeps a
// later dispatch out of a historical pass's (ruleT3 passes the prefix
// recorded up to and including the pass).
func freshDispatchedHeader(events []Event, task, attempt string) *BriefHeader {
	var h *BriefHeader
	for i := range events {
		e := &events[i]
		if e.Task == task && e.Kind == "dispatched" && e.Attempt == attempt && e.Header != nil {
			h = e.Header
		}
	}
	return h
}

// amendedAfterDispatch returns the headers of task's amended events recorded
// after the latest dispatched event of attempt, in log order (issue #281). An
// amendment is a recorded decision, not on-disk drift, so its widened owns and
// exclusive take effect on a fresh attempt already dispatched; its gates never
// do (RecordAmended refuses an inert gate change). An amended event without a
// recorded header contributes nothing.
func amendedAfterDispatch(events []Event, task, attempt string) []BriefHeader {
	last := -1
	for i := range events {
		e := &events[i]
		if e.Task == task && e.Kind == "dispatched" && e.Attempt == attempt {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	var out []BriefHeader
	for _, e := range events[last+1:] {
		if e.Task == task && e.Kind == "amended" && e.Header != nil {
			out = append(out, *e.Header)
		}
	}
	return out
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
