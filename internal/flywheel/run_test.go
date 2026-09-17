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
	"sync"
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

// writeDelta writes the default resume delta file for task under dir.
func writeDelta(t *testing.T, dir, task string) {
	t.Helper()
	briefs := filepath.Join(dir, ".flywheel", "briefs")
	if err := os.MkdirAll(briefs, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", briefs, err)
	}
	if err := os.WriteFile(filepath.Join(briefs, task+".delta.txt"), []byte("delta\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
}

// TestRunDispatchedCarriesPromptHeader checks the dispatched event records
// the parsed header of the exact prompt it sent: the planned brief on a
// fresh attempt, the delta on a correction (issue #259), so a later reader
// measures the attempt against the ledger rather than the mutable file.
func TestRunDispatchedCarriesPromptHeader(t *testing.T) {
	dir := setupTask(t)
	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("fresh Run() error = %v", err)
	}
	briefHeader, err := ParseBriefHeader(filepath.Join(dir, "b.txt"))
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d1 Event
	for _, e := range evs {
		if e.Kind == "dispatched" {
			d1 = e
		}
	}
	if d1.Attempt != "r1" || d1.Header == nil {
		t.Fatalf("dispatched r1 event = %+v, want a recorded header", d1)
	}
	if !reflect.DeepEqual(*d1.Header, briefHeader) {
		t.Errorf("dispatched r1 header = %+v, want the brief's parsed header %+v", *d1.Header, briefHeader)
	}
	delta := filepath.Join(dir, "d.txt")
	if err := os.WriteFile(delta, []byte("owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK: delta\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta}); err != nil {
		t.Fatalf("delta Run() error = %v", err)
	}
	deltaHeader, err := ParseBriefHeader(delta)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	evs, err = ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d2 Event
	for _, e := range evs {
		if e.Kind == "dispatched" {
			d2 = e
		}
	}
	if d2.Attempt != "c1" || d2.Header == nil {
		t.Fatalf("dispatched c1 event = %+v, want a recorded header", d2)
	}
	if !reflect.DeepEqual(*d2.Header, deltaHeader) {
		t.Errorf("dispatched c1 header = %+v, want the delta's parsed header %+v", *d2.Header, deltaHeader)
	}
}

// TestRunResumeModelGateRefusesUnapproved checks L-03: a resume naming a
// model that differs from the worker's model and is not an approved
// fallback is refused before any event is read (issue #23).
func TestRunResumeModelGateRefusesUnapproved(t *testing.T) {
	dir := setupTask(t)
	other := fixturePath("longcost.jsonl", t)
	cfg := simConfig(fixturePath("clean.jsonl", t))
	cfg.Workers[0].Fallbacks = []Fallback{{Model: other}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T1", Resume: true, Model: other})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "L-03" {
		t.Fatalf("Run() error = %v, want a RuleRefusal L-03", err)
	}
}

// TestRunResumeModelGateApprovedResumesNormally checks a resume onto an
// approved fallback dispatches normally.
func TestRunResumeModelGateApprovedResumesNormally(t *testing.T) {
	dir := setupTask(t)
	approved := fixturePath("longcost.jsonl", t)
	cfg := simConfig(fixturePath("clean.jsonl", t))
	cfg.Workers[0].Fallbacks = []Fallback{{Model: approved, Approved: true}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("fresh Run() error = %v", err)
	}
	writeDelta(t, dir, "T1")
	res, err := Run(dir, RunOptions{Task: "T1", Resume: true, Model: approved})
	if err != nil {
		t.Fatalf("resume Run() error = %v", err)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("resume result = %+v, want rc 0 reason stop", res)
	}
}

// TestRunResumeModelGateForceModelBypasses checks --force-model dispatches a
// resume onto a model that is not an approved fallback (or a fallback at
// all).
func TestRunResumeModelGateForceModelBypasses(t *testing.T) {
	dir := setupTask(t)
	other := fixturePath("longcost.jsonl", t)
	cfg := simConfig(fixturePath("clean.jsonl", t))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("fresh Run() error = %v", err)
	}
	writeDelta(t, dir, "T1")
	res, err := Run(dir, RunOptions{Task: "T1", Resume: true, Model: other, ForceModel: true})
	if err != nil {
		t.Fatalf("resume Run() with --force-model error = %v", err)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("resume result = %+v, want rc 0 reason stop", res)
	}
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
	var f Event
	for _, e := range evs {
		if e.Kind == "finished" {
			f = e
		}
	}
	if f.Kind != "finished" || f.Reason != "length" {
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
	if f.PeakReasoning != 50 {
		t.Errorf("finished peak_reasoning = %d, want 50 (max of 50 then 5)", f.PeakReasoning)
	}
	hint := "T1 r1 hint: reason=length peak=50 reasoning tokens in one step; " +
		"split files into named parts, use smaller increments, or try another variant"
	if !strings.Contains(plog, hint) {
		t.Errorf("progress missing hint %q; got:\n%s", hint, plog)
	}
}

// TestRunCappedSignalAfterFinished checks a run finishing reason length
// records a capped signal AFTER its finished event, carrying the attempt and
// the run file in Path (issue #37).
func TestRunCappedSignalAfterFinished(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("capped.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "length" {
		t.Fatalf("reason = %q, want length", res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var sig Event
	sigIdx := -1
	for i, e := range evs {
		if e.Kind == "signal" {
			sig = e
			sigIdx = i
		}
	}
	if sig.Kind != "signal" || sig.Signal != "capped" {
		t.Fatalf("signal event = %v, want signal capped", sig)
	}
	if sig.Attempt != "r1" || sig.Path != ".flywheel/runs/T1.r1.jsonl" {
		t.Errorf("capped signal = %v, want attempt r1 and the run file path", sig)
	}
	if sigIdx < 0 || evs[sigIdx-1].Kind != "finished" {
		t.Errorf("capped signal at %d is not right after the finished event; events = %v", sigIdx, evs)
	}
}

// TestRunProviderErrorSignalAfterFinished checks a run finishing reason error
// records a provider-error signal after its finished event (issue #37).
func TestRunProviderErrorSignalAfterFinished(t *testing.T) {
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
		t.Fatalf("reason = %q, want error", res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var sig Event
	sigIdx := -1
	for i, e := range evs {
		if e.Kind == "signal" {
			sig = e
			sigIdx = i
		}
	}
	if sig.Kind != "signal" || sig.Signal != "provider-error" {
		t.Fatalf("signal event = %v, want signal provider-error", sig)
	}
	if sig.Attempt != "r1" || sig.Path != ".flywheel/runs/T1.r1.jsonl" {
		t.Errorf("provider-error signal = %v, want attempt r1 and the run file path", sig)
	}
	if sigIdx < 0 || evs[sigIdx-1].Kind != "finished" {
		t.Errorf("provider-error signal at %d is not right after the finished event; events = %v", sigIdx, evs)
	}
}

// TestRunCleanStopRecordsNoSignal checks a clean stop run records no signal
// event at all (issue #37).
func TestRunCleanStopRecordsNoSignal(t *testing.T) {
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
	for _, e := range evs {
		if e.Kind == "signal" {
			t.Errorf("events include a signal for a clean stop run: %v", e)
		}
	}
}

// TestRunNoDuplicateSignalForOneAttempt checks a run that crosses the plan
// threshold AND finishes length records exactly one signal per condition,
// never twice for the same condition and attempt (issue #37).
func TestRunNoDuplicateSignalForOneAttempt(t *testing.T) {
	dir := setupTask(t)
	session := "ses_test_both_001"
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"step_start","sessionID":%q,"part":{"type":"step_start"}}`+"\n", session)
	for i := 0; i < 22; i++ {
		fmt.Fprintf(&b, `{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":"length"}}`+"\n", session)
	}
	model := filepath.Join(t.TempDir(), "both.jsonl")
	if err := os.WriteFile(model, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := WriteConfig(dir, simConfig(model)); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	res, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "length" {
		t.Fatalf("reason = %q, want length", res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	counts := map[string]int{}
	noPlan := 0
	for _, e := range evs {
		if e.Kind == "signal" {
			counts[e.Signal]++
		}
		if e.Kind == "no-plan" {
			noPlan++
		}
	}
	if noPlan != 1 {
		t.Errorf("no-plan events = %d, want exactly 1", noPlan)
	}
	if counts["no-plan"] != 1 {
		t.Errorf("no-plan signals = %d, want exactly 1", counts["no-plan"])
	}
	if counts["capped"] != 1 {
		t.Errorf("capped signals = %d, want exactly 1", counts["capped"])
	}
	if len(counts) != 2 {
		t.Errorf("signal conditions = %v, want exactly no-plan and capped", counts)
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
	var f Event
	for _, e := range evs {
		if e.Kind == "finished" {
			f = e
		}
	}
	if f.Kind != "finished" || f.Reason != "error" {
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
	var f Event
	for _, e := range evs {
		if e.Kind == "finished" {
			f = e
		}
	}
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
	var f Event
	for _, e := range evs {
		if e.Kind == "finished" {
			f = e
		}
	}
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

// TestRunWritesWorkerRules checks a run writes .flywheel/worker-rules.md
// (matching workerRules) and points the policy's "instructions" at it
// (issue #31).
func TestRunWritesWorkerRules(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	policyB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "opencode-worker.json"))
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	var doc struct {
		Instructions []string `json:"instructions"`
	}
	if err := json.Unmarshal(policyB, &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if len(doc.Instructions) != 1 || doc.Instructions[0] != "worker-rules.md" {
		t.Errorf("policy instructions = %v, want [worker-rules.md]", doc.Instructions)
	}
	rulesB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "worker-rules.md"))
	if err != nil {
		t.Fatalf("read worker-rules.md: %v", err)
	}
	if normLF(rulesB) != workerRules {
		t.Errorf("worker-rules.md = %q, want workerRules %q", normLF(rulesB), workerRules)
	}
}

// TestRunLeavesPreExistingWorkerFilesUntouched checks a run never overwrites
// an opencode-worker.json or worker-rules.md that already exists (issue #31).
func TestRunLeavesPreExistingWorkerFilesUntouched(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	policyPath := filepath.Join(dir, ".flywheel", "opencode-worker.json")
	rulesPath := filepath.Join(dir, ".flywheel", "worker-rules.md")
	if err := os.WriteFile(policyPath, []byte("KEEPME-POLICY"), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if err := os.WriteFile(rulesPath, []byte("KEEPME-RULES"), 0o644); err != nil {
		t.Fatalf("write rules: %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	policyB, err := os.ReadFile(policyPath)
	if err != nil || string(policyB) != "KEEPME-POLICY" {
		t.Errorf("policy = %q, %v, want KEEPME-POLICY untouched", policyB, err)
	}
	rulesB, err := os.ReadFile(rulesPath)
	if err != nil || string(rulesB) != "KEEPME-RULES" {
		t.Errorf("worker-rules.md = %q, %v, want KEEPME-RULES untouched", rulesB, err)
	}
}

// TestWorkerRulesMatchesReferenceFile checks workerRules stays identical to
// skills/flywheel/references/worker-rules.md (issue #31).
func TestWorkerRulesMatchesReferenceFile(t *testing.T) {
	b, err := os.ReadFile("../../skills/flywheel/references/worker-rules.md")
	if err != nil {
		t.Fatalf("read reference worker-rules.md: %v", err)
	}
	if normLF(b) != workerRules {
		t.Errorf("reference worker-rules.md = %q, want workerRules %q", normLF(b), workerRules)
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

// lastDispatched returns the last dispatched event for a task, or zero.
func lastDispatched(t *testing.T, dir, task string) Event {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == task && e.Kind == "dispatched" {
			d = e
		}
	}
	return d
}

// TestRunBriefDriftWarns checks a brief edited after its dispatch is caught
// at the next dispatch: the drift line is printed, the run still dispatches,
// and the newly recorded dispatched hash is the brief's CURRENT content
// (issue #135).
func TestRunBriefDriftWarns(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() first error = %v", err)
	}
	if d := lastDispatched(t, dir, "T1"); d.Attempt != "r1" {
		t.Fatalf("first dispatched attempt = %q, want r1", d.Attempt)
	}

	edited := []byte("one line brief\nedited after dispatch\n")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), edited, 0o644); err != nil {
		t.Fatalf("write edited brief: %v", err)
	}
	buf.Reset()
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() second error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "brief-drift") {
		t.Errorf("second run output = %q, want a brief-drift line", out)
	}
	d := lastDispatched(t, dir, "T1")
	if d.Attempt != "r2" {
		t.Errorf("last dispatched attempt = %q, want r2", d.Attempt)
	}
	if d.SHA256 != contentSHA(edited) {
		t.Errorf("last dispatched sha256 = %q, want the edited brief's %q", d.SHA256, contentSHA(edited))
	}
}

// TestRunBriefDriftStrictRefuses checks --strict-brief turns drift into a T1
// RuleRefusal with no event appended: a strict refusal never half-dispatches
// (issue #135).
func TestRunBriefDriftStrictRefuses(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() first error = %v", err)
	}
	before, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("one line brief\nedited after dispatch\n"), 0o644); err != nil {
		t.Fatalf("write edited brief: %v", err)
	}
	_, err = Run(dir, RunOptions{Task: "T1", StrictBrief: true, Progress: &buf})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "T1" {
		t.Fatalf("Run() error = %v, want a T1 RuleRefusal", err)
	}
	if !strings.Contains(r.Fix, "brief on disk differs") || !strings.Contains(r.Fix, "flywheel log --task T1 --kind planned --brief b.txt") {
		t.Errorf("RuleRefusal fix = %q, want it naming the re-record fix", r.Fix)
	}
	after, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("events grew from %d to %d after a strict refusal, want none appended", len(before), len(after))
	}
}

// TestRunBriefDriftQuietAfterReplan checks the lead-correction path: after a
// drift, recording a fresh planned event with the edited brief and
// dispatching again warns nothing (issue #135).
func TestRunBriefDriftQuietAfterReplan(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() first error = %v", err)
	}

	edited := []byte("one line brief\nedited after dispatch\n")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), edited, 0o644); err != nil {
		t.Fatalf("write edited brief: %v", err)
	}
	if err := RecordPlanned(dir, "T1", "b.txt"); err != nil {
		t.Fatalf("RecordPlanned() error = %v", err)
	}
	buf.Reset()
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() after re-plan error = %v", err)
	}
	if out := buf.String(); strings.Contains(out, "brief-drift") {
		t.Errorf("run after re-plan output = %q, want no brief-drift line", out)
	}
	d := lastDispatched(t, dir, "T1")
	if d.SHA256 != contentSHA(edited) {
		t.Errorf("dispatched sha256 = %q, want the re-planned brief's %q", d.SHA256, contentSHA(edited))
	}
}

