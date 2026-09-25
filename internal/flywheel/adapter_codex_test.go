package flywheel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexParseCleanFixture(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("codex")
	obs := parseAll(a, fixtureLines("codex-clean.jsonl", t))
	if len(obs) != 6 {
		t.Fatalf("codex-clean.jsonl parsed to %d observations, want 6", len(obs))
	}
	kinds := []string{}
	for _, o := range obs {
		kinds = append(kinds, o.Kind)
	}
	wantKinds := []string{"start", "text", "tool", "tool", "text", "step"}
	for i, want := range wantKinds {
		if i < len(kinds) && kinds[i] != want {
			t.Errorf("obs[%d].Kind = %q, want %q", i, kinds[i], want)
		}
	}
	if obs[0].Session != "0199a213-81c0-7800-8aa1-bbab2a035a53" {
		t.Errorf("obs[0].Session = %q, want 0199a213-81c0-7800-8aa1-bbab2a035a53", obs[0].Session)
	}
	if !strings.HasPrefix(obs[1].Text, "PLAN files-to-read:") {
		t.Errorf("obs[1].Text starts with %q, want PLAN files-to-read:", obs[1].Text[:20])
	}
	if obs[2].Tool != "bash" {
		t.Errorf("obs[2].Tool = %q, want bash", obs[2].Tool)
	}
	if obs[3].Tool != "edit" || obs[3].Path != "a.go" {
		t.Errorf("obs[3] = Tool %q Path %q, want tool edit a.go", obs[3].Tool, obs[3].Path)
	}
	endsTurnCount := 0
	for _, o := range obs {
		if o.EndsTurn {
			endsTurnCount++
		}
	}
	if endsTurnCount != 4 {
		t.Errorf("exactly %d observations have EndsTurn, want 4", endsTurnCount)
	}
	if obs[5].Reason != "stop" || !obs[5].Aggregate {
		t.Errorf("obs[5] = Reason %q Aggregate %v, want stop true", obs[5].Reason, obs[5].Aggregate)
	}
	if obs[5].Tokens == nil || obs[5].Tokens.Input != 315 || obs[5].Tokens.CacheRead != 24448 ||
		obs[5].Tokens.Output != 58 || obs[5].Tokens.Reasoning != 64 {
		if obs[5].Tokens == nil {
			t.Errorf("obs[5].Tokens = nil, want {Input: 315, CacheRead: 24448, Output: 58, Reasoning: 64}")
		} else {
			t.Errorf("obs[5].Tokens = {Input: %d, CacheRead: %d, Output: %d, Reasoning: %d}, want {315, 24448, 58, 64}",
				obs[5].Tokens.Input, obs[5].Tokens.CacheRead, obs[5].Tokens.Output, obs[5].Tokens.Reasoning)
		}
	}
}

func TestCodexParseErrorFixture(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("codex")
	obs := parseAll(a, fixtureLines("codex-error.jsonl", t))
	if len(obs) != 3 {
		t.Fatalf("codex-error.jsonl parsed to %d observations, want 3", len(obs))
	}
	if obs[0].Kind != "start" {
		t.Errorf("obs[0].Kind = %q, want start", obs[0].Kind)
	}
	if obs[1].Kind != "error" || !strings.Contains(obs[1].Error, "not supported") {
		t.Errorf("obs[1] = Kind %q Error %q, want error with 'not supported'", obs[1].Kind, obs[1].Error)
	}
	if obs[2].Kind != "step" || obs[2].Reason != "error" || !strings.Contains(obs[2].Error, "not supported") {
		t.Errorf("obs[2] = Kind %q Reason %q Error %q, want step error with 'not supported'", obs[2].Kind, obs[2].Reason, obs[2].Error)
	}
}

