package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		f.Steps != 2 || f.Tokens == nil || f.Tokens.Input != 200 ||
		f.Cost < 0.00399 || f.Cost > 0.00401 {
		t.Errorf("finished event = %v", f)
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
		"T1 r1 finished rc=0 reason=stop steps=2",
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
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
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
	if f := evs[len(evs)-1]; f.Kind != "finished" || f.Reason != "silent" {
		t.Errorf("finished event = %v, want reason silent", f)
	}
	runB, err := os.ReadFile(filepath.Join(dir, ".flywheel", "runs", "T1.r1.jsonl"))
	if err != nil {
		t.Fatalf("read run file: %v", err)
	}
	if len(runB) != 0 {
		t.Errorf("run file = %d bytes, want empty", len(runB))
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
	sum := sha256.Sum256(b)
	if sha != hex.EncodeToString(sum[:]) {
		t.Errorf("workerPolicySHA() = %q, want %q", sha, hex.EncodeToString(sum[:]))
	}
}

func TestWorkerPolicyMatchesCanonicalFile(t *testing.T) {
	b, err := os.ReadFile("../../skills/flywheel/references/worker-permissions.json")
	if err != nil {
		t.Fatalf("read canonical policy: %v", err)
	}
	content := strings.ReplaceAll(string(b), "\r\n", "\n")
	if content != workerPermissionPolicy {
		t.Error("embedded workerPermissionPolicy differs from skills/flywheel/references/worker-permissions.json")
	}
	var doc struct {
		Schema     string `json:"$schema"`
		Permission struct {
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
	}
	if err := json.Unmarshal([]byte(workerPermissionPolicy), &doc); err != nil {
		t.Fatalf("policy is not valid JSON: %v", err)
	}
	if doc.Permission.Bash["*"] != "allow" {
		t.Errorf(`permission.bash["*"] = %q, want allow`, doc.Permission.Bash["*"])
	}
	if doc.Permission.Bash["git stash*"] != "deny" {
		t.Errorf(`permission.bash["git stash*"] = %q, want deny`, doc.Permission.Bash["git stash*"])
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

// TestRunDispatchedPreservesExternalBriefPath checks a planned brief outside
// the repo keeps its absolute path in dispatched.Brief.
func TestRunDispatchedPreservesExternalBriefPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	ext := t.TempDir()
	briefPath := filepath.Join(ext, "external.txt")
	if err := os.WriteFile(briefPath, []byte("external brief\n"), 0o644); err != nil {
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
	if d := evs[1]; d.Kind != "dispatched" || d.Brief != briefPath {
		t.Errorf("dispatched = %v, want the external brief %q preserved", d, briefPath)
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
