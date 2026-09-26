package flywheel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestParseReviewFindings checks the reviewer's answer parser (issue #389): a
// bare object, a fenced block with prose around it (the last block wins),
// severity normalisation, an empty list, and malformed answers.
func TestParseReviewFindings(t *testing.T) {
	t.Parallel()
	bare := `{"findings":[{"severity":"major","category":"correctness","file":"a.go","line":3,"claim":"c","scenario":"s","fix":"f"}]}`
	got, err := parseReviewFindings(bare)
	if err != nil || len(got) != 1 || got[0].Severity != "major" || got[0].Line != 3 || got[0].File != "a.go" {
		t.Fatalf("bare = %+v, %v", got, err)
	}

	fenced := "I read the diff.\r\n```json\r\n{\"findings\":[{\"severity\":\"nit\",\"file\":\"x.go\",\"claim\":\"old\"}]}\r\n```\r\n" +
		"On reflection:\n```json\n{\"findings\":[{\"severity\":\"BLOCKER\",\"file\":\"b.go\",\"line\":9,\"claim\":\"loses data\"}," +
		"{\"severity\":\"Minor\",\"file\":\"c.md\",\"claim\":\"doc lies\"}]}\n```\nThat is all."
	got, err = parseReviewFindings(fenced)
	if err != nil {
		t.Fatalf("fenced: %v", err)
	}
	if len(got) != 2 || got[0].Severity != "blocker" || got[0].File != "b.go" || got[1].Severity != "minor" {
		t.Fatalf("fenced = %+v, want the last block normalised", got)
	}

	got, err = parseReviewFindings("Nothing wrong.\n```json\n{\"findings\": []}\n```")
	if err != nil || len(got) != 0 {
		t.Fatalf("empty = %+v, %v", got, err)
	}
	got, err = parseReviewFindings(`Clean. {"findings": []} Done.`)
	if err != nil || len(got) != 0 {
		t.Fatalf("bare empty with prose = %+v, %v", got, err)
	}

	for name, text := range map[string]string{
		"no json":          "Looks fine to me.",
		"broken json":      "```json\n{\"findings\": [ {\"severity\": \n```",
		"unclosed fence":   "```json\n{\"findings\": []}",
		"no findings key":  "```json\n{\"issues\": []}\n```",
		"unknown severity": `{"findings":[{"severity":"critical","file":"a.go","claim":"c"}]}`,
		"no file":          `{"findings":[{"severity":"minor","claim":"c"}]}`,
	} {
		if got, err := parseReviewFindings(text); err == nil {
			t.Errorf("%s: parsed %+v, want an error", name, got)
		} else if !strings.Contains(err.Error(), "review answer") {
			t.Errorf("%s: error %q does not name the review answer", name, err)
		}
	}
}

// reviewAgentRepo is initTask with a claude worker, an uncommitted change to
// a.go, and a fake claude on PATH (the TestMain stand-in) whose final
// assistant text is answer.
func reviewAgentRepo(t *testing.T, answer string) string {
	t.Helper()
	dir, err := initTask(t, []string{"exit 0"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n\nfunc Changed() {}\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	fakeClaudeAnswer(t, answer)
	return dir
}

// fakeClaudeAnswer installs the test binary as claude on PATH, replaying a
// stream whose one assistant message is answer.
func fakeClaudeAnswer(t *testing.T, answer string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "claude")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	if err := linkOrCopy(exe, fake); err != nil {
		t.Fatalf("install fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	text, _ := json.Marshal(answer)
	const session = "ses_review_001"
	stream := fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`+"\n"+
		`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"text","text":%s}]}}`+"\n"+
		`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":%q,"total_cost_usd":0.01}`+"\n",
		session, session, text, session)
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	t.Setenv(fakeClaudeEnv, path)
}

// reviewKinds counts task T1's review_finding and reviewed events.
func reviewKinds(t *testing.T, dir string) (findings []Event, reviewed []Event) {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		switch e.Kind {
		case "review_finding":
			findings = append(findings, e)
		case "reviewed":
			reviewed = append(reviewed, e)
		}
	}
	return findings, reviewed
}

