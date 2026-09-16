package flywheel

import (
	"encoding/json"
	"fmt"
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
	Kind    string // "start", "text", "tool", "step", "error"
	Session string
	Text    string
	Tool    string
	Path    string
	Reason  string // step finish reason
	Error   string
	Tokens  *Tokens
	Cost    float64
}

// Adapter turns a run request into a dispatch command and a stream of JSONL
// lines into observations.
type Adapter interface {
	Name() string
	Command(r RunRequest) (bin string, args []string)
	Parse(line []byte) (Observation, bool)
}

// AdapterFor returns the adapter named name. Phase 1 ships opencode and the
// offline sim adapter.
func AdapterFor(name string) (Adapter, error) {
	switch name {
	case "opencode":
		return opencodeAdapter{}, nil
	case "sim":
		return simAdapter{}, nil
	}
	return nil, fmt.Errorf("unknown adapter %q; want \"opencode\" or \"sim\"", name)
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
