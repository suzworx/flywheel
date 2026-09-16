package flywheel

import (
	"os"
	"path/filepath"
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
// on this machine. The captured session failed to authenticate, so the
// assistant line's text is an API error message and the result line carries
// is_error: true; that is still real structure, asserted as captured, not as
// a successful run would read.
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
	if !ok || obs.Kind != "step" || obs.Reason != "stop" || obs.Cost != 0 {
		t.Errorf("result line = %v, %v, want step stop cost 0 (is_error:true does not override stop_reason:stop_sequence)", obs, ok)
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
