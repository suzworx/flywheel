package flywheel

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestPiCommand(t *testing.T) {
	t.Parallel()
	a, err := AdapterFor("pi")
	if err != nil || a.Name() != "pi" {
		t.Fatalf("AdapterFor(pi) = %v, %v", a, err)
	}
	base := []string{"-p", "--mode", "json", "--no-extensions", "--model", "anthropic/claude-sonnet-5"}
	cases := []struct {
		name string
		r    RunRequest
		tail []string
	}{
		{"fresh", RunRequest{Model: "anthropic/claude-sonnet-5"}, []string{freshMessage}},
		{"variant", RunRequest{Model: "anthropic/claude-sonnet-5", Variant: "high"}, []string{"--thinking", "high", freshMessage}},
		{"resume", RunRequest{Model: "anthropic/claude-sonnet-5", Resume: true, Session: "s-1", PromptFile: "/tmp/delta.md"},
			[]string{"--session", "s-1", "@/tmp/delta.md", resumeMessage}},
		{"resume without session", RunRequest{Model: "anthropic/claude-sonnet-5", Resume: true}, []string{freshMessage}},
		{"prompt file", RunRequest{Model: "anthropic/claude-sonnet-5", PromptFile: `C:\w\brief.md`, Increment: 2},
			[]string{`@C:\w\brief.md`, freshMessage + " Do increment 2 only, then report and STOP."}},
	}
	for _, tc := range cases {
		bin, args := a.Command(tc.r)
		if want := append(append([]string{}, base...), tc.tail...); bin != "pi" || !reflect.DeepEqual(args, want) {
			t.Errorf("%s: Command = %s %q, want pi %q", tc.name, bin, args, want)
		}
		if msg := args[len(args)-1]; strings.ContainsAny(msg, "\r\n") {
			t.Errorf("%s: message %q has a newline", tc.name, msg)
		}
	}
}

