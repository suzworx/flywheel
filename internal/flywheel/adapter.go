package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// RunRequest is everything the adapter needs to build a dispatch command.
type RunRequest struct {
	Task       string
	Attempt    string
	PromptFile string // absolute path to the brief (fresh) or delta (resume)
	Model      string
	Variant    string
	Session    string
	Title      string
	Resume     bool
	Increment  int // > 0: a fresh run that does only this increment of the brief (issue #83)
	// AllowedTools and DisallowedTools are the worker's resolved claude tool
	// patterns (worker.allowedTools()/worker.disallowedTools(), populated by
	// Run). The claude adapter passes them as --allowedTools/--disallowedTools;
	// an empty list appends no flag (issue #192).
	AllowedTools    []string
	DisallowedTools []string
	// NoWorkerRules marks a non-worker dispatch such as the review agent
	// (issue #389): claude gets no --append-system-prompt workerRules.
	NoWorkerRules bool
}

// The message placed on the command line before --file. Brief text itself is
// never an argument: on Windows the opencode binary is an npm shim that passes
// arguments through cmd.exe, which truncates multi-line arguments at the first
// newline and would interpret metacharacters like & and |.
const (
	freshMessage  = "Follow the attached brief exactly."
	resumeMessage = "Apply the attached correction to the same task."
)

// freshPrompt is the message a fresh dispatch leads with: freshMessage, plus
// the increment instruction when r.Increment > 0 (issue #83).
func freshPrompt(r RunRequest) string {
	if r.Increment > 0 {
		return fmt.Sprintf("%s Do increment %d only, then report and STOP.", freshMessage, r.Increment)
	}
	return freshMessage
}

// Observation is one decoded event from a run stream.
type Observation struct {
	Kind    string // "start", "text", "tool", "step", "error"
	Session string
	Text    string
	Tool    string
	Path    string
	Paths   []string // every path a multi-file tool touched (codex file_change); Path is the first of them. Empty for single-path observations.
	Reason  string   // step finish reason
	Error   string
	Tokens  *Tokens
	// Aggregate marks Tokens as a session total (the claude result line),
	// not one model call's usage: it counts toward totals but never toward
	// the per-call reasoning peak (issue #286).
	Aggregate bool
	Cost      float64
	EndsTurn  bool // true when this line completes one model turn; run.go counts turns with it
	// Denials are the tool names the harness denied, from the claude result
	// line's permission_denials, each with " <file_path>" when one was given
	// (issue #364).
	Denials []string
	// Command is the shell command of a shell tool call (issue #365).
	Command string
	// ResetText is a rate-limit message's reset clause, the text after
	// "resets " (e.g. "10:20am (America/Los_Angeles)"); set only when Reason
	// is "rate-limited" (issue #380).
	ResetText string
	// Background marks a shell call started with run_in_background; ToolUseID
	// is its tool_use id, a provisional key until its tool_result names the
	// shell. On a "tool_result" observation ToolUseID is the call it answers
	// and ShellID the background shell (or subagent) id its text reported
	// (issue #390).
	Background bool
	ToolUseID  string
	ShellID    string
	// Input is the raw JSON text of a tool_use block's input; run.go matches
	// a background shell id against it to see the shell collected (issue #390).
	Input string
}

// Adapter turns a run request into a dispatch command and a stream of JSONL
// lines into observations.
type Adapter interface {
	Name() string
	Command(r RunRequest) (bin string, args []string)
	Parse(line []byte) (Observation, bool)
}

// obsPaths returns every path an observation touched: Paths when set, else
// Path alone, else nothing.
func obsPaths(o Observation) []string {
	if len(o.Paths) > 0 {
		return o.Paths
	}
	if o.Path != "" {
		return []string{o.Path}
	}
	return nil
}

// AdapterFor returns the adapter named name: opencode, the offline sim
// adapter, claude (issue #49), or codex (issue #275).
func AdapterFor(name string) (Adapter, error) {
	switch name {
	case "opencode":
		return opencodeAdapter{}, nil
	case "sim":
		return simAdapter{}, nil
	case "claude":
		return claudeAdapter{}, nil
	case "codex":
		return codexAdapter{}, nil
	}
	return nil, fmt.Errorf("unknown adapter %q; want \"opencode\", \"sim\", \"claude\", or \"codex\"", name)
}

