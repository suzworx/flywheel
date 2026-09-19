package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
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
	// AllowedTools and DisallowedTools are the worker's resolved claude tool
	// patterns (worker.allowedTools()/worker.disallowedTools(), populated by
	// Run). The claude adapter passes them as --allowedTools/--disallowedTools;
	// an empty list appends no flag (issue #192).
	AllowedTools    []string
	DisallowedTools []string
}

// The message placed on the command line before --file. Brief text itself is
// never an argument: on Windows the opencode binary is an npm shim that passes
// arguments through cmd.exe, which truncates multi-line arguments at the first
// newline and would interpret metacharacters like & and |.
const (
	freshMessage  = "Follow the attached brief exactly."
	resumeMessage = "Apply the attached correction to the same task."
)

// Observation is one decoded event from a run stream.
type Observation struct {
	Kind     string // "start", "text", "tool", "step", "error"
	Session  string
	Text     string
	Tool     string
	Path     string
	Reason   string // step finish reason
	Error    string
	Tokens   *Tokens
	Cost     float64
	EndsTurn bool // true when this line completes one model turn; run.go counts turns with it
}

// Adapter turns a run request into a dispatch command and a stream of JSONL
// lines into observations.
type Adapter interface {
	Name() string
	Command(r RunRequest) (bin string, args []string)
	Parse(line []byte) (Observation, bool)
}

// AdapterFor returns the adapter named name: opencode, the offline sim
// adapter, or claude (issue #49).
func AdapterFor(name string) (Adapter, error) {
	switch name {
	case "opencode":
		return opencodeAdapter{}, nil
	case "sim":
		return simAdapter{}, nil
	case "claude":
		return claudeAdapter{}, nil
	}
	return nil, fmt.Errorf("unknown adapter %q; want \"opencode\", \"sim\", or \"claude\"", name)
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
	msg := freshMessage
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
// appends no flag. A resume (r.Resume with a non-empty r.Session) leads the
// prompt with resumeMessage instead of freshMessage and adds
// --resume <session>, mirroring opencodeAdapter.Command.
func (a claudeAdapter) Command(r RunRequest) (string, []string) {
	prompt, _ := os.ReadFile(r.PromptFile)
	msg := freshMessage
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
// assistant becomes "tool" from the first tool_use content block, else
// "text" from the first text block (message.usage supplies Tokens either
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
		obs.Cost, _ = rawFloat(m, "total_cost_usd")
		// The result line's top-level usage is the session total (issue
		// #286); claudeTokens reads m["usage"], the same shape as a message's.
		obs.Tokens = claudeTokens(m)
	default:
		return Observation{}, false
	}
	if s, ok := rawStringOK(m, "session_id"); ok {
		obs.Session = s
	}
	return obs, true
}

// claudeAssistantObs decodes one assistant line's message.content: the first
// tool_use block when present, else the first text block; both carry Tokens
// from message.usage. False means content had neither.
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
	var textBlock map[string]json.RawMessage
	for _, block := range content {
		switch rawString(block, "type") {
		case "tool_use":
			return Observation{
				Kind:   "tool",
				Tool:   claudeToolName(rawString(block, "name")),
				Path:   claudeToolPath(block),
				Tokens: tok,
			}, true
		case "text":
			if textBlock == nil {
				textBlock = block
			}
		}
	}
	if textBlock != nil {
		return Observation{Kind: "text", Text: rawString(textBlock, "text"), Tokens: tok}, true
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
	return oc.Parse(line)
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
	inputRaw, ok := block["input"]
	if !ok {
		return ""
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(inputRaw, &input); err != nil {
		return ""
	}
	if p := rawString(input, "file_path"); p != "" {
		return p
	}
	return rawString(input, "path")
}

// claudeTokens decodes message.usage into a Tokens pointer, or nil when
// usage is absent. Reasoning is output_tokens_details.thinking_tokens when
// present, else 0.
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
