package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAdapterForClaude checks AdapterFor("claude") resolves to the claude
// adapter (issue #49).
func TestAdapterForClaude(t *testing.T) {
	t.Parallel()
	a, err := AdapterFor("claude")
	if err != nil || a.Name() != "claude" {
		t.Errorf("AdapterFor(claude) = %v, %v", a, err)
	}
}

// TestClaudeCommandFlags checks Command's binary and required flags: the
// binary "claude" driving --output-format stream-json and the model.
func TestClaudeCommandFlags(t *testing.T) {
	t.Parallel()
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
	// not parallel: t.Setenv PATH
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	if c1d.Brief != ".flywheel/briefs/T1.c1.delta.txt" {
		t.Errorf("resume dispatched brief = %q, want the snapshot .flywheel/briefs/T1.c1.delta.txt", c1d.Brief)
	}
}

func TestRunResumeWithoutSessionErrors(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	git(t, dir, []string{"init", "-q"})
	git(t, dir, []string{"config", "core.autocrlf", "false"})
	git(t, dir, []string{"config", "gc.auto", "0"})
	git(t, dir, []string{"config", "maintenance.auto", "false"})
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	b, err := os.ReadFile("../../skills/flywheel/references/worker-rules.md")
	if err != nil {
		t.Fatalf("read reference worker-rules.md: %v", err)
	}
	if normLF(b) != workerRules {
		t.Errorf("reference worker-rules.md = %q, want workerRules %q", normLF(b), workerRules)
	}
}

func TestRunLongRunWithEarlyFirstLineFinishesStop(t *testing.T) {
	t.Parallel()
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
	// not parallel: sets the package-level commandHook
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
	// not parallel: sets the package-level commandHook
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	// not parallel: sets the package-level commandHook
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
	if d.Brief != ".flywheel/briefs/T1.c1.delta.txt" {
		t.Errorf("dispatched brief = %q, want the per-attempt delta snapshot", d.Brief)
	}
}