// opencodeAdapter parses OpenCode's --format json JSONL run stream.
type opencodeAdapter struct{}

func (a opencodeAdapter) Name() string {
	return "opencode"
}

// Command builds the dispatch arguments. The brief is attached with --file,
// never passed on the command line; --file is the final pair and must come
// after the message (it is an array option that swallows trailing
// positionals). A resume adds --session before the message.
func (a opencodeAdapter) Command(r RunRequest) (string, []string) {
	msg := freshPrompt(r)
	if r.Resume {
		msg = resumeMessage
	}
	args := []string{"run", "--pure", "-m", r.Model}
	if r.Variant != "" {
		args = append(args, "--variant", r.Variant)
	}
	args = append(args, "--auto", "--format", "json", "--title", r.Title)
	if r.Session != "" {
		args = append(args, "--session", r.Session)
	}
	args = append(args, msg, "--file", r.PromptFile)
	return "opencode", args
}

// Parse decodes one JSONL line. step_start becomes "start" (carrying the
// session from the top-level sessionID), text "text", tool_use "tool",
// step_finish "step" and error "error"; unknown types and undecodable lines
// return false.
func (a opencodeAdapter) Parse(line []byte) (Observation, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return Observation{}, false
	}
	typRaw, ok := m["type"]
	if !ok {
		return Observation{}, false
	}
	var typ string
	if err := json.Unmarshal(typRaw, &typ); err != nil {
		return Observation{}, false
	}
	var obs Observation
	switch typ {
	case "step_start":
		obs.Kind = "start"
	case "text":
		obs.Kind = "text"
		obs.Text, _ = partString(m, "text")
	case "tool_use":
		obs.Kind = "tool"
		obs.Tool, _ = partString(m, "tool")
		if p, has := partStateInputString(m, "filePath"); has {
			obs.Path = p
		} else {
			// grep and glob carry their target under "path" instead of
			// "filePath" (issue #72).
			obs.Path, _ = partStateInputString(m, "path")
		}
		if obs.Tool == "bash" {
			obs.Command, _ = partStateInputString(m, "command")
		}
	case "step_finish":
		obs.Kind = "step"
		obs.Reason, _ = partString(m, "reason")
		obs.Tokens, _ = partTokens(m)
		obs.Cost, _ = partFloat(m, "cost")
		obs.EndsTurn = true
	case "error":
		obs.Kind = "error"
		obs.Error, _ = errorMessage(m)
	default:
		return Observation{}, false
	}
	if sessRaw, has := m["sessionID"]; has {
		_ = json.Unmarshal(sessRaw, &obs.Session)
	}
	return obs, true
}

// partValue returns the value of key inside the top-level "part" object.
func partValue(m map[string]json.RawMessage, key string) (json.RawMessage, bool) {
	partRaw, ok := m["part"]
	if !ok {
		return []byte{}, false
	}
	var part map[string]json.RawMessage
	if err := json.Unmarshal(partRaw, &part); err != nil {
		return []byte{}, false
	}
	v, present := part[key]
	return v, present
}