// TestReviewAgent runs the review agent against a fake claude (issue #389):
// a blocker and a minor record two review_finding events and a reviewed
// correct; a clean answer records pass in the next round; a worker session
// is refused; a garbage answer records nothing; the dispatch is read-only.
func TestReviewAgent(t *testing.T) {
	// not parallel: fakeClaudeAnswer sets PATH
	answer := "I read the diff.\n```json\n{\"findings\":[" +
		`{"severity":"blocker","category":"correctness","file":"a.go","line":3,"claim":"Changed drops data","scenario":"any call loses the input","fix":"return it"},` +
		`{"severity":"minor","category":"docs","file":"a.go","line":1,"claim":"no doc comment","scenario":"godoc shows nothing","fix":"add one"}` +
		"]}\n```"
	dir := reviewAgentRepo(t, answer)
	res, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
	if err != nil {
		t.Fatalf("ReviewAgent() error = %v", err)
	}
	if res.Verdict != "correct" || res.Round != 1 || len(res.Findings) != 2 || res.Note != "2 finding(s): 1 blocker, 0 major, 1 minor" {
		t.Errorf("result = %+v", res)
	}
	findings, reviewed := reviewKinds(t, dir)
	if len(findings) != 2 || len(reviewed) != 1 {
		t.Fatalf("events: %d review_finding, %d reviewed; want 2 and 1", len(findings), len(reviewed))
	}
	if f := findings[0]; f.Finding != "T1-r1-1" || f.Severity != "blocker" || f.Path != "a.go" || f.LineNo != 3 || f.Session != "rev-1" || f.Tree == "" {
		t.Errorf("first finding = %+v", f)
	}
	if r := reviewed[0]; r.Verdict != "correct" || r.Persona != "reviewer" || r.Adapter != "claude" || r.Tree != findings[0].Tree {
		t.Errorf("reviewed = %+v", r)
	}
	prompt, err := os.ReadFile(res.Prompt)
	if err != nil || !strings.Contains(string(prompt), "func Changed()") || !strings.Contains(string(prompt), "# You are the reviewer") || !strings.Contains(string(prompt), "# TASK: gauges") {
		t.Errorf("prompt %s lacks the instructions, brief or diff (err %v)", res.Prompt, err)
	}
	if _, err := os.Stat(res.Transcript); err != nil {
		t.Errorf("transcript: %v", err)
	}

	fakeClaudeAnswer(t, "Nothing wrong.\n```json\n{\"findings\": []}\n```")
	res, err = ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
	if err != nil || res.Verdict != "pass" || res.Round != 2 {
		t.Errorf("clean round = %+v, %v; want pass in round 2", res, err)
	}

	logFinished(t, dir, "T1", "w1")
	if _, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "w1"}); refusalRule(t, err) != "T4" {
		t.Errorf("worker session: err = %v, want a T4 refusal", err)
	}

	garbage := reviewAgentRepo(t, "Looks fine to me.")
	_, err = ReviewAgent(garbage, "T1", ReviewAgentOptions{Session: "rev-1"})
	if err == nil || !strings.Contains(err.Error(), filepath.Join(".flywheel", "reviews", "T1.1.jsonl")) {
		t.Errorf("garbage answer: err = %v, want one naming the transcript", err)
	}
	if f, r := reviewKinds(t, garbage); len(f)+len(r) != 0 {
		t.Errorf("garbage answer recorded %d findings and %d reviewed", len(f), len(r))
	}

	_, args := claudeAdapter{}.Command(reviewRunRequest("T1", 1, res.Prompt, "m"))
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--disallowedTools Edit Write NotebookEdit") || strings.Contains(joined, "--allowedTools Bash ") {
		t.Errorf("claude args = %q, want a read-only tool policy", joined)
	}
}