// TestRunFirstDispatchNeverWarnsBriefDrift checks a task with no previous
// dispatch cannot drift and warns nothing (issue #135).
func TestRunFirstDispatchNeverWarnsBriefDrift(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out := buf.String(); strings.Contains(out, "brief-drift") {
		t.Errorf("first run output = %q, want no brief-drift line", out)
	}
}

// TestRunUnchangedBriefNeverWarnsBriefDrift checks an unchanged brief never
// warns, no matter how often it is dispatched (issue #135).
func TestRunUnchangedBriefNeverWarnsBriefDrift(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	for i := 0; i < 2; i++ {
		buf.Reset()
		if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
			t.Fatalf("Run() #%d error = %v", i+1, err)
		}
		if out := buf.String(); strings.Contains(out, "brief-drift") {
			t.Errorf("run #%d output = %q, want no brief-drift line", i+1, out)
		}
	}
}

// TestRunBriefDriftWarnsOnResume mirrors the CLI acceptance path: a brief
// edited after a fresh dispatch, then resumed with a delta, warns and still
// dispatches the correction (issue #135).
func TestRunBriefDriftWarnsOnResume(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() first error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("one line brief\nedited after dispatch\n"), 0o644); err != nil {
		t.Fatalf("write edited brief: %v", err)
	}
	writeDelta(t, dir, "T1")
	buf.Reset()
	res, err := Run(dir, RunOptions{Task: "T1", Resume: true, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() resume error = %v", err)
	}
	if res.Attempt != "c1" {
		t.Errorf("resume attempt = %q, want c1", res.Attempt)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("resume result = %+v, want rc 0 reason stop", res)
	}
	if out := buf.String(); !strings.Contains(out, "brief-drift") {
		t.Errorf("resume output = %q, want a brief-drift line", out)
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

// TestRunSimCleanStepsPinned pins the step count for the existing clean.jsonl
// fixture, proving the opencode/sim path did not shift when step counting moved
// from obs.Kind == "step" to obs.EndsTurn (issue #187).
func TestRunSimCleanStepsPinned(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	res, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Steps != 2 {
		t.Errorf("steps = %d, want 2 (clean.jsonl step count must not shift)", res.Steps)
	}
}

// TestRunNoPlanAtExactly20Steps checks a run whose stream ends turns exactly
// 20 times with no PLAN text records one no-plan event (issue #187).
func TestRunNoPlanAtExactly20Steps(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(noPlanFixture(t, 20, false))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.RC != 0 || res.Reason != "stop" {
		t.Errorf("rc/reason = %d/%q, want 0/stop", res.RC, res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count := 0
	for _, e := range evs {
		if e.Kind == "no-plan" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("no-plan events = %d, want exactly 1", count)
	}
}

// TestRunNoPlanSignalRecordsBoth checks a run crossing the plan threshold
// records BOTH its no-plan event and a signal event whose Signal is no-plan,
// carrying the attempt, the session and the run file in Path (issue #37).
func TestRunNoPlanSignalRecordsBoth(t *testing.T) {
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
		t.Errorf("rc/reason = %d/%q, want 0/stop (a signal must not change the outcome)", res.RC, res.Reason)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	noPlan := 0
	signals := 0
	var sig Event
	for _, e := range evs {
		if e.Kind == "no-plan" {
			noPlan++
		}
		if e.Kind == "signal" {
			signals++
			sig = e
		}
	}
	if noPlan != 1 {
		t.Fatalf("no-plan events = %d, want exactly 1", noPlan)
	}
	if sig.Kind != "signal" || sig.Signal != "no-plan" {
		t.Fatalf("signal event = %v, want signal no-plan", sig)
	}
	if sig.Task != "T1" || sig.Attempt != "r1" {
		t.Errorf("signal = %v, want task T1 attempt r1", sig)
	}
	if sig.Path != ".flywheel/runs/T1.r1.jsonl" {
		t.Errorf("signal path = %q, want the run file", sig.Path)
	}
	if sig.Session != "ses_test_noplan_001" {
		t.Errorf("signal session = %q, want the run session", sig.Session)
	}
	// One condition, one signal: a clean stop run adds no finish signal, so
	// exactly one signal records the whole run.
	if signals != 1 {
		t.Errorf("signal events = %d, want exactly 1 (no duplicate for one attempt)", signals)
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

// wroteFixture writes a fixture of one step_start, one tool_use event per
// (tool, path) call, a text reply, and a final step_finish with the given
// reason, and returns its absolute path (issue #163).
func wroteFixture(t *testing.T, calls [][2]string, reason string) string {
	t.Helper()
	session := "ses_test_wrote_001"
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"step_start","sessionID":%q,"part":{"type":"step_start"}}`+"\n", session)
	for _, c := range calls {
		tool, path := c[0], c[1]
		fmt.Fprintf(&b, `{"type":"tool_use","sessionID":%q,"part":{"type":"tool_use","tool":%q,"state":{"input":{"filePath":%q}}}}`+"\n", session, tool, path)
	}
	fmt.Fprintf(&b, `{"type":"text","sessionID":%q,"part":{"type":"text","text":"reply"}}`+"\n", session)
	fmt.Fprintf(&b, `{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":%q}}`+"\n", session, reason)
	path := filepath.Join(t.TempDir(), "wrote.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// TestRunWroteRecordsSortedDedupedFiles checks edit/write tool calls are
// recorded on the finished event's wrote field, sorted and deduplicated
// (issue #163).
func TestRunWroteRecordsSortedDedupedFiles(t *testing.T) {
	dir := setupTask(t)
	calls := [][2]string{
		{"edit", filepath.Join(dir, "b.go")},
		{"write", filepath.Join(dir, "a.go")},
		{"edit", filepath.Join(dir, "b.go")},
	}
	if err := WriteConfig(dir, simConfig(wroteFixture(t, calls, "stop"))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	want := []string{filepath.Join(dir, "a.go"), filepath.Join(dir, "b.go")}
	if len(f.Wrote) != 2 || f.Wrote[0] != want[0] || f.Wrote[1] != want[1] {
		t.Errorf("wrote = %v, want %v (sorted, deduplicated)", f.Wrote, want)
	}
}

// TestRunNoEditsOmitsWroteField checks a run with no edit/write tool calls
// omits the wrote field from its finished event (issue #163).
func TestRunNoEditsOmitsWroteField(t *testing.T) {
	dir := setupTask(t)
	calls := [][2]string{{"read", filepath.Join(dir, "a.go")}}
	if err := WriteConfig(dir, simConfig(wroteFixture(t, calls, "stop"))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if f := evs[len(evs)-1]; len(f.Wrote) != 0 {
		t.Errorf("wrote = %v, want empty (no edits)", f.Wrote)
	}
}

// TestRunUncleanFinishWithFilesPrintsExtraLine checks an unclean finish
// (reason length) that wrote files prints one extra progress line naming
// them after the finished line (issue #163).
func TestRunUncleanFinishWithFilesPrintsExtraLine(t *testing.T) {
	dir := setupTask(t)
	editPath := filepath.Join(dir, "a.go")
	calls := [][2]string{{"edit", editPath}}
	if err := WriteConfig(dir, simConfig(wroteFixture(t, calls, "length"))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := fmt.Sprintf("T1 r1 wrote 1 file(s) before failing: %s", editPath)
	if plog := buf.String(); !strings.Contains(plog, want) {
		t.Errorf("progress missing %q; got:\n%s", want, plog)
	}
}

// TestRunCleanFinishNoExtraWroteLine checks a clean finish (reason stop) that
// wrote files prints no extra "wrote ... before failing" line, even though the
// finished event itself still records the files (issue #163).
func TestRunCleanFinishNoExtraWroteLine(t *testing.T) {
	dir := setupTask(t)
	calls := [][2]string{{"edit", filepath.Join(dir, "a.go")}}
	if err := WriteConfig(dir, simConfig(wroteFixture(t, calls, "stop"))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if plog := buf.String(); strings.Contains(plog, "before failing") {
		t.Errorf("clean finish printed an extra wrote line; got:\n%s", plog)
	}
}

// ownsBrief writes a brief file under dir with the given owns: line and a
// minimal valid header, and returns the repo-relative path.
func ownsBrief(t *testing.T, dir, name, owns string) string {
	t.Helper()
	content := "owns: " + owns + "\ngate: true\n\n# TASK: " + name + "\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write brief %s: %v", name, err)
	}
	return name
}

// planAndDispatch records a planned event (brief at path) and a dispatched
// event for task, leaving it in flight (derived status dispatched).
func planAndDispatch(t *testing.T, dir, task, brief string) {
	t.Helper()
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: task, Kind: "planned", Brief: brief}); err != nil {
		t.Fatalf("AppendEvent() planned %s: %v", task, err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:01Z", Task: task, Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched %s: %v", task, err)
	}
}

// planOnly records a planned event for task (brief at path).
func planOnly(t *testing.T, dir, task, brief string) {
	t.Helper()
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: task, Kind: "planned", Brief: brief}); err != nil {
		t.Fatalf("AppendEvent() planned %s: %v", task, err)
	}
}

// exclBrief writes a brief with the given owns and exclusive lines and
// returns its path.
func exclBrief(t *testing.T, dir, name, owns, exclusive string) string {
	t.Helper()
	content := "owns: " + owns + "\nexclusive: " + exclusive + "\ngate: true\n\n# TASK: " + name + "\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write brief %s: %v", name, err)
	}
	return name
}

// gateBrief writes a brief with the given owns and gate lines and returns
// its path.
func gateBrief(t *testing.T, dir, name, owns, gate string) string {
	t.Helper()
	content := "owns: " + owns + "\ngate: " + gate + "\n\n# TASK: " + name + "\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write brief %s: %v", name, err)
	}
	return name
}

// TestRunRefusesOwnsCollision checks a dispatch whose owns: shares a path
// with an in-flight task's owns: is refused with the owns RuleRefusal, the
// message names the shared path and the owning task, and no event is appended
// (issue #164).
func TestRunRefusesOwnsCollision(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "a.go, shared.go"))
	planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "b.go, shared.go"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T2"})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "owns" {
		t.Fatalf("Run() error = %v, want a RuleRefusal owns", err)
	}
	if !strings.Contains(r.Fix, "shared.go") || !strings.Contains(r.Fix, "T1") {
		t.Errorf("refusal fix = %q, want it naming the shared path shared.go and the owning task T1", r.Fix)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 3 {
		t.Errorf("events = %d, want 3 (planned/dispatched T1, planned T2); a refused dispatch must record nothing", len(evs))
	}
}