func TestPiParse(t *testing.T) {
	t.Parallel()
	usage := `"usage":{"input":10,"output":5,"cacheRead":7,"cacheWrite":3,"totalTokens":25,"cost":{"input":0.1,"output":0.2,"cacheRead":0,"cacheWrite":0,"total":0.3}}`
	tok := &Tokens{Input: 10, Output: 5, CacheRead: 7, CacheWrite: 3}
	cases := []struct {
		name, line string
		ok         bool
		want       Observation
	}{
		{"session", `{"type":"session","version":3,"id":"u-1","timestamp":"t","cwd":"/w"}`, true, Observation{Kind: "start", Session: "u-1"}},
		{"read", `{"type":"tool_execution_start","toolCallId":"c","toolName":"read","args":{"path":"a.go"}}`, true, Observation{Kind: "tool", Tool: "read", Path: "a.go"}},
		{"bash", `{"type":"tool_execution_start","toolCallId":"c","toolName":"bash","args":{"command":"go vet"}}`, true, Observation{Kind: "tool", Tool: "bash", Command: "go vet"}},
		{"text", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"a"},{"type":"text","text":"b"}],` + usage + `,"stopReason":"stop"}}` + "\r", true,
			Observation{Kind: "text", Text: "a\nb", Tokens: tok, Cost: 0.3}},
		{"tool call only", `{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"c","name":"read","arguments":{}}],` + usage + `,"stopReason":"toolUse"}}`, false, Observation{}},
		{"user message", `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, false, Observation{}},
		{"error", `{"type":"message_end","message":{"role":"assistant","content":[],` + usage + `,"stopReason":"error","errorMessage":"boom"}}`, true,
			Observation{Kind: "error", Error: "boom", Tokens: tok, Cost: 0.3}},
		{"aborted", `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"aborted","errorMessage":"aborted"}}`, true, Observation{Kind: "error", Error: "aborted"}},
		{"rate limit", `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"429 rate limit reached, resets 10:20am"}}`, true,
			Observation{Kind: "step", Reason: "rate-limited", ResetText: "10:20am", Error: "429 rate limit reached, resets 10:20am"}},
		{"turn_end tool", `{"type":"turn_end","message":{"role":"assistant","content":[],` + usage + `,"stopReason":"toolUse"},"toolResults":[]}`, true,
			Observation{Kind: "step", Reason: "tool-calls", Tokens: tok, Cost: 0.3, EndsTurn: true}},
		{"turn_end stop", `{"type":"turn_end","message":{"role":"assistant","content":[],` + usage + `,"stopReason":"stop"},"toolResults":[]}`, true,
			Observation{Kind: "step", Reason: "stop", Tokens: tok, Cost: 0.3, EndsTurn: true}},
		{"turn_end length", `{"type":"turn_end","message":{"role":"assistant","content":[],"stopReason":"length"},"toolResults":[]}`, true, Observation{Kind: "step", Reason: "length", EndsTurn: true}},
		{"turn_end error", `{"type":"turn_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"boom"},"toolResults":[]}`, true,
			Observation{Kind: "step", Reason: "error", Error: "boom", EndsTurn: true}},
		{"message_update", `{"type":"message_update","assistantMessageEvent":{"type":"text_delta"},` + usage + `}`, false, Observation{}},
		{"agent_end", `{"type":"agent_end","messages":[],"willRetry":false}`, false, Observation{}},
		{"unknown", `{"type":"compaction_start"}`, false, Observation{}},
		{"malformed", `{"type":"session",`, false, Observation{}},
		{"session without id", `{"type":"session","version":3}`, false, Observation{Kind: "start"}},
	}
	for _, tc := range cases {
		got, ok := piAdapter{}.Parse([]byte(tc.line))
		if ok != tc.ok || (ok && !reflect.DeepEqual(got, tc.want)) {
			t.Errorf("%s: Parse = %+v %v, want %+v %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// piTotals sums a fixture's observations the way run.go does: tokens and cost
// from step observations, the last step reason, and an error observation
// making the run an error.
func piTotals(obs []Observation) (tok Tokens, cost float64, reason string, turns int) {
	errored := false
	for _, o := range obs {
		if o.EndsTurn {
			turns++
		}
		switch o.Kind {
		case "step":
			if o.Reason != "" {
				reason = o.Reason
			}
			if o.Tokens != nil {
				tok.Input, tok.Output = tok.Input+o.Tokens.Input, tok.Output+o.Tokens.Output
				tok.CacheRead, tok.CacheWrite = tok.CacheRead+o.Tokens.CacheRead, tok.CacheWrite+o.Tokens.CacheWrite
			}
			cost += o.Cost
		case "error":
			errored = true
		}
	}
	if errored {
		reason = "error"
	}
	return tok, cost, reason, turns
}

func TestPiFixture(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("pi")
	obs := parseAll(a, fixtureLines("pi-clean.jsonl", t))
	if len(obs) == 0 || obs[0].Kind != "start" || obs[0].Session != "7b1e4c2a-9f3d-4e8b-a6c1-2d5f8e0b3a91" {
		t.Fatalf("pi-clean.jsonl first observation = %+v, want start with the session id", obs)
	}
	var tools, texts []string
	for _, o := range obs {
		switch o.Kind {
		case "tool":
			tools = append(tools, o.Tool+" "+o.Path+o.Command)
		case "text":
			texts = append(texts, o.Text)
		}
	}
	if want := []string{"read brief.md", "write hello.txt", "bash go test ./..."}; !reflect.DeepEqual(tools, want) {
		t.Errorf("tools = %q, want %q", tools, want)
	}
	if len(texts) != 2 || !strings.HasPrefix(texts[0], "PLAN files-to-read:") {
		t.Errorf("texts = %q, want the PLAN first and the final report", texts)
	}
	tok, cost, reason, turns := piTotals(obs)
	if want := (Tokens{Input: 1265, Output: 165, CacheRead: 6600, CacheWrite: 900}); tok != want {
		t.Errorf("tokens = %+v, want %+v", tok, want)
	}
	if math.Abs(cost-0.011625) > 1e-9 || reason != "stop" || turns != 4 {
		t.Errorf("cost %v reason %q turns %d, want 0.011625 stop 4", cost, reason, turns)
	}

	obs = parseAll(a, fixtureLines("pi-error.jsonl", t))
	if len(obs) != 3 || obs[0].Session != "c0ffee00-1234-4abc-9def-0123456789ab" {
		t.Fatalf("pi-error.jsonl = %+v, want start, error, step", obs)
	}
	if obs[1].Kind != "error" || !strings.Contains(obs[1].Error, "model not supported") {
		t.Errorf("obs[1] = %+v, want an error naming the provider message", obs[1])
	}
	if _, _, reason, turns := piTotals(obs); reason != "error" || turns != 1 {
		t.Errorf("reason %q turns %d, want error 1", reason, turns)
	}
}