// TestRunResumeWithDelta checks --resume --delta D keeps resuming the
// worker's session while sending D.
func TestRunResumeWithDelta(t *testing.T) {
	// not parallel: sets the package-level commandHook
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

// lastFinished returns the last finished event for a task, or zero.
func lastFinished(t *testing.T, dir, task string) Event {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var f Event
	for _, e := range evs {
		if e.Task == task && e.Kind == "finished" {
			f = e
		}
	}
	return f
}

// TestRunStopWithoutWrites checks a clean stop is not silently "done" (issue
// #364): no writes records no signal but prints a line (the floor shows it as
// no-writes), a write records none, and a result line with permission denials
// records permission-denied and names them in the finished note.
func TestRunStopWithoutWrites(t *testing.T) {
	t.Parallel()
	session := "ses_test_nowrites_001"
	start := fmt.Sprintf(`{"type":"step_start","sessionID":%q,"part":{"type":"step_start"}}`+"\n", session)
	stop := fmt.Sprintf(`{"type":"step_finish","sessionID":%q,"part":{"type":"step_finish","reason":"stop"}}`+"\n", session)
	denied := `{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","session_id":"` + session +
		`","permission_denials":[{"tool_name":"Edit","tool_input":{"file_path":"/x/a.go"}},{"tool_name":"Bash","tool_input":{"command":"ls"}}]}` + "\n"
	cases := []struct {
		name, fixture, signal, line, note string
	}{
		{"no writes", start + stop, "", "T1 r1 finished without writing a file", ""},
		{"wrote", fmt.Sprintf(start+`{"type":"tool_use","sessionID":%q,"part":{"type":"tool_use","tool":"write","state":{"input":{"filePath":"a.go"}}}}`+"\n"+stop, session), "", "", ""},
		{"denied", start + denied, "permission-denied", "T1 r1 permission-denied: Edit /x/a.go, Bash", "permission denied: Edit /x/a.go, Bash"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := setupTask(t)
			model := filepath.Join(t.TempDir(), "f.jsonl")
			if err := os.WriteFile(model, []byte(c.fixture), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			if err := WriteConfig(dir, simConfig(model)); err != nil {
				t.Fatalf("WriteConfig() error = %v", err)
			}
			var buf bytes.Buffer
			res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if res.Reason != "stop" {
				t.Fatalf("reason = %q, want stop", res.Reason)
			}
			evs, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			var sigs []string
			for _, e := range evs {
				if e.Kind == "signal" {
					sigs = append(sigs, e.Signal)
				}
			}
			var want []string
			if c.signal != "" {
				want = []string{c.signal}
			}
			if !reflect.DeepEqual(sigs, want) {
				t.Errorf("signals = %q, want %q", sigs, want)
			}
			if c.line != "" && !strings.Contains(buf.String(), c.line+"\n") {
				t.Errorf("progress missing %q:\n%s", c.line, buf.String())
			}
			if f := lastFinished(t, dir, "T1"); f.Note != c.note {
				t.Errorf("finished note = %q, want %q", f.Note, c.note)
			}
		})
	}
}

// TestRunBriefDriftWarns checks a brief edited after its dispatch is caught
// at the next dispatch: the drift line is printed, the run still dispatches,
// and the newly recorded dispatched hash is the brief's CURRENT content
// (issue #135).
func TestRunBriefDriftWarns(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	if !strings.Contains(r.Fix, "brief on disk differs") || !strings.Contains(r.Fix, "flywheel log --task T1 --kind amended --brief b.txt") {
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

// TestBriefDriftAdvice checks the drift message names the step that takes
// effect on a dispatched attempt (issue #387): an owns-only edit is recorded
// with --kind amended, never --kind planned, and a gate edit also names a
// correction delta.
func TestBriefDriftAdvice(t *testing.T) {
	t.Parallel()
	orig := []byte("owns: a.go\ngate: go test ./...\n\n# Task\nbody\n")
	cases := []struct {
		name      string
		edited    string
		wantDelta bool
	}{
		{"owns only", "owns: a.go, b.go\ngate: go test ./...\n\n# Task\nbody\n", false},
		{"gate change", "owns: a.go\ngate: go test ./...\ngate: go vet ./...\n\n# Task\nbody\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "b.txt"), orig, 0o644); err != nil {
				t.Fatalf("write brief: %v", err)
			}
			h, err := ParseBriefHeaderBytes(orig)
			if err != nil {
				t.Fatalf("ParseBriefHeaderBytes() error = %v", err)
			}
			events := []Event{
				{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt", Header: &h},
				{TS: "2026-09-12T00:01:00Z", Task: "T1", Kind: "dispatched", Attempt: "r1", Brief: "b.txt", SHA256: h.SHA256, Header: &h},
			}
			if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(c.edited), 0o644); err != nil {
				t.Fatalf("write edited brief: %v", err)
			}
			msg := briefDriftAdvice(dir, events, "T1", "b.txt", "r1")
			if !strings.Contains(msg, "flywheel log --task T1 --kind amended --brief b.txt") {
				t.Errorf("briefDriftAdvice() = %q, want the --kind amended step", msg)
			}
			if strings.Contains(msg, "--kind planned") {
				t.Errorf("briefDriftAdvice() = %q, want no --kind planned advice", msg)
			}
			if got := strings.Contains(msg, "flywheel run T1 --delta"); got != c.wantDelta {
				t.Errorf("briefDriftAdvice() = %q, names --delta = %v, want %v", msg, got, c.wantDelta)
			}
		})
	}
}

// TestRunBriefDriftQuietAfterReplan checks the lead-correction path: after a
// drift, recording a fresh planned event with the edited brief and
// dispatching again warns nothing (issue #135).
func TestRunBriefDriftQuietAfterReplan(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	// not parallel: swaps the package-level now
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestRunWroteOutsideWorktree checks a write outside the worktree is named in
// the finished event's note and records one off-course signal for the
// attempt, even when outside reads already recorded it; writes inside the
// worktree record neither (issue #359).
func TestRunWroteOutsideWorktree(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		outsideWrite bool
		outsideReads bool
	}{
		{"outside write", true, false},
		{"outside write after outside reads", true, true},
		{"inside writes only", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupTask(t)
			outside := filepath.Join(t.TempDir(), "main.go")
			var calls [][2]string
			if tc.outsideReads {
				o := shortOutsideDir(dir)
				for _, n := range []string{"o1.go", "o2.go", "o3.go", "o4.go", "o5.go"} {
					calls = append(calls, [2]string{"read", filepath.Join(o, n)})
				}
			}
			calls = append(calls, [2]string{"write", filepath.Join(dir, "a.go")})
			if tc.outsideWrite {
				calls = append(calls, [2]string{"edit", outside})
			}
			if err := WriteConfig(dir, simConfig(wroteFixture(t, calls, "stop"))); err != nil {
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
			var fin Event
			signals := 0
			for _, e := range evs {
				if e.Kind == "finished" {
					fin = e
				}
				if e.Kind == "signal" && e.Signal == "off-course" && e.Attempt == "r1" {
					signals++
				}
			}
			has := strings.Contains(fin.Note, "wrote outside the worktree:")
			if !tc.outsideWrite {
				if has || signals != 0 {
					t.Errorf("note = %q, off-course signals = %d; want neither for inside writes", fin.Note, signals)
				}
				return
			}
			if !has || !strings.Contains(fin.Note, outside) {
				t.Errorf("finished note = %q, want it to name the outside write %q", fin.Note, outside)
			}
			if strings.Contains(fin.Note, filepath.Join(dir, "a.go")) {
				t.Errorf("finished note = %q, want the inside write left out", fin.Note)
			}
			if signals != 1 {
				t.Errorf("off-course signals = %d, want exactly 1", signals)
			}
			if !strings.Contains(buf.String(), "T1 r1 off-course (wrote outside the worktree: ") {
				t.Errorf("progress = %q, want an off-course line for the outside write", buf.String())
			}
		})
	}
}

// TestRunNoEditsOmitsWroteField checks a run with no edit/write tool calls
// omits the wrote field from its finished event (issue #163).
func TestRunNoEditsOmitsWroteField(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
// event for task, leaving it in flight (derived status dispatched). The
// dispatch is stamped now, so it is not an abandoned attempt that flywheel
// run marks lost (issue #402).
func planAndDispatch(t *testing.T, dir, task, brief string) {
	t.Helper()
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: task, Kind: "planned", Brief: brief}); err != nil {
		t.Fatalf("AppendEvent() planned %s: %v", task, err)
	}
	if err := AppendEvent(dir, Event{TS: now().UTC().Format(time.RFC3339Nano), Task: task, Kind: "dispatched", Attempt: "r1"}); err != nil {
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	for _, final := range []string{"landed", "passed"} {
		t.Run(final, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Init(dir, false); err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", "a.go, shared.go"))
			if final == "landed" {
				if err := AppendEvent(dir, Event{TS: now().UTC().Add(time.Second).Format(time.RFC3339Nano), Task: "T1", Kind: "landed"}); err != nil {
					t.Fatalf("AppendEvent() landed: %v", err)
				}
			} else {
				if err := AppendEvent(dir, Event{TS: now().UTC().Add(time.Second).Format(time.RFC3339Nano), Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector"}); err != nil {
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
	t.Parallel()
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

// TestCollisionNegated checks a negated owns: entry is part of the contract
// (issue #388): apps/inc/** with !apps/inc/wake.h does not collide with an
// in-flight task owning apps/inc/wake.h, in either direction, while
// apps/inc/** alone still does.
func TestCollisionNegated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		t1, t2     string
		wantCollid bool
	}{
		{"negated new side", "apps/inc/wake.h", "apps/inc/**, !apps/inc/wake.h", false},
		{"negated in-flight side", "apps/inc/**, !apps/inc/wake.h", "apps/inc/wake.h", false},
		{"no negation", "apps/inc/wake.h", "apps/inc/**", true},
		{"negation elsewhere", "apps/inc/wake.h", "apps/inc/**, !apps/inc/other.h", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Init(dir, false); err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			planAndDispatch(t, dir, "T1", ownsBrief(t, dir, "b1.txt", tc.t1))
			evs, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			got := ownsCollisionWith(dir, evs, "T2", strings.Split(strings.ReplaceAll(tc.t2, " ", ""), ","))
			if (got != nil) != tc.wantCollid {
				t.Fatalf("ownsCollisionWith(%q vs %q) = %+v, want collision %v", tc.t2, tc.t1, got, tc.wantCollid)
			}
			if got != nil && !strings.Contains(strings.Join(got.paths, ","), "apps/inc/wake.h") {
				t.Errorf("collision paths = %v, want apps/inc/wake.h", got.paths)
			}
		})
	}
}

// TestRunRefusesExclusiveCollision checks a dispatch whose exclusive: names a
// resource an in-flight task already holds is refused with the exclusive
// RuleRefusal, the message names the resource and the holding task, and no
// event is appended (issue #220).
func TestRunRefusesExclusiveCollision(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	for _, final := range []string{"landed", "passed"} {
		t.Run(final, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Init(dir, false); err != nil {
				t.Fatalf("Init() error = %v", err)
			}
			planAndDispatch(t, dir, "T1", exclBrief(t, dir, "b1.txt", "a.go", "db"))
			if final == "landed" {
				if err := AppendEvent(dir, Event{TS: now().UTC().Add(time.Second).Format(time.RFC3339Nano), Task: "T1", Kind: "landed"}); err != nil {
					t.Fatalf("AppendEvent() landed: %v", err)
				}
			} else {
				if err := AppendEvent(dir, Event{TS: now().UTC().Add(time.Second).Format(time.RFC3339Nano), Task: "T1", Kind: "inspected", Verdict: "pass", Session: "i1", Persona: "inspector"}); err != nil {
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestRunIncrementRecordedOnDispatched checks that Increment is recorded on
// the dispatched event and derived into TaskState (issue #83).
func TestRunIncrementRecordedOnDispatched(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("brief\n\n## Increments\n1. first\n2. second\n"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Increment: 2}); err != nil {
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
	if d.Increment != 2 {
		t.Errorf("dispatched event increment = %d, want 2", d.Increment)
	}
	if d.Attempt != "r1" {
		t.Errorf("dispatched event attempt = %q, want r1", d.Attempt)
	}
	st := Derive(evs)
	if len(st.Tasks) != 1 {
		t.Fatalf("Derive() produced %d tasks, want 1", len(st.Tasks))
	}
	if st.Tasks[0].Increment != 2 {
		t.Errorf("task state increment = %d, want 2", st.Tasks[0].Increment)
	}
}

// TestRunIncrementRejectsResumeAndDelta checks that --increment cannot be
// combined with --resume or --delta (issue #83).
func TestRunIncrementRejectsResumeAndDelta(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	writeDelta(t, dir, "T1")

	// Increment with Resume should error
	_, err := Run(dir, RunOptions{Task: "T1", Increment: 1, Resume: true})
	if err == nil {
		t.Error("Run with Increment and Resume = nil error, want an error")
	}

	// Increment with DeltaPath should error
	_, err = Run(dir, RunOptions{Task: "T1", Increment: 1, DeltaPath: filepath.Join(dir, ".flywheel", "briefs", "T1.delta.txt")})
	if err == nil {
		t.Error("Run with Increment and DeltaPath = nil error, want an error")
	}

	// No events should have been dispatched
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "dispatched" {
			t.Error("unexpected dispatched event; increment validation should have prevented it")
		}
	}
}

// TestRunLimitsPerHostRefused checks that a dispatch is refused when
// limits.per_host attempts are already in flight.
func TestRunLimitsPerHostRefused(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	cfg := simConfig(noPlanFixture(t, 5, false))
	cfg.Limits.PerHost = 1
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Add another task T2 in flight (dispatched).
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: now().UTC().Format(time.RFC3339Nano), Task: "T2", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	// Try to run T1; should be refused with limits RuleRefusal.
	_, err := Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "limits" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='limits'", err)
	}
	// Verify no dispatched event was recorded for T1.
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "dispatched" {
			t.Errorf("dispatched event recorded for T1 despite limits refusal: %v", e)
		}
	}
}

// TestRunLimitsPerHostAllowsBelowCap checks that a dispatch succeeds when
// fewer than limits.per_host attempts are in flight.
func TestRunLimitsPerHostAllowsBelowCap(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	cfg := simConfig(noPlanFixture(t, 5, false))
	cfg.Limits.PerHost = 2
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Add another task T2 in flight (dispatched).
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:01Z", Task: "T2", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	// Run T1; should succeed because only 1 of 2 allowed are in flight.
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

// TestRunBudgetReachedRefused checks that a dispatch is refused when
// the ledger's recorded spend has reached limits.budget.wave_cost_usd.
func TestRunBudgetReachedRefused(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	cfg := simConfig(noPlanFixture(t, 5, false))
	cfg.Limits.Budget = &Budget{WaveCostUSD: 0.5}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Add another task T2 with a finished event recording Cost 0.6 (exceeding budget).
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:01Z", Task: "T2", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:02Z", Task: "T2", Kind: "finished", Attempt: "r1", Cost: 0.6}); err != nil {
		t.Fatalf("AppendEvent() finished T2: %v", err)
	}
	// Try to run T1; should be refused with budget RuleRefusal.
	_, err := Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "budget" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='budget'", err)
	}
	// Verify no dispatched event was recorded for T1.
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "dispatched" {
			t.Errorf("dispatched event recorded for T1 despite budget refusal: %v", e)
		}
	}
}

// TestRunBudgetBelowCapDispatches checks that a dispatch succeeds when
// the ledger's recorded spend is below limits.budget.wave_cost_usd.
func TestRunBudgetBelowCapDispatches(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	cfg := simConfig(noPlanFixture(t, 5, false))
	cfg.Limits.Budget = &Budget{WaveCostUSD: 0.5}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// Add another task T2 with a finished event recording Cost 0.1 (below budget).
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:01Z", Task: "T2", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:02Z", Task: "T2", Kind: "finished", Attempt: "r1", Cost: 0.1}); err != nil {
		t.Fatalf("AppendEvent() finished T2: %v", err)
	}
	// Run T1; should succeed because budget is not yet reached.
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

// TestBreakerOpenAfterConsecutiveErrors checks that breakerOpen returns open=true
// when the newest b.Errors finished events for a model all have reason "error".
func TestBreakerOpenAfterConsecutiveErrors(t *testing.T) {
	t.Parallel()
	now, _ := time.Parse(time.RFC3339, "2026-09-12T00:00:00Z")
	events := []Event{
		{TS: "2026-09-12T23:58:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "error"},
		{TS: "2026-09-12T23:59:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "error"},
	}
	b := Breaker{Errors: 2, Cooldown: "10m"}
	open, until := breakerOpen(events, "m", b, now)
	if !open {
		t.Errorf("breakerOpen() open = %v, want true", open)
	}
	expectedUntil, _ := time.Parse(time.RFC3339, "2026-09-12T23:59:00Z")
	expectedUntil = expectedUntil.Add(10 * time.Minute)
	if until != expectedUntil {
		t.Errorf("breakerOpen() until = %v, want %v", until, expectedUntil)
	}
}

// TestBreakerClosedWhenLatestSucceeded checks that breakerOpen returns open=false
// when the latest finished event for a model has reason other than "error".
func TestBreakerClosedWhenLatestSucceeded(t *testing.T) {
	t.Parallel()
	now, _ := time.Parse(time.RFC3339, "2026-09-12T00:00:00Z")
	events := []Event{
		{TS: "2026-09-12T23:58:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "error"},
		{TS: "2026-09-12T23:59:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "error"},
		{TS: "2026-09-12T23:59:30Z", Task: "T1", Kind: "finished", Model: "m", Reason: "stop"},
	}
	b := Breaker{Errors: 2, Cooldown: "10m"}
	open, _ := breakerOpen(events, "m", b, now)
	if open {
		t.Errorf("breakerOpen() open = %v, want false", open)
	}
}

// TestBreakerIgnoresRateLimit checks that a rate-limited finish is not a
// provider error: rate limits alone never open the breaker, and one between
// errors neither counts nor breaks the error streak (issue #380).
func TestBreakerIgnoresRateLimit(t *testing.T) {
	t.Parallel()
	now, _ := time.Parse(time.RFC3339, "2026-09-13T00:00:00Z")
	b := Breaker{Errors: 2, Cooldown: "10m"}
	limits := []Event{
		{TS: "2026-09-12T23:58:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "rate-limited"},
		{TS: "2026-09-12T23:59:00Z", Task: "T2", Kind: "finished", Model: "m", Reason: "rate-limited"},
	}
	if open, _ := breakerOpen(limits, "m", b, now); open {
		t.Errorf("breakerOpen(two rate limits) open = true, want false")
	}
	mixed := []Event{
		{TS: "2026-09-12T23:57:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "error"},
		{TS: "2026-09-12T23:58:00Z", Task: "T2", Kind: "finished", Model: "m", Reason: "rate-limited"},
		{TS: "2026-09-12T23:59:00Z", Task: "T3", Kind: "finished", Model: "m", Reason: "error"},
	}
	if open, _ := breakerOpen(mixed, "m", b, now); !open {
		t.Errorf("breakerOpen(error, rate-limited, error) open = false, want true")
	}
	oneError := []Event{
		{TS: "2026-09-12T23:58:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "rate-limited"},
		{TS: "2026-09-12T23:59:00Z", Task: "T2", Kind: "finished", Model: "m", Reason: "error"},
	}
	if open, _ := breakerOpen(oneError, "m", b, now); open {
		t.Errorf("breakerOpen(rate-limited, error) open = true, want false")
	}
}

// TestBreakerClosedAfterCooldown checks that breakerOpen returns open=false
// when the cooldown has passed since the newest error.
func TestBreakerClosedAfterCooldown(t *testing.T) {
	t.Parallel()
	now, _ := time.Parse(time.RFC3339, "2026-09-13T00:06:00Z")
	events := []Event{
		{TS: "2026-09-12T23:40:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "error"},
		{TS: "2026-09-12T23:45:00Z", Task: "T1", Kind: "finished", Model: "m", Reason: "error"},
	}
	b := Breaker{Errors: 2, Cooldown: "10m"}
	open, _ := breakerOpen(events, "m", b, now)
	if open {
		t.Errorf("breakerOpen() open = %v, want false", open)
	}
}

// TestBreakerIgnoresOtherModels checks that breakerOpen ignores errors for
// models other than the one being checked.
func TestBreakerIgnoresOtherModels(t *testing.T) {
	t.Parallel()
	now, _ := time.Parse(time.RFC3339, "2026-09-12T00:00:00Z")
	events := []Event{
		{TS: "2026-09-12T23:58:00Z", Task: "T1", Kind: "finished", Model: "other", Reason: "error"},
		{TS: "2026-09-12T23:59:00Z", Task: "T1", Kind: "finished", Model: "other", Reason: "error"},
	}
	b := Breaker{Errors: 2, Cooldown: "10m"}
	open, _ := breakerOpen(events, "m", b, now)
	if open {
		t.Errorf("breakerOpen() open = %v, want false", open)
	}
}

// TestBreakerHalfOpenAdmitsOneProbe checks that after the cooldown only one
// probe runs: while a dispatch of the model made after the newest error has
// not finished, the breaker stays open (#304 review).
func TestBreakerHalfOpenAdmitsOneProbe(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	b := Breaker{Errors: 2, Cooldown: "10m"}
	events := []Event{
		{TS: ts(-30 * time.Minute), Task: "A", Kind: "finished", Attempt: "r1", Model: "m", Reason: "error"},
		{TS: ts(-20 * time.Minute), Task: "B", Kind: "finished", Attempt: "r1", Model: "m", Reason: "error"},
	}
	if open, _ := breakerOpen(events, "m", b, now); open {
		t.Fatal("breaker open after the cooldown with no probe in flight, want half-open (closed for one probe)")
	}
	probe := append(events, Event{TS: ts(-1 * time.Minute), Task: "C", Kind: "dispatched", Attempt: "r1", Model: "m"})
	if open, _ := breakerOpen(probe, "m", b, now); !open {
		t.Error("breaker closed while a probe is in flight, want open")
	}
	finishedProbe := append(probe, Event{TS: ts(-30 * time.Second), Task: "C", Kind: "finished", Attempt: "r1", Model: "m", Reason: "stop"})
	if open, _ := breakerOpen(finishedProbe, "m", b, now); open {
		t.Error("breaker open after the probe succeeded, want closed")
	}
}

// TestRunBreakerRefusesDispatch checks that Run refuses a dispatch when the
// circuit breaker for the model is open due to consecutive provider errors.
func TestRunBreakerRefusesDispatch(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	fixture := noPlanFixture(t, 5, false)
	cfg := simConfig(fixture)
	cfg.Limits.Breaker = &Breaker{Errors: 2, Cooldown: "1h"}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := time.Now()
	// Add another task T2 with two recent finished events with reason "error" for the same model.
	t1 := now.Add(-2 * time.Minute)
	t2 := now.Add(-1 * time.Minute)
	if err := AppendEvent(dir, Event{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() planned T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t1.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r1", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r2", Model: model}); err != nil {
		t.Fatalf("AppendEvent() dispatched T2 r2: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: t2.Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r2", Model: model, Reason: "error"}); err != nil {
		t.Fatalf("AppendEvent() finished T2 r2: %v", err)
	}
	// Verify the events were written
	readEvs, rerr := ReadEvents(dir)
	if rerr != nil {
		t.Fatalf("ReadEvents() error = %v", rerr)
	}
	var errorCount int
	for _, e := range readEvs {
		if e.Kind == "finished" && e.Model == model && e.Reason == "error" {
			errorCount++
		}
	}
	if errorCount < 2 {
		t.Fatalf("expected at least 2 finished events with reason=error for model %q, got %d", model, errorCount)
	}
	// Try to run T1; should be refused with breaker RuleRefusal.
	var err error
	_, err = Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "breaker" {
		t.Errorf("Run() error = %v, want RuleRefusal with Rule='breaker'", err)
	}
	// Verify no dispatched event was recorded for T1.
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "dispatched" {
			t.Errorf("dispatched event recorded for T1 despite breaker refusal: %v", e)
		}
	}
}

// TestRunRefusesPausedModel checks that a fresh Run refuses a model another
// unit's rate limit paused until its reset (issue #383).
func TestRunRefusesPausedModel(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	cfg := simConfig(noPlanFixture(t, 5, false))
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	model := cfg.DefaultWorker().Model
	now := time.Now()
	reset := now.Add(30 * time.Minute).UTC().Truncate(time.Second)
	for _, e := range []Event{
		{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "planned", Brief: "b.txt"},
		{TS: now.Add(-2 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "dispatched", Attempt: "r1", Model: model},
		{TS: now.Add(-1 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "finished", Attempt: "r1", Model: model, Reason: "rate-limited", ResetAt: reset.Format(time.RFC3339)},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent(%s %s) error = %v", e.Task, e.Kind, err)
		}
	}
	_, err := Run(dir, RunOptions{Task: "T1"})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "rate-limit" {
		t.Fatalf("Run() error = %v, want RuleRefusal with Rule='rate-limit'", err)
	}
	if !strings.Contains(rf.Fix, model+" is paused by a rate limit until") || !strings.Contains(rf.Fix, reset.Format(time.RFC3339)) {
		t.Errorf("Fix = %q, want the model and the reset %s", rf.Fix, reset.Format(time.RFC3339))
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "dispatched" {
			t.Errorf("dispatched event recorded for T1 despite the pause: %v", e)
		}
	}
}

// TestRunLimitsPerHostCountsSameTask checks a second fresh run of a task that
// is already running counts toward limits.per_host: it is another attempt on
// the host (#293 review).
func TestRunLimitsPerHostCountsSameTask(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	cfg := simConfig(noPlanFixture(t, 5, false))
	cfg.Limits.PerHost = 1
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: now().UTC().Format(time.RFC3339Nano), Task: "T1", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched T1: %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T1"})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "limits" {
		t.Fatalf("Run() error = %v, want RuleRefusal with Rule='limits' for a second attempt of a running task", err)
	}
}

// TestRunIncrementMissingFromBriefRefused checks a brief without increment N
// is refused before anything is dispatched (#295 review).
func TestRunIncrementMissingFromBriefRefused(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	_, err := Run(dir, RunOptions{Task: "T1", Increment: 1})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "increment" {
		t.Fatalf("Run() error = %v, want a RuleRefusal with Rule increment", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Kind == "dispatched" {
			t.Fatalf("dispatched event recorded despite the refusal: %+v", e)
		}
	}
}

// TestBriefHasIncrement checks where an increment may be defined: a numbered
// item inside an Increments section, or an "Increment N" heading.
func TestBriefHasIncrement(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		n    int
		want bool
	}{
		{"list item", "# T\n## Increments\n1. a\n2. b\n", 2, true},
		{"missing item", "# T\n## Increments\n1. a\n2. b\n", 3, false},
		{"paren item", "## Increments\n- 1) a\n", 1, true},
		{"heading", "## Increment 2: wire the flag\ntext\n", 2, true},
		{"heading prefix only", "## Increment 20\n", 2, false},
		{"list outside section", "## Steps\n1. a\n", 1, false},
		{"section ended", "## Increments\n1. a\n## Notes\n2. b\n", 2, false},
		{"crlf", "## Increments" + "\r\n1. a\r\n", 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := briefHasIncrement(c.text, c.n); got != c.want {
				t.Errorf("briefHasIncrement(%q, %d) = %v, want %v", c.text, c.n, got, c.want)
			}
		})
	}
}

// TestRunNoResultLineIsError checks a worker that completes steps and then
// exits non-zero with no result line (killed or crashed) finishes with reason
// error and a note naming its exit code, and its written owned file is
// checkpointed. It sets process-wide env, so it does not run in parallel.
func TestRunNoResultLineIsError(t *testing.T) {
	dir := worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a // partial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
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
	const session = "ses_killed_001"
	stream := fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", session)
	for range 3 {
		stream += fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"a.go"}}]}}`+"\n", session)
	}
	p := filepath.Join(t.TempDir(), "killed.jsonl")
	if err := os.WriteFile(p, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeClaudeEnv, p)
	t.Setenv(fakeClaudeExitEnv, "3")
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Worktree: true, Progress: &buf})
	if err != nil || res.Reason != "error" {
		t.Fatalf("Run() = %+v, %v; want reason error; progress:\n%s", res, err, buf.String())
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var fin Event
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "finished" {
			fin = e
		}
	}
	if fin.Reason != "error" || fin.Steps == 0 {
		t.Errorf("finished reason/steps = %q/%d, want error and some steps", fin.Reason, fin.Steps)
	}
	if want := "worker exited rc=3 with no result line (killed or crashed)"; !strings.Contains(fin.Note, want) {
		t.Errorf("finished note = %q, want it to contain %q", fin.Note, want)
	}
	if fin.Checkpoint == "" {
		t.Errorf("finished = %+v, want a checkpoint of a.go", fin)
	}
}

// fakeClaudeEnv names the stream file the test binary replays when it runs
// as a fake claude (see TestMain and TestRunPlanBeforeTool).
const fakeClaudeEnv = "FLYWHEEL_TEST_FAKE_CLAUDE_STREAM"

// fakeClaudeExitEnv, when set, is the exit code the fake claude exits with
// after replaying its stream (default 0).
const fakeClaudeExitEnv = "FLYWHEEL_TEST_FAKE_CLAUDE_EXIT"

// fakeClaudeStdinEnv names the file the fake claude saves its stdin to.
const fakeClaudeStdinEnv = "FLYWHEEL_TEST_FAKE_CLAUDE_STDIN"

// TestMain lets the test binary stand in for the claude CLI: copied to a
// PATH directory as claude and started with fakeClaudeEnv set, it prints that
// file as its stream-json output and exits 0. It first drains its stdin, where
// claude reads its prompt (issue #427), so the pipe never blocks, and saves
// it to the fakeClaudeStdinEnv file when that is set.
func TestMain(m *testing.M) {
	if stream := os.Getenv(fakeClaudeEnv); stream != "" && strings.HasPrefix(filepath.Base(os.Args[0]), "claude") {
		prompt, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if p := os.Getenv(fakeClaudeStdinEnv); p != "" {
			if err := os.WriteFile(p, prompt, 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		b, err := os.ReadFile(stream)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(b)
		code, _ := strconv.Atoi(os.Getenv(fakeClaudeExitEnv))
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// limitRun runs RunResumingLimits on a claude worker whose first attempt hits
// a rate limit resetting at 10:20am Los Angeles (now is 10:00 there) and whose
// second attempt stops cleanly. It returns the result, the injected sleeps,
// the dispatch requests and the events (issue #380).
func limitRun(t *testing.T, retries *int, maxWait string) (Result, []time.Duration, []RunRequest, []Event) {
	dir := setupTask(t)
	brief := "owns: a.go\nneeds: none\ngate: go vet ./...\n\n# TASK: x\n"
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}},
		Limits: Limits{RateLimitRetries: retries, RateLimitMaxWait: maxWait}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
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

	const session = "ses_limit_001"
	head := fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", session) +
		fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"a.go"}}]}}`+"\n", session)
	streams := []string{
		head + fmt.Sprintf(`{"type":"result","subtype":"success","stop_reason":"stop_sequence","is_error":true,"api_error_status":429,"session_id":%q,"result":"You've hit your session limit · resets 10:20am (America/Los_Angeles)"}`+"\n", session),
		head + fmt.Sprintf(`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":%q,"total_cost_usd":0.01}`+"\n", session),
	}
	var paths []string
	for i, s := range streams {
		p := filepath.Join(t.TempDir(), fmt.Sprintf("stream%d.jsonl", i+1))
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatalf("write stream: %v", err)
		}
		paths = append(paths, p)
	}
	t.Setenv(fakeClaudeEnv, paths[0])
	var reqs []RunRequest
	commandHook = func(r RunRequest) {
		reqs = append(reqs, r)
		_ = os.Setenv(fakeClaudeEnv, paths[min(len(reqs), len(paths))-1])
	}
	defer func() { commandHook = nil }()

	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	var sleeps []time.Duration
	var buf bytes.Buffer
	res, err := RunResumingLimits(dir, RunOptions{Task: "T1", Progress: &buf},
		func(d time.Duration) { sleeps = append(sleeps, d) },
		func() time.Time { return time.Date(2026, 9, 23, 10, 0, 0, 0, la) })
	if err != nil {
		t.Fatalf("RunResumingLimits() error = %v; progress:\n%s", err, buf.String())
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	return res, sleeps, reqs, evs
}

// TestResumeAbandonedJob checks that an abandoned-job attempt is resumed
// once, without a wait, on the same session with a job delta naming the
// killed command, and that a second abandoned-job is returned as is (issue
// #390).
func TestResumeAbandonedJob(t *testing.T) {
	// not parallel: t.Setenv PATH to a fake claude
	const session = "ses_job_001"
	head := fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", session)
	bg := fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"tool_use","id":"toolu_bg","name":"Bash","input":{"command":"go test ./...","run_in_background":true}}]}}`+"\n", session) +
		fmt.Sprintf(`{"type":"user","session_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"toolu_bg","content":"Command running in background with ID: bsh01"}]}}`+"\n", session)
	fg := fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"tool_use","id":"toolu_fg","name":"Bash","input":{"command":"go test ./..."}}]}}`+"\n", session)
	end := fmt.Sprintf(`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":%q,"total_cost_usd":0.01}`+"\n", session)
	run := func(t *testing.T, streams []string) (Result, []RunRequest) {
		dir := setupTask(t)
		brief := "owns: a.go\nneeds: none\ngate: go vet ./...\n\n# TASK: x\n"
		if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
			t.Fatalf("write brief: %v", err)
		}
		cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
		if err := WriteConfig(dir, cfg); err != nil {
			t.Fatalf("WriteConfig() error = %v", err)
		}
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
		var paths []string
		for i, s := range streams {
			p := filepath.Join(t.TempDir(), fmt.Sprintf("stream%d.jsonl", i+1))
			if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
				t.Fatalf("write stream: %v", err)
			}
			paths = append(paths, p)
		}
		t.Setenv(fakeClaudeEnv, paths[0])
		var reqs []RunRequest
		commandHook = func(r RunRequest) {
			reqs = append(reqs, r)
			_ = os.Setenv(fakeClaudeEnv, paths[min(len(reqs), len(paths))-1])
		}
		defer func() { commandHook = nil }()
		var buf bytes.Buffer
		res, err := RunResumingLimits(dir, RunOptions{Task: "T1", Progress: &buf},
			func(d time.Duration) { t.Errorf("sleep(%s) called; an abandoned-job resume never waits", d) },
			time.Now)
		if err != nil {
			t.Fatalf("RunResumingLimits() error = %v; progress:\n%s", err, buf.String())
		}
		return res, reqs
	}
	t.Run("resumes once", func(t *testing.T) {
		res, reqs := run(t, []string{head + bg + end, head + fg + end})
		if len(reqs) != 2 || reqs[0].Resume || !reqs[1].Resume || reqs[1].Session != session {
			t.Fatalf("dispatch requests = %+v, want a fresh run then a resume of %s", reqs, session)
		}
		delta, err := os.ReadFile(reqs[1].PromptFile)
		if err != nil || !strings.HasSuffix(filepath.ToSlash(reqs[1].PromptFile), ".flywheel/briefs/T1.job-1.txt") {
			t.Fatalf("resume prompt %s: %v, want .flywheel/briefs/T1.job-1.txt", reqs[1].PromptFile, err)
		}
		want := "owns: a.go\nneeds: none\ngate: go vet ./...\n\n" + fmt.Sprintf(jobContinue, "go test ./...")
		if string(delta) != want {
			t.Errorf("delta = %q, want %q", delta, want)
		}
		if res.Reason != "stop" {
			t.Errorf("result = %+v, want the resume's stop", res)
		}
	})
	t.Run("second abandoned-job", func(t *testing.T) {
		res, reqs := run(t, []string{head + bg + end, head + bg + end, head + fg + end})
		if len(reqs) != 2 || res.Reason != "abandoned-job" {
			t.Errorf("requests = %d, result = %+v, want 2 dispatches and abandoned-job returned", len(reqs), res)
		}
	})
}