// TestRunOwnsDisjointFromRunningDispatches checks a dispatch whose owns: is
// disjoint from the in-flight task's owns: dispatches normally (issue #164).
func TestRunOwnsDisjointFromRunningDispatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "a.go, shared.go"))
	planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "b.go"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	res, err := Run(dir, RunOptions{Task: "T2"})
	if err != nil {
		t.Fatalf("Run() error = %v, want a normal dispatch (owns disjoint)", err)
	}
	if res.Attempt != "r1" || res.Reason != "stop" {
		t.Errorf("result = %+v, want a clean r1 run", res)
	}
}

// TestRunAllowOverlapRecordsOwnsOverlap checks --allow-overlap dispatches
// despite an owns: collision and records the overlap on the dispatched event's
// note, naming the colliding paths and the other task (issue #164).
func TestRunAllowOverlapRecordsOwnsOverlap(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "shared.go"))
	planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "b.go, shared.go"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T2", AllowOverlap: true}); err != nil {
		t.Fatalf("Run() with --allow-overlap error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T2" && e.Kind == "dispatched" {
			d = e
		}
	}
	if !strings.Contains(d.Note, "owns-overlap: shared.go with T1") {
		t.Errorf("dispatched note = %q, want it to record owns-overlap: shared.go with T1", d.Note)
	}
}