// partString returns the string value of key inside "part".
func partString(m map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := partValue(m, key)
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// partFloat returns the float value of key inside "part".
func partFloat(m map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := partValue(m, key)
	if !ok {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

// partTokens decodes part.tokens into a Tokens pointer, or nil when absent.
func partTokens(m map[string]json.RawMessage) (*Tokens, bool) {
	raw, ok := partValue(m, "tokens")
	if !ok {
		return nil, false
	}
	var tm map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tm); err != nil {
		return nil, false
	}
	p := new(Tokens)
	*p = Tokens{
		Input:      tokenInt(tm, "input"),
		Output:     tokenInt(tm, "output"),
		Reasoning:  tokenInt(tm, "reasoning"),
		CacheRead:  tokenIntNested(tm, "cache", "read"),
		CacheWrite: tokenIntNested(tm, "cache", "write"),
	}
	return p, true
}

// tokenInt reads an integer key, defaulting to 0.
func tokenInt(m map[string]json.RawMessage, key string) int {
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0
	}
	return v
}

// tokenIntNested reads an integer nested under outer.inner, e.g. cache.read.
func tokenIntNested(m map[string]json.RawMessage, outer, inner string) int {
	raw, ok := m[outer]
	if !ok {
		return 0
	}
	var om map[string]json.RawMessage
	if err := json.Unmarshal(raw, &om); err != nil {
		return 0
	}
	iv, ok := om[inner]
	if !ok {
		return 0
	}
	var v int
	if err := json.Unmarshal(iv, &v); err != nil {
		return 0
	}
	return v
}

// partStateInputString returns key inside part.state.input, e.g. filePath.
func partStateInputString(m map[string]json.RawMessage, key string) (string, bool) {
	stateRaw, ok := partValue(m, "state")
	if !ok {
		return "", false
	}
	var st map[string]json.RawMessage
	if err := json.Unmarshal(stateRaw, &st); err != nil {
		return "", false
	}
	inRaw, ok := st["input"]
	if !ok {
		return "", false
	}
	var inM map[string]json.RawMessage
	if err := json.Unmarshal(inRaw, &inM); err != nil {
		return "", false
	}
	raw, ok := inM[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// errorMessage returns error.data.message when present, else the raw error
// JSON.
func errorMessage(m map[string]json.RawMessage) (string, bool) {
	errRaw, ok := m["error"]
	if !ok {
		return "", false
	}
	var em map[string]json.RawMessage
	if err := json.Unmarshal(errRaw, &em); err != nil {
		return "", false
	}
	if dataRaw, ok := em["data"]; ok {
		var data map[string]json.RawMessage
		if err := json.Unmarshal(dataRaw, &data); err == nil {
			if msg, ok := data["message"]; ok {
				var s string
				if err := json.Unmarshal(msg, &s); err == nil {
					return s, true
				}
			}
		}
	}
	return string(errRaw), true
}

// claudeAdapter runs Claude Code directly (issue #49): its own
// --output-format stream-json JSONL stream, not OpenCode's.
type claudeAdapter struct{}

func (a claudeAdapter) Name() string {
	return "claude"
}

// Command builds the dispatch arguments. Unlike opencodeAdapter's --file,
// the Claude CLI's -p flag takes the prompt text itself, so the prompt file
// is read here; a read failure yields an empty prompt rather than a panic,
// surfacing downstream as a start-failed run like any other unreadable
// brief. --permission-mode acceptEdits grants file edits without a prompt
// and nothing else — notably NOT Bash, so a worker running under it alone
// could not run its own gates (issue #192). The dispatch therefore also
// passes the worker's resolved tool policy: --allowedTools (r.AllowedTools,
// by default "Bash") so the worker can run its gate lines, and
// --disallowedTools (r.DisallowedTools, by default the git-write family) so
// the worker permission policy ("workers never commit, stash, reset,
// checkout or push") is enforced by the permission layer. An empty list
// appends no flag. --setting-sources user loads only the user's settings:
// the worker's permissions come from the flags above, and a checkout's
// project or local settings (for example permissions.additionalDirectories
// naming the repository root) must not widen where the worker may write
// (issue #359). A resume (r.Resume with a non-empty r.Session) leads the
// prompt with resumeMessage instead of freshPrompt and adds
// --resume <session>, mirroring opencodeAdapter.Command. The worker rules
// (workerRules, PLAN check-in included) reach OpenCode through the policy's
// "instructions" file; claude gets them as --append-system-prompt, on fresh
// and resumed runs alike (issue #360), unless r.NoWorkerRules marks a
// non-worker dispatch such as the review agent (issue #389). The value is multi-line, which is safe
// as a command-line argument because claude is a native binary, not an npm
// shim run through cmd.exe.
func (a claudeAdapter) Command(r RunRequest) (string, []string) {
	prompt, _ := os.ReadFile(r.PromptFile)
	msg := freshPrompt(r)
	resuming := r.Resume && r.Session != ""
	if resuming {
		msg = resumeMessage
	}
	args := []string{
		"-p", msg + "\n" + string(prompt),
		"--output-format", "stream-json",
		"--verbose",
		"--max-turns", "200",
		"--model", r.Model,
		"--permission-mode", "acceptEdits",
		"--setting-sources", "user",
	}
	if !r.NoWorkerRules {
		args = append(args, "--append-system-prompt", workerRules)
	}
	if len(r.AllowedTools) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, r.AllowedTools...)
	}
	if len(r.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, r.DisallowedTools...)
	}
	if resuming {
		args = append(args, "--resume", r.Session)
	}
	return "claude", args
}

