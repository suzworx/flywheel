package flywheel

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// ContextPack is the factory's state sized for a joining agent (issue #58).
type ContextPack struct {
	Goals     []GoalView     `json:"goals"`
	InFlight  []HandoffTask  `json:"in_flight"`
	Blocked   []string       `json:"blocked"`
	Ready     []string       `json:"ready"`
	Unjudged  []GateBlocker  `json:"unjudged"`
	Learnings []LearningView `json:"learnings"`
	Model     string         `json:"model"`
	Role      string         `json:"role,omitempty"`
	Unaudited []string       `json:"unaudited"`
}

// BuildContext builds a ContextPack from events, config, and maxLearnings.
// Goals keeps only active goals. InFlight, Blocked, Ready come from DeriveHandoff.
// Unjudged lists Gate blockers (uninspected and untriaged).
// Learnings contains the last maxLearnings undismissed learnings in log order.
// Model is the config's default worker model.
// Every slice is non-nil (empty slices print as [] in JSON).
func BuildContext(events []Event, cfg Config, maxLearnings int) ContextPack {
	goals := Goals(events)
	var activeGoals []GoalView
	for _, g := range goals {
		if g.Status == "active" {
			activeGoals = append(activeGoals, g)
		}
	}
	if activeGoals == nil {
		activeGoals = []GoalView{}
	}

	handoff := DeriveHandoff(events)
	if handoff.InFlight == nil {
		handoff.InFlight = []HandoffTask{}
	}
	if handoff.Blocked == nil {
		handoff.Blocked = []string{}
	}
	if handoff.Ready == nil {
		handoff.Ready = []string{}
	}

	gateResult := Gate(events)
	blockers := gateResult.Blockers
	if blockers == nil {
		blockers = []GateBlocker{}
	}

	allLearnings := Learnings(events)
	var recentLearnings []LearningView
	for _, l := range allLearnings {
		if !l.Dismissed {
			recentLearnings = append(recentLearnings, l)
		}
	}
	if len(recentLearnings) > maxLearnings {
		recentLearnings = recentLearnings[len(recentLearnings)-maxLearnings:]
	}
	if recentLearnings == nil {
		recentLearnings = []LearningView{}
	}

	return ContextPack{
		Goals:     activeGoals,
		InFlight:  handoff.InFlight,
		Blocked:   handoff.Blocked,
		Ready:     handoff.Ready,
		Unjudged:  blockers,
		Learnings: recentLearnings,
		Model:     cfg.DefaultWorker().Model,
		Unaudited: []string{},
	}
}

// ContextRoles lists the valid roles for role-filtered context packs.
var ContextRoles = []string{"lead", "planner", "foreman", "inspector", "steward", "auditor"}

// BuildRoleContext builds a ContextPack filtered for a specific role.
// role "" or "lead" returns the full context (with Role set if non-empty and Unaudited).
// unknown role returns an error.
// Other roles get the full context with sections they don't own cleared.
func BuildRoleContext(events []Event, cfg Config, maxLearnings int, role string) (ContextPack, error) {
	base := BuildContext(events, cfg, maxLearnings)

	// Compute unaudited tasks: landed tasks with no audited event
	state := Derive(events)
	var unaudited []string
	for _, ts := range state.Tasks {
		if ts.Status == "landed" {
			hasAudited := false
			for _, e := range events {
				if e.Task == ts.ID && e.Kind == "audited" {
					hasAudited = true
					break
				}
			}
			if !hasAudited {
				unaudited = append(unaudited, ts.ID)
			}
		}
	}
	slices.Sort(unaudited)
	if unaudited == nil {
		unaudited = []string{} // JSON prints [], never null
	}
	base.Unaudited = unaudited

	if role == "" || role == "lead" {
		base.Role = role
		return base, nil
	}

	// Validate role
	if !slices.Contains(ContextRoles, role) {
		return ContextPack{}, fmt.Errorf("unknown role %q: one of %s", role, strings.Join(ContextRoles, ", "))
	}

	// Clear sections based on role
	switch role {
	case "planner":
		base.InFlight = []HandoffTask{}
		base.Unjudged = []GateBlocker{}
		base.Learnings = []LearningView{}
	case "foreman":
		base.Goals = []GoalView{}
		base.Blocked = []string{}
		base.Unjudged = []GateBlocker{}
		base.Learnings = []LearningView{}
	case "inspector":
		base.Goals = []GoalView{}
		base.InFlight = []HandoffTask{}
		base.Blocked = []string{}
		base.Ready = []string{}
		base.Learnings = []LearningView{}
		// Filter Unjudged to only "uninspected"
		var filtered []GateBlocker
		for _, ub := range base.Unjudged {
			if ub.Kind == "uninspected" {
				filtered = append(filtered, ub)
			}
		}
		base.Unjudged = filtered
		if base.Unjudged == nil {
			base.Unjudged = []GateBlocker{}
		}
	case "steward":
		base.Goals = []GoalView{}
		base.InFlight = []HandoffTask{}
		base.Blocked = []string{}
		base.Ready = []string{}
		// Filter Unjudged to only "untriaged"
		var filtered []GateBlocker
		for _, ub := range base.Unjudged {
			if ub.Kind == "untriaged" {
				filtered = append(filtered, ub)
			}
		}
		base.Unjudged = filtered
		if base.Unjudged == nil {
			base.Unjudged = []GateBlocker{}
		}
	case "auditor":
		base.Goals = []GoalView{}
		base.InFlight = []HandoffTask{}
		base.Blocked = []string{}
		base.Ready = []string{}
		base.Unjudged = []GateBlocker{}
		base.Learnings = []LearningView{}
	}

	if role != "auditor" {
		base.Unaudited = []string{} // only the lead and the auditor own audits
	}
	base.Role = role
	return base, nil
}