// TestRunOwnsCollisionWithNotInFlightDispatches checks a collision with a task
// that is no longer in flight (landed or passed) does not refuse (issue #164).
func TestRunOwnsCollisionWithNotInFlightDispatches(t *testing.T) {
	for _, final := range []string{"landed", "passed"} {
		t.Run(final, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Init(dir, false); err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "a.go, shared.go"))
			if final == "landed" {
				if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:02Z", Task: "T1", Kind: "landed"}); err != nil {
					t.Fatalf("AppendEvent() landed: %v", err)
				}
			} else {
				if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector"}); err != nil {
					t.Fatalf("AppendEvent() inspected: %v", err)
				}
			}
			planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "b.go, shared.go"))
			if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
				t.Fatalf("WriteConfig() error = %v", err)
			}
			if _, err := Run(dir, RunOptions{Task: "T2"}); err != nil {
				t.Fatalf("Run() error = %v, want a normal dispatch (the owning task is no longer in flight)", err)
			}
		})
	}
}

// TestRunRefusesDirectoryPrefixOwnsCollision checks a directory-prefix owns:
// entry (internal/foo/) collides with a file owned by an in-flight task under
// it (issue #164).
func TestRunRefusesDirectoryPrefixOwnsCollision(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "internal/foo/"))
	planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "internal/foo/bar.go"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T2"})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "owns" {
		t.Fatalf("Run() error = %v, want a RuleRefusal owns", err)
	}
	if !strings.Contains(r.Fix, "internal/foo/bar.go") {
		t.Errorf("refusal fix = %q, want it naming the file under the directory prefix", r.Fix)
	}
}

