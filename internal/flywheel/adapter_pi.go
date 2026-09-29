package flywheel

import (
	"bytes"
	"encoding/json"
	"strings"
)

// piAdapter runs the pi coding agent (https://pi.dev) via `pi -p --mode json`
// (issue #275): its stdout is a JSONL event stream.
type piAdapter struct{}

func (a piAdapter) Name() string {
	return "pi"
}

// Command builds the dispatch arguments for `pi -p --mode json`. The brief is
// attached as @<file>, never inlined, and the message is one line: on Windows
// pi is an npm .cmd shim and cmd.exe cuts an argument at its first newline
// (the codex adapter's trap). --no-extensions keeps runs reproducible; a
// resume with a session adds --session.
func (a piAdapter) Command(r RunRequest) (string, []string) {
	msg := freshPrompt(r)
	resuming := r.Resume && r.Session != ""
	if resuming {
		msg = resumeMessage
	}
	msg = strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)), " ")
	args := []string{"-p", "--mode", "json", "--no-extensions", "--model", r.Model}
	if r.Variant != "" {
		args = append(args, "--thinking", r.Variant)
	}
	if resuming {
		args = append(args, "--session", r.Session)
	}
	if r.PromptFile != "" {
		args = append(args, "@"+r.PromptFile)
	}
	return "pi", append(args, msg)
}

// piMessage is the part of an assistant message flywheel reads.
type piMessage struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *struct {
		Input      int `json:"input"`
		Output     int `json:"output"`
		CacheRead  int `json:"cacheRead"`
		CacheWrite int `json:"cacheWrite"`
		Cost       struct {
			Total float64 `json:"total"`
		} `json:"cost"`
	} `json:"usage"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
}

// piReason maps an assistant stopReason to a step Reason: stop is the clean
// "stop", toolUse opencode's "tool-calls", length "length", error and aborted
// "error" — or "rate-limited" with its reset clause when errorMessage names a
// rate, usage or session limit (claudeRateLimit). Anything else passes through.
func piReason(m piMessage) (reason, reset string) {
	switch m.StopReason {
	case "toolUse":
		return "tool-calls", ""
	case "error", "aborted":
		if r, ok := claudeRateLimit(0, m.ErrorMessage); ok {
			return "rate-limited", r
		}
		return "error", ""
	}
	return m.StopReason, ""
}

// Parse decodes one line of pi's JSONL stream. session becomes "start";
// tool_execution_start "tool"; an assistant message_end "text" (its text
// blocks joined, with its usage), or "error" when it failed (a rate limit is
// a "rate-limited" step instead, so run.go can wait for the reset); turn_end
// a "step" that ends the turn and carries the call's tokens, cost and reason,
// so totals count each model call once. Anything else returns false.
func (a piAdapter) Parse(line []byte) (Observation, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimRight(line, "\r"), &m); err != nil {
		return Observation{}, false
	}
	switch rawString(m, "type") {
	case "session":
		s, ok := rawStringOK(m, "id")
		return Observation{Kind: "start", Session: s}, ok
	case "tool_execution_start":
		var args map[string]json.RawMessage
		_ = json.Unmarshal(m["args"], &args)
		return Observation{Kind: "tool", Tool: rawString(m, "toolName"), Path: rawString(args, "path"), Command: rawString(args, "command")}, true
	case "message_end", "turn_end":
	default:
		return Observation{}, false
	}
	var msg piMessage
	if err := json.Unmarshal(m["message"], &msg); err != nil || msg.Role != "assistant" {
		return Observation{}, false
	}
	var obs Observation
	if msg.Usage != nil {
		u := msg.Usage
		obs.Tokens = &Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
		obs.Cost = u.Cost.Total
	}
	reason, reset := piReason(msg)
	if rawString(m, "type") == "turn_end" {
		obs.Kind, obs.Reason, obs.ResetText, obs.EndsTurn, obs.Error = "step", reason, reset, true, msg.ErrorMessage
		return obs, true
	}
	switch reason {
	case "rate-limited":
		// Not counted here: the turn_end step carries this call's usage.
		return Observation{Kind: "step", Reason: reason, ResetText: reset, Error: msg.ErrorMessage}, true
	case "error":
		obs.Kind, obs.Error = "error", msg.ErrorMessage
		return obs, true
	}
	var texts []string
	for _, b := range msg.Content {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	if len(texts) == 0 {
		return Observation{}, false
	}
	obs.Kind, obs.Text = "text", strings.Join(texts, "\n")
	return obs, true
}
