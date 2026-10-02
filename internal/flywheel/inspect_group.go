package flywheel

import (
	"errors"
	"fmt"
)

// InspectGroup inspects every member of group (issue #775): a check pass runs
// every member through InspectTask without recording anything, and only when
// every member passes it is each member's inspection recorded, in member
// order, with the inspected event carrying GroupTask(group). The first
// refusal or error is returned wrapped with the member's task, so a
// *RuleRefusal stays reachable with errors.As. It returns the inspected tasks.
func InspectGroup(dir, group string, o InspectOptions) ([]string, error) {
	if o.Dir == "" {
		o.Dir = dir
	}
	if o.Dir == "" {
		o.Dir = "."
	}
	events, err := ReadEvents(o.Dir)
	if err != nil {
		return nil, err
	}
	members, err := GroupMembers(events, group)
	if err != nil {
		return nil, err
	}
	o.Group = group
	o.check = true
	for _, m := range members {
		if err := InspectTask(o.Dir, m, o); err != nil {
			return nil, fmt.Errorf("member %s: %w", m, err)
		}
	}
	o.check = false
	var done []string
	for _, m := range members {
		if err := InspectTask(o.Dir, m, o); err != nil {
			return done, fmt.Errorf("member %s: %w", m, err)
		}
		done = append(done, m)
	}
	return done, nil
}

// LandGroup lands every member of group on commit (issue #775): every member
// not already landed on commit must be passed (rule T5 otherwise, and nothing
// lands), then each lands through LandTaskWithException's path with its landed
// event carrying GroupTask(group). A member already landed on commit is
// skipped, so a rerun resumes. It returns the tasks it landed.
func LandGroup(dir, group, commit, note, allowUntriaged string) ([]string, error) {
	if dir == "" {
		dir = "."
	}
	events, err := ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	members, err := GroupMembers(events, group)
	if err != nil {
		return nil, err
	}
	status := map[string]string{}
	for _, t := range Derive(events).Tasks {
		status[t.ID] = t.Status
	}
	for _, m := range members {
		if landed, _, _ := landedCommit(events, m); landed != "" && landed == commit {
			continue
		}
		if status[m] != "passed" {
			return nil, &RuleRefusal{Rule: "T5", Fix: fmt.Sprintf("group %s member %s is not passed (status %q); land the group only after every member passes: flywheel inspect --group %s --verdict pass --session <session>", GroupID(group), m, status[m], group)}
		}
	}
	// --allow-untriaged covers the members with untriaged signals only: T9
	// refuses it on a member that has none.
	untriaged := map[string]bool{}
	for _, s := range UntriagedSignals(events) {
		untriaged[s.Task] = true
	}
	gt := GroupTask(group)
	var done []string
	for _, m := range members {
		allow := ""
		if untriaged[m] {
			allow = allowUntriaged
		}
		err := landTaskGroup(dir, m, commit, note, false, "", "", "", allow, nil, gt)
		if errors.Is(err, ErrAlreadyLanded) {
			continue
		}
		if err != nil {
			return done, fmt.Errorf("member %s: %w", m, err)
		}
		done = append(done, m)
	}
	return done, nil
}