// TestRunRefusesExclusiveCollision checks a dispatch whose exclusive: names a
// resource an in-flight task already holds is refused with the exclusive
// RuleRefusal, the message names the resource and the holding task, and no
// event is appended (issue #220).
func TestRunRefusesExclusiveCollision(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", exclBrief(t, dir, "b1.txt", "a.go", "db"))
	planOnly(t, dir, "T2", exclBrief(t, dir, "b2.txt", "b.go", "db"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T2"})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "exclusive" {
		t.Fatalf("Run() error = %v, want a RuleRefusal exclusive", err)
	}
	if !strings.Contains(r.Fix, `"db"`) || !strings.Contains(r.Fix, "T1") {
		t.Errorf("refusal fix = %q, want it naming the resource db and the holding task T1", r.Fix)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 3 {
		t.Errorf("events = %d, want 3 (planned/dispatched T1, planned T2); a refused dispatch must record nothing", len(evs))
	}
}

// TestRunExclusiveNamesDisjointBothDispatch checks two briefs declaring
// different exclusive names both dispatch (issue #220).
func TestRunExclusiveNamesDisjointBothDispatch(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", exclBrief(t, dir, "b1.txt", "a.go", "db"))
	planOnly(t, dir, "T2", exclBrief(t, dir, "b2.txt", "b.go", "cache"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	res, err := Run(dir, RunOptions{Task: "T2"})
	if err != nil {
		t.Fatalf("Run() error = %v, want a normal dispatch (exclusive names disjoint)", err)
	}
	if res.Attempt != "r1" || res.Reason != "stop" {
		t.Errorf("result = %+v, want a clean r1 run", res)
	}
}

// TestRunAllowOverlapRecordsExclusiveOverlap checks --allow-overlap dispatches
// despite an exclusive: clash and records the crossing on the dispatched
// event's note (issue #220).
func TestRunAllowOverlapRecordsExclusiveOverlap(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", exclBrief(t, dir, "b1.txt", "a.go", "db"))
	planOnly(t, dir, "T2", exclBrief(t, dir, "b2.txt", "b.go", "db"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T2", AllowOverlap: true}); err != nil {
		t.Fatalf("Run() with --allow-overlap error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T2" && e.Kind == "dispatched" {
			d = e
		}
	}
	if !strings.Contains(d.Note, "exclusive-overlap: db with T1") {
		t.Errorf("dispatched note = %q, want it to record exclusive-overlap: db with T1", d.Note)
	}
}

// TestRunExclusiveClashWithNotInFlightDispatches checks a clash with a task
// that is no longer in flight (landed or passed) does not refuse (issue #220).
func TestRunExclusiveClashWithNotInFlightDispatches(t *testing.T) {
	for _, final := range []string{"landed", "passed"} {
		t.Run(final, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Init(dir, false); err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			planAndDispatch(t, dir, "T1", exclBrief(t, dir, "b1.txt", "a.go", "db"))
			if final == "landed" {
				if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:02Z", Task: "T1", Kind: "landed"}); err != nil {
					t.Fatalf("AppendEvent() landed: %v", err)
				}
			} else {
				if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:02Z", Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector"}); err != nil {
					t.Fatalf("AppendEvent() inspected: %v", err)
				}
			}
			planOnly(t, dir, "T2", exclBrief(t, dir, "b2.txt", "b.go", "db"))
			if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
				t.Fatalf("WriteConfig() error = %v", err)
			}
			if _, err := Run(dir, RunOptions{Task: "T2"}); err != nil {
				t.Fatalf("Run() error = %v, want a normal dispatch (the holding task is no longer in flight)", err)
			}
		})
	}
}

// TestRunExclusiveAloneDispatches checks a brief declaring an exclusive name
// dispatches normally when no in-flight peer holds it (issue #220).
func TestRunExclusiveAloneDispatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planOnly(t, dir, "T1", exclBrief(t, dir, "b1.txt", "a.go", "db"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	res, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v, want a normal dispatch (no in-flight peer holds the resource)", err)
	}
	if res.Attempt != "r1" || res.Reason != "stop" {
		t.Errorf("result = %+v, want a clean r1 run", res)
	}
}

// TestRunOwnsCollisionBeatsExclusiveClash checks a dispatch whose owns:
// overlaps an in-flight task's owns: and whose exclusive: clashes with it too
// is refused as owns, not exclusive — owns is checked first (issue #220).
func TestRunOwnsCollisionBeatsExclusiveClash(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", exclBrief(t, dir, "b1.txt", "shared.go", "db"))
	planOnly(t, dir, "T2", exclBrief(t, dir, "b2.txt", "shared.go", "db"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T2"})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "owns" {
		t.Fatalf("Run() error = %v, want a RuleRefusal owns (owns is checked first)", err)
	}
}

// TestRunSharedGateWarnsAndDispatches is the guard: a dispatch whose gate
// line is byte-identical to an in-flight task's records the shared-gate
// warning on its dispatched note and prints it through progress, and does
// NOT refuse — the run proceeds with the exit unchanged (issue #223).
func TestRunSharedGateWarnsAndDispatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", gateBrief(t, dir, "b1.txt", "a.go", "npm run validate"))
	planOnly(t, dir, "T2", gateBrief(t, dir, "b2.txt", "b.go", "npm run validate"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T2", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v, want a normal dispatch (a shared gate is a warning, not a refusal)", err)
	}
	if res.Attempt != "r1" || res.Reason != "stop" {
		t.Errorf("result = %+v, want a clean r1 run", res)
	}
	if !strings.Contains(buf.String(), "gate 1 is shared with 1 in-flight units; they will contend") {
		t.Errorf("progress = %q, want the shared-gate warning line", buf.String())
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T2" && e.Kind == "dispatched" {
			d = e
		}
	}
	if d.Task != "T2" {
		t.Fatal("no dispatched event for T2")
	}
	if !strings.Contains(d.Note, "shared-gate: gate 1 is shared with 1 in-flight units; they will contend") {
		t.Errorf("dispatched note = %q, want it to record shared-gate: gate 1 is shared with 1 in-flight units; they will contend", d.Note)
	}
}

