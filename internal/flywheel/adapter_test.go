package flywheel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureLines returns the newline-delimited lines of a testdata fixture.
// Tests run with the package directory as the working directory.
func fixtureLines(name string, t *testing.T) []string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// fixturePath returns the absolute path of a testdata fixture.
func fixturePath(name string, t *testing.T) string {
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("resolve fixture %s: %v", name, err)
	}
	return p
}

// parseAll decodes every line of a fixture, skipping unknown types.
func parseAll(adap Adapter, lines []string) []Observation {
	var out []Observation
	for _, ln := range lines {
		if obs, ok := adap.Parse([]byte(ln)); ok {
			out = append(out, obs)
		}
	}
	return out
}

func TestAdapterFor(t *testing.T) {
	a, err := AdapterFor("opencode")
	if err != nil || a.Name() != "opencode" {
		t.Errorf("AdapterFor(opencode) = %v, %v", a, err)
	}
	a, err = AdapterFor("sim")
	if err != nil || a.Name() != "sim" {
		t.Errorf("AdapterFor(sim) = %v, %v", a, err)
	}
	if _, err := AdapterFor("bogus"); err == nil {
		t.Error("AdapterFor(bogus) = nil error, want an error")
	}
}

func TestOpenCodeCommandFresh(t *testing.T) {
	a, _ := AdapterFor("opencode")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1",
		PromptFile: briefPath,
	})
	if bin != "opencode" {
		t.Errorf("bin = %q, want opencode", bin)
	}
	want := []string{"run", "--pure", "-m", "m1", "--auto", "--format", "json", "--title", "T1-r1", freshMessage, "--file", briefPath}
	if len(args) != len(want) {
		t.Errorf("args = %v, want %v", args, want)
	} else {
		for i := 0; i < len(want); i++ {
			if args[i] != want[i] {
				t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
			}
		}
	}
}

func TestOpenCodeCommandResume(t *testing.T) {
	a, _ := AdapterFor("opencode")
	deltaPath := filepath.Join(t.TempDir(), "delta.txt")
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "c1", Title: "T1-c1", Model: "m1", Variant: "v2",
		Session: "ses_emitted_9", PromptFile: deltaPath, Resume: true,
	})
	if bin != "opencode" {
		t.Errorf("bin = %q, want opencode", bin)
	}
	want := []string{"run", "--pure", "-m", "m1", "--variant", "v2", "--auto", "--format", "json", "--title", "T1-c1", "--session", "ses_emitted_9", resumeMessage, "--file", deltaPath}
	if len(args) != len(want) {
		t.Errorf("args = %v, want %v", args, want)
	} else {
		for i := 0; i < len(want); i++ {
			if args[i] != want[i] {
				t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
			}
		}
	}
}

