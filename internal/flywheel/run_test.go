package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestAdapterForClaude checks AdapterFor("claude") resolves to the claude
// adapter (issue #49).
func TestAdapterForClaude(t *testing.T) {
	a, err := AdapterFor("claude")
	if err != nil || a.Name() != "claude" {
		t.Errorf("AdapterFor(claude) = %v, %v", a, err)
	}
}

// TestClaudeCommandFlags checks Command's binary and required flags: the
// binary "claude" driving --output-format stream-json and the model.
func TestClaudeCommandFlags(t *testing.T) {
	a, _ := AdapterFor("claude")
	briefPath := filepath.Join(t.TempDir(), "brief.txt")
	if err := os.WriteFile(briefPath, []byte("do the thing"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	bin, args := a.Command(RunRequest{Task: "T1", Attempt: "r1", Model: "claude-sonnet-5", PromptFile: briefPath})
	if bin != "claude" {
		t.Errorf("bin = %q, want claude", bin)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--output-format stream-json") {
		t.Errorf("args = %v, want --output-format stream-json", args)
	}
	if !strings.Contains(joined, "claude-sonnet-5") {
		t.Errorf("args = %v, want the model claude-sonnet-5", args)
	}
}

// TestRunGeneralizesToNonSimAdapters checks Run's process-launch guard
// (internal/flywheel/run.go, formerly `worker.Adapter == "opencode"`, now
// `worker.Adapter != "sim"`) actually reaches adap.Command and exec.Command
// for a worker configured with adapter "claude" — not just opencode
// (issue #49). commandHook fires before that branch, so it captures the
// RunRequest Run built without needing the real claude binary; PATH is
// cleared first so exec.Command's own lookup fails fast instead of finding
// and starting the real Claude CLI installed on this machine (the one used
// to capture testdata/claude-real.jsonl) — this test never spawns it.
func TestRunGeneralizesToNonSimAdapters(t *testing.T) {
	dir := setupTask(t)
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	t.Setenv("PATH", t.TempDir())

	var got RunRequest
	commandHook = func(r RunRequest) { got = r }
	defer func() { commandHook = nil }()

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() error = nil, want a lookup failure with PATH cleared (proves no real binary ran)")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("Run() error = %v, want it naming the claude binary it tried to start", err)
	}
	if got.Model != "claude-sonnet-5" {
		t.Fatalf("commandHook did not fire before the launch branch; got = %+v", got)
	}

	a, aerr := AdapterFor("claude")
	if aerr != nil {
		t.Fatalf("AdapterFor(claude) error = %v", aerr)
	}
	bin, args := a.Command(got)
	if bin != "claude" {
		t.Errorf("resolved bin = %q, want claude", bin)
	}
	if !strings.Contains(strings.Join(args, " "), "--output-format stream-json") {
		t.Errorf("resolved args = %v, want --output-format stream-json", args)
	}
}

// setupTask returns an initialized temp dir with a planned T1 event pointing
// at a brief file.
func setupTask(t *testing.T) string {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("one line brief\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	return dir
}

// simConfig returns a config whose single worker replays the fixture model.
func simConfig(model string) Config {
	return Config{
		Version: 1,
		Workers: []Worker{{Name: "sim", Adapter: "sim", Model: model}},
	}
}

// normLF normalises \r\n to \n so fixture bytes compare equal on CRLF
// checkouts.
func normLF(b []byte) string {
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestRunSimClean(t *testing.T) {
	dir := setupTask(t)
	model := fixturePath("clean.jsonl", t)
	if err := WriteConfig(dir, simConfig(model)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Attempt != "r1" {
		t.Errorf("attempt = %q, want r1", res.Attempt)
	}
	if res.Session != "ses_test_clean_001" {
		t.Errorf("session = %q, want ses_test_clean_001", res.Session)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("rc/reason = %d/%q, want 0/stop", res.RC, res.Reason)
	}
	if res.Steps != 2 {
		t.Errorf("steps = %d, want 2", res.Steps)
	}
	if res.Tokens == nil || res.Tokens.Input != 200 || res.Tokens.Output != 70 ||
		res.Tokens.Reasoning != 15 || res.Tokens.CacheRead != 1000 || res.Tokens.CacheWrite != 5 {
		t.Errorf("tokens = %v, want 200/70/15/1000/5", res.Tokens)
	}
	if res.Cost < 0.00399 || res.Cost > 0.00401 {
		t.Errorf("cost = %v, want about 0.004", res.Cost)
	}
	if ExitCode(res) != 0 {
		t.Errorf("ExitCode() = %d, want 0", ExitCode(res))
	}

	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 6 {
		t.Fatalf("events = %d, want 6", len(evs))
	}
	if evs[0].Kind != "planned" {
		t.Errorf("evs[0] kind = %q, want planned", evs[0].Kind)
	}
	d := evs[1]
	if d.Kind != "dispatched" || d.Attempt != "r1" || d.Adapter != "sim" ||
		d.Model != model || d.Path != ".flywheel/runs/T1.r1.jsonl" ||
		d.SHA256 != contentSHA([]byte("one line brief\n")) {
		t.Errorf("dispatched event = %v", d)
	}
	if d.Brief != "b.txt" {
		t.Errorf("dispatched brief = %q, want the repo-relative prompt path b.txt", d.Brief)
	}
	policyB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "opencode-worker.json"))
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	policySum := sha256.Sum256(policyB)
	if d.Note != "policy sha256="+hex.EncodeToString(policySum[:]) {
		t.Errorf("dispatched note = %q, want the policy sha256", d.Note)
	}
	if evs[2].Kind != "started" || evs[2].Session != "ses_test_clean_001" {
		t.Errorf("started event = %v", evs[2])
	}
	if evs[3].Kind != "worker_plan" || evs[3].Path != ".flywheel/runs/T1.r1.plan.md" || evs[3].SHA256 == "" {
		t.Errorf("worker_plan event = %v", evs[3])
	}
	if evs[4].Kind != "report" || evs[4].Path != ".flywheel/runs/T1.r1.report.md" || evs[4].SHA256 == "" {
		t.Errorf("report event = %v", evs[4])
	}
	f := evs[5]
	if f.Kind != "finished" || f.RC == nil || *f.RC != 0 || f.Reason != "stop" ||
		f.Model != model || f.Steps != 2 || f.Tokens == nil || f.Tokens.Input != 200 ||
		f.Cost < 0.00399 || f.Cost > 0.00401 {
		t.Errorf("finished event = %v, want model %q", f, model)
	}
	runSHA := shaOf(filepath.Join(dir, ".flywheel", "runs", "T1.r1.jsonl"), t)
	if f.SHA256 != runSHA {
		t.Errorf("finished sha256 = %q, want run file %q", f.SHA256, runSHA)
	}

	// The run file reproduces the fixture modulo line endings: on a CRLF
	// checkout the fixture is checked out with \r\n while the runner writes
	// LF, so normalise both sides before comparing. Plan and report hold the
	// recorded texts.
	runB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "runs", "T1.r1.jsonl"))
	if err != nil {
		t.Fatalf("read run file: %v", err)
	}
	fixtureB, err := os.ReadFile(model)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if normLF(runB) != normLF(fixtureB) {
		t.Error("run file does not reproduce the fixture (after line-ending normalisation)")
	}
	planB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "runs", "T1.r1.plan.md"))
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if string(planB) != "PLAN read the layout, then implement the command registry." {
		t.Errorf("plan file = %q", planB)
	}
	reportB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "runs", "T1.r1.report.md"))
	if err != nil {
		t.Fatalf("read report file: %v", err)
	}
	if string(reportB) != "Implemented the command registry. Files changed: main.go. Tests pass." {
		t.Errorf("report file = %q", reportB)
	}
	plog := string(buf.Bytes())
	for _, want := range []string{
		"T1 r1 dispatched sim",
		"T1 r1 started ses_test_clean_001",
		"T1 r1 plan recorded",
		"T1 r1 report recorded",
		"T1 r1 finished rc=0 reason=stop model=" + model + " steps=2",
	} {
		if !strings.Contains(plog, want) {
			t.Errorf("progress missing %q; got:\n%s", want, plog)
		}
	}
}