// TestRunDifferentGatesNoSharedGateWarning checks two in-flight tasks whose
// gate lines differ produce no shared-gate warning (issue #223).
func TestRunDifferentGatesNoSharedGateWarning(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", gateBrief(t, dir, "b1.txt", "a.go", "npm run validate"))
	planOnly(t, dir, "T2", gateBrief(t, dir, "b2.txt", "b.go", "go test ./..."))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T2"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T2" && e.Kind == "dispatched" {
			d = e
		}
	}
	if d.Task != "T2" {
		t.Fatal("no dispatched event for T2")
	}
	if strings.Contains(d.Note, "shared-gate") {
		t.Errorf("dispatched note = %q, want no shared-gate warning (gate lines differ)", d.Note)
	}
}

// TestRunSharedGateNoInFlightSiblingsNoWarning checks a dispatch with no
// in-flight sibling produces no shared-gate warning (issue #223).
func TestRunSharedGateNoInFlightSiblingsNoWarning(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planOnly(t, dir, "T1", gateBrief(t, dir, "b1.txt", "a.go", "npm run validate"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "dispatched" {
			d = e
		}
	}
	if d.Task != "T1" {
		t.Fatal("no dispatched event for T1")
	}
	if strings.Contains(d.Note, "shared-gate") {
		t.Errorf("dispatched note = %q, want no shared-gate warning (no in-flight sibling)", d.Note)
	}
}

// TestRunSharedGateCountsUnitsNotGates checks the warning counts in-flight
// units, not gate declarations: three siblings sharing the gate say three
// (issue #223).
func TestRunSharedGateCountsUnitsNotGates(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", gateBrief(t, dir, "b1.txt", "a.go", "npm run validate"))
	planAndDispatch(t, dir, "T2", gateBrief(t, dir, "b2.txt", "b.go", "npm run validate"))
	planAndDispatch(t, dir, "T3", gateBrief(t, dir, "b3.txt", "c.go", "npm run validate"))
	planOnly(t, dir, "T4", gateBrief(t, dir, "b4.txt", "d.go", "npm run validate"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T4"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T4" && e.Kind == "dispatched" {
			d = e
		}
	}
	if d.Task != "T4" {
		t.Fatal("no dispatched event for T4")
	}
	if !strings.Contains(d.Note, "shared-gate: gate 1 is shared with 3 in-flight units; they will contend") {
		t.Errorf("dispatched note = %q, want it to count the 3 in-flight units sharing the gate", d.Note)
	}
}

// TestRunCorrectionDeltaGateWarns is the w223-c1 correction's guard: a
// correction whose delta declares a gate an in-flight sibling also declares
// must produce the shared-gate warning, even though the previous attempt's
// gate (the base brief's) differs from the sibling's. Without the fix, Run
// compared the previous attempt's gates and missed the contention the delta
// actually introduces, so the two units ran the gate at once with no warning
// (issue #223).
func TestRunCorrectionDeltaGateWarns(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", gateBrief(t, dir, "b1.txt", "a.go", "npm run validate"))
	base := gateBrief(t, dir, "b2.txt", "b.go", "go test ./...")
	planAndDispatch(t, dir, "T2", base)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	delta := filepath.Join(dir, "d2.txt")
	if err := os.WriteFile(delta, []byte("owns: b.go\ngate: npm run validate\n\n# TASK: delta\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T2", DeltaPath: delta, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v, want a normal dispatch (a shared gate is a warning, not a refusal)", err)
	}
	if res.Attempt != "c1" || res.Reason != "stop" {
		t.Errorf("result = %+v, want a clean c1 run", res)
	}
	if !strings.Contains(buf.String(), "gate 1 is shared with 1 in-flight units; they will contend") {
		t.Errorf("progress = %q, want the shared-gate warning for the delta's gate", buf.String())
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T2" && e.Kind == "dispatched" {
			d = e
		}
	}
	if d.Attempt != "c1" {
		t.Fatalf("dispatched event = %+v, want the c1 correction", d)
	}
	if !strings.Contains(d.Note, "shared-gate: gate 1 is shared with 1 in-flight units; they will contend") {
		t.Errorf("dispatched note = %q, want it to record the delta gate's shared-gate warning", d.Note)
	}
}

// TestRunCorrectionDeltaGateReplacedSharedNoWarning is the w223-c1
// correction's invented-contention half: a correction whose delta replaces a
// gate the base brief (and therefore the previous attempt) shared with an
// in-flight sibling must produce no warning. Without the fix, Run compared
// the previous attempt's gates and warned about contention the delta removes
// (issue #223).
func TestRunCorrectionDeltaGateReplacedSharedNoWarning(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planAndDispatch(t, dir, "T1", gateBrief(t, dir, "b1.txt", "a.go", "npm run validate"))
	base := gateBrief(t, dir, "b2.txt", "b.go", "npm run validate")
	planAndDispatch(t, dir, "T2", base)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	delta := filepath.Join(dir, "d2.txt")
	if err := os.WriteFile(delta, []byte("owns: b.go\ngate: go test ./...\n\n# TASK: delta\n"), 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T2", DeltaPath: delta, Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Attempt != "c1" || res.Reason != "stop" {
		t.Errorf("result = %+v, want a clean c1 run", res)
	}
	if strings.Contains(buf.String(), "gate 1 is shared") {
		t.Errorf("progress = %q, want no shared-gate warning (the delta replaces the shared gate)", buf.String())
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var d Event
	for _, e := range evs {
		if e.Task == "T2" && e.Kind == "dispatched" {
			d = e
		}
	}
	if d.Attempt != "c1" {
		t.Fatalf("dispatched event = %+v, want the c1 correction", d)
	}
	if strings.Contains(d.Note, "shared-gate") {
		t.Errorf("dispatched note = %q, want no shared-gate warning (the delta replaces the shared gate)", d.Note)
	}
}

// concurrentOutcome pairs one concurrent Run's result and error.
type concurrentOutcome struct {
	res Result
	err error
}

// TestRunConcurrentOwnsCollisionDispatchesOnce is the regression guard for
// the dispatch race (issue #242): two Run calls started at the same moment
// on tasks whose owns: overlaps must not both dispatch. Without the lock,
// both read a log in which neither has dispatched, both pass the collision
// checks and both dispatch — taking the same owned file. With the lock,
// exactly one dispatched event lands and the other Run returns the owns
// collision refusal. SimDelay keeps the winning run in flight (derived
// status dispatched) while the losing run reads the log, so the collision
// it must see is actually visible.
func TestRunConcurrentOwnsCollisionDispatchesOnce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planOnly(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "shared.go"))
	planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "shared.go"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	done := make(chan concurrentOutcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, task := range []string{"T1", "T2"} {
		go func(task string) {
			defer wg.Done()
			res, err := Run(dir, RunOptions{Task: task, SimDelay: 500 * time.Millisecond})
			done <- concurrentOutcome{res, err}
		}(task)
	}
	wg.Wait()
	close(done)
	dispatched, refused := 0, 0
	for o := range done {
		if o.err == nil {
			dispatched++
			continue
		}
		var r *RuleRefusal
		if !errors.As(o.err, &r) || r.Rule != "owns" {
			t.Errorf("Run() error = %v, want the owns RuleRefusal", o.err)
			continue
		}
		refused++
	}
	if dispatched != 1 || refused != 1 {
		t.Errorf("concurrent overlapping dispatches: %d dispatched, %d refused; want exactly 1 and 1 (the dispatch lock must serialise them)", dispatched, refused)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count := 0
	for _, e := range evs {
		if e.Kind == "dispatched" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("dispatched events = %d, want exactly 1", count)
	}
}

// TestRunConcurrentExclusiveCollisionDispatchesOnce checks the same lock
// serialises two concurrent dispatches whose exclusive: names the same
// resource (issue #242): exactly one dispatched event lands, and the other
// Run returns the exclusive collision refusal. Owns are disjoint so only the
// exclusive check can refuse.
func TestRunConcurrentExclusiveCollisionDispatchesOnce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planOnly(t, dir, "T1", exclBrief(t, dir, "b1.txt", "a.go", "db"))
	planOnly(t, dir, "T2", exclBrief(t, dir, "b2.txt", "b.go", "db"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	done := make(chan concurrentOutcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, task := range []string{"T1", "T2"} {
		go func(task string) {
			defer wg.Done()
			res, err := Run(dir, RunOptions{Task: task, SimDelay: 500 * time.Millisecond})
			done <- concurrentOutcome{res, err}
		}(task)
	}
	wg.Wait()
	close(done)
	dispatched, refused := 0, 0
	for o := range done {
		if o.err == nil {
			dispatched++
			continue
		}
		var r *RuleRefusal
		if !errors.As(o.err, &r) || r.Rule != "exclusive" {
			t.Errorf("Run() error = %v, want the exclusive RuleRefusal", o.err)
			continue
		}
		refused++
	}
	if dispatched != 1 || refused != 1 {
		t.Errorf("concurrent exclusive dispatches: %d dispatched, %d refused; want exactly 1 and 1", dispatched, refused)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count := 0
	for _, e := range evs {
		if e.Kind == "dispatched" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("dispatched events = %d, want exactly 1", count)
	}
}

// TestRunConcurrentDisjointOwnsBothDispatch checks the dispatch lock does
// not turn independent units into a queue that fails: two concurrent
// dispatches with disjoint owns: and no shared exclusive both succeed
// (issue #242).
func TestRunConcurrentDisjointOwnsBothDispatch(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	planOnly(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "a.go"))
	planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "b.go"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	done := make(chan concurrentOutcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, task := range []string{"T1", "T2"} {
		go func(task string) {
			defer wg.Done()
			res, err := Run(dir, RunOptions{Task: task})
			done <- concurrentOutcome{res, err}
		}(task)
	}
	wg.Wait()
	close(done)
	ok := 0
	for o := range done {
		if o.err != nil {
			t.Errorf("Run() error = %v, want a normal dispatch (owns disjoint)", o.err)
			continue
		}
		if o.res.Attempt != "r1" || o.res.Reason != "stop" {
			t.Errorf("result = %+v, want a clean r1 run", o.res)
		}
		ok++
	}
	if ok != 2 {
		t.Errorf("successful dispatches = %d, want 2 (the lock must not fail disjoint units)", ok)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	count := 0
	for _, e := range evs {
		if e.Kind == "dispatched" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("dispatched events = %d, want 2", count)
	}
}

// TestRunStaleDispatchLockCleared checks a dispatch.lock whose mtime is well
// in the past — a crashed holder — is removed and the dispatch proceeds, so
// a dead lead never wedges the repository (issue #242).
func TestRunStaleDispatchLockCleared(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	lock := filepath.Join(dir, ".flywheel", "dispatch.lock")
	if err := os.WriteFile(lock, []byte("pid 999 locked stale\n"), 0o644); err != nil {
		t.Fatalf("write dispatch.lock: %v", err)
	}
	past := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(lock, past, past); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v, want a normal dispatch past a stale lock", err)
	}
	if res.Attempt != "r1" || res.Reason != "stop" {
		t.Errorf("result = %+v, want a clean r1 run", res)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("dispatch.lock still exists after the stale-lock recovery, want it removed")
	}
}

// TestRunDispatchLockRemovedAfterRun checks the lock file never outlives the
// locked window: after Run returns on the success path and on the refusal
// path alike, .flywheel/dispatch.lock does not exist (issue #242).
func TestRunDispatchLockRemovedAfterRun(t *testing.T) {
	lockPath := func(dir string) string {
		return filepath.Join(dir, ".flywheel", "dispatch.lock")
	}
	t.Run("success", func(t *testing.T) {
		dir := setupTask(t)
		if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
			t.Errorf("dispatch.lock exists after a successful run, want it removed")
		}
	})
	t.Run("refusal", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := Init(dir, false); err != nil {
			t.Fatalf("Init() error = %v", err)
		}
		planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "shared.go"))
		planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "shared.go"))
		if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
		_, err := Run(dir, RunOptions{Task: "T2"})
		var r *RuleRefusal
		if !errors.As(err, &r) || r.Rule != "owns" {
			t.Fatalf("Run() error = %v, want the owns RuleRefusal", err)
		}
		if _, err := os.Stat(lockPath(dir)); !os.IsNotExist(err) {
			t.Errorf("dispatch.lock exists after a refused run, want it removed")
		}
	})
}