// Parse decodes one line of `claude -p ... --output-format stream-json
// --verbose`. system/init becomes "start" (session from session_id);
// assistant becomes "tool" from the first tool_use content block (Text the
// text blocks before it), else "text" from the first text block
// (message.usage supplies Tokens either
// way); a "result" line, or any line carrying a top-level is_error, becomes
// "step" (Reason from stop_reason/subtype/is_error — see claudeReason; Cost
// from total_cost_usd; Tokens from the top-level usage, the session total).
// Anything else — hook events, user echoes — returns false, and unparseable
// JSON never panics.
func (a claudeAdapter) Parse(line []byte) (Observation, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return Observation{}, false
	}
	typ := rawString(m, "type")
	var obs Observation
	switch {
	case typ == "system" && rawString(m, "subtype") == "init":
		obs.Kind = "start"
	case typ == "assistant":
		var ok bool
		if obs, ok = claudeAssistantObs(m); !ok {
			return Observation{}, false
		}
		obs.EndsTurn = true
	case typ == "result" || hasKey(m, "is_error"):
		obs.Kind = "step"
		obs.EndsTurn = true
		var isError bool
		if raw, ok := m["is_error"]; ok {
			_ = json.Unmarshal(raw, &isError)
		}
		obs.Reason = claudeReason(rawString(m, "stop_reason"), rawString(m, "subtype"), isError)
		if status, _ := rawFloat(m, "api_error_status"); isError || status == 429 {
			if reset, ok := claudeRateLimit(status, rawString(m, "result")); ok {
				obs.Reason = "rate-limited"
				obs.ResetText = reset
			}
		}
		obs.Cost, _ = rawFloat(m, "total_cost_usd")
		// The result line's top-level usage is the session total (issue
		// #286); claudeTokens reads m["usage"], the same shape as a message's.
		obs.Tokens = claudeTokens(m)
		obs.Aggregate = true
		obs.Denials = claudeDenials(m)
	case typ == "user":
		return claudeToolResultObs(m)
	default:
		return Observation{}, false
	}
	if s, ok := rawStringOK(m, "session_id"); ok {
		obs.Session = s
	}
	return obs, true
}

// claudeBackgroundID matches the id a background Bash call or subagent
// reports in its tool_result text (issue #390).
var claudeBackgroundID = regexp.MustCompile(`(?:running in background with ID|agentId):\s*([A-Za-z0-9_-]+)`)