// finishedOf returns the finished events, and whether any signal was recorded.
func finishedOf(evs []Event) (fins []Event, signaled bool) {
	for _, e := range evs {
		switch e.Kind {
		case "finished":
			fins = append(fins, e)
		case "signal":
			signaled = true
		}
	}
	return fins, signaled
}

// TestRunRetriesRateLimit checks that a rate-limited attempt is recorded as
// rate-limited with its reset, waits (injected) until the reset plus a
// minute, and resumes the same session with a continue delta; retries 0 or a
// reset beyond the max wait does not resume (issue #380).
func TestRunRetriesRateLimit(t *testing.T) {
	// not parallel: limitRun sets PATH to a fake claude
	t.Run("resumes", func(t *testing.T) {
		res, sleeps, reqs, evs := limitRun(t, nil, "")
		if len(sleeps) != 1 || sleeps[0] != 21*time.Minute {
			t.Errorf("sleeps = %v, want one of 21m (the reset plus a minute)", sleeps)
		}
		fins, signaled := finishedOf(evs)
		if len(fins) != 2 || fins[0].Reason != "rate-limited" || fins[1].Reason != "stop" {
			t.Fatalf("finished events = %+v, want reasons rate-limited then stop", fins)
		}
		if !strings.Contains(fins[0].Note, "limit resets 10:20am (America/Los_Angeles)") {
			t.Errorf("rate-limited note = %q, want the reset", fins[0].Note)
		}
		if signaled {
			t.Errorf("a signal was recorded; a rate limit records none")
		}
		if len(reqs) != 2 || reqs[0].Resume || !reqs[1].Resume || reqs[1].Session != "ses_limit_001" {
			t.Fatalf("dispatch requests = %+v, want a fresh run then a resume of ses_limit_001", reqs)
		}
		delta, err := os.ReadFile(reqs[1].PromptFile)
		if err != nil || !strings.HasSuffix(filepath.ToSlash(reqs[1].PromptFile), ".flywheel/briefs/T1.limit-1.txt") {
			t.Fatalf("resume prompt %s: %v, want .flywheel/briefs/T1.limit-1.txt", reqs[1].PromptFile, err)
		}
		if want := "owns: a.go\nneeds: none\ngate: go vet ./...\n\n" + limitContinue; string(delta) != want {
			t.Errorf("delta = %q, want %q", delta, want)
		}
		if res.Reason != "stop" || res.Attempt != "c1" {
			t.Errorf("result = %+v, want the resume's stop on c1", res)
		}
	})
	t.Run("retries 0", func(t *testing.T) {
		zero := 0
		res, sleeps, reqs, evs := limitRun(t, &zero, "")
		fins, _ := finishedOf(evs)
		if len(sleeps) != 0 || len(reqs) != 1 || len(fins) != 1 || res.Reason != "rate-limited" {
			t.Errorf("sleeps %v, %d dispatches, %d finished, result %+v; want no resume", sleeps, len(reqs), len(fins), res)
		}
		if res.ResetText != "10:20am (America/Los_Angeles)" || ExitCode(res) != 4 {
			t.Errorf("ResetText %q, exit %d; want the reset and exit 4", res.ResetText, ExitCode(res))
		}
	})
	t.Run("beyond max wait", func(t *testing.T) {
		res, sleeps, reqs, _ := limitRun(t, nil, "10m")
		if len(sleeps) != 0 || len(reqs) != 1 || res.Reason != "rate-limited" {
			t.Errorf("sleeps %v, %d dispatches, result %+v; want no resume past the max wait", sleeps, len(reqs), res)
		}
	})
}

