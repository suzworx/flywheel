package flywheel

import (
	"bytes"
	"encoding/json"
)

// toolProgress reads a claude stream tool_progress line, the heartbeat the
// stream writes every 30s while one tool call is open (issue #917): the tool
// name and the call's elapsed seconds. ok is false on any other line or on
// bad JSON. A heartbeat keeps the run file growing, so the run-file stall
// watchdog never sees a gap; this is what bounds the open call instead.
func toolProgress(line []byte) (tool string, elapsed int, ok bool) {
	if !bytes.Contains(line, []byte(`"tool_progress"`)) {
		return "", 0, false
	}
	var v struct {
		Type    string  `json:"type"`
		Tool    string  `json:"tool_name"`
		Elapsed float64 `json:"elapsed_time_seconds"`
	}
	if err := json.Unmarshal(line, &v); err != nil || v.Type != "tool_progress" {
		return "", 0, false
	}
	return v.Tool, int(v.Elapsed), true
}

// toolStallState refines a live run state with the run's open tool call
// (issue #917): open is the latest tool_progress elapsed seconds, 0 once a
// later line arrived; timeout is the default worker's tool_stall_timeout in
// seconds. It sits in the age-based stalled/long-step slot: an open call at
// the timeout is stalled, at half of it long-step, and every state classifyRun
// ranks above that slot (done, provider-error, capped, no-writes, silent) or
// already at stalled is kept.
func toolStallState(state string, open, timeout int) string {
	if timeout <= 0 || open <= 0 {
		return state
	}
	switch state {
	case "running", "exploring", "long-step":
		if open >= timeout {
			return "stalled"
		}
		if open >= timeout/2 && state != "long-step" {
			return "long-step"
		}
	}
	return state
}
