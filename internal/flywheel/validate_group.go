package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// GroupMemberResult is one member's share of a group pass (issue #775): its
// attempt and its own gates' outcomes, in its brief's order.
type GroupMemberResult struct {
	Task    string
	Attempt string
	Gates   []GateOut
	Files   []FileShape
}

// GroupGaugeResult reports `flywheel validate --group`: the combined tree, one
// GateOut per distinct gate command in run order, the group's outside paths
// and conflict markers, and every member's readings.
type GroupGaugeResult struct {
	Group   string // GroupTask(group)
	Tree    string
	Commit  string
	Gates   []GateOut
	Outside []string
	Markers []string
	Members []GroupMemberResult
}

// OK reports whether every gate passed, nothing sits outside the group's owns
// and no changed file holds a conflict marker.
func (r GroupGaugeResult) OK() bool {
	for _, g := range r.Gates {
		if g.RC != 0 || g.HostBlocked {
			return false
		}
	}
	return len(r.Outside) == 0 && len(r.Markers) == 0
}

// ValidateGroup runs a group's gates once on one combined tree (o.Workdir) and
// records a reading per member (issue #775): each distinct gate command runs
// once, for the first member that declares it, and every other member that
// declares it gets a copy of that validated event under its own task, attempt
// and gate index. One owns_checked per member follows, all on the same tree, so
// each member's T3 check accepts them as its own. Two members owning one owns
// entry or one changed path refuse with rule group-owns before any gate runs.
func ValidateGroup(dir, group string, o ValidateOptions) (GroupGaugeResult, error) {
	if o.Dir == "" {
		o.Dir = dir
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	events, err := ReadEvents(o.Dir)
	if err != nil {
		return GroupGaugeResult{}, err
	}
	members, err := GroupMembers(events, group)
	if err != nil {
		return GroupGaugeResult{}, err
	}
	res := GroupGaugeResult{Group: GroupTask(group)}
	headers := make([]BriefHeader, len(members))
	var allOwns []string
	for i, m := range members {
		h, briefPaths, err := AttemptBrief(o.Dir, events, m)
		if err != nil {
			return GroupGaugeResult{}, err
		}
		if len(h.Gates) == 0 {
			return GroupGaugeResult{}, fmt.Errorf("brief %s declares no gate: lines; add a `gate:` line to the brief header", briefPaths[0])
		}
		attempt := "r1"
		for _, e := range events {
			if e.Task == m && e.Attempt != "" {
				attempt = e.Attempt
			}
		}
		headers[i] = h
		res.Members = append(res.Members, GroupMemberResult{Task: m, Attempt: attempt})
		allOwns = append(allOwns, h.Owns...)
		for j := 0; j < i; j++ {
			for _, a := range headers[j].Owns {
				for _, b := range h.Owns {
					if a == b {
						return GroupGaugeResult{}, groupOwnsRefusal(a, members[j], m)
					}
				}
			}
		}
	}
	wd := o.Workdir
	if wd == "" {
		wd = o.Dir
	}
	wd = absPath(wd)
	if res.Tree, err = treeHash(wd); err != nil {
		return GroupGaugeResult{}, err
	}
	res.Commit = headCommit(wd)
	// The changed paths come first: an overlap refuses before any gate runs.
	intRef := integrationRef(wd)
	seen := map[string]bool{}
	var changed []string
	for _, mr := range res.Members {
		paths, _, _, err := unitChangedPathsWalk(wd, dispatchBase(events, mr.Task, mr.Attempt), mr.Task, intRef)
		if err != nil {
			return GroupGaugeResult{}, err
		}
		for _, p := range paths {
			if !seen[p] {
				seen[p] = true
				changed = append(changed, p)
			}
		}
	}
	for _, p := range changed {
		owner := -1
		matched := false
		for i, h := range headers {
			if ownsContains(h.Owns, p) {
				if owner >= 0 {
					return GroupGaugeResult{}, groupOwnsRefusal(p, members[owner], members[i])
				}
				owner = i
			}
			matched = matched || ownsContains(h.Owns, p) || ownsContains(h.NeedsState, p)
		}
		if !matched && !isFlywheelOwnPath(p) {
			res.Outside = append(res.Outside, p)
		}
	}
	res.Markers = conflictMarkers(wd, changed)
	if err := groupGates(o.Dir, wd, events, headers, allOwns, &res); err != nil {
		return GroupGaugeResult{}, err
	}
	for i := range res.Members {
		mr := &res.Members[i]
		mr.Files = measureFiles(wd, headers[i].Owns, changed)
		if err := AppendEvent(o.Dir, Event{
			Task: mr.Task, Kind: "owns_checked", Attempt: mr.Attempt, Tree: res.Tree, Commit: res.Commit,
			Outside: res.Outside, Files: mr.Files, Markers: res.Markers, Persona: "supervisor",
			Workdir: workdirField(wd, o.Dir), Group: res.Group,
		}); err != nil {
			return GroupGaugeResult{}, err
		}
	}
	_, _ = WriteState(o.Dir)
	return res, nil
}

// groupGates walks the members in order, each member's gates in order, and
// runs each distinct gate command once on wd through measureGate under the
// host gate lock (gateTurn), recording its validated event for the first
// member that declares it; every later member declaring the same command gets
// a copy under its own task, attempt and gate index. Every event carries Group.
// owns is the union of the members' owns, so an inconclusive reading names
// only paths outside the whole group.
func groupGates(dir, wd string, events []Event, headers []BriefHeader, owns []string, res *GroupGaugeResult) error {
	wait, _ := Limits{}.QuietWaitDuration()
	if cfg, _, err := LoadConfig(dir); err == nil {
		if d, derr := cfg.Limits.QuietWaitDuration(); derr == nil && d > 0 {
			wait = d
		}
	}
	type reading struct {
		out GateOut
		ev  Event
	}
	ran := map[string]reading{}
	for i := range res.Members {
		mr := &res.Members[i]
		for j, gate := range headers[i].Gates {
			n := strconv.Itoa(j + 1)
			r, ok := ran[gate]
			if ok {
				r.ev.TS, r.ev.Prev, r.ev.Task, r.ev.Attempt, r.ev.Gate = "", "", mr.Task, mr.Attempt, n
				r.out.Gate = n
			} else {
				evDir := filepath.Join(dir, ".flywheel", "evidence", mr.Task, mr.Attempt)
				if err := os.MkdirAll(evDir, 0o755); err != nil {
					return fmt.Errorf("create %s: %w", evDir, err)
				}
				release, note, err := gateTurn(dir, mr.Task, n, wait, now, quietSleep)
				if err != nil {
					return err
				}
				r.out, r.ev, err = measureGate(dir, wd, mr.Task, mr.Attempt, res.Tree, res.Commit, UnitBase(events, mr.Task), owns, n, n, gate, false, nil, note, "")
				release()
				if err != nil {
					return err
				}
				ran[gate] = r
				res.Gates = append(res.Gates, r.out)
			}
			r.ev.Group = res.Group
			if err := AppendEvent(dir, r.ev); err != nil {
				return err
			}
			mr.Gates = append(mr.Gates, r.out)
		}
	}
	return nil
}

// groupOwnsRefusal is the group-owns refusal for path p owned by tasks a and b.
func groupOwnsRefusal(p, a, b string) *RuleRefusal {
	return &RuleRefusal{Rule: "group-owns", Fix: fmt.Sprintf("%s is owned by both %s and %s; a group's members must own disjoint paths", p, a, b)}
}