// runFakeClaudeStream runs task T1 on a fake claude worker that replays
// stream, and returns the task dir and the result.
func runFakeClaudeStream(t *testing.T, stream string) (string, Result) {
	t.Helper()
	dir := setupTask(t)
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
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
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	t.Setenv(fakeClaudeEnv, path)
	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return dir, res
}

// TestRunAbandonedJob: a clean stop that leaves a background Bash job
// uncollected finishes abandoned-job, names the command and records no
// signal; collecting it by the shell id its tool_result reported keeps the
// stop, and a background call whose result gives no id is not tracked
// (issue #390).
func TestRunAbandonedJob(t *testing.T) {
	// not parallel: runFakeClaudeStream sets PATH to a fake claude
	const session = "ses_bg_001"
	bg := fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"tool_use","id":"toolu_012eoA","name":"Bash","input":{"command":"go test ./...","run_in_background":true}}]}}`, session)
	result := fmt.Sprintf(`{"type":"user","session_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"toolu_012eoA","content":"Command running in background with ID: bzkt5tsmf. Output is being written to: C:\\tasks\\bzkt5tsmf.output"}]}}`, session)
	noID := fmt.Sprintf(`{"type":"user","session_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"toolu_012eoA","content":"started"}]}}`, session)
	collect := fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"tool_use","id":"toolu_out","name":"BashOutput","input":{"bash_id":"bzkt5tsmf"}}]}}`, session)
	build := func(lines ...string) string {
		var b strings.Builder
		fmt.Fprintf(&b, `{"type":"system","subtype":"init","session_id":%q}`+"\n", session)
		for _, line := range lines {
			b.WriteString(line + "\n")
		}
		fmt.Fprintf(&b, `{"type":"assistant","session_id":%q,"message":{"content":[{"type":"text","text":"I'll wait for the run's completion notification."}]}}`+"\n", session)
		fmt.Fprintf(&b, `{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":%q,"total_cost_usd":0.01}`+"\n", session)
		return b.String()
	}
	for _, tc := range []struct {
		name   string
		tools  []string
		reason string
	}{
		{"uncollected", []string{bg, result}, "abandoned-job"},
		{"collected", []string{bg, result, collect}, "stop"},
		{"no id", []string{bg, noID}, "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, res := runFakeClaudeStream(t, build(tc.tools...))
			if res.Reason != tc.reason {
				t.Errorf("Result.Reason = %q, want %q", res.Reason, tc.reason)
			}
			evs, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			var fin *Event
			for i := range evs {
				if evs[i].Kind == "finished" {
					fin = &evs[i]
				}
				if tc.reason == "abandoned-job" && evs[i].Kind == "signal" {
					t.Errorf("signal %q recorded, want none", evs[i].Signal)
				}
			}
			if fin == nil || fin.Reason != tc.reason {
				t.Fatalf("finished = %+v, want reason %s", fin, tc.reason)
			}
			if tc.reason == "abandoned-job" {
				if !strings.Contains(fin.Note, "background job never collected: go test ./...") {
					t.Errorf("finished note = %q, want it to name the job", fin.Note)
				}
				if len(res.Jobs) != 1 || res.Jobs[0] != "go test ./..." || ExitCode(res) != 4 {
					t.Errorf("Jobs = %v, ExitCode = %d, want [go test ./...] and 4", res.Jobs, ExitCode(res))
				}
			}
		})
	}
}