// claudeToolResultObs decodes a user line's tool_result blocks: the first
// whose text reports a background shell or subagent id becomes a
// "tool_result" observation, ToolUseID the call it answers and ShellID that
// id. It never ends a turn. Any other user line returns false (issue #390).
func claudeToolResultObs(m map[string]json.RawMessage) (Observation, bool) {
	var msg struct {
		Content []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(m["message"], &msg); err != nil {
		return Observation{}, false
	}
	for _, block := range msg.Content {
		if block.Type != "tool_result" || block.ToolUseID == "" {
			continue
		}
		// content is a string or an array of {"type":"text","text":...}.
		var text string
		if json.Unmarshal(block.Content, &text) != nil {
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(block.Content, &parts)
			var texts []string
			for _, p := range parts {
				if p.Type == "text" {
					texts = append(texts, p.Text)
				}
			}
			text = strings.Join(texts, "\n")
		}
		if sm := claudeBackgroundID.FindStringSubmatch(text); sm != nil {
			return Observation{Kind: "tool_result", ToolUseID: block.ToolUseID, ShellID: sm[1]}, true
		}
	}
	return Observation{}, false
}

// claudeDenials decodes a result line's permission_denials: one entry per
// denial, the tool name plus " <file_path>" when tool_input carries one.
// A missing or malformed array is nil (issue #364).
func claudeDenials(m map[string]json.RawMessage) []string {
	raw, ok := m["permission_denials"]
	if !ok {
		return nil
	}
	var items []struct {
		ToolName  string                     `json:"tool_name"`
		ToolInput map[string]json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var out []string
	for _, it := range items {
		if it.ToolName == "" {
			continue
		}
		d := it.ToolName
		if p := rawString(it.ToolInput, "file_path"); p != "" {
			d += " " + p
		}
		out = append(out, d)
	}
	return out
}

// claudeAssistantObs decodes one assistant line's message.content: the first
// tool_use block when present, its Text the text blocks before it joined
// with "\n" (a model writes its plan there, before its first tool call —
// issue #360), else the first text block; both carry Tokens from
// message.usage. False means content had neither.
func claudeAssistantObs(m map[string]json.RawMessage) (Observation, bool) {
	msgRaw, ok := m["message"]
	if !ok {
		return Observation{}, false
	}
	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return Observation{}, false
	}
	var content []map[string]json.RawMessage
	if raw, ok := msg["content"]; ok {
		_ = json.Unmarshal(raw, &content)
	}
	tok := claudeTokens(msg)
	var texts []string
	for _, block := range content {
		switch rawString(block, "type") {
		case "tool_use":
			obs := Observation{
				Kind:   "tool",
				Tool:   claudeToolName(rawString(block, "name")),
				Path:   claudeToolPath(block),
				Text:   strings.Join(texts, "\n"),
				Tokens: tok,
				Input:  string(block["input"]),
			}
			if rawString(block, "name") == "Bash" {
				obs.Command = claudeToolInput(block, "command")
				if claudeToolInputBool(block, "run_in_background") {
					obs.Background = true
					obs.ToolUseID = rawString(block, "id")
				}
			}
			return obs, true
		case "text":
			texts = append(texts, rawString(block, "text"))
		}
	}
	if len(texts) > 0 {
		return Observation{Kind: "text", Text: texts[0], Tokens: tok}, true
	}
	return Observation{}, false
}

// simAdapter replays a recorded OpenCode run; the model string is the fixture
// path and Command returns an empty bin so the runner never executes a
// process.
type simAdapter struct{}

func (a simAdapter) Name() string {
	return "sim"
}

func (a simAdapter) Command(r RunRequest) (string, []string) {
	return "", []string{}
}

// Parse uses the opencode parser: a fixture is a recorded OpenCode run.
func (a simAdapter) Parse(line []byte) (Observation, bool) {
	oc := opencodeAdapter{}
	if obs, ok := oc.Parse(line); ok {
		return obs, true
	}
	// A claude result line replays through the claude parser, so a fixture
	// can carry permission_denials (issue #364).
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) == nil && rawString(m, "type") == "result" {
		return claudeAdapter{}.Parse(line)
	}
	return Observation{}, false
}

// codexAdapter runs OpenAI Codex via `codex exec --json` (issue #275): parses
// its JSONL event stream into Observations matching the claude and opencode
// adapters' event shapes.
type codexAdapter struct{}

func (a codexAdapter) Name() string {
	return "codex"
}

// Command builds the dispatch arguments for `codex exec --json`. The prompt
// MUST be one line: on Windows, codex is an npm .cmd shim and cmd.exe cuts
// multi-line arguments at the first newline, so the brief is never inlined.
// Instead, the prompt names the brief file and codex reads it.
func (a codexAdapter) Command(r RunRequest) (string, []string) {
	msg := freshPrompt(r)
	resuming := r.Resume && r.Session != ""
	if resuming {
		msg = resumeMessage
	}
	prompt := msg
	if r.PromptFile != "" {
		prompt += " The brief is the file " + r.PromptFile + "; read all of it before doing anything else."
	}
	args := []string{"exec", "--json", "--model", r.Model, "--sandbox", "workspace-write"}
	if r.Variant != "" {
		args = append(args, "-c", "model_reasoning_effort="+r.Variant)
	}
	if resuming {
		args = append(args, "resume", r.Session)
	}
	args = append(args, prompt)
	return "codex", args
}