// TestCostK checks the human-line cost formatter maps the issue's three
// values exactly (issue #81).
func TestCostK(t *testing.T) {
	for _, tt := range []struct {
		c    float64
		want string
	}{
		{0, "0"},
		{0.0015970879999999998, "0.0016"},
		{1.23456, "1.2345"},
	} {
		if got := costK(tt.c); got != tt.want {
			t.Errorf("costK(%v) = %q, want %q", tt.c, got, tt.want)
		}
	}
}

// TestRunSimCostRounded checks the finish line prints the rounded cost while
// the finished event keeps the full float (issue #81).
func TestRunSimCostRounded(t *testing.T) {
	dir := setupTask(t)
	model := fixturePath("longcost.jsonl", t)
	if err := WriteConfig(dir, simConfig(model)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Cost != 0.0015970879999999998 {
		t.Errorf("result cost = %v, want the full float 0.0015970879999999998", res.Cost)
	}
	plog := string(buf.Bytes())
	if !strings.Contains(plog, "cost=$0.0016") {
		t.Errorf("finish line missing the rounded cost; got:\n%s", plog)
	}
	if strings.Contains(plog, "cost=$0.001597") {
		t.Errorf("finish line prints the raw float; got:\n%s", plog)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" || f.Cost != 0.0015970879999999998 {
		t.Errorf("finished event = %v, want the full cost 0.0015970879999999998", f)
	}
}

func TestRunSimAttemptNumbering(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res1, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() 1 error = %v", err)
	}
	res2, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() 2 error = %v", err)
	}
	if res1.Attempt != "r1" || res2.Attempt != "r2" {
		t.Errorf("fresh attempts = %q, %q, want r1, r2", res1.Attempt, res2.Attempt)
	}

	delta := filepath.Join(dir, ".flywheel", "briefs", "T1.delta.txt")
	if err := os.MkdirAll(filepath.Dir(delta), 0o755); err != nil {
		t.Fatalf("mkdir briefs: %v", err)
	}
	if err := os.WriteFile(delta, []byte("fix the registry\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	resC1, err := Run(dir, RunOptions{Task: "T1", Resume: true, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() c1 error = %v", err)
	}
	resC2, err := Run(dir, RunOptions{Task: "T1", Resume: true, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() c2 error = %v", err)
	}
	if resC1.Attempt != "c1" || resC2.Attempt != "c2" {
		t.Errorf("resume attempts = %q, %q, want c1, c2", resC1.Attempt, resC2.Attempt)
	}
	if resC1.Session != "ses_test_clean_001" {
		t.Errorf("c1 session = %q, want ses_test_clean_001", resC1.Session)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var r1d, c1d Event
	for _, e := range evs {
		if e.Kind != "dispatched" {
			continue
		}
		if e.Attempt == "r1" {
			r1d = e
		}
		if e.Attempt == "c1" {
			c1d = e
		}
	}
	if r1d.Brief != "b.txt" {
		t.Errorf("fresh dispatched brief = %q, want b.txt", r1d.Brief)
	}
	if c1d.Brief != ".flywheel/briefs/T1.delta.txt" {
		t.Errorf("resume dispatched brief = %q, want .flywheel/briefs/T1.delta.txt", c1d.Brief)
	}
}

func TestRunResumeWithoutSessionErrors(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	_, err := Run(dir, RunOptions{Task: "T1", Resume: true, Progress: &buf})
	if err == nil {
		t.Fatal("Run() resume without a session: got nil error, want refusal")
	}
	if !strings.Contains(err.Error(), "no worker session") {
		t.Errorf("Run() error = %v, want 'no worker session'", err)
	}
	var e *NoWorkerSession
	if !errors.As(err, &e) {
		t.Errorf("Run() error = %v, want the NoWorkerSession refusal", err)
	}
	if e.Task != "T1" {
		t.Errorf("refusal task = %q, want T1", e.Task)
	}
}

func TestRunResumeWithoutSessionRecordsNoEvents(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	_, err := Run(dir, RunOptions{Task: "T1", Resume: true, Progress: &buf})
	if err == nil {
		t.Fatal("Run() resume without a session: got nil error, want refusal")
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 1 || evs[0].Kind != "planned" {
		t.Errorf("events = %v, want only the planned event (the refusal must record nothing)", evs)
	}
}

func TestRunUnplannedTaskErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	_, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err == nil {
		t.Fatal("Run() unplanned task: got nil error, want refusal")
	} else if !strings.Contains(err.Error(), "no planned event") {
		t.Errorf("Run() error = %v, want 'no planned event'", err)
	}
}

// TestRunRecordsBaseline checks a dispatch in a git repo hashes every dirty
// path and records the baseline on the dispatched event.
func TestRunRecordsBaseline(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	git(t, dir, []string{"init", "-q"})
	git(t, dir, []string{"config", "core.autocrlf", "false"})
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write a.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("one line brief\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "init"})
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package y\n"), 0o644); err != nil {
		t.Fatalf("write dirty.go: %v", err)
	}
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	d := evs[1]
	if d.Kind != "dispatched" {
		t.Fatalf("evs[1] = %v, want the dispatched event", d)
	}
	want := shaOf(filepath.Join(dir, "dirty.go"), t)
	if len(d.Baseline) != 1 || d.Baseline["dirty.go"] != want {
		t.Errorf("dispatched baseline = %v, want {dirty.go: %q}", d.Baseline, want)
	}
}

// TestRunSimCapped checks a run cut off by the output-token cap (reason
// length) records no report event, keeps the last reply as a partial file
// instead, and a clean run still records its report (issue #131).
func TestRunSimCapped(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("capped.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RC != 0 || res.Reason != "length" {
		t.Errorf("rc/reason = %d/%q, want 0/length", res.RC, res.Reason)
	}
	if ExitCode(res) != 4 {
		t.Errorf("ExitCode() = %d, want 4", ExitCode(res))
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "report" {
			t.Errorf("events include a report event for a capped run: %v", e)
		}
	}
	if f := evs[len(evs)-1]; f.Kind != "finished" || f.Reason != "length" {
		t.Errorf("finished event = %v, want reason length", f)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "runs", "T1.r1.report.md")); !os.IsNotExist(err) {
		t.Errorf("report.md exists for a capped run, want none (err=%v)", err)
	}
	partialB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "runs", "T1.r1.partial.md"))
	if err != nil {
		t.Fatalf("read partial.md: %v", err)
	}
	if string(partialB) != "Ran out of output budget mid-write." {
		t.Errorf("partial.md = %q, want the last reply text", partialB)
	}
	plog := string(buf.Bytes())
	want := "T1 r1 partial reply kept at .flywheel/runs/T1.r1.partial.md (reason=length)"
	if !strings.Contains(plog, want) {
		t.Errorf("progress missing %q; got:\n%s", want, plog)
	}
	if f := evs[len(evs)-1]; f.PeakReasoning != 50 {
		t.Errorf("finished peak_reasoning = %d, want 50 (max of 50 then 5)", f.PeakReasoning)
	}
	hint := "T1 r1 hint: reason=length peak=50 reasoning tokens in one step; " +
		"split files into named parts, use smaller increments, or try another variant"
	if !strings.Contains(plog, hint) {
		t.Errorf("progress missing hint %q; got:\n%s", hint, plog)
	}
}

