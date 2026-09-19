package flywheel

import (
	"fmt"
	"io"
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
	}
}

// RenderContext renders a ContextPack as Markdown to w.
func RenderContext(w io.Writer, p ContextPack) error {
	var sb strings.Builder
	sb.WriteString("# flywheel context\n\n")

	// Goals
	sb.WriteString("## Goals\n")
	if len(p.Goals) == 0 {
		sb.WriteString("- none\n")
	} else {
		for _, g := range p.Goals {
			sb.WriteString(fmt.Sprintf("- %s %s — %d/%d accepted\n", g.ID, g.Title, g.Accepted, g.Total))
		}
	}
	sb.WriteString("\n")

	// In flight
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

	// Blocked
	sb.WriteString("## Blocked\n")
	if len(p.Blocked) == 0 {
		sb.WriteString("- none\n")
	} else {
		for _, id := range p.Blocked {
			sb.WriteString(fmt.Sprintf("- %s\n", id))
		}
	}
	sb.WriteString("\n")

	// Ready
	sb.WriteString("## Ready\n")
	if len(p.Ready) == 0 {
		sb.WriteString("- none\n")
	} else {
		for _, id := range p.Ready {
			sb.WriteString(fmt.Sprintf("- %s\n", id))
		}
	}
	sb.WriteString("\n")

	// Needs a verdict or triage
	sb.WriteString("## Needs a verdict or triage\n")
	if len(p.Unjudged) == 0 {
		sb.WriteString("- none\n")
	} else {
		for _, ub := range p.Unjudged {
			sb.WriteString(fmt.Sprintf("- %s %s: %s\n", ub.Task, ub.Kind, ub.Detail))
		}
	}
	sb.WriteString("\n")

	// Recent learnings
	sb.WriteString("## Recent learnings\n")
	if len(p.Learnings) == 0 {
		sb.WriteString("- none\n")
	} else {
		for _, l := range p.Learnings {
			sb.WriteString(fmt.Sprintf("- %s %s %s (%s)\n", l.ID, l.Severity, l.Title, l.Task))
		}
	}
	sb.WriteString("\n")

	sb.WriteString(fmt.Sprintf("Worker model: %s\n", p.Model))

	_, err := io.WriteString(w, sb.String())
	if err != nil {
		return fmt.Errorf("write context: %w", err)
	}
	return nil
}