// RenderContext renders a ContextPack as Markdown to w.
func RenderContext(w io.Writer, p ContextPack) error {
	var sb strings.Builder

	// Title
	if p.Role != "" {
		sb.WriteString(fmt.Sprintf("# flywheel context — %s\n\n", p.Role))
	} else {
		sb.WriteString("# flywheel context\n\n")
	}

	// Render only sections the role owns
	shouldShow := func(section string) bool {
		if p.Role == "" || p.Role == "lead" {
			return true
		}
		switch section {
		case "goals":
			return p.Role == "planner"
		case "inflight":
			return p.Role == "foreman"
		case "blocked":
			return p.Role == "planner"
		case "ready":
			return p.Role == "planner" || p.Role == "foreman"
		case "unjudged":
			return p.Role == "inspector" || p.Role == "steward"
		case "learnings":
			return p.Role == "steward"
		case "unaudited":
			return p.Role == "lead" || p.Role == "auditor"
		}
		return false
	}

	// Goals
	if shouldShow("goals") {
		sb.WriteString("## Goals\n")
		if len(p.Goals) == 0 {
			sb.WriteString("- none\n")
		} else {
			for _, g := range p.Goals {
				sb.WriteString(fmt.Sprintf("- %s %s — %d/%d accepted\n", g.ID, g.Title, g.Accepted, g.Total))
			}
		}
		sb.WriteString("\n")
	}

	// In flight
	if shouldShow("inflight") {
		sb.WriteString("## In flight\n")
		if len(p.InFlight) == 0 {
			sb.WriteString("- none\n")
		} else {
			for _, t := range p.InFlight {
				line := fmt.Sprintf("- %s %s", t.ID, t.Status)
				if t.Model != "" {
					line += fmt.Sprintf(" %s", t.Model)
				}
				if t.Session != "" {
					line += fmt.Sprintf(" (session %s)", t.Session)
				}
				sb.WriteString(line + "\n")
			}
		}
		sb.WriteString("\n")
	}

	// Blocked
	if shouldShow("blocked") {
		sb.WriteString("## Blocked\n")
		if len(p.Blocked) == 0 {
			sb.WriteString("- none\n")
		} else {
			for _, id := range p.Blocked {
				sb.WriteString(fmt.Sprintf("- %s\n", id))
			}
		}
		sb.WriteString("\n")
	}

	// Ready
	if shouldShow("ready") {
		sb.WriteString("## Ready\n")
		if len(p.Ready) == 0 {
			sb.WriteString("- none\n")
		} else {
			for _, id := range p.Ready {
				sb.WriteString(fmt.Sprintf("- %s\n", id))
			}
		}
		sb.WriteString("\n")
	}

	// Needs a verdict or triage
	if shouldShow("unjudged") {
		sb.WriteString("## Needs a verdict or triage\n")
		if len(p.Unjudged) == 0 {
			sb.WriteString("- none\n")
		} else {
			for _, ub := range p.Unjudged {
				sb.WriteString(fmt.Sprintf("- %s %s: %s\n", ub.Task, ub.Kind, ub.Detail))
			}
		}
		sb.WriteString("\n")
	}

	// Recent learnings
	if shouldShow("learnings") {
		sb.WriteString("## Recent learnings\n")
		if len(p.Learnings) == 0 {
			sb.WriteString("- none\n")
		} else {
			for _, l := range p.Learnings {
				sb.WriteString(fmt.Sprintf("- %s %s %s (%s)\n", l.ID, l.Severity, l.Title, l.Task))
			}
		}
		sb.WriteString("\n")
	}

	// Landed, never audited
	if shouldShow("unaudited") {
		sb.WriteString("## Landed, never audited\n")
		if len(p.Unaudited) == 0 {
			sb.WriteString("- none\n")
		} else {
			for _, id := range p.Unaudited {
				sb.WriteString(fmt.Sprintf("- %s\n", id))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString(fmt.Sprintf("Worker model: %s\n", p.Model))

	_, err := io.WriteString(w, sb.String())
	if err != nil {
		return fmt.Errorf("write context: %w", err)
	}
	return nil
}