// TestRunPlanBeforeTool checks a claude run whose first assistant message
// holds the PLAN text beside a tool_use records worker_plan and, past step
// 20, no no-plan (issue #360).
func TestRunPlanBeforeTool(t *testing.T) {
	// not parallel: t.Setenv PATH to a fake claude
	dir := setupTask(t)
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	binDir := t.TempDir()
	// Named exactly "claude" (plus ".exe" on Windows): the test binary's own
	// extension is ".test" on Linux and macOS, which PATH lookup never finds.
	fake := filepath.Join(binDir, "claude")
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	if err := linkOrCopy(exe, fake); err != nil {
		t.Fatalf("install fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	const session = "ses_plan_tool_001"
	plan, _ := json.Marshal("PLAN files-to-read: a.go\nPLAN files-to-change: a.go\nPLAN order: edit\nPLAN checks: go test")
	tool := `{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}`
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"system","subtype":"init","session_id":%q}`+"\n", session)
	fmt.Fprintf(&b, `{"type":"assistant","session_id":%q,"message":{"content":[{"type":"text","text":%s},%s]}}`+"\n", session, plan, tool)
	for i := 0; i < 21; i++ {
		fmt.Fprintf(&b, `{"type":"assistant","session_id":%q,"message":{"content":[%s]}}`+"\n", session, tool)
	}
	fmt.Fprintf(&b, `{"type":"result","subtype":"success","stop_reason":"stop_sequence","session_id":%q,"total_cost_usd":0.01}`+"\n", session)
	stream := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(stream, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	t.Setenv(fakeClaudeEnv, stream)

	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	plans := 0
	for _, e := range evs {
		switch e.Kind {
		case "worker_plan":
			plans++
		case "no-plan":
			t.Errorf("events include a no-plan event when the PLAN came beside a tool call: %v", e)
		}
	}
	if plans != 1 {
		t.Errorf("worker_plan events = %d, want exactly 1; events = %v", plans, evs)
	}
}

// TestRunLargePrompt checks a 100 KB brief, over Windows' ~32K command-line
// cap, dispatches to claude and reaches it whole on stdin (issue #427).
func TestRunLargePrompt(t *testing.T) {
	// not parallel: t.Setenv PATH to a fake claude
	dir := setupTask(t)
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	brief := "one line brief\n" + strings.Repeat("a long brief line with & | ^ %PATH% in it\n", 2500)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
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
	const session = "ses_large_001"
	stream := filepath.Join(t.TempDir(), "stream.jsonl")
	lines := fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", session) +
		fmt.Sprintf(`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":%q,"total_cost_usd":0.01}`+"\n", session)
	if err := os.WriteFile(stream, []byte(lines), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	t.Setenv(fakeClaudeEnv, stream)
	got := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv(fakeClaudeStdinEnv, got)

	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v; progress:\n%s", err, buf.String())
	}
	prompt, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("fake claude saved no stdin: %v", err)
	}
	if len(brief) < 100*1024 || string(prompt) != freshMessage+"\n"+brief {
		t.Errorf("stdin = %d bytes (%.80q), want freshMessage then the whole %d-byte brief", len(prompt), prompt, len(brief))
	}
}