// TestFreshDispatchHeaderUsedAfterDrift is the issue #259 correction's 🔴
// guard: a brief edited on disk after its planned event but dispatched without
// --strict-brief must be measured by its own dispatched header — the owns and
// gates the worker was actually given — not by the stale planned header.
// Without the fix, AttemptBrief returns the planned 1-gate header and
// validation checks work nobody measured; with it, the fresh dispatch's
// recorded header (2 gates, owns a.go and shared.go) wins.
func TestFreshDispatchHeaderUsedAfterDrift(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte("owns: a.go\nneeds: none\ngate: exit 0\n\n# TASK\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	planned, err := ParseBriefHeader(briefPath)
	if err != nil {
		t.Fatalf("ParseBriefHeader() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-17T00:00:00Z", Task: "T1", Kind: "planned", Brief: "brief.txt", Header: &planned}); err != nil {
		t.Fatalf("append planned: %v", err)
	}
	// The lead edits the brief on disk to add owns: shared.go and a second
	// gate, recording nothing; a fresh dispatch without --strict-brief is
	// given the edited prompt.
	if err := os.WriteFile(briefPath, []byte("owns: a.go, shared.go\nneeds: none\ngate: exit 0\ngate: exit 0\n\n# TASK\n"), 0o644); err != nil {
		t.Fatalf("edit brief: %v", err)
	}
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
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
	if d.Header == nil || len(d.Header.Gates) != 2 || len(d.Header.Owns) != 2 {
		t.Fatalf("dispatched header = %+v, want 2 gates and owns a.go, shared.go (the edited prompt)", d.Header)
	}
	header, _, err := AttemptBrief(dir, evs, "T1")
	if err != nil {
		t.Fatalf("AttemptBrief() error = %v", err)
	}
	if len(header.Gates) != 2 {
		t.Errorf("AttemptBrief gates = %v, want the edited 2-gate set, not the planned 1-gate", header.Gates)
	}
	wantOwns := []string{"a.go", "shared.go"}
	if len(header.Owns) != len(wantOwns) {
		t.Fatalf("AttemptBrief owns = %v, want %v", header.Owns, wantOwns)
	}
	for i, w := range wantOwns {
		if header.Owns[i] != w {
			t.Errorf("owns[%d] = %q, want %q", i, header.Owns[i], w)
		}
	}
}

// TestRunDispatchedHeaderMatchesRecordedSHA256 is the issue #259 correction's
// 🟡 guard: the header recorded on dispatched is parsed from the same bytes as
// the recorded SHA256 (the prompt actually sent), so a concurrent editor
// between the two reads can never make them disagree. ParseBriefHeaderBytes is
// exercised directly in brief_test.go; here the dispatch must record a header
// whose sha256 field equals its own SHA256.
func TestRunDispatchedHeaderMatchesRecordedSHA256(t *testing.T) {
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
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
	if d.Header == nil {
		t.Fatal("dispatched event carries no header")
	}
	if d.Header.SHA256 != d.SHA256 {
		t.Errorf("header sha256 = %q, want it to equal the dispatched sha256 %q (same prompt bytes)", d.Header.SHA256, d.SHA256)
	}
}