// TestReviewAgentRefusedAnswer: a first answer naming a missing file is
// refused and the reviewer runs once more; the valid second answer is
// recorded once, from the second transcript (issue #389).
func TestReviewAgentRefusedAnswer(t *testing.T) {
	// not parallel: fakeClaudeAnswer sets PATH; sets the package-level commandHook
	bad := "```json\n{\"findings\":[{\"severity\":\"major\",\"file\":\"nope.go\",\"line\":1,\"claim\":\"c\",\"scenario\":\"s\"}]}\n```"
	good := "```json\n{\"findings\":[{\"severity\":\"major\",\"file\":\"a.go\",\"line\":3,\"claim\":\"real\",\"scenario\":\"s\"}]}\n```"
	dir := reviewAgentRepo(t, good)
	second := os.Getenv(fakeClaudeEnv)
	fakeClaudeAnswer(t, bad)
	// The first run replays bad; the hook switches the second run to good.
	var reqs []RunRequest
	commandHook = func(r RunRequest) {
		reqs = append(reqs, r)
		if len(reqs) == 2 {
			_ = os.Setenv(fakeClaudeEnv, second)
		}
	}
	defer func() { commandHook = nil }()
	res, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
	commandHook = nil
	if err != nil {
		t.Fatalf("ReviewAgent() error = %v", err)
	}
	if len(reqs) != 2 || !strings.HasSuffix(res.Transcript, "T1.1b.jsonl") {
		t.Errorf("runs = %d, transcript %s; want 2 runs and the b transcript", len(reqs), res.Transcript)
	}
	findings, reviewed := reviewKinds(t, dir)
	if len(findings) != 1 || len(reviewed) != 1 || findings[0].Title != "real" {
		t.Fatalf("recorded %d findings (%+v) and %d reviewed; want one, from the second answer", len(findings), findings, len(reviewed))
	}
	prompt, _ := os.ReadFile(res.Prompt)
	if !strings.Contains(string(prompt), "# Your previous answer was refused") || !strings.Contains(string(prompt), "nope.go") {
		t.Errorf("retry prompt lacks the refusal section")
	}

	twice := reviewAgentRepo(t, bad)
	_, err = ReviewAgent(twice, "T1", ReviewAgentOptions{Session: "rev-1"})
	if err == nil || !strings.Contains(err.Error(), "T1.1.jsonl") || !strings.Contains(err.Error(), "T1.1b.jsonl") || !strings.Contains(err.Error(), "nope.go") {
		t.Errorf("refused twice: err = %v, want both transcripts and the violation", err)
	}
	if f, r := reviewKinds(t, twice); len(f)+len(r) != 0 {
		t.Errorf("refused twice recorded %d findings and %d reviewed", len(f), len(r))
	}
}

// TestValidateFindings checks the findings contract (issue #389).
func TestValidateFindings(t *testing.T) {
	t.Parallel()
	wd := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wd, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "d", "f.go"), []byte("a\nb\nc"), 0o644); err != nil {
		t.Fatal(err)
	}
	ok := ReviewFinding{Severity: "minor", File: "d/f.go", Line: 3, Claim: "c", Scenario: "s"}
	with := func(mod func(*ReviewFinding)) ReviewFinding { f := ok; mod(&f); return f }
	nit := with(func(f *ReviewFinding) { f.Severity = "nit" })
	for name, c := range map[string]struct {
		findings []ReviewFinding
		want     int
	}{
		"valid":                    {[]ReviewFinding{ok, with(func(f *ReviewFinding) { f.Line = 0 })}, 0},
		"deleted file":             {[]ReviewFinding{with(func(f *ReviewFinding) { f.File = "gone.go"; f.Line = 9 })}, 0},
		"missing file":             {[]ReviewFinding{with(func(f *ReviewFinding) { f.File = "nope.go" })}, 1},
		"backslash":                {[]ReviewFinding{with(func(f *ReviewFinding) { f.File = `d\f.go` })}, 1},
		"absolute":                 {[]ReviewFinding{with(func(f *ReviewFinding) { f.File = "/d/f.go" })}, 1},
		"line past end":            {[]ReviewFinding{with(func(f *ReviewFinding) { f.Line = 4 })}, 1},
		"empty claim":              {[]ReviewFinding{with(func(f *ReviewFinding) { f.Claim = " " })}, 1},
		"empty scenario and claim": {[]ReviewFinding{with(func(f *ReviewFinding) { f.Claim, f.Scenario = "", "" })}, 1},
		"three nits":               {[]ReviewFinding{nit, nit, nit}, 0},
		"four nits":                {[]ReviewFinding{nit, nit, nit, nit}, 1},
	} {
		if got := validateFindings(wd, []string{"gone.go", "d/f.go"}, c.findings, ""); len(got) != c.want {
			t.Errorf("%s: violations %q, want %d", name, got, c.want)
		}
	}
	// A panel member's answer (issue #420): every finding carries its
	// dimension, or the finding is a violation.
	tests := with(func(f *ReviewFinding) { f.Category = "tests" })
	if got := validateFindings(wd, nil, []ReviewFinding{tests}, "tests"); len(got) != 0 {
		t.Errorf("in-dimension finding: violations %q, want none", got)
	}
	got := validateFindings(wd, nil, []ReviewFinding{tests, ok, with(func(f *ReviewFinding) { f.Category = "docs" })}, "tests")
	if len(got) != 2 || !strings.Contains(got[0], "finding 2") || !strings.Contains(got[1], `category "docs" is outside your dimension`) {
		t.Errorf("out-of-dimension findings: violations %q, want findings 2 and 3 refused", got)
	}
}