// TestRunRecordsCommands: the finished event carries the shell commands a
// claude worker ran, in order, and names the gate it never ran (issue #365).
func TestRunRecordsCommands(t *testing.T) {
	// not parallel: t.Setenv PATH
	dir := setupTask(t)
	brief := "gate: go build ./...\ngate: go test -count=1 ./...\n\n# TASK\n"
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
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

	const session = "ses_commands_001"
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"system","subtype":"init","session_id":%q}`+"\n", session)
	for _, block := range []string{
		`{"type":"tool_use","name":"Write","input":{"file_path":"a.go"}}`,
		`{"type":"tool_use","name":"Bash","input":{"command":"go  build ./... ; echo \"exit=$?\""}}`,
		`{"type":"tool_use","name":"Bash","input":{"command":"go vet ./..."}}`,
	} {
		fmt.Fprintf(&b, `{"type":"assistant","session_id":%q,"message":{"content":[%s]}}`+"\n", session, block)
	}
	fmt.Fprintf(&b, `{"type":"result","subtype":"success","stop_reason":"stop_sequence","session_id":%q,"total_cost_usd":0.01}`+"\n", session)
	stream := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(stream, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	t.Setenv(fakeClaudeEnv, stream)

	var buf bytes.Buffer
	res, err := Run(dir, RunOptions{Task: "T1", Progress: &buf})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Reason != "stop" {
		t.Fatalf("Run() reason = %q, want stop; progress:\n%s", res.Reason, buf.String())
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var fin *Event
	for i := range evs {
		if evs[i].Kind == "finished" {
			fin = &evs[i]
		}
	}
	if fin == nil {
		t.Fatalf("no finished event; events = %v", evs)
	}
	wantCmds := []string{`go  build ./... ; echo "exit=$?"`, "go vet ./..."}
	if !reflect.DeepEqual(fin.Commands, wantCmds) {
		t.Errorf("finished Commands = %q, want %q", fin.Commands, wantCmds)
	}
	if !reflect.DeepEqual(fin.GatesUnrun, []string{"2"}) {
		t.Errorf("finished GatesUnrun = %q, want [2]", fin.GatesUnrun)
	}
	if !strings.Contains(fin.Note, "gates never run by the worker: 2") {
		t.Errorf("finished Note = %q, want it to name gate 2", fin.Note)
	}
	if !strings.Contains(buf.String(), "T1 "+fin.Attempt+" never ran gate(s) 2") {
		t.Errorf("progress lacks the never-ran line:\n%s", buf.String())
	}
}

// TestGatesUnrun unit-tests gateRan (issue #365).
func TestGatesUnrun(t *testing.T) {
	t.Parallel()
	long := "for f in $(git ls-files -m -o --exclude-standard -- '*.go'); do gofmt -l \"$f\"; done"
	cases := []struct {
		name, gate string
		cmds       []string
		want       bool
	}{
		{"exact", "go build ./...", []string{"go build ./..."}, true},
		{"echo suffix", "go vet ./...", []string{`go vet ./... ; echo "exit=$?"`}, true},
		{"whitespace", "go  test\t-count=1 ./...", []string{"cd x &&  go test -count=1   ./..."}, true},
		{"long prefix", long, []string{long[:45] + " | head"}, true},
		{"not run", "go test ./...", []string{"go build ./...", "go vet ./..."}, false},
		{"no commands", "go build ./...", nil, false},
	}
	for _, c := range cases {
		if got := gateRan(c.gate, c.cmds); got != c.want {
			t.Errorf("%s: gateRan(%q, %q) = %v, want %v", c.name, c.gate, c.cmds, got, c.want)
		}
	}
}

// TestRunIgnoresLostCollision checks an attempt abandoned three days ago —
// no lease, no run file — does not block a dispatch owning the same file:
// flywheel run marks it lost (reason idle) before the owns check (issue #402).
func TestRunIgnoresLostCollision(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	old := now().UTC().Add(-72 * time.Hour)
	if err := AppendEvent(dir, Event{TS: old.Format(time.RFC3339Nano), Task: "T1", Kind: "planned", Brief: ownsBrief(t, dir, "b1.txt", "a.txt")}); err != nil {
		t.Fatalf("AppendEvent() planned T1: %v", err)
	}
	if err := AppendEvent(dir, Event{TS: old.Add(time.Second).Format(time.RFC3339Nano), Task: "T1", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatalf("AppendEvent() dispatched T1: %v", err)
	}
	planOnly(t, dir, "T2", ownsBrief(t, dir, "b2.txt", "a.txt"))
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T2"}); err != nil {
		t.Fatalf("Run() error = %v, want a normal dispatch (T1 is abandoned)", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	lost := 0
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "lost" {
			lost++
			if e.Attempt != "r1" || e.Reason != "idle" || !strings.Contains(e.Note, "no live lease") {
				t.Errorf("lost event = %+v, want attempt r1, reason idle, a no-live-lease note", e)
			}
		}
	}
	if lost != 1 {
		t.Errorf("T1 lost events = %d, want 1", lost)
	}
	for _, ts := range Derive(evs).Tasks {
		if ts.ID == "T1" && ts.Status != "lost" {
			t.Errorf("T1 status = %q, want lost", ts.Status)
		}
	}
}

// TestRunCommitsAttempt checks that a clean stop of a --worktree run is
// committed by flywheel on fw/<task> (issue #391): the owned change lands in
// the commit named on the finished event, the unowned one is left and named
// on the note, and no git-write signal is raised for flywheel's own commit.
func TestRunCommitsAttempt(t *testing.T) {
	t.Parallel()
	dir := worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.go": "package a\n", "notes.txt": "not owned\n"} {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var fin *Event
	for i := range events {
		if events[i].Kind == "signal" && events[i].Signal == "git-write" {
			t.Errorf("git-write signal for flywheel's own commit: %+v", events[i])
		}
		if events[i].Task == "T1" && events[i].Kind == "finished" {
			fin = &events[i]
		}
	}
	if fin == nil {
		t.Fatal("no finished event")
	}
	if fin.Reason != "stop" || !CommitOK(fin.Commit) {
		t.Fatalf("finished reason=%q commit=%q, want stop with a commit", fin.Reason, fin.Commit)
	}
	if !strings.Contains(fin.Note, "left uncommitted (outside owns): notes.txt") {
		t.Errorf("note = %q, want the unowned path named", fin.Note)
	}
	if got := strings.TrimSpace(git(t, wt, []string{"rev-parse", "refs/heads/fw/T1"})); got != fin.Commit {
		t.Errorf("fw/T1 = %s, finished commit = %s", got, fin.Commit)
	}
	if files := strings.TrimSpace(git(t, wt, []string{"show", "--name-only", "--format=", fin.Commit})); files != "a.go" {
		t.Errorf("committed files = %q, want a.go", files)
	}
	if msg := git(t, wt, []string{"log", "-1", "--format=%B", fin.Commit}); !strings.Contains(msg, "Flywheel-Task: T1") {
		t.Errorf("commit message %q lacks the Flywheel-Task trailer", msg)
	}
}

// TestRunDetectsWorkerGitAdd checks a worker `git add` the guard never saw
// (#423) raises a git-write signal naming the staged path, and that flywheel
// unstages it BEFORE its own attempt commit (#391): the commit still lands the
// owned change and flywheel's index refresh is not charged to the worker.
func TestRunDetectsWorkerGitAdd(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	commandHook = func(RunRequest) {
		if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a\n"), 0o644); err != nil {
			return
		}
		cmd := exec.Command("git", "add", "a.go")
		cmd.Dir = wt
		_ = cmd.Run()
	}
	t.Cleanup(func() { commandHook = nil })
	if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var fin *Event
	signals := 0
	for i := range events {
		if events[i].Kind == "signal" && events[i].Signal == "git-write" {
			signals++
		}
		if events[i].Task == "T1" && events[i].Kind == "finished" {
			fin = &events[i]
		}
	}
	if fin == nil {
		t.Fatal("no finished event")
	}
	if signals != 1 {
		t.Errorf("git-write signals = %d, want 1", signals)
	}
	for _, want := range []string{"index: staged a.go", "index restored: a.go"} {
		if !strings.Contains(fin.Note, want) {
			t.Errorf("note = %q, want %q", fin.Note, want)
		}
	}
	if fin.Reason != "stop" || !CommitOK(fin.Commit) {
		t.Fatalf("finished reason=%q commit=%q, want stop with flywheel's commit", fin.Reason, fin.Commit)
	}
	if files := strings.TrimSpace(git(t, wt, []string{"show", "--name-only", "--format=", fin.Commit})); files != "a.go" {
		t.Errorf("committed files = %q, want a.go", files)
	}
}

// TestRunRestoresIndex checks that after a run whose worker staged a file the
// index is clean again and the file's content is still in the working tree
// (#423).
func TestRunRestoresIndex(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := setupTask(t)
	gitRepoWithCommit(t, dir)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatal(err)
	}
	commandHook = func(RunRequest) {
		if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("kept\n"), 0o644); err != nil {
			return
		}
		cmd := exec.Command("git", "add", "staged.txt")
		cmd.Dir = dir
		_ = cmd.Run()
	}
	t.Cleanup(func() { commandHook = nil })
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if cached := strings.TrimSpace(git(t, dir, []string{"diff", "--cached", "--name-only"})); cached != "" {
		t.Errorf("git diff --cached = %q after the run, want empty", cached)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "staged.txt")); err != nil || string(b) != "kept\n" {
		t.Errorf("staged.txt = %q, %v; want its content kept", b, err)
	}
}

// TestRunRefusedDuringQuietGate checks a dispatch is refused with rule quiet,
// naming the running quiet gate, while quiet.lock is held by a live process,
// and records nothing (issue #411). A holder on another host cannot be probed
// and counts as live.
func TestRunRefusedDuringQuietGate(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(noPlanFixture(t, 5, false))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	p := quietLockPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	lock := `{"task":"T9","gate":"3","pid":4242,"host":"another-host","started_at":"2026-09-24T00:00:00Z"}`
	if err := os.WriteFile(p, []byte(lock), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(dir, RunOptions{Task: "T1"})
	var rf *RuleRefusal
	if !errors.As(err, &rf) || rf.Rule != "quiet" {
		t.Fatalf("Run() error = %v, want RuleRefusal with Rule='quiet'", err)
	}
	if want := "a quiet gate (T9 gate 3) is running on this host; dispatch after it ends"; rf.Fix != want {
		t.Errorf("Fix = %q, want %q", rf.Fix, want)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Task == "T1" && e.Kind == "dispatched" {
			t.Errorf("dispatched event recorded for T1 despite the quiet refusal: %v", e)
		}
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run() after the quiet gate ended error = %v", err)
	}
}

// TestResetFromEvent: a rate-limited finish takes reset_at from the stream's
// latest rate_limit_event (the exact epoch) over the parsed result clause,
// and every finish that saw an event records its utilization, reset and
// window; with no event the parsed clause still decides (issue #417).
func TestResetFromEvent(t *testing.T) {
	// not parallel: runFakeClaudeStream sets PATH to a fake claude
	const session = "ses_rle_001"
	event := func(util float64) string {
		return fmt.Sprintf(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1790304000,"rateLimitType":"five_hour","utilization":%v},"session_id":%q}`, util, session)
	}
	limited := fmt.Sprintf(`{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"result":"You've hit your session limit · resets 10:20am (America/Los_Angeles)","session_id":%q}`, session)
	clean := fmt.Sprintf(`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":%q,"total_cost_usd":0.01}`, session)
	build := func(lines ...string) string {
		var b strings.Builder
		fmt.Fprintf(&b, `{"type":"system","subtype":"init","session_id":%q}`+"\n", session)
		fmt.Fprintf(&b, `{"type":"assistant","session_id":%q,"message":{"content":[{"type":"text","text":"working"}]}}`+"\n", session)
		for _, line := range lines {
			b.WriteString(line + "\n")
		}
		return b.String()
	}
	const exact = "2026-09-25T02:40:00Z" // 1790304000
	for _, tc := range []struct {
		name      string
		stream    string
		reason    string
		resetAt   string // "" = must be empty; "parsed" = non-empty and not exact
		util      float64
		limitAt   string
		limitWind string
	}{
		{"rate-limited with event", build(event(0.91), event(0.96), limited), "rate-limited", exact, 0.96, exact, "five_hour"},
		{"rate-limited without event", build(limited), "rate-limited", "parsed", 0, "", ""},
		{"clean with event", build(event(0.42), clean), "", "", 0.42, exact, "five_hour"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, res := runFakeClaudeStream(t, tc.stream)
			evs, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			fins, _ := finishedOf(evs)
			if len(fins) != 1 {
				t.Fatalf("finished events = %+v, want one", fins)
			}
			f := fins[0]
			if tc.reason != "" && f.Reason != tc.reason {
				t.Errorf("Reason = %q, want %q", f.Reason, tc.reason)
			}
			switch tc.resetAt {
			case "parsed":
				if f.ResetAt == "" || f.ResetAt == exact {
					t.Errorf("ResetAt = %q, want the parsed clause", f.ResetAt)
				}
			default:
				if f.ResetAt != tc.resetAt || res.ResetAt != tc.resetAt {
					t.Errorf("ResetAt = %q (result %q), want %q", f.ResetAt, res.ResetAt, tc.resetAt)
				}
			}
			if f.LimitUtilization != tc.util || f.LimitResetAt != tc.limitAt || f.LimitWindow != tc.limitWind {
				t.Errorf("limit fields = %v %q %q, want %v %q %q", f.LimitUtilization, f.LimitResetAt, f.LimitWindow, tc.util, tc.limitAt, tc.limitWind)
			}
		})
	}
}