// Parse decodes one line of `codex exec --json` JSONL output. Each line is
// an object with a "type" field; the mapping to Observations follows the
// Codex protocol (issue #275).
func (a codexAdapter) Parse(line []byte) (Observation, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return Observation{}, false
	}
	typ := rawString(m, "type")
	var obs Observation
	switch typ {
	case "thread.started":
		obs.Kind = "start"
		obs.Session, _ = rawStringOK(m, "thread_id")
		return obs, true
	case "turn.started":
		return Observation{}, false
	case "item.started", "item.updated":
		return Observation{}, false
	case "item.completed":
		itemRaw, ok := m["item"]
		if !ok {
			return Observation{}, false
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			return Observation{}, false
		}
		itemType := rawString(item, "type")
		switch itemType {
		case "agent_message":
			obs.Kind = "text"
			obs.Text, _ = rawStringOK(item, "text")
			obs.EndsTurn = true
			return obs, true
		case "command_execution":
			obs.Kind = "tool"
			obs.Tool = "bash"
			obs.Command, _ = rawStringOK(item, "command")
			obs.EndsTurn = true
			return obs, true
		case "file_change":
			obs.Kind = "tool"
			obs.EndsTurn = true
			changesRaw, ok := item["changes"]
			if !ok {
				return Observation{}, false
			}
			var changes []map[string]json.RawMessage
			if err := json.Unmarshal(changesRaw, &changes); err != nil {
				return Observation{}, false
			}
			if len(changes) == 0 {
				return Observation{}, false
			}
			firstKind := rawString(changes[0], "kind")
			switch firstKind {
			case "add":
				obs.Tool = "write"
			case "delete":
				obs.Tool = "delete"
			default:
				obs.Tool = "edit"
			}
			obs.Path, _ = rawStringOK(changes[0], "path")
			for _, ch := range changes {
				if p, ok := rawStringOK(ch, "path"); ok && p != "" {
					obs.Paths = append(obs.Paths, p)
				}
			}
			return obs, true
		case "mcp_tool_call":
			obs.Kind = "tool"
			obs.Tool = "mcp_tool_call"
			obs.EndsTurn = true
			return obs, true
		case "web_search":
			obs.Kind = "tool"
			obs.Tool = "web_search"
			obs.EndsTurn = true
			return obs, true
		default:
			return Observation{}, false
		}
	case "turn.completed":
		obs.Kind = "step"
		obs.Reason = "stop"
		obs.Aggregate = true
		obs.EndsTurn = false
		usageRaw, ok := m["usage"]
		if ok {
			var usage map[string]json.RawMessage
			if err := json.Unmarshal(usageRaw, &usage); err == nil {
				input := rawInt(usage, "input_tokens")
				cached := rawInt(usage, "cached_input_tokens")
				output := rawInt(usage, "output_tokens")
				reasoning := rawInt(usage, "reasoning_output_tokens")
				t := &Tokens{
					Input:     input - cached,
					CacheRead: cached,
					Output:    output - reasoning,
					Reasoning: reasoning,
				}
				if t.Input < 0 {
					t.Input = 0
				}
				if t.Output < 0 {
					t.Output = 0
				}
				obs.Tokens = t
			}
		}
		return obs, true
	case "turn.failed":
		obs.Kind = "step"
		obs.Reason = "error"
		obs.EndsTurn = false
		errRaw, ok := m["error"]
		if ok {
			var errObj map[string]json.RawMessage
			if err := json.Unmarshal(errRaw, &errObj); err == nil {
				obs.Error, _ = rawStringOK(errObj, "message")
			}
		}
		return obs, true
	case "error":
		obs.Kind = "error"
		obs.Error, _ = rawStringOK(m, "message")
		return obs, true
	default:
		return Observation{}, false
	}
}

// claudeToolNames maps a Claude tool_use name to the lowercase Tool an
// Observation carries; any other name is lowercased unchanged.
var claudeToolNames = map[string]string{
	"Read": "read", "Write": "write", "Edit": "edit", "Grep": "grep", "Glob": "glob",
}

func claudeToolName(name string) string {
	if n, ok := claudeToolNames[name]; ok {
		return n
	}
	return strings.ToLower(name)
}

// claudeToolPath returns a tool_use block's target: input.file_path, else
// input.path, else "".
func claudeToolPath(block map[string]json.RawMessage) string {
	if p := claudeToolInput(block, "file_path"); p != "" {
		return p
	}
	return claudeToolInput(block, "path")
}

