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

// roleSections is the single authority on which pack sections each role owns
// (issue #58): BuildRoleContext empties the others and RenderContext prints
// only these, so JSON and Markdown can never disagree. The lead (or no role)
// owns every section.
var roleSections = map[string][]string{
	"planner":   {"goals", "blocked", "ready"},
	"foreman":   {"inflight", "ready"},
	"inspector": {"unjudged"},
	"steward":   {"unjudged", "learnings"},
	"auditor":   {"unaudited"},
}

// ValidRole reports whether role is one of ContextRoles.
func ValidRole(role string) bool { return slices.Contains(ContextRoles, role) }

// RoleOwns reports whether role owns a pack section ("goals", "inflight",
// "blocked", "ready", "unjudged", "learnings", "unaudited").
func RoleOwns(role, section string) bool {
	if role == "" || role == "lead" {
		return true
	}
	return slices.Contains(roleSections[role], section)
}

// BuildRoleContext builds a ContextPack filtered for a role: the full pack for
// "" or "lead", and for any other role only the sections RoleOwns grants it,
// the rest empty (never nil). The inspector's needs-a-verdict list keeps only
// uninspected units and the steward's only untriaged signals. Unaudited lists
// the landed tasks with no audited event. An unknown role is an error.
func BuildRoleContext(events []Event, cfg Config, maxLearnings int, role string) (ContextPack, error) {
	if role != "" && !ValidRole(role) {
		return ContextPack{}, fmt.Errorf("unknown role %q: one of %s", role, strings.Join(ContextRoles, ", "))
	}
	p := BuildContext(events, cfg, maxLearnings)
	p.Role = role

	audited := map[string]bool{}
	for _, e := range events {
		if e.Kind == "audited" {
			audited[e.Task] = true
		}
	}
	p.Unaudited = []string{}
	for _, ts := range Derive(events).Tasks {
		if ts.Status == "landed" && !audited[ts.ID] {
			p.Unaudited = append(p.Unaudited, ts.ID)
		}
	}
	slices.Sort(p.Unaudited)

	keep := map[string]string{"inspector": "uninspected", "steward": "untriaged"}
	if kind, ok := keep[role]; ok {
		filtered := []GateBlocker{}
		for _, b := range p.Unjudged {
			if b.Kind == kind {
				filtered = append(filtered, b)
			}
		}
		p.Unjudged = filtered
	}

	if !RoleOwns(role, "goals") {
		p.Goals = []GoalView{}
	}
	if !RoleOwns(role, "inflight") {
		p.InFlight = []HandoffTask{}
	}
	if !RoleOwns(role, "blocked") {
		p.Blocked = []string{}
	}
	if !RoleOwns(role, "ready") {
		p.Ready = []string{}
	}
	if !RoleOwns(role, "unjudged") {
		p.Unjudged = []GateBlocker{}
	}
	if !RoleOwns(role, "learnings") {
		p.Learnings = []LearningView{}
	}
	if !RoleOwns(role, "unaudited") {
		p.Unaudited = []string{}
	}
	return p, nil
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
	shouldShow := func(section string) bool { return RoleOwns(p.Role, section) }

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