func TestCodexParseIgnoresNoise(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("codex")
	cases := []string{
		"not json",
		"{}",
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"i","type":"reasoning","text":"x"}}`,
	}
	for _, line := range cases {
		if _, ok := a.Parse([]byte(line)); ok {
			t.Errorf("Parse(%q) = ok, want false", line)
		}
	}
}

func TestCodexParseFileChangeAdd(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("codex")
	line := []byte(`{"type":"item.completed","item":{"id":"i","type":"file_change","changes":[{"path":"new.go","kind":"add"}]}}`)
	obs, ok := a.Parse(line)
	if !ok || obs.Kind != "tool" || obs.Tool != "write" {
		t.Errorf("Parse() = %v %v, want tool write ok=true", obs, ok)
	}
}

func TestCodexCommandFresh(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("codex")
	dir := t.TempDir()
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte("do the thing"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "gpt-5-codex", Variant: "high",
		PromptFile: briefPath,
	})
	if bin != "codex" {
		t.Errorf("bin = %q, want codex", bin)
	}
	if len(args) < 1 {
		t.Fatalf("args too short: %v", args)
	}
	if !strings.HasPrefix(args[0], "exec") {
		t.Errorf("args[0] = %q, want to start with exec", args[0])
	}
	wantPrefix := []string{"exec", "--json", "--model", "gpt-5-codex", "--sandbox", "workspace-write", "-c", "model_reasoning_effort=high"}
	for i, want := range wantPrefix {
		if i >= len(args) || args[i] != want {
			if i < len(args) {
				t.Errorf("args[%d] = %q, want %q", i, args[i], want)
			} else {
				t.Errorf("args too short at index %d, want %q", i, want)
			}
		}
	}
	if len(args) < 1 {
		t.Fatalf("no args")
	}
	lastArg := args[len(args)-1]
	if !strings.HasPrefix(lastArg, freshMessage) {
		t.Errorf("last arg = %q, want to start with %q", lastArg, freshMessage)
	}
	if strings.Contains(lastArg, "\n") {
		t.Errorf("last arg contains newline: %q", lastArg)
	}
	hasResume := false
	for _, arg := range args {
		if arg == "resume" {
			hasResume = true
		}
	}
	if hasResume {
		t.Error("fresh command must not have resume")
	}
}

func TestCodexCommandResume(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("codex")
	dir := t.TempDir()
	deltaPath := filepath.Join(dir, "delta.txt")
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "c1", Title: "T1-c1", Model: "gpt-5-codex",
		Session: "t-1", PromptFile: deltaPath, Resume: true,
	})
	if bin != "codex" {
		t.Errorf("bin = %q, want codex", bin)
	}
	resumeIdx := -1
	sessionIdx := -1
	for i, arg := range args {
		if arg == "resume" {
			resumeIdx = i
		}
		if arg == "t-1" {
			sessionIdx = i
		}
	}
	if resumeIdx < 0 || sessionIdx < 0 {
		t.Errorf("args = %v, want resume followed by t-1", args)
	} else if sessionIdx != resumeIdx+1 {
		t.Errorf("session at index %d, resume at %d, want adjacent", sessionIdx, resumeIdx)
	}
	if len(args) < 2 {
		t.Fatalf("not enough args")
	}
	beforeLast := args[len(args)-2]
	lastArg := args[len(args)-1]
	if beforeLast != "t-1" || !strings.HasPrefix(lastArg, resumeMessage) {
		t.Errorf("last two args = [%q, %q], want [t-1, msg starting with %q]", beforeLast, lastArg, resumeMessage)
	}
}

func TestCodexCommandNoVariantNoPrompt(t *testing.T) {
	t.Parallel()
	a, _ := AdapterFor("codex")
	bin, args := a.Command(RunRequest{
		Task: "T1", Attempt: "r1", Title: "T1-r1", Model: "gpt-5-codex",
	})
	if bin != "codex" {
		t.Errorf("bin = %q, want codex", bin)
	}
	hasCFlag := false
	for _, arg := range args {
		if arg == "-c" {
			hasCFlag = true
		}
	}
	if hasCFlag {
		t.Error("command with no Variant must not have -c flag")
	}
	lastArg := args[len(args)-1]
	if lastArg != freshMessage {
		t.Errorf("last arg = %q, want exactly %q", lastArg, freshMessage)
	}
}

func TestCodexAdapterFor(t *testing.T) {
	t.Parallel()
	a, err := AdapterFor("codex")
	if err != nil || a.Name() != "codex" {
		t.Errorf("AdapterFor(codex) = %v, %v, want codex adapter nil error", a, err)
	}
	_, err = AdapterFor("nope")
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Errorf("AdapterFor(nope) error = %v, want message mentioning codex", err)
	}
}