// claudeToolInput returns the string input.<key> of a tool_use block, or "".
func claudeToolInput(block map[string]json.RawMessage, key string) string {
	inputRaw, ok := block["input"]
	if !ok {
		return ""
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		return ""
	}
	return rawString(input, key)
}

// claudeToolInputBool reports whether input.<key> of a tool_use block is the
// JSON boolean true.
func claudeToolInputBool(block map[string]json.RawMessage, key string) bool {
	inputRaw, ok := block["input"]
	if !ok {
		return false
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		return false
	}
	var b bool
	return json.Unmarshal(input[key], &b) == nil && b
}

// claudeTokens decodes message.usage into a Tokens pointer, or nil when
// usage is absent. Claude's output_tokens includes thinking; Output is
// stored without it, so Tokens means the same for every adapter: Output is
// the non-reasoning output, and Reasoning is counted separately.
func claudeTokens(msg map[string]json.RawMessage) *Tokens {
	usageRaw, ok := msg["usage"]
	if !ok {
		return nil
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(usageRaw, &usage); err != nil {
		return nil
	}
	t := &Tokens{
		Input:      rawInt(usage, "input_tokens"),
		Output:     rawInt(usage, "output_tokens"),
		CacheRead:  rawInt(usage, "cache_read_input_tokens"),
		CacheWrite: rawInt(usage, "cache_creation_input_tokens"),
	}
	if detailsRaw, ok := usage["output_tokens_details"]; ok {
		var details map[string]json.RawMessage
		if json.Unmarshal(detailsRaw, &details) == nil {
			t.Reasoning = rawInt(details, "thinking_tokens")
		}
	}
	t.Output -= t.Reasoning
	if t.Output < 0 {
		t.Output = 0
	}
	return t
}

// claudeReason maps a result line's stop_reason/subtype/is_error to a step
// Reason. A top-level is_error of true is never a clean stop, whatever
// stop_reason or subtype say: testdata/claude-limit.jsonl is a real captured
// line pairing stop_sequence and subtype "success" with is_error:true and a
// session-limit message — that run did no work, so is_error wins. Otherwise:
// end_turn, stop_sequence, or subtype "success" is "stop"; max_tokens is
// "length"; anything else is "error".
func claudeReason(stopReason, subtype string, isError bool) string {
	if isError {
		return "error"
	}
	switch {
	case stopReason == "end_turn" || stopReason == "stop_sequence" || subtype == "success":
		return "stop"
	case stopReason == "max_tokens":
		return "length"
	default:
		return "error"
	}
}

// claudeRateLimit reports whether a result line is a rate limit: an
// api_error_status of 429, or a result message naming a rate, usage or
// session limit. reset is the text after "resets " when the message has one
// (issue #380).
func claudeRateLimit(status float64, result string) (reset string, ok bool) {
	lower := strings.ToLower(result)
	if status != 429 && !strings.Contains(lower, "rate limit") &&
		!strings.Contains(lower, "usage limit") && !strings.Contains(lower, "session limit") {
		return "", false
	}
	for _, key := range []string{"resets ", "Resets "} {
		if i := strings.Index(result, key); i >= 0 {
			reset = strings.TrimSpace(result[i+len(key):])
			break
		}
	}
	return reset, true
}

// rawString returns the string value of key in m, or "" when absent or not
// a string.
func rawString(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// rawStringOK is rawString but also reports whether key was present and a
// string.
func rawStringOK(m map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// rawFloat returns the float64 value of key in m.
func rawFloat(m map[string]json.RawMessage, key string) (float64, bool) {
	raw, ok := m[key]
	if !ok {
		return 0, false
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	return v, true
}

// rawInt returns the int value of key in m, or 0 when absent or not a
// number.
func rawInt(m map[string]json.RawMessage, key string) int {
	raw, ok := m[key]
	if !ok {
		return 0
	}
	var v int
	_ = json.Unmarshal(raw, &v)
	return v
}

// hasKey reports whether m carries key at all, regardless of its value.
func hasKey(m map[string]json.RawMessage, key string) bool {
	_, ok := m[key]
	return ok
}