func TestOpenCodeCommandNeverPassesBriefText(t *testing.T) {
	a, _ := AdapterFor("opencode")
	brief := "owns: hello.txt (new)\n& echo pwned | more\n"
	dir := t.TempDir()
	cases := []struct {
		name string
		req  RunRequest
	}{
		{"fresh", RunRequest{Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1", PromptFile: filepath.Join(dir, "brief.txt")}},
		{"resume", RunRequest{Task: "T1", Attempt: "c1", Title: "T1-c1", Model: "m1", Session: "s1", PromptFile: filepath.Join(dir, "delta.txt"), Resume: true}},
	}
	for _, tc := range cases {
		_, args := a.Command(tc.req)
		for i, arg := range args {
			if strings.Contains(arg, "\n") || strings.Contains(arg, "&") || strings.Contains(arg, "|") {
				t.Errorf("%s: argument %d contains a newline or a shell metacharacter: %q", tc.name, i, arg)
			}
			if strings.Contains(arg, brief) {
				t.Errorf("%s: argument %d contains the brief text", tc.name, i)
			}
		}
		fileIdx := -1
		for i, arg := range args {
			if arg == "--file" {
				fileIdx = i
			}
		}
		if fileIdx < 1 || (args[fileIdx-1] != freshMessage && args[fileIdx-1] != resumeMessage) {
			t.Errorf("%s: --file not immediately preceded by the message constant: args = %v", tc.name, args)
		}
		if fileIdx != len(args)-2 {
			t.Errorf("%s: --file is not the final pair: args = %v", tc.name, args)
		}
		if filepath.IsAbs(args[fileIdx+1]) == false {
			t.Errorf("%s: --file path %q is not absolute", tc.name, args[fileIdx+1])
		}
	}
}

func TestOpenCodeParseCleanFixture(t *testing.T) {
	a, _ := AdapterFor("opencode")
	obs := parseAll(a, fixtureLines("clean.jsonl", t))
	if len(obs) != 6 {
		t.Fatalf("clean.jsonl parsed to %d observations, want 6", len(obs))
	}
	if obs[0].Kind != "start" || obs[0].Session != "ses_test_clean_001" {
		t.Errorf("obs[0] = %v, want start with the session", obs[0])
	}
	if obs[1].Kind != "text" || !strings.HasPrefix(obs[1].Text, "PLAN ") {
		t.Errorf("obs[1] = %v, want a PLAN text", obs[1])
	}
	if obs[2].Kind != "tool" || obs[2].Tool != "read" || obs[2].Path != "cmd/flywheel/main.go" {
		t.Errorf("obs[2] = %v, want tool read with the file path", obs[2])
	}
	if obs[3].Kind != "step" || obs[3].Reason != "stop" {
		t.Errorf("obs[3] = %v, want step stop", obs[3])
	}
	if obs[3].Tokens == nil || obs[3].Tokens.Input != 120 || obs[3].Tokens.Output != 40 ||
		obs[3].Tokens.Reasoning != 10 || obs[3].Tokens.CacheRead != 800 || obs[3].Tokens.CacheWrite != 5 {
		t.Errorf("obs[3] tokens = %v, want the recorded step tokens", obs[3].Tokens)
	}
	if obs[3].Cost != 0.003 {
		t.Errorf("obs[3] cost = %v, want 0.003", obs[3].Cost)
	}
	if obs[4].Kind != "text" || !strings.Contains(obs[4].Text, "Implemented") {
		t.Errorf("obs[4] = %v, want the final report text", obs[4])
	}
	if obs[5].Kind != "step" || obs[5].Reason != "stop" {
		t.Errorf("obs[5] = %v, want the last step stop", obs[5])
	}
	for _, o := range obs {
		if o.Session != "ses_test_clean_001" {
			t.Errorf("observation session = %q, want ses_test_clean_001", o.Session)
		}
	}
}

func TestOpenCodeParseCappedFixture(t *testing.T) {
	a, _ := AdapterFor("opencode")
	obs := parseAll(a, fixtureLines("capped.jsonl", t))
	if len(obs) != 5 {
		t.Fatalf("capped.jsonl parsed to %d observations, want 5", len(obs))
	}
	if obs[len(obs)-1].Kind != "step" || obs[len(obs)-1].Reason != "length" {
		t.Errorf("last observation = %v, want step length", obs[len(obs)-1])
	}
	if obs[len(obs)-1].Tokens == nil || obs[len(obs)-1].Tokens.CacheRead != 0 {
		t.Errorf("last tokens = %v, want cache read 0", obs[len(obs)-1].Tokens)
	}
}

func TestOpenCodeParseProviderErrorFixture(t *testing.T) {
	a, _ := AdapterFor("opencode")
	obs := parseAll(a, fixtureLines("provider-error.jsonl", t))
	if len(obs) != 4 {
		t.Fatalf("provider-error.jsonl parsed to %d observations, want 4", len(obs))
	}
	if obs[2].Kind != "error" || !strings.Contains(obs[2].Error, "HTTP 402") {
		t.Errorf("obs[2] = %v, want the provider error message", obs[2])
	}
}

func TestOpenCodeParseUnknownTypesReturnFalse(t *testing.T) {
	a, _ := AdapterFor("opencode")
	if _, ok := a.Parse([]byte(`{"type":"bogus","sessionID":"s"}`)); ok {
		t.Error("Parse() accepted an unknown type")
	}
	if _, ok := a.Parse([]byte("not json")); ok {
		t.Error("Parse() accepted a non-JSON line")
	}
}

func TestOpenCodeParseStepWithoutTokens(t *testing.T) {
	a, _ := AdapterFor("opencode")
	line := []byte(`{"type":"step_finish","sessionID":"s","part":{"type":"step_finish","reason":"stop"}}`)
	obs, ok := a.Parse(line)
	if !ok {
		t.Fatal("Parse() rejected a step_finish without tokens")
	}
	if obs.Kind != "step" || obs.Reason != "stop" {
		t.Errorf("obs = %v, want step stop", obs)
	}
	if obs.Tokens != nil {
		t.Errorf("tokens = %v, want nil when absent", obs.Tokens)
	}
}

// TestOpenCodeParseToolPathFallsBackToPathField checks a grep or glob
// tool_use, which carries its target under part.state.input.path rather than
// filePath, still decodes into Observation.Path (issue #72).
func TestOpenCodeParseToolPathFallsBackToPathField(t *testing.T) {
	a, _ := AdapterFor("opencode")
	grepLine := []byte(`{"type":"tool_use","sessionID":"s","part":{"type":"tool_use","tool":"grep","state":{"input":{"path":"../outside/lib.go"}}}}`)
	obs, ok := a.Parse(grepLine)
	if !ok || obs.Kind != "tool" || obs.Tool != "grep" || obs.Path != "../outside/lib.go" {
		t.Errorf("grep tool_use = %v, %v, want tool grep with Path from state.input.path", obs, ok)
	}
	globLine := []byte(`{"type":"tool_use","sessionID":"s","part":{"type":"tool_use","tool":"glob","state":{"input":{"path":"/outside/pkg"}}}}`)
	obs, ok = a.Parse(globLine)
	if !ok || obs.Kind != "tool" || obs.Tool != "glob" || obs.Path != "/outside/pkg" {
		t.Errorf("glob tool_use = %v, %v, want tool glob with Path from state.input.path", obs, ok)
	}
	// filePath still wins when both are present.
	both := []byte(`{"type":"tool_use","sessionID":"s","part":{"type":"tool_use","tool":"read","state":{"input":{"filePath":"a.go","path":"b.go"}}}}`)
	obs, ok = a.Parse(both)
	if !ok || obs.Path != "a.go" {
		t.Errorf("tool_use with both fields = %v, %v, want Path a.go (filePath takes priority)", obs, ok)
	}
}

// TestClaudeParseRealFixture parses testdata/claude-real.jsonl, five REAL
// lines captured from `claude -p ... --output-format stream-json --verbose`
// on this machine. The captured session failed to authenticate and did no
// work, so even though the result line's stop_reason is stop_sequence and
// subtype is "success", its top-level is_error: true makes the step Reason
// "error", per claudeReason.
func TestClaudeParseRealFixture(t *testing.T) {
	a, _ := AdapterFor("claude")
	lines := fixtureLines("claude-real.jsonl", t)
	if len(lines) != 5 {
		t.Fatalf("claude-real.jsonl has %d lines, want 5", len(lines))
	}
	for i, ln := range lines[:2] {
		if _, ok := a.Parse([]byte(ln)); ok {
			t.Errorf("hook line %d parsed, want skipped (hook_started/hook_response)", i)
		}
	}
	obs, ok := a.Parse([]byte(lines[2]))
	if !ok || obs.Kind != "start" || obs.Session != "e28e5e42-67d3-4524-b574-ced45f82b831" {
		t.Errorf("init line = %v, %v, want start with the session", obs, ok)
	}
	obs, ok = a.Parse([]byte(lines[3]))
	if !ok || obs.Kind != "text" || obs.Text != "Failed to authenticate: OAuth session expired and could not be refreshed" {
		t.Errorf("assistant line = %v, %v, want the captured auth-error text", obs, ok)
	}
	obs, ok = a.Parse([]byte(lines[4]))
	if !ok || obs.Kind != "step" || obs.Reason != "error" || obs.Cost != 0 {
		t.Errorf("result line = %v, %v, want step error cost 0 (is_error:true overrides stop_reason:stop_sequence)", obs, ok)
	}
}

// TestClaudeParseToolUseFixture parses testdata/claude-tool.jsonl, a
// HAND-BUILT fixture (not captured bytes): an Edit tool_use (file_path), a
// Grep tool_use (path, no file_path), and a result with stop_reason
// max_tokens.
func TestClaudeParseToolUseFixture(t *testing.T) {
	a, _ := AdapterFor("claude")
	lines := fixtureLines("claude-tool.jsonl", t)
	if len(lines) != 3 {
		t.Fatalf("claude-tool.jsonl has %d lines, want 3", len(lines))
	}
	obs, ok := a.Parse([]byte(lines[0]))
	if !ok || obs.Kind != "tool" || obs.Tool != "edit" || obs.Path != "internal/flywheel/adapter.go" {
		t.Errorf("Edit tool_use = %v, %v, want tool edit with the file path", obs, ok)
	}
	obs, ok = a.Parse([]byte(lines[1]))
	if !ok || obs.Kind != "tool" || obs.Tool != "grep" || obs.Path != "internal/flywheel" {
		t.Errorf("Grep tool_use = %v, %v, want tool grep with the path", obs, ok)
	}
	obs, ok = a.Parse([]byte(lines[2]))
	if !ok || obs.Kind != "step" || obs.Reason != "length" {
		t.Errorf("max_tokens result = %v, %v, want step length", obs, ok)
	}
}

// TestClaudeParseLimitFixture parses testdata/claude-limit.jsonl, one REAL
// line captured from `claude -p ...` on this machine after it hit its
// session limit: stop_reason stop_sequence and subtype "success", but with
// a top-level is_error: true and a session-limit result message. is_error
// wins, so the step Reason is "error".
func TestClaudeParseLimitFixture(t *testing.T) {
	a, _ := AdapterFor("claude")
	lines := fixtureLines("claude-limit.jsonl", t)
	if len(lines) != 1 {
		t.Fatalf("claude-limit.jsonl has %d lines, want 1", len(lines))
	}
	obs, ok := a.Parse([]byte(lines[0]))
	if !ok || obs.Kind != "step" || obs.Reason != "error" || obs.Session != "340a5fef-5f5d-4fb6-a42d-0a7d55b65a38" {
		t.Errorf("limit result line = %v, %v, want step error with the session", obs, ok)
	}
}

// TestClaudeParseResultUsage parses a result line with a top-level usage
// field, checking that the result observation's step carries Tokens from that
// session total (issue #286).
func TestClaudeParseResultUsage(t *testing.T) {
	a, _ := AdapterFor("claude")
	line := []byte(`{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","total_cost_usd":1.12,"session_id":"s1","usage":{"input_tokens":673,"cache_creation_input_tokens":95706,"cache_read_input_tokens":7912520,"output_tokens":28634,"output_tokens_details":{"thinking_tokens":5343}}}`)
	obs, ok := a.Parse(line)
	if !ok {
		t.Fatalf("Parse() rejected a valid result line")
	}
	if obs.Kind != "step" {
		t.Errorf("Kind = %q, want step", obs.Kind)
	}
	if obs.Cost != 1.12 {
		t.Errorf("Cost = %v, want 1.12", obs.Cost)
	}
	if obs.Tokens == nil {
		t.Fatalf("Tokens = nil, want non-nil")
	}
	if obs.Tokens.Input != 673 {
		t.Errorf("Tokens.Input = %d, want 673", obs.Tokens.Input)
	}
	if obs.Tokens.Output != 23291 {
		t.Errorf("Tokens.Output = %d, want 23291 (output_tokens 28634 minus thinking_tokens 5343)", obs.Tokens.Output)
	}
	if obs.Tokens.CacheRead != 7912520 {
		t.Errorf("Tokens.CacheRead = %d, want 7912520", obs.Tokens.CacheRead)
	}
	if obs.Tokens.CacheWrite != 95706 {
		t.Errorf("Tokens.CacheWrite = %d, want 95706", obs.Tokens.CacheWrite)
	}
	if obs.Tokens.Reasoning != 5343 {
		t.Errorf("Tokens.Reasoning = %d, want 5343", obs.Tokens.Reasoning)
	}
	if !obs.Aggregate {
		t.Error("Aggregate = false, want true: the result line's usage is the session total, not one call's")
	}
}

// TestClaudeParseResultWithoutUsage parses a result line without a top-level
// usage field, checking that Tokens is nil (issue #286).
func TestClaudeParseResultWithoutUsage(t *testing.T) {
	a, _ := AdapterFor("claude")
	line := []byte(`{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","total_cost_usd":1.12,"session_id":"s1"}`)
	obs, ok := a.Parse(line)
	if !ok {
		t.Fatalf("Parse() rejected a valid result line")
	}
	if obs.Kind != "step" {
		t.Errorf("Kind = %q, want step", obs.Kind)
	}
	if obs.Cost != 1.12 {
		t.Errorf("Cost = %v, want 1.12", obs.Cost)
	}
	if obs.Tokens != nil {
		t.Errorf("Tokens = %v, want nil", obs.Tokens)
	}
}

// TestClaudeParseStopSequenceWithoutIsError checks that a result line
// carrying stop_reason stop_sequence with no top-level is_error still reads
// as a clean stop.
func TestClaudeParseStopSequenceWithoutIsError(t *testing.T) {
	a, _ := AdapterFor("claude")
	line := []byte(`{"type":"result","subtype":"success","stop_reason":"stop_sequence","session_id":"ses_x","total_cost_usd":0.01}`)
	obs, ok := a.Parse(line)
	if !ok || obs.Kind != "step" || obs.Reason != "stop" {
		t.Errorf("result line = %v, %v, want step stop", obs, ok)
	}
}

// TestClaudeTokensOutputExcludesThinking checks that Output never includes
// thinking_tokens, so Output and Reasoning are counted separately as they are
// for every adapter (issue #59).
func TestClaudeTokensOutputExcludesThinking(t *testing.T) {
	a, _ := AdapterFor("claude")
	cases := []struct {
		name          string
		line          string
		wantOutput    int
		wantReasoning int
	}{
		{
			name:          "thinking less than output",
			line:          `{"type":"assistant","session_id":"ses_x","message":{"content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"output_tokens":100,"output_tokens_details":{"thinking_tokens":30}}}}`,
			wantOutput:    70,
			wantReasoning: 30,
		},
		{
			name:          "thinking equals output",
			line:          `{"type":"assistant","session_id":"ses_x","message":{"content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"output_tokens":50,"output_tokens_details":{"thinking_tokens":50}}}}`,
			wantOutput:    0,
			wantReasoning: 50,
		},
		{
			name:          "thinking greater than output",
			line:          `{"type":"assistant","session_id":"ses_x","message":{"content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"output_tokens":40,"output_tokens_details":{"thinking_tokens":60}}}}`,
			wantOutput:    0,
			wantReasoning: 60,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs, ok := a.Parse([]byte(tc.line))
			if !ok {
				t.Fatalf("Parse() rejected the line")
			}
			if obs.Tokens == nil {
				t.Fatalf("Tokens = nil, want non-nil")
			}
			if obs.Tokens.Output != tc.wantOutput {
				t.Errorf("Output = %d, want %d", obs.Tokens.Output, tc.wantOutput)
			}
			if obs.Tokens.Reasoning != tc.wantReasoning {
				t.Errorf("Reasoning = %d, want %d", obs.Tokens.Reasoning, tc.wantReasoning)
			}
		})
	}
}

func TestClaudeCommandFresh(t *testing.T) {
	a, _ := AdapterFor("claude")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1",
		PromptFile: briefPath,
	})
	if bin != "claude" {
		t.Errorf("bin = %q, want claude", bin)
	}
	for i, arg := range args {
		if arg == "--resume" {
			t.Errorf("args[%d] = --resume, want no --resume on a fresh run: %v", i, args)
		}
	}
	if len(args) < 2 || !strings.HasPrefix(args[1], freshMessage) {
		t.Errorf("args[1] = %q, want it to lead with freshMessage", args[1])
	}
}