// setupRepo is worktreeRepo with worktree.setup set to command (issue #430).
func setupRepo(t *testing.T, command string) string {
	t.Helper()
	dir := worktreeRepo(t)
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Worktree = &WorktreeConfig{Setup: command}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestWorktreeSetupRuns checks run --worktree runs worktree.setup in the
// task's worktree before the worker starts (issue #430): the marker the
// command writes is there when the fake worker runs, and one worktree_setup
// event records rc 0, before the dispatched event.
func TestWorktreeSetupRuns(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := setupRepo(t, `echo "setting up $FLYWHEEL_TASK" && echo ok > setup.marker`)
	wt := filepath.Join(dir, ".flywheel", "worktrees", "T1")
	markerSeen := false
	commandHook = func(RunRequest) {
		_, err := os.Stat(filepath.Join(wt, "setup.marker"))
		markerSeen = err == nil
	}
	t.Cleanup(func() { commandHook = nil })
	if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !markerSeen {
		t.Error("setup.marker not in the worktree when the worker ran")
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	setupAt, dispatchedAt := -1, -1
	for i, e := range events {
		switch {
		case e.Task == "T1" && e.Kind == "worktree_setup":
			setupAt = i
			if e.RC == nil || *e.RC != 0 || e.Attempt != "r1" || !strings.Contains(e.Note, "setting up T1") {
				t.Errorf("worktree_setup = %+v, want rc 0, attempt r1, the output tail", e)
			}
		case e.Task == "T1" && e.Kind == "dispatched":
			dispatchedAt = i
		}
	}
	if setupAt < 0 || dispatchedAt < 0 || setupAt > dispatchedAt {
		t.Errorf("worktree_setup at %d, dispatched at %d; want setup recorded before dispatched", setupAt, dispatchedAt)
	}
}

// TestWorktreeSetupFailureRefuses checks a setup that exits non-zero refuses
// the dispatch with rule setup (issue #430): no dispatched event, no worker.
func TestWorktreeSetupFailureRefuses(t *testing.T) {
	// not parallel: sets the package-level commandHook
	dir := setupRepo(t, `echo "install broke"; exit 1`)
	workerRan := false
	commandHook = func(RunRequest) { workerRan = true }
	t.Cleanup(func() { commandHook = nil })
	_, err := Run(dir, RunOptions{Task: "T1", Worktree: true})
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "setup" {
		t.Fatalf("Run() error = %v, want a RuleRefusal with rule setup", err)
	}
	if !strings.Contains(rr.Fix, "install broke") || !strings.Contains(rr.Fix, "exited 1") {
		t.Errorf("fix = %q, want the exit and the output tail", rr.Fix)
	}
	if workerRan {
		t.Error("worker started after a failed setup")
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	setups := 0
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "dispatched" {
			t.Errorf("dispatched event recorded after a failed setup: %+v", e)
		}
		if e.Task == "T1" && e.Kind == "worktree_setup" {
			setups++
			if e.RC == nil || *e.RC != 1 {
				t.Errorf("worktree_setup rc = %v, want 1", e.RC)
			}
		}
	}
	if setups != 1 {
		t.Errorf("worktree_setup events = %d, want 1", setups)
	}
}

// correctionDispatch returns task's dispatched event for attempt.
func correctionDispatch(t *testing.T, dir, task, attempt string) Event {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, e := range evs {
		if e.Task == task && e.Kind == "dispatched" && e.Attempt == attempt {
			return e
		}
	}
	t.Fatalf("no dispatched %s event for %s", attempt, task)
	return Event{}
}

// TestCorrectionDeltaSnapshot checks a correction's delta is snapshotted per
// attempt at dispatch (issue #452): dispatched.Brief names
// .flywheel/briefs/<task>.<attempt>.delta.txt, whose bytes are the prompt,
// and the operator's file is left as it was.
func TestCorrectionDeltaSnapshot(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	deltaB := []byte("fix the registry\n")
	delta := filepath.Join(dir, "d.txt")
	if err := os.WriteFile(delta, deltaB, 0o644); err != nil {
		t.Fatalf("write delta: %v", err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	d := correctionDispatch(t, dir, "T1", "c1")
	if want := ".flywheel/briefs/T1.c1.delta.txt"; d.Brief != want {
		t.Fatalf("dispatched brief = %q, want the snapshot %q", d.Brief, want)
	}
	snap, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(d.Brief)))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !bytes.Equal(snap, deltaB) {
		t.Errorf("snapshot = %q, want the prompt %q", snap, deltaB)
	}
	if orig, err := os.ReadFile(delta); err != nil || !bytes.Equal(orig, deltaB) {
		t.Errorf("operator delta = %q, %v; want it untouched", orig, err)
	}
}

// TestDeltaReuseKeepsEarlierT1 checks two corrections dispatched from the
// same --delta file with different contents both pass T1 afterwards: the
// second write to the file no longer breaks the first correction's record
// (issue #452).
func TestDeltaReuseKeepsEarlierT1(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	delta := filepath.Join(dir, "d.txt")
	for i, body := range []string{"first correction\n", "second correction\n"} {
		if err := os.WriteFile(delta, []byte(body), 0o644); err != nil {
			t.Fatalf("write delta: %v", err)
		}
		if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta}); err != nil {
			t.Fatalf("Run() %d error = %v", i+1, err)
		}
	}
	correctionDispatch(t, dir, "T1", "c2")
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	for _, it := range ruleT1(dir, "T1", evs) {
		if !it.Pass {
			t.Errorf("T1 failed: %s", it.Reason)
		}
	}
}