// TestReviewAgentLargePrompt: a review prompt over 40 KB, past Windows' ~32K
// command-line cap, reaches the fake reviewer intact on stdin (issue #427).
func TestReviewAgentLargePrompt(t *testing.T) {
	// not parallel: fakeClaudeAnswer sets PATH and the fake claude stdin env
	dir := reviewAgentRepo(t, "Nothing wrong.\n```json\n{\"findings\": []}\n```")
	big := "package x\n\nfunc Changed() {}\n" + strings.Repeat("// a long diff line with & | ^ %PATH% in it\n", 1200)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(big), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	got := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv(fakeClaudeStdinEnv, got)
	res, err := ReviewAgent(dir, "T1", ReviewAgentOptions{Session: "rev-1"})
	if err != nil {
		t.Fatalf("ReviewAgent() error = %v", err)
	}
	prompt, err := os.ReadFile(res.Prompt)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	stdin, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("fake reviewer saved no stdin: %v", err)
	}
	if len(prompt) <= 40*1024 || !strings.Contains(string(prompt), "a long diff line") {
		t.Fatalf("prompt = %d bytes, want over 40 KB carrying the diff", len(prompt))
	}
	if string(stdin) != freshMessage+"\n"+string(prompt) {
		t.Errorf("stdin = %d bytes, want freshMessage then the whole %d-byte prompt", len(stdin), len(prompt))
	}
}

// TestReviewerNoWorkerRules: the reviewer's claude args carry no worker
// rules; a normal worker request still does (issue #389).
func TestReviewerNoWorkerRules(t *testing.T) {
	t.Parallel()
	_, args := claudeAdapter{}.Command(reviewRunRequest("T1", 1, "missing.md", "m"))
	if strings.Contains(strings.Join(args, " "), "--append-system-prompt") {
		t.Errorf("reviewer args carry --append-system-prompt: %q", args)
	}
	_, args = claudeAdapter{}.Command(RunRequest{Task: "T1", PromptFile: "missing.md", Model: "m"})
	if !strings.Contains(strings.Join(args, " "), "--append-system-prompt") {
		t.Errorf("worker args lack --append-system-prompt: %q", args)
	}
}

// TestReviewAgentFailureCause checks why a failed reviewer run failed (issue
// #469): the stream's error result first, else the stderr tail, one clipped
// line either way.
func TestReviewAgentFailureCause(t *testing.T) {
	t.Parallel()
	if got := reviewFailureCause("  API Error: 529 overloaded\n", "stderr noise"); got != "API Error: 529 overloaded" {
		t.Errorf("result preferred: got %q", got)
	}
	if got := reviewFailureCause("", "panic: boom\n  at main.go:3"); got != "panic: boom at main.go:3" {
		t.Errorf("err tail fallback: got %q", got)
	}
	if got := reviewFailureCause(" ", ""); got != "" {
		t.Errorf("no cause: got %q", got)
	}
	long := reviewFailureCause(strings.Repeat("é", 400), "")
	if len(long) > maxFailureCause || !strings.HasSuffix(long, "...") || !utf8.ValidString(long) {
		t.Errorf("long cause: %d bytes, %q...; want <= %d valid bytes ending ...", len(long), long[:10], maxFailureCause)
	}
	if got := lastNonEmptyLine("first\r\nlast line\r\n\r\n  \n"); got != "last line" {
		t.Errorf("lastNonEmptyLine = %q", got)
	}
	for _, c := range []struct {
		line, want string
		ok         bool
	}{
		{`{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached"}`, "Claude AI usage limit reached", true},
		{`{"type":"result","subtype":"error_max_turns","is_error":true}`, "error_max_turns", true},
		{`{"type":"result","subtype":"success","is_error":false,"result":"done"}`, "", false},
		{`{"type":"assistant","is_error":true}`, "", false},
		{`not json`, "", false},
	} {
		if got, ok := resultErrorText([]byte(c.line)); got != c.want || ok != c.ok {
			t.Errorf("resultErrorText(%s) = %q, %v; want %q, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}