func TestClaudeCommandResume(t *testing.T) {
	a, _ := AdapterFor("claude")
	deltaPath := filepath.Join(t.TempDir(), "delta.txt")
	_, args := a.Command(RunRequest{
		Task: "T1", Attempt: "c1", Title: "T1-c1", Model: "m1",
		Session: "ses_emitted_9", PromptFile: deltaPath, Resume: true,
	})
	found := false
	for i, arg := range args {
		if arg == "--resume" {
			found = true
			if i+1 >= len(args) || args[i+1] != "ses_emitted_9" {
				t.Errorf("--resume not followed by the session: args = %v", args)
			}
		}
	}
	if !found {
		t.Errorf("args = %v, want --resume ses_emitted_9", args)
	}
	if len(args) < 2 || !strings.HasPrefix(args[1], resumeMessage) {
		t.Errorf("args[1] = %q, want it to lead with resumeMessage", args[1])
	}
}

func TestClaudeCommandResumeWithoutSession(t *testing.T) {
	a, _ := AdapterFor("claude")
	deltaPath := filepath.Join(t.TempDir(), "delta.txt")
	_, args := a.Command(RunRequest{
		Task: "T1", Attempt: "c1", Title: "T1-c1", Model: "m1",
		PromptFile: deltaPath, Resume: true,
	})
	for i, arg := range args {
		if arg == "--resume" {
			t.Errorf("args[%d] = --resume, want no --resume when Session is empty: %v", i, args)
		}
	}
	if len(args) < 2 || !strings.HasPrefix(args[1], freshMessage) {
		t.Errorf("args[1] = %q, want it to lead with freshMessage when Session is empty", args[1])
	}
}