// TestRunWroteFromTree checks wrote also takes the tree's changes since
// dispatch (issue #463): a file written through a shell, with no write
// observation, is in wrote and wrote_from_tree; a file dirty at dispatch and
// untouched is in neither; a file an observed write names is in wrote once
// and not in wrote_from_tree.
func TestRunWroteFromTree(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	git(t, dir, []string{"init", "-q"})
	git(t, dir, []string{"config", "core.autocrlf", "false"})
	git(t, dir, []string{"config", "gc.auto", "0"})
	git(t, dir, []string{"config", "maintenance.auto", "false"})
	for name, body := range map[string]string{".gitignore": ".flywheel/\nflywheel.md\n", "a.go": "package x\n", "b.txt": "one line brief\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "init"})
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package y\n"), 0o644); err != nil {
		t.Fatalf("write dirty.go: %v", err)
	}
	observed := filepath.Join(dir, "obs.go")
	if err := WriteConfig(dir, simConfig(wroteFixture(t, [][2]string{{"write", observed}}, "stop"))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	// The "worker" writes when Run reports the dispatch, which it does
	// synchronously after the baseline is taken and before the worker starts:
	// shell.go through a shell, obs.go through the observed write.
	w := &dispatchWriter{do: func() error {
		if err := os.WriteFile(filepath.Join(dir, "shell.go"), []byte("package z\n"), 0o644); err != nil {
			return err
		}
		return os.WriteFile(observed, []byte("package o\n"), 0o644)
	}}
	if _, err := Run(dir, RunOptions{Task: "T1", Progress: w}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !w.done {
		t.Fatalf("no dispatched progress line")
	}
	if w.err != nil {
		t.Fatalf("worker write: %v", w.err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	f := evs[len(evs)-1]
	if f.Kind != "finished" {
		t.Fatalf("last event = %+v, want finished", f)
	}
	if want := []string{observed, "shell.go"}; !slices.Equal(f.Wrote, want) {
		t.Errorf("wrote = %v, want %v", f.Wrote, want)
	}
	if want := []string{"shell.go"}; !slices.Equal(f.WroteFromTree, want) {
		t.Errorf("wrote_from_tree = %v, want %v", f.WroteFromTree, want)
	}
}

// dispatchWriter is a RunOptions.Progress that runs do, once, the first time
// it sees a dispatched line, recording do's error.
type dispatchWriter struct {
	mu   sync.Mutex
	do   func() error
	done bool
	err  error
}

func (w *dispatchWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done && strings.Contains(string(p), " dispatched ") {
		w.done = true
		w.err = w.do()
	}
	return len(p), nil
}

// TestTreeWrites checks treeWrites against a dispatch baseline (issue #463):
// new and re-changed paths are in, unchanged baseline paths, .flywheel/ and
// observed paths are out, and 50 observed paths leave no room.
func TestTreeWrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git(t, dir, []string{"-c", "core.autocrlf=false", "init", "-q"})
	git(t, dir, []string{"config", "core.autocrlf", "false"})
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("same.go", "package a\n")
	write("changed.go", "package b\n")
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"-c", "core.autocrlf=false", "commit", "-q", "-m", "init"})
	// Dirty at dispatch: same.go stays as it was, changed.go changes again.
	write("same.go", "package a // dirty\n")
	write("changed.go", "package b // dirty\n")
	baseline := computeBaseline(dir)
	if len(baseline) != 2 {
		t.Fatalf("baseline = %v, want same.go and changed.go", baseline)
	}
	write("changed.go", "package b // again\n")
	write("new.go", "package n\n")
	write(".flywheel/state.json", "{}\n")
	write("obs.go", "package o\n")

	observed := []string{filepath.Join(dir, "obs.go")}
	if got, want := treeWrites(dir, baseline, observed), []string{"changed.go", "new.go"}; !slices.Equal(got, want) {
		t.Errorf("treeWrites() = %v, want %v", got, want)
	}
	full := make([]string, 50)
	for i := range full {
		full[i] = filepath.Join(dir, fmt.Sprintf("f%d.go", i))
	}
	if got := treeWrites(dir, baseline, full); got != nil {
		t.Errorf("treeWrites() with 50 observed = %v, want nil", got)
	}
}

// TestRunCommitAttemptDeltaOwns checks a correction whose delta header widens
// owns to a new path (issue #477): the attempt commit on fw/<task> includes
// it, and nothing is left uncommitted.
func TestRunCommitAttemptDeltaOwns(t *testing.T) {
	t.Parallel()
	dir := worktreeRepo(t) // T1 owns a.go
	if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true}); err != nil {
		t.Fatalf("Run() r1 error = %v", err)
	}
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "b.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	delta := filepath.Join(dir, ".flywheel", "T1.delta.txt")
	if err := os.WriteFile(delta, []byte("owns: b.go\nneeds: none\n\n# TASK: widen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", DeltaPath: delta, Worktree: true}); err != nil {
		t.Fatalf("Run() correction error = %v", err)
	}
	fin := lastFinished(t, dir, "T1")
	if !isCorrection(fin.Attempt) || fin.Reason != "stop" || !CommitOK(fin.Commit) {
		t.Fatalf("finished attempt=%q reason=%q commit=%q, want a correction stop with a commit", fin.Attempt, fin.Reason, fin.Commit)
	}
	if len(fin.Uncommitted) != 0 || strings.Contains(fin.Note, "left uncommitted") {
		t.Errorf("finished uncommitted=%v note=%q, want nothing left out", fin.Uncommitted, fin.Note)
	}
	if files := strings.TrimSpace(git(t, wt, []string{"show", "--name-only", "--format=", fin.Commit})); files != "b.go" {
		t.Errorf("committed files = %q, want b.go", files)
	}
}

// TestRunRecordsUncommitted checks a changed path outside every owns (issue
// #477) is recorded on the finished event as uncommitted and warned about.
func TestRunRecordsUncommitted(t *testing.T) {
	t.Parallel()
	dir := worktreeRepo(t) // T1 owns a.go
	wt, err := TaskWorktree(dir, "T1")
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"a.go": "package a\n", "notes.txt": "not owned\n"} {
		if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if _, err := Run(dir, RunOptions{Task: "T1", Worktree: true, Progress: &buf}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	fin := lastFinished(t, dir, "T1")
	if !reflect.DeepEqual(fin.Uncommitted, []string{"notes.txt"}) {
		t.Errorf("finished uncommitted = %v, want [notes.txt]", fin.Uncommitted)
	}
	want := "warning: T1 r1: attempt commit left 1 changed path(s) uncommitted (outside owns): notes.txt"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("progress = %q, want line %q", buf.String(), want)
	}
}

// TestEventUncommittedOnlyOnFinished checks the events validation refuses
// uncommitted on any kind but finished (issue #477).
func TestEventUncommittedOnlyOnFinished(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatal(err)
	}
	err := AppendEvent(dir, Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Uncommitted: []string{"x.go"}})
	if err == nil || !strings.Contains(err.Error(), "cannot carry uncommitted") {
		t.Errorf("AppendEvent() dispatched with uncommitted error = %v, want a refusal", err)
	}
}