// TestRunCleanRecordsNoPeakOrHint checks a clean run (reason stop) with zero
// per-step reasoning tokens omits peak_reasoning from the finished event and
// prints no hint line (issue #84).
func TestRunCleanRecordsNoPeakOrHint(t *testing.T) {
	dir := setupTask(t)
	session := "ses_test_zeroreason_001"
	fixture := filepath.Join(t.TempDir(), "zeroreason.jsonl")
	content := fmt.Sprintf(
		`{"type":"step_start","sessionID":%q,"part":{"type":"step_start"}}`+"\n"+
			`{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":"stop","tokens":{"input":50,"output":20,"reasoning":0}},"cost":0.001}`+"\n",
		session, session)
	if err := os.WriteFile(fixture, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := WriteConfig(dir, simConfig(fixture)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "stop" {
		t.Errorf("reason = %q, want stop", res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" || f.PeakReasoning != 0 {
		t.Errorf("finished event = %v, want peak_reasoning 0", f)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if strings.Contains(string(b), "peak_reasoning") {
		t.Errorf("events.jsonl carries peak_reasoning for a zero-reasoning run: %q", b)
	}
	if strings.Contains(buf.String(), "hint:") {
		t.Errorf("progress carries a hint line for a clean run: %q", buf.String())
	}
}

func TestRunSimProviderError(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("provider-error.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "error" {
		t.Errorf("reason = %q, want error", res.Reason)
	}
	if ExitCode(res) != 4 {
		t.Errorf("ExitCode() = %d, want 4", ExitCode(res))
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if f := evs[len(evs)-1]; f.Kind != "finished" || f.Reason != "error" {
		t.Errorf("finished event = %v, want reason error", f)
	}
}

func TestRunStartTimeoutSilent(t *testing.T) {
	dir := setupTask(t)
	model := fixturePath("clean.jsonl", t)
	if err := WriteConfig(dir, simConfig(model)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Pre-seed the attempt's stderr file the way the opencode adapter would; a
	// sim run never touches it, and the silent note now reads it too.
	errPath := filepath.Join(dir, ".flywheel", "runs", "T1.r1.err")
	if err := os.MkdirAll(filepath.Dir(errPath), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	if err := os.WriteFile(errPath, []byte("\nauth failed: bad api key\n"), 0o644); err != nil {
		t.Fatalf("write stderr: %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{
		Task: "T1", StartTimeout: 50 * time.Millisecond, SimDelay: time.Second, Progress: &buf,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RC != -1 || res.Reason != "silent" {
		t.Errorf("rc/reason = %d/%q, want -1/silent", res.RC, res.Reason)
	}
	if ExitCode(res) != 3 {
		t.Errorf("ExitCode() = %d, want 3", ExitCode(res))
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" || f.Reason != "silent" || f.Model != model || f.Note != "auth failed: bad api key" {
		t.Errorf("finished event = %v, want reason silent, model %q, note from stderr", f, model)
	}
	runB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "runs", "T1.r1.jsonl"))
	if err != nil {
		t.Fatalf("read run file: %v", err)
	}
	if len(runB) != 0 {
		t.Errorf("run file = %d bytes, want empty", len(runB))
	}
	plog := string(buf.Bytes())
	want := "T1 r1 finished rc=-1 reason=silent model=" + model +
		" note=auth failed: bad api key err=.flywheel/runs/T1.r1.err"
	if !strings.Contains(plog, want) {
		t.Errorf("progress missing %q; got:\n%s", want, plog)
	}
}

// stallFixture writes a fixture of n step_finish lines (reason "stop"), one
// per line, for the mid-stream stall tests (issue #85). The sim adapter's
// SimLineDelay applies uniformly after every line, so a delay exceeding the
// stall timeout is already exceeded by the first inter-line gap: the run
// stops right after the first line, and steps holds whatever that first
// line completed.
func stallFixture(t *testing.T, n int) string {
	t.Helper()
	session := "ses_test_stall_001"
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":"stop"}}`+"\n", session)
	}
	path := filepath.Join(t.TempDir(), "stall.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestRunStalledMidStream checks a run gone silent mid-stream for longer than
// --stall-timeout is stopped and recorded as finished reason=stalled (exit 7,
// distinct from silent's 3 and a generic failure's 4), keeping the last
// completed step, and that the half-timeout notice reaches Stderr (issue #85).
func TestRunStalledMidStream(t *testing.T) {
	dir := setupTask(t)
	model := stallFixture(t, 2)
	if err := WriteConfig(dir, simConfig(model)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var out, errBuf bytes.Buffer
	res, err := Run(dir, RunOptions{
		Task: "T1", StallTimeout: time.Second, SimLineDelay: 1500 * time.Millisecond,
		Progress: &out, Stderr: &errBuf,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "stalled" {
		t.Errorf("reason = %q, want stalled", res.Reason)
	}
	if res.Steps != 1 {
		t.Errorf("steps = %d, want 1 (the one step completed before the stall)", res.Steps)
	}
	if ExitCode(res) != 7 {
		t.Errorf("ExitCode() = %d, want 7", ExitCode(res))
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" || f.Reason != "stalled" || f.Steps != 1 || f.Model != model {
		t.Errorf("finished event = %v, want reason stalled, steps 1, model %q", f, model)
	}
	notice := "T1 r1 long step: no output for 0s; the stall timeout fires at 1s"
	if !strings.Contains(errBuf.String(), notice) {
		t.Errorf("stderr missing the half-timeout notice %q; got:\n%s", notice, errBuf.String())
	}
	want := "T1 r1 finished rc=-1 reason=stalled model=" + model + " steps=1"
	if !strings.Contains(out.String(), want) {
		t.Errorf("progress missing %q; got:\n%s", want, out.String())
	}
}

// TestRunStalledUsesWorkerConfigWhenFlagAbsent checks the worker's configured
// stall_timeout applies when --stall-timeout is not given (issue #85).
func TestRunStalledUsesWorkerConfigWhenFlagAbsent(t *testing.T) {
	dir := setupTask(t)
	model := stallFixture(t, 1)
	cfg := Config{Version: 1, Workers: []Worker{{Name: "sim", Adapter: "sim", Model: model, StallTimeout: 1}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", SimLineDelay: 1500 * time.Millisecond, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "stalled" {
		t.Errorf("reason = %q, want stalled (from the worker's configured 1s stall_timeout)", res.Reason)
	}
}

// TestRunStallTimeoutOverrideCleanRun checks a generous --stall-timeout never
// interferes with a normal clean run (issue #85).
func TestRunStallTimeoutOverrideCleanRun(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", StallTimeout: 5 * time.Minute, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("rc/reason = %d/%q, want 0/stop", res.RC, res.Reason)
	}
	if ExitCode(res) != 0 {
		t.Errorf("ExitCode() = %d, want 0", ExitCode(res))
	}
}

func TestWorkerEnvAndPolicy(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	env := workerEnv(dir)
	found := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG=") {
			found = kv
		}
	}
	if found != "OPENCODE_CONFIG="+filepath.Join(dir, ".flywheel", "opencode-worker.json") {
		t.Errorf("workerEnv() = %q, want OPENCODE_CONFIG at the policy path", found)
	}
	sha, err := workerPolicySHA(dir)
	if err != nil {
		t.Fatalf("workerPolicySHA() error = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "opencode-worker.json"))
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	if string(b) != workerPermissionPolicy {
		t.Errorf("policy file = %q, want the embedded policy", b)
	}
	if !strings.Contains(string(b), `"external_directory": "deny"`) {
		t.Error(`policy file missing "external_directory": "deny" (issue #87)`)
	}
	sum := sha256.Sum256(b)
	if sha != hex.EncodeToString(sum[:]) {
		t.Errorf("workerPolicySHA() = %q, want %q", sha, hex.EncodeToString(sum[:]))
	}
}

// policyDoc is the shape both the embedded and canonical worker permission
// policies parse into.
type policyDoc struct {
	Permission struct {
		Bash              map[string]string `json:"bash"`
		ExternalDirectory string            `json:"external_directory"`
	} `json:"permission"`
}

// TestWorkerPolicyMatchesCanonicalFile checks the embedded policy's bash
// rules stay identical to the canonical reference file, while the embedded
// copy alone carries external_directory: deny and the canonical file stays
// byte-identical without it (issue #87).
func TestWorkerPolicyMatchesCanonicalFile(t *testing.T) {
	b, err := os.ReadFile("../../skills/flywheel/references/worker-permissions.json")
	if err != nil {
		t.Fatalf("read canonical policy: %v", err)
	}
	content := strings.ReplaceAll(string(b), "\r\n", "\n")

	var canon, embedded policyDoc
	if err := json.Unmarshal([]byte(content), &canon); err != nil {
		t.Fatalf("canonical policy is not valid JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(workerPermissionPolicy), &embedded); err != nil {
		t.Fatalf("embedded policy is not valid JSON: %v", err)
	}
	if canon.Permission.ExternalDirectory != "" {
		t.Errorf("canonical policy external_directory = %q, want it byte-identical (unchanged, no such key)", canon.Permission.ExternalDirectory)
	}
	if embedded.Permission.ExternalDirectory != "deny" {
		t.Errorf(`embedded policy external_directory = %q, want "deny"`, embedded.Permission.ExternalDirectory)
	}
	if !reflect.DeepEqual(canon.Permission.Bash, embedded.Permission.Bash) {
		t.Errorf("embedded bash rules = %v, want the canonical rules %v", embedded.Permission.Bash, canon.Permission.Bash)
	}
	if embedded.Permission.Bash["*"] != "allow" {
		t.Errorf(`permission.bash["*"] = %q, want allow`, embedded.Permission.Bash["*"])
	}
	if embedded.Permission.Bash["git stash*"] != "deny" {
		t.Errorf(`permission.bash["git stash*"] = %q, want deny`, embedded.Permission.Bash["git stash*"])
	}
}

func TestRunLongRunWithEarlyFirstLineFinishesStop(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{
		Task: "T1", StartTimeout: 200 * time.Millisecond, SimLineDelay: 300 * time.Millisecond, Progress: &buf,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("rc/reason = %d/%q, want 0/stop (a long run with an early first line is not silent)", res.RC, res.Reason)
	}
}

func TestRunCommandSessionOnlyOnResume(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	delta := filepath.Join(dir, ".flywheel", "briefs", "T1.delta.txt")
	if err := os.MkdirAll(filepath.Dir(delta), 0o755); err != nil {
		t.Fatalf("mkdir briefs: %v", err)
	}
	if err := os.WriteFile(delta, []byte("fix it\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	var got []RunRequest
	commandHook = func(req RunRequest) { got = append(got, req) }
	defer func() { commandHook = nil }()

	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() fresh error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Resume: true, Progress: &buf}); err != nil {
		t.Fatalf("Run() resume error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("commandHook captured %d requests, want 2", len(got))
	}
	if got[0].Session != "" {
		t.Errorf("fresh run session = %q, want empty (a fresh run must not pass --session)", got[0].Session)
	}
	if got[1].Session != "ses_test_clean_001" {
		t.Errorf("resume session = %q, want ses_test_clean_001", got[1].Session)
	}
	for i, req := range got {
		if req.PromptFile == "" || !filepath.IsAbs(req.PromptFile) {
			t.Errorf("request %d PromptFile = %q, want an absolute path", i, req.PromptFile)
		}
	}
	if got[0].PromptFile != filepath.Join(dir, "b.txt") {
		t.Errorf("fresh PromptFile = %q, want the brief %q", got[0].PromptFile, filepath.Join(dir, "b.txt"))
	}
	if got[1].PromptFile != delta {
		t.Errorf("resume PromptFile = %q, want the delta %q", got[1].PromptFile, delta)
	}
}

func TestRunResumeAfterInspectedUsesWorkerSession(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	delta := filepath.Join(dir, ".flywheel", "briefs", "T1.delta.txt")
	if err := os.MkdirAll(filepath.Dir(delta), 0o755); err != nil {
		t.Fatalf("mkdir briefs: %v", err)
	}
	if err := os.WriteFile(delta, []byte("fix it\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	var got []RunRequest
	commandHook = func(req RunRequest) { got = append(got, req) }
	defer func() { commandHook = nil }()

	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() fresh error = %v", err)
	}
	// An inspector reworks with its own session; the resume must still pick
	// up the worker session, never the inspector's.
	if err := AppendEvent(dir, Event{
		TS: "2026-09-12T01:00:00Z", Task: "T1", Kind: "inspected",
		Verdict: "rework", Session: "i1", Persona: "inspector",
	}); err != nil {
		t.Fatalf("AppendEvent() inspected error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Resume: true, Progress: &buf}); err != nil {
		t.Fatalf("Run() resume error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("commandHook captured %d requests, want 2", len(got))
	}
	if got[1].Session != "ses_test_clean_001" {
		t.Errorf("resume session = %q, want the worker session ses_test_clean_001, not the inspector session i1", got[1].Session)
	}
}

func TestRunMissingFixtureRecordsFinished(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(filepath.Join(dir, "nope.jsonl"))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	_, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err == nil {
		t.Fatal("Run() with a missing fixture: got nil error, want failure")
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" || f.Reason != "error" {
		t.Errorf("last event = %v, want finished reason error", f)
	}
	if f.Note == "" {
		t.Error("finished error event has no note")
	}
}

func TestWorkerEnvUsesAbsoluteConfigPath(t *testing.T) {
	env := workerEnv(".")
	found := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "OPENCODE_CONFIG=") {
			found = strings.TrimPrefix(kv, "OPENCODE_CONFIG=")
		}
	}
	if found == "" {
		t.Fatal("workerEnv() missing OPENCODE_CONFIG")
	}
	if !filepath.IsAbs(found) {
		t.Errorf("OPENCODE_CONFIG = %q, want an absolute path", found)
	}
}

// TestRunCopiesExternalBriefIntoWorktree checks a planned brief outside the
// worktree is copied into .flywheel/briefs/<task>.txt byte-for-byte and
// dispatched.Brief records the in-worktree path with the sha256 unchanged
// (issue #87); a brief already inside the workdir is attached as is, with no
// copy made.
func TestRunCopiesExternalBriefIntoWorktree(t *testing.T) {
	t.Run("external brief is copied", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Init(dir, false); err != nil {
			t.Fatalf("Init() error = %v", err)
		}
		ext := t.TempDir()
		briefPath := filepath.Join(ext, "external.txt")
		briefBytes := []byte("external brief\n")
		if err := os.WriteFile(briefPath, briefBytes, 0o644); err != nil {
			t.Fatalf("write external brief: %v", err)
		}
		if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: briefPath}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
		if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		var buf bytes.Buffer
		if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		evs, err := ReadEvents(dir)
		if err != nil {
			t.Fatalf("ReadEvents() error = %v", err)
		}
		wantBrief := ".flywheel/briefs/T1.txt"
		if d := evs[1]; d.Kind != "dispatched" || d.Brief != wantBrief || d.SHA256 != contentSHA(briefBytes) {
			t.Errorf("dispatched = %v, want brief %q and sha256 %q (unchanged by the copy)", d, wantBrief, contentSHA(briefBytes))
		}
		copied, err := os.ReadFile(filepath.Join(dir, ".flywheel", "briefs", "T1.txt"))
		if err != nil {
			t.Fatalf("read copied brief: %v", err)
		}
		if string(copied) != string(briefBytes) {
			t.Errorf("copied brief = %q, want %q byte-for-byte", copied, briefBytes)
		}
	})

	t.Run("internal brief is not copied", func(t *testing.T) {
		dir := setupTask(t)
		if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		var buf bytes.Buffer
		if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		evs, err := ReadEvents(dir)
		if err != nil {
			t.Fatalf("ReadEvents() error = %v", err)
		}
		if d := evs[1]; d.Brief != "b.txt" {
			t.Errorf("dispatched brief = %q, want the original b.txt (no copy made)", d.Brief)
		}
		if _, err := os.Stat(filepath.Join(dir, ".flywheel", "briefs", "T1.txt")); !os.IsNotExist(err) {
			t.Errorf(".flywheel/briefs/T1.txt should not exist for an internal brief")
		}
	})
}

// TestRunRecordsOtherWorktreesAtDispatch checks a dispatched event snapshots
// another linked worktree of the same repo (git worktree list --porcelain,
// issue #87): a freshly added worktree with no changes yet still appears,
// with an empty file map.
func TestRunRecordsOtherWorktreesAtDispatch(t *testing.T) {
	dir := setupTask(t)
	initRepo(t, dir)
	wt := filepath.Join(t.TempDir(), "other")
	git(t, dir, []string{"worktree", "add", "--detach", wt})
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	d := evs[1]
	if d.Kind != "dispatched" {
		t.Fatalf("evs[1] kind = %q, want dispatched", d.Kind)
	}
	if len(d.Worktrees) != 1 {
		t.Fatalf("worktrees = %v, want exactly the one other worktree", d.Worktrees)
	}
	for p, files := range d.Worktrees {
		if !samePath(p, wt) {
			t.Errorf("worktree path = %q, want %q", p, wt)
		}
		if len(files) != 0 {
			t.Errorf("worktree files = %v, want none (freshly checked out clean)", files)
		}
	}
}

// TestRunStartFailedOnEmptyFixture checks a worker that exits before any
// completed step records finished reason start-failed.
func TestRunStartFailedOnEmptyFixture(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("empty.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "start-failed" {
		t.Errorf("reason = %q, want start-failed", res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" || f.Reason != "start-failed" {
		t.Errorf("finished event = %v, want reason start-failed", f)
	}
	if f.Note != "" {
		t.Errorf("finished note = %q, want empty (no stderr)", f.Note)
	}
}

// TestRunStartFailedOnFailedFixture checks a worker that emits output but
// exits before any completed step also records start-failed.
func TestRunStartFailedOnFailedFixture(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("start-failed.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "start-failed" || res.Steps != 0 {
		t.Errorf("reason/steps = %q/%d, want start-failed/0", res.Reason, res.Steps)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" || f.Reason != "start-failed" {
		t.Errorf("finished event = %v, want reason start-failed", f)
	}
}

// TestRunStartFailedTruncatesStderrNote checks the finished note carries the
// first nonempty stderr line, trimmed to at most 200 characters.
func TestRunStartFailedTruncatesStderrNote(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("empty.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Pre-seed the attempt's stderr file the way the opencode adapter would;
	// a sim run never touches it, and the start-failed note reads it.
	errPath := filepath.Join(dir, ".flywheel", "runs", "T1.r1.err")
	if err := os.MkdirAll(filepath.Dir(errPath), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	seed := "   \n" + strings.Repeat("y", 220) + "\nsecond stderr line\n"
	if err := os.WriteFile(errPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("write stderr: %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	want := strings.Repeat("y", 200)
	if f.Kind != "finished" || f.Reason != "start-failed" || f.Note != want {
		t.Errorf("finished = %v, want start-failed with note %q", f, want)
	}
}

// TestRunDeltaWithoutResume checks a given --delta is the prompt even
// without --resume: the dispatched event hashes the delta (not the brief),
// the attempt is c1, the brief records the delta path, and no session flag
// reaches the adapter (issue #106).
func TestRunDeltaWithoutResume(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	deltaB := []byte("fix the registry\n")
	delta := filepath.Join(dir, "d.txt")
	if err := os.WriteFile(delta, deltaB, 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	var got []RunRequest
	commandHook = func(req RunRequest) { got = append(got, req) }
	defer func() { commandHook = nil }()

	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Attempt != "c1" {
		t.Errorf("attempt = %q, want c1", res.Attempt)
	}
	if len(got) != 1 {
		t.Fatalf("commandHook captured %d requests, want 1", len(got))
	}
	if got[0].Session != "" {
		t.Errorf("session = %q, want empty (a delta without --resume starts a fresh session)", got[0].Session)
	}
	if got[0].PromptFile != delta {
		t.Errorf("PromptFile = %q, want the delta %q", got[0].PromptFile, delta)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Kind == "dispatched" {
			d = e
		}
	}
	deltaSum := contentSHA(deltaB)
	if d.SHA256 != deltaSum {
		t.Errorf("dispatched sha256 = %q, want the delta's %q (not the brief's)", d.SHA256, deltaSum)
	}
	if d.Attempt != "c1" {
		t.Errorf("dispatched attempt = %q, want c1", d.Attempt)
	}
	if d.Brief != "d.txt" {
		t.Errorf("dispatched brief = %q, want the delta path d.txt", d.Brief)
	}
}

// TestRunResumeWithDelta checks --resume --delta D keeps resuming the
// worker's session while sending D.
func TestRunResumeWithDelta(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	delta := filepath.Join(dir, "d2.txt")
	if err := os.WriteFile(delta, []byte("resume with delta\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	var got []RunRequest
	commandHook = func(req RunRequest) { got = append(got, req) }
	defer func() { commandHook = nil }()

	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() fresh error = %v", err)
	}
	res, err := Run(dir, RunOptions{Task: "T1", Resume: true, DeltaPath: delta, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() resume error = %v", err)
	}
	if res.Attempt != "c1" {
		t.Errorf("attempt = %q, want c1", res.Attempt)
	}
	if len(got) != 2 {
		t.Fatalf("commandHook captured %d requests, want 2", len(got))
	}
	if got[1].Session != "ses_test_clean_001" {
		t.Errorf("resume session = %q, want ses_test_clean_001", got[1].Session)
	}
	if got[1].PromptFile != delta {
		t.Errorf("resume PromptFile = %q, want the delta %q", got[1].PromptFile, delta)
	}
}

// leaseFor returns the lease of one attempt from .flywheel/leases.
func leaseFor(t *testing.T, dir, task, attempt string) (Lease, bool) {
	t.Helper()
	leases, err := ReadLeases(dir)
	if err != nil {
		t.Fatalf("ReadLeases() error = %v", err)
	}
	for _, l := range leases {
		if l.Task == task && l.Attempt == attempt {
			return l, true
		}
	}
	return Lease{}, false
}

// waitFor polls cond until it reports true or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestRunWritesAndRenewsLease checks a run writes its lease before the
// worker's first line, renews it at least once when the renew interval is
// small, and removes it after finished.
func TestRunWritesAndRenewsLease(t *testing.T) {
	dir := setupTask(t)
	cfg := simConfig(fixturePath("clean.jsonl", t))
	cfg.Lease = &LeaseConfig{RenewInterval: "20ms", TTL: "2s"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	var buf bytes.Buffer
	go func() {
		res, err := Run(dir, RunOptions{Task: "T1", SimDelay: 800 * time.Millisecond, Progress: &buf})
		done <- outcome{res, err}
	}()

	waitFor(t, 2*time.Second, "the lease to be written before the first line", func() bool {
		_, ok := leaseFor(t, dir, "T1", "r1")
		return ok
	})
	l, ok := leaseFor(t, dir, "T1", "r1")
	if !ok {
		t.Fatal("lease missing right after it appeared")
	}
	if l.Task != "T1" || l.Attempt != "r1" || l.PID <= 0 || l.Host == "" ||
		l.RunFile != ".flywheel/runs/T1.r1.jsonl" {
		t.Errorf("lease = %+v, want task T1, attempt r1, a pid, a host and the run file", l)
	}
	if _, err := time.Parse(time.RFC3339, l.StartedAt); err != nil {
		t.Errorf("started_at %q is not RFC3339: %v", l.StartedAt, err)
	}
	firstExpires := l.ExpiresAt
	waitFor(t, 2*time.Second, "a renewal (expires_at moves forward)", func() bool {
		l, ok := leaseFor(t, dir, "T1", "r1")
		return ok && l.ExpiresAt > firstExpires
	})

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("Run() error = %v", o.err)
		}
		if o.res.Attempt != "r1" {
			t.Errorf("attempt = %q, want r1", o.res.Attempt)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not finish in time")
	}
	if leases, err := ReadLeases(dir); err != nil || len(leases) != 0 {
		t.Errorf("leases after finished = %v, %v; want none", leases, err)
	}
}

// TestRunRemovesLeaseOnErrorPath checks the lease is removed after the
// finished event on the error path.
func TestRunRemovesLeaseOnErrorPath(t *testing.T) {
	dir := setupTask(t)
	cfg := simConfig(filepath.Join(dir, "nope.jsonl"))
	cfg.Lease = &LeaseConfig{RenewInterval: "20ms", TTL: "2s"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err == nil {
		t.Fatal("Run() with a missing fixture: got nil error, want failure")
	}
	leases, err := ReadLeases(dir)
	if err != nil {
		t.Fatalf("ReadLeases() error = %v", err)
	}
	if len(leases) != 0 {
		t.Errorf("leases after the error path = %v, want none", leases)
	}
}

// TestLeaseRemainsWhenRunKilled simulates a run that never records finished:
// a run still in progress, with the clock frozen at dispatch time, must keep
// its lease file, whose expires_at is in the past relative to a later now.
func TestLeaseRemainsWhenRunKilled(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	t0 := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	oldNow := now
	now = func() time.Time { return t0 }
	defer func() { now = oldNow }()

	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := Run(dir, RunOptions{Task: "T1", SimDelay: time.Second})
		done <- outcome{res, err}
	}()

	waitFor(t, 2*time.Second, "the lease file", func() bool {
		_, ok := leaseFor(t, dir, "T1", "r1")
		return ok
	})
	l, ok := leaseFor(t, dir, "T1", "r1")
	if !ok {
		t.Fatal("lease missing right after it appeared")
	}
	want := t0.Format(time.RFC3339)
	if l.StartedAt != want || l.ExpiresAt != t0.Add(45*time.Second).Format(time.RFC3339) {
		t.Errorf("lease timestamps = %s/%s, want %s/%s (default ttl 45s)", l.StartedAt, l.ExpiresAt, want, t0.Add(45*time.Second).Format(time.RFC3339))
	}
	// The run is still sleeping in SimDelay; it never recorded finished, so
	// the lease file must remain in place.
	if _, ok := leaseFor(t, dir, "T1", "r1"); !ok {
		t.Error("lease file removed while the run never recorded finished")
	}
	later := t0.Add(45*time.Second + time.Second)
	if LeaseLive(l, later) {
		t.Error("LeaseLive() = true after the ttl relative to the injected later now, want false")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not finish in time")
	}
}

// noPlanFixture writes a fixture of one step_start followed by n step_finish
// events (all reason "stop"), with an optional PLAN text line inserted right
// after the step_start, and returns its absolute path (issue #65).
func noPlanFixture(t *testing.T, n int, withPlan bool) string {
	t.Helper()
	session := "ses_test_noplan_001"
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"step_start","sessionID":%q,"part":{"type":"step_start"}}`+"\n", session)
	if withPlan {
		fmt.Fprintf(&b, `{"type":"text","sessionID":%q,"part":{"type":"text","text":"PLAN files-to-read: a.go"}}`+"\n", session)
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":"stop"}}`+"\n", session)
	}
	path := filepath.Join(t.TempDir(), "noplan.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestRunNoPlanFlaggedAt20Steps checks a 22-step run with no PLAN text
// records exactly one no-plan event, carrying the task and attempt, and
// finishes normally (issue #65).
func TestRunNoPlanFlaggedAt20Steps(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(noPlanFixture(t, 22, false))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("rc/reason = %d/%q, want 0/stop (no-plan must not change the outcome)", res.RC, res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count := 0
	var np Event
	for _, e := range evs {
		if e.Kind == "no-plan" {
			count++
			np = e
		}
	}
	if count != 1 {
		t.Fatalf("no-plan events = %d, want exactly 1", count)
	}
	if np.Task != "T1" || np.Attempt != "r1" {
		t.Errorf("no-plan event = %v, want task T1 attempt r1", np)
	}
}

// TestRunPlanRecordedNoNoPlan checks a 22-step run with a PLAN line records
// no no-plan event (issue #65).
func TestRunPlanRecordedNoNoPlan(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(noPlanFixture(t, 22, true))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "no-plan" {
			t.Errorf("events include a no-plan event when a PLAN was recorded: %v", e)
		}
	}
}

// TestRunShortRunNoNoPlan checks a run that never reaches step 20 records no
// no-plan event even without a PLAN line (issue #65).
func TestRunShortRunNoNoPlan(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(noPlanFixture(t, 5, false))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "no-plan" {
			t.Errorf("events include a no-plan event for a short (5-step) run: %v", e)
		}
	}
}

// offCourseFixture writes a fixture of one step_start, one tool_use event per
// (tool, path) call, and a final step_finish reason stop, and returns its
// absolute path. grep and glob calls carry their path under state.input.path;
// every other tool carries it under state.input.filePath (issue #72).
func offCourseFixture(t *testing.T, calls [][2]string) string {
	t.Helper()
	session := "ses_test_offcourse_001"
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"step_start","sessionID":%q,"part":{"type":"step_start"}}`+"\n", session)
	for _, c := range calls {
		tool, path := c[0], c[1]
		key := "filePath"
		if tool == "grep" || tool == "glob" {
			key = "path"
		}
		fmt.Fprintf(&b, `{"type":"tool_use","sessionID":%q,"part":{"type":"tool_use","tool":%q,"state":{"input":{%q:%q}}}}`+"\n", session, tool, key, path)
	}
	fmt.Fprintf(&b, `{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":"stop"}}`+"\n", session)
	path := filepath.Join(t.TempDir(), "offcourse.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// shortOutsideDir returns a short absolute path outside dir's tree, on the
// same volume, without touching the filesystem: long temp-dir names (Windows
// especially) would otherwise blow past clipNote's 200-character cap before
// a test can see all 5 distinct paths in the off-course note.
func shortOutsideDir(dir string) string {
	return filepath.Join(filepath.VolumeName(dir)+string(filepath.Separator), "fw72-outside")
}

// TestRunOffCourseFlaggedAt5DistinctPaths checks 5 distinct reads of paths
// outside the worktree record exactly one off-course event naming the paths,
// without changing the run's outcome (issue #72).
func TestRunOffCourseFlaggedAt5DistinctPaths(t *testing.T) {
	dir := setupTask(t)
	outside := shortOutsideDir(dir)
	calls := [][2]string{
		{"read", filepath.Join(outside, "o1.go")},
		{"read", filepath.Join(outside, "o2.go")},
		{"read", filepath.Join(outside, "o3.go")},
		{"read", filepath.Join(outside, "o4.go")},
		{"read", filepath.Join(outside, "o5.go")},
	}
	if err := WriteConfig(dir, simConfig(offCourseFixture(t, calls))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("rc/reason = %d/%q, want 0/stop (off-course must not change the outcome)", res.RC, res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count := 0
	var oc Event
	for _, e := range evs {
		if e.Kind == "off-course" {
			count++
			oc = e
		}
	}
	if count != 1 {
		t.Fatalf("off-course events = %d, want exactly 1", count)
	}
	if oc.Task != "T1" || oc.Attempt != "r1" {
		t.Errorf("off-course event = %v, want task T1 attempt r1", oc)
	}
	for _, c := range calls {
		if !strings.Contains(oc.Note, c[1]) {
			t.Errorf("off-course note = %q, want it to list %q", oc.Note, c[1])
		}
	}
}

// TestRunFourDistinctOutsideNoOffCourse checks 4 distinct outside paths never
// trigger the off-course event (issue #72).
func TestRunFourDistinctOutsideNoOffCourse(t *testing.T) {
	dir := setupTask(t)
	outside := t.TempDir()
	calls := [][2]string{
		{"read", filepath.Join(outside, "o1.go")},
		{"read", filepath.Join(outside, "o2.go")},
		{"read", filepath.Join(outside, "o3.go")},
		{"read", filepath.Join(outside, "o4.go")},
	}
	if err := WriteConfig(dir, simConfig(offCourseFixture(t, calls))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "off-course" {
			t.Errorf("events include an off-course event at 4 distinct outside paths: %v", e)
		}
	}
}

// TestRunInsideReadsNeverCount checks reads of paths inside the worktree
// never count toward off-course, however many distinct ones there are
// (issue #72).
func TestRunInsideReadsNeverCount(t *testing.T) {
	dir := setupTask(t)
	calls := [][2]string{
		{"read", filepath.Join(dir, "a.go")},
		{"read", filepath.Join(dir, "b.go")},
		{"read", filepath.Join(dir, "c.go")},
		{"read", filepath.Join(dir, "d.go")},
		{"read", filepath.Join(dir, "e.go")},
		{"read", filepath.Join(dir, "f.go")},
	}
	if err := WriteConfig(dir, simConfig(offCourseFixture(t, calls))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "off-course" {
			t.Errorf("events include an off-course event for in-worktree reads: %v", e)
		}
	}
}

// TestRunGrepGlobCountThroughPathFallback checks grep and glob tool calls,
// whose path arrives via the state.input.path fallback (not filePath), still
// count toward off-course (issue #72).
func TestRunGrepGlobCountThroughPathFallback(t *testing.T) {
	dir := setupTask(t)
	outside := shortOutsideDir(dir)
	calls := [][2]string{
		{"grep", filepath.Join(outside, "o1.go")},
		{"glob", filepath.Join(outside, "o2.go")},
		{"grep", filepath.Join(outside, "o3.go")},
		{"glob", filepath.Join(outside, "o4.go")},
		{"grep", filepath.Join(outside, "o5.go")},
	}
	if err := WriteConfig(dir, simConfig(offCourseFixture(t, calls))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count := 0
	for _, e := range evs {
		if e.Kind == "off-course" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("off-course events via grep/glob = %d, want exactly 1", count)
	}
}