// TestClaudeCommandAppendsRules checks a claude dispatch carries the worker
// rules, PLAN check-in included, as --append-system-prompt on fresh and
// resumed runs alike (issue #360).
func TestClaudeCommandAppendsRules(t *testing.T) {
	a, _ := AdapterFor("claude")
	promptPath := filepath.Join(t.TempDir(), "brief.txt")
	for _, r := range []RunRequest{
		{Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1", PromptFile: promptPath},
		{Task: "T1", Attempt: "c1", Title: "T1-c1", Model: "m1", Session: "ses_1", PromptFile: promptPath, Resume: true},
	} {
		_, args := a.Command(r)
		found := false
		for i, arg := range args {
			if arg == "--append-system-prompt" {
				found = true
				if i+1 >= len(args) || !strings.Contains(args[i+1], "PLAN files-to-read") {
					t.Errorf("resume=%v: --append-system-prompt not followed by the rules: %v", r.Resume, args)
				}
			}
		}
		if !found {
			t.Errorf("resume=%v: args = %v, want --append-system-prompt", r.Resume, args)
		}
	}
}

// TestClaudeAssistantTextWithTool checks an assistant message holding text
// then a tool_use keeps the text on the tool observation, where a model
// writes its plan before its first tool call (issue #360).
func TestClaudeAssistantTextWithTool(t *testing.T) {
	a, _ := AdapterFor("claude")
	plan := "PLAN files-to-read: a\nPLAN files-to-change: b\nPLAN order: c\nPLAN checks: d"
	planJSON, _ := json.Marshal(plan)
	line := `{"type":"assistant","session_id":"s","message":{"content":[{"type":"text","text":` + string(planJSON) + `},{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}`
	obs, ok := a.Parse([]byte(line))
	if !ok || obs.Kind != "tool" || obs.Text != plan {
		t.Errorf("Parse(text+tool_use) = %+v, %v, want Kind tool with Text %q", obs, ok, plan)
	}
	line = `{"type":"assistant","session_id":"s","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}`
	obs, ok = a.Parse([]byte(line))
	if !ok || obs.Kind != "tool" || obs.Text != "" {
		t.Errorf("Parse(tool_use only) = %+v, %v, want Kind tool with empty Text", obs, ok)
	}
}

// TestClaudeCommandSettingSources checks a claude dispatch, fresh or resumed,
// loads only the user's settings, so a checkout's project or local settings
// cannot widen where the worker may write (issue #359).
func TestClaudeCommandSettingSources(t *testing.T) {
	a, _ := AdapterFor("claude")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	for _, tc := range []struct {
		name string
		req  RunRequest
	}{
		{"fresh", RunRequest{Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1", PromptFile: briefPath}},
		{"resume", RunRequest{Task: "T1", Attempt: "c1", Title: "T1-c1", Model: "m1", PromptFile: briefPath, Session: "ses_1", Resume: true}},
	} {
		_, args := a.Command(tc.req)
		if got := flagValues(args, "--setting-sources"); len(got) == 0 || got[0] != "user" {
			t.Errorf("%s: --setting-sources = %v, want user; args = %v", tc.name, got, args)
		}
	}
}

func TestSimAdapter(t *testing.T) {
	a, _ := AdapterFor("sim")
	if a.Name() != "sim" {
		t.Errorf("sim Name() = %q", a.Name())
	}
	bin, args := a.Command(RunRequest{Model: "fixture.jsonl"})
	if bin != "" || len(args) != 0 {
		t.Errorf("sim Command() = %q, %v, want empty bin and no args", bin, args)
	}
	line := []byte(`{"type":"text","sessionID":"ses_x","part":{"type":"text","text":"PLAN hello"}}`)
	obs, ok := a.Parse(line)
	if !ok || obs.Kind != "text" || !strings.HasPrefix(obs.Text, "PLAN ") {
		t.Errorf("sim Parse() = %v, %v, want the opencode text observation", obs, ok)
	}
}

// TestOpenCodeParseEndsTurn checks EndsTurn is set only on step_finish: that
// is the line that completes a model turn in the opencode stream (issue #187).
func TestOpenCodeParseEndsTurn(t *testing.T) {
	a, _ := AdapterFor("opencode")
	cases := []struct {
		name     string
		line     string
		wantEnds bool
	}{
		{"step_start", `{"type":"step_start","sessionID":"s","part":{"type":"step_start"}}`, false},
		{"text", `{"type":"text","sessionID":"s","part":{"type":"text","text":"hello"}}`, false},
		{"tool_use", `{"type":"tool_use","sessionID":"s","part":{"type":"tool_use","tool":"read","state":{"input":{"filePath":"a.go"}}}}`, false},
		{"step_finish", `{"type":"step_finish","sessionID":"s","part":{"type":"step_finish","reason":"stop"}}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs, ok := a.Parse([]byte(tc.line))
			if !ok {
				t.Fatalf("Parse() rejected %s", tc.name)
			}
			if obs.EndsTurn != tc.wantEnds {
				t.Errorf("%s: EndsTurn = %v, want %v", tc.name, obs.EndsTurn, tc.wantEnds)
			}
		})
	}
}

// TestClaudeParseEndsTurn checks EndsTurn is set on every assistant line and
// on the terminal result line, and false on the system/init line (issue #187).
func TestClaudeParseEndsTurn(t *testing.T) {
	a, _ := AdapterFor("claude")
	cases := []struct {
		name     string
		line     string
		wantKind string
		wantEnds bool
	}{
		{
			name:     "system_init",
			line:     `{"type":"system","subtype":"init","session_id":"ses_x"}`,
			wantKind: "start",
			wantEnds: false,
		},
		{
			name:     "assistant_text",
			line:     `{"type":"assistant","session_id":"ses_x","message":{"content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
			wantKind: "text",
			wantEnds: true,
		},
		{
			name:     "assistant_tool",
			line:     `{"type":"assistant","session_id":"ses_x","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
			wantKind: "tool",
			wantEnds: true,
		},
		{
			name:     "result",
			line:     `{"type":"result","subtype":"success","stop_reason":"stop_sequence","session_id":"ses_x","total_cost_usd":0.01}`,
			wantKind: "step",
			wantEnds: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs, ok := a.Parse([]byte(tc.line))
			if !ok {
				t.Fatalf("Parse() rejected %s", tc.name)
			}
			if obs.Kind != tc.wantKind {
				t.Errorf("%s: Kind = %q, want %q", tc.name, obs.Kind, tc.wantKind)
			}
			if obs.EndsTurn != tc.wantEnds {
				t.Errorf("%s: EndsTurn = %v, want %v", tc.name, obs.EndsTurn, tc.wantEnds)
			}
		})
	}
}

// flagValues returns the values following flag in args, stopping at the next
// argument that begins with "--" (the next flag).
func flagValues(args []string, flag string) []string {
	for i := 0; i < len(args); i++ {
		if args[i] != flag {
			continue
		}
		var vals []string
		for j := i + 1; j < len(args) && !strings.HasPrefix(args[j], "--"); j++ {
			vals = append(vals, args[j])
		}
		return vals
	}
	return nil
}

// TestClaudeCommandToolPolicy checks a claude dispatch carries the worker's
// resolved tool policy: the default allowed list ("Bash") via --allowedTools
// and the git-write family via --disallowedTools; a worker with explicit
// lists produces exactly those instead; an empty list appends no flag
// (issue #192).
func TestClaudeCommandToolPolicy(t *testing.T) {
	a, _ := AdapterFor("claude")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	base := RunRequest{Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1", PromptFile: briefPath}

	_, args := a.Command(RunRequest{
		Task: base.Task, Attempt: base.Attempt, Title: base.Title, Model: base.Model, PromptFile: base.PromptFile,
		AllowedTools: (Worker{}).allowedTools(), DisallowedTools: (Worker{}).disallowedTools(),
	})
	if got := flagValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Bash"}) {
		t.Errorf("--allowedTools = %v, want the default [Bash]", got)
	}
	if got := flagValues(args, "--disallowedTools"); !reflect.DeepEqual(got, defaultDisallowedTools) {
		t.Errorf("--disallowedTools = %v, want the default git-write family %v", got, defaultDisallowedTools)
	}

	_, args = a.Command(RunRequest{
		Task: base.Task, Attempt: base.Attempt, Title: base.Title, Model: base.Model, PromptFile: base.PromptFile,
		AllowedTools:    []string{"Bash(go:*)", "Edit"},
		DisallowedTools: []string{"Bash(git commit:*)"},
	})
	if got := flagValues(args, "--allowedTools"); !reflect.DeepEqual(got, []string{"Bash(go:*)", "Edit"}) {
		t.Errorf("--allowedTools = %v, want the explicit list, not the default", got)
	}
	if got := flagValues(args, "--disallowedTools"); !reflect.DeepEqual(got, []string{"Bash(git commit:*)"}) {
		t.Errorf("--disallowedTools = %v, want the explicit list, not the default", got)
	}

	_, args = a.Command(base)
	if got := flagValues(args, "--allowedTools"); got != nil {
		t.Errorf("empty allowed list appended %v, want no --allowedTools", got)
	}
	if got := flagValues(args, "--disallowedTools"); got != nil {
		t.Errorf("empty disallowed list appended %v, want no --disallowedTools", got)
	}
}

// TestOpenCodeCommandUnchangedByToolPolicy checks the opencode adapter's argv
// ignores the tool lists entirely: the same RunRequest that gives a claude
// dispatch its --allowedTools/--disallowedTools leaves the opencode dispatch
// exactly as it was (issue #192).
func TestOpenCodeCommandUnchangedByToolPolicy(t *testing.T) {
	a, _ := AdapterFor("opencode")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1", PromptFile: briefPath,
		AllowedTools: (Worker{}).allowedTools(), DisallowedTools: (Worker{}).disallowedTools(),
	})
	if bin != "opencode" {
		t.Errorf("bin = %q, want opencode", bin)
	}
	want := []string{"run", "--pure", "-m", "m1", "--auto", "--format", "json", "--title", "T1-r1", freshMessage, "--file", briefPath}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("opencode args changed by the tool policy:\n got %v\nwant %v", args, want)
	}
}

// TestClaudeCommandIncrement checks that a dispatch with Increment > 0
// leads the prompt with freshMessage plus the increment instruction (issue #83).
func TestClaudeCommandIncrement(t *testing.T) {
	a, _ := AdapterFor("claude")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	if err := os.WriteFile(briefPath, []byte("do the thing"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "r1", Model: "claude-sonnet-5", PromptFile: briefPath, Increment: 2,
	})
	if bin != "claude" {
		t.Errorf("bin = %q, want claude", bin)
	}
	if len(args) < 2 {
		t.Fatalf("args too short: %v", args)
	}
	want := freshMessage + " Do increment 2 only, then report and STOP."
	if !strings.HasPrefix(args[1], want) {
		t.Errorf("args[1] = %q, want to start with %q", args[1], want)
	}
}

// TestOpencodeCommandIncrement checks that a dispatch with Increment > 0
// leads the prompt with freshMessage plus the increment instruction (issue #83).
func TestOpencodeCommandIncrement(t *testing.T) {
	a, _ := AdapterFor("opencode")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "m1", PromptFile: briefPath, Increment: 3,
	})
	if bin != "opencode" {
		t.Errorf("bin = %q, want opencode", bin)
	}
	want := freshMessage + " Do increment 3 only, then report and STOP."
	found := false
	for _, arg := range args {
		if arg == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("args = %v, want to contain %q", args, want)
	}
}

// TestFreshPromptNoIncrement checks that freshPrompt with no increment returns freshMessage.
func TestFreshPromptNoIncrement(t *testing.T) {
	result := freshPrompt(RunRequest{})
	if result != freshMessage {
		t.Errorf("freshPrompt(empty request) = %q, want %q", result, freshMessage)
	}
}
