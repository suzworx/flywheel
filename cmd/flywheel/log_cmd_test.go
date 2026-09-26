package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/flywheel"
)

func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "-c", "core.autocrlf=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false", "init", "-q")
	cmd.Dir = dir
	if _, err := cmd.Output(); err != nil {
		var e *exec.ExitError
		if errors.As(err, &e) {
			t.Fatalf("git init: %v", err)
		}
		t.Skipf("git unavailable: %v", err)
	}
	// Recorded in the repository as well: -c applies to the init command
	// alone, so a later commit could still start git's background
	// maintenance and race t.TempDir's cleanup (issue #352, #356 review).
	for _, kv := range [][2]string{{"core.autocrlf", "false"}, {"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		cmd := exec.Command("git", "-C", dir, "config", kv[0], kv[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %v: %s", kv[0], err, out)
		}
	}
}

// TestAppendEventsPlannedTakesNoFeedbackLock checks a batch with no learning
// or dismissed event takes no feedback lock at all: logging a planned event
// must never wait on a feedback command, so the lock file never appears
// (issue #260).
func TestAppendEventsPlannedTakesNoFeedbackLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{{Task: "t1", Kind: "planned", Brief: "b.txt"}}, true)
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "feedback.lock")); !os.IsNotExist(err) {
		t.Errorf("feedback.lock appeared for a planned event (stat err=%v)", err)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].Kind != "planned" {
		t.Errorf("events = %+v, want the single planned event", events)
	}
}

// TestAppendEventsLearningRebuildsArtifact checks a batch carrying a learning
// event routes through the feedback transaction: the event is appended, the
// artifact is rebuilt from the log, and the lock is released again (no lock
// file remains).
func TestAppendEventsLearningRebuildsArtifact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"},
	}, true)
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "learnings.md"))
	if err != nil {
		t.Fatalf("read learnings.md: %v", err)
	}
	if !strings.Contains(string(b), "## L-01 — Terse") {
		t.Errorf("learnings.md lacks the imported learning:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "feedback.lock")); !os.IsNotExist(err) {
		t.Errorf("feedback.lock still present after appendEvents (stat err=%v)", err)
	}
}

// TestAppendEventsMixedBatchKeepsBatchSemantics checks a batch carrying a
// learning event imports the whole batch — plain and learning events together
// — as one transaction, not one locked mutation per event.
func TestAppendEventsMixedBatchKeepsBatchSemantics(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "planned", Brief: "b.txt"},
		{Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"},
	}, true)
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want both batch events appended", events)
	}
	if events[0].Kind != "planned" || events[1].Kind != "learning" {
		t.Errorf("events = %+v, want the batch order preserved", events)
	}
}

// TestAppendEventsAmendedRoutesThroughCheck checks the JSON log path routes
// an amended event through the checked, dispatch-locked path instead of
// appending it raw (issue #272): a benign amendment lands, and the amended
// event is present in the log afterwards.
func TestAppendEventsAmendedRoutesThroughCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := flywheel.BriefHeader{Owns: []string{"a.go"}, Gates: []string{"go build ./..."}}
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "planned", Brief: "b.txt", Header: &base},
		{Task: "t1", Kind: "dispatched", Attempt: "r1", Brief: "b.txt", Header: &base},
	}, true)
	amended := flywheel.BriefHeader{Owns: []string{"a.go", "b.go"}, Gates: []string{"go build ./..."}}
	appendEvents(dir, []flywheel.Event{{Task: "t1", Kind: "amended", Brief: "b.txt", Header: &amended, Note: "widen owns"}}, true)
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 3 || events[2].Kind != "amended" {
		t.Errorf("events = %+v, want the benign amended event appended", events)
	}
}

// TestAppendEventsAmendedInFeedbackBatchLandsBoth checks an amended event
// riding in a feedback batch is routed through the check before the batch
// lands as the feedback transaction, so neither the refusal nor the
// learnings.md rebuild is skipped (issue #272, #260).
func TestAppendEventsAmendedInFeedbackBatchLandsBoth(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := flywheel.BriefHeader{Owns: []string{"a.go"}, Gates: []string{"go build ./..."}}
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "planned", Brief: "b.txt", Header: &base},
		{Task: "t1", Kind: "dispatched", Attempt: "r1", Brief: "b.txt", Header: &base},
	}, true)
	amended := flywheel.BriefHeader{Owns: []string{"a.go", "b.go"}, Gates: []string{"go build ./..."}}
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "amended", Brief: "b.txt", Header: &amended, Note: "widen owns"},
		{Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"},
	}, true)
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4 (planned, dispatched, amended, learning)", len(events))
	}
	if events[2].Kind != "amended" || events[3].Kind != "learning" {
		t.Errorf("events = %+v, want the amended then the learning event appended", events)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "learnings.md"))
	if err != nil {
		t.Fatalf("read learnings.md: %v", err)
	}
	if !strings.Contains(string(b), "## L-01 — Terse") {
		t.Errorf("learnings.md lacks the imported learning:\n%s", b)
	}
}

// TestBatchHasLearning checks the routing predicate: only batches carrying a
// learning or dismissed event are routed through the feedback transaction.
func TestBatchHasLearning(t *testing.T) {
	t.Parallel()
	plain := []flywheel.Event{{Kind: "planned"}, {Kind: "finished"}}
	if batchHasLearning(plain) {
		t.Error("batchHasLearning() = true for a batch with no learning or dismissed event")
	}
	withLearning := []flywheel.Event{{Kind: "planned"}, {Kind: "learning"}}
	if !batchHasLearning(withLearning) {
		t.Error("batchHasLearning() = false for a batch carrying a learning event")
	}
	withDismissed := []flywheel.Event{{Kind: "dismissed"}}
	if !batchHasLearning(withDismissed) {
		t.Error("batchHasLearning() = false for a batch carrying a dismissed event")
	}
}

// TestLogShardSealsAndMarksConfig checks that runLogShard seals the legacy log
// and marks the config with log.shards = true.
func TestLogShardSealsAndMarksConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "planned", Brief: "b.txt"},
		{Task: "t1", Kind: "dispatched", Attempt: "r1", Brief: "b.txt"},
	}, true)
	err := runLogShard(dir, os.Stdout, os.Stderr, shardTestClock)
	if err != nil {
		t.Fatalf("runLogShard() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "events", "@floor.jsonl")); err != nil {
		t.Errorf("@floor.jsonl not created: %v", err)
	}
	cfg, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Log == nil || !cfg.Log.Shards {
		t.Errorf("log.shards = %+v, want true", cfg.Log)
	}
}

// TestLogShardIdempotent checks that a second call to runLogShard prints
// "already uses the sharded log" and does not fail.
func TestLogShardIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{
		{Task: "t1", Kind: "planned", Brief: "b.txt"},
	}, true)
	if err := runLogShard(dir, os.Stdout, os.Stderr, shardTestClock); err != nil {
		t.Fatalf("first runLogShard() error = %v", err)
	}
	var buf strings.Builder
	if err := runLogShard(dir, &buf, os.Stderr, shardTestClock); err != nil {
		t.Fatalf("second runLogShard() error = %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "already uses the sharded log") {
		t.Errorf("output = %q, want to contain \"already uses the sharded log\"", output)
	}
}

// TestLogShardFlagConflictsAreUsage checks that logShardConflicts validates
// flags properly and returns errors for conflicts.
func TestLogShardFlagConflictsAreUsage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts *logOptions
	}{
		{"task", &logOptions{shard: true, task: "t1"}},
		{"kind", &logOptions{shard: true, kind: "planned"}},
		{"brief", &logOptions{shard: true, brief: "b.txt"}},
		{"json", &logOptions{shard: true, jsonIn: "e.jsonl"}},
		{"no-state", &logOptions{shard: true, noState: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := logShardConflicts(tt.opts)
			if err == nil {
				t.Errorf("logShardConflicts() = nil, want error")
			}
		})
	}
}

// TestLogShardWarnsOnPinnedAuditWorkflow checks that runLogShard warns when
// the audit workflow pins a flywheel version.
func TestLogShardWarnsOnPinnedAuditWorkflow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{{Task: "t1", Kind: "planned", Brief: "b.txt"}}, true)
	workflowDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatalf("create workflow dir: %v", err)
	}
	workflowPath := filepath.Join(workflowDir, "flywheel-audit.yml")
	if err := os.WriteFile(workflowPath, []byte("flywheel@v0.17.0\n"), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	var stderrBuf strings.Builder
	if err := runLogShard(dir, os.Stdout, &stderrBuf, shardTestClock); err != nil {
		t.Fatalf("runLogShard() error = %v", err)
	}
	stderr := stderrBuf.String()
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "pin") {
		t.Errorf("stderr = %q, want warning about pin", stderr)
	}
}

// TestInitShardCreatesShardedRepo checks that init --shard creates a sharded
// repository with ShardedLayout = true.
func TestInitShardCreatesShardedRepo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitInitRepo(t, dir)
	o := &initOptions{dir: dir, shard: true}
	path, _, err := flywheel.InitSeeded(o.dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	if err := runLogShard(path, os.Stdout, os.Stderr, shardTestClock); err != nil {
		t.Fatalf("runLogShard() error = %v", err)
	}
	layout, err := flywheel.ShardedLayout(path)
	if err != nil {
		t.Fatalf("ShardedLayout() error = %v", err)
	}
	if !layout {
		t.Errorf("ShardedLayout() = false, want true")
	}
}

// runLogHelperEnv carries runLog's arguments to the re-executed test binary,
// separated by \x1f, so a test can observe runLog's stderr and exit status.
const runLogHelperEnv = "FLYWHEEL_TEST_RUNLOG_ARGS"

// runLogProcess runs runLog(args) in a child copy of the test binary and
// returns its stderr and exit code.
func runLogProcess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLogMissingFlagErrors$")
	cmd.Env = append(os.Environ(), runLogHelperEnv+"="+strings.Join(args, "\x1f"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var e *exec.ExitError
	if errors.As(err, &e) {
		return stderr.String(), e.ExitCode()
	}
	if err != nil {
		t.Fatalf("run child: %v", err)
	}
	return stderr.String(), 0
}

// TestLogMissingFlagErrors checks a missing flag prints the error and the
// fixed command, not the whole usage, and exits 2 (issue #392).
func TestLogMissingFlagErrors(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(runLogHelperEnv); ok {
		runLog(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"amended without --note",
			[]string{"--task", "T", "--kind", "amended", "--brief", "B", "--dir", dir},
			[]string{"requires --note", "try: flywheel log --task T --kind amended --brief", `--note "<why>"`}},
		{"note without --note",
			[]string{"--task", "T", "--kind", "note", "--dir", dir},
			[]string{"--kind note requires --note <text>", "try: flywheel log --task T --kind note --dir", `--note "<text>"`}},
		{"no --kind",
			[]string{"--task", "T", "--brief", "my brief.md", "--dir", dir},
			[]string{"--kind is required", `try: flywheel log --task T --brief "my brief.md"`, "--kind <kind>"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stderr, code := runLogProcess(t, tt.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2; stderr:\n%s", code, stderr)
			}
			for _, w := range tt.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr lacks %q:\n%s", w, stderr)
				}
			}
			if strings.Contains(stderr, "usage: flywheel <subcommand>") {
				t.Errorf("stderr carries the full usage:\n%s", stderr)
			}
		})
	}
}

// TestLogReanchorFlags checks flywheel log --reanchor (issue #436): --note is
// required and --kind/--task/--json do not combine with it (exit 2); an
// intact chain and an unforced removed break are refusals (exit 6).
func TestLogReanchorFlags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--reanchor", "--dir", dir}, 2, "--reanchor requires --note"},
		{[]string{"--reanchor", "--note", "x", "--kind", "note", "--dir", dir}, 2, "--kind cannot be used with --reanchor"},
		{[]string{"--reanchor", "--note", "x", "--task", "T", "--dir", dir}, 2, "--task cannot be used with --reanchor"},
		{[]string{"--reanchor", "--note", "x", "--json", "-", "--dir", dir}, 2, "--json cannot be used with --reanchor"},
		{[]string{"--kind", "note", "--note", "x", "--force", "--dir", dir}, 2, "--force applies to --reanchor only"},
		{[]string{"--reanchor", "--note", "x", "--dir", dir}, 6, "the log chain is intact; nothing to re-anchor"},
	} {
		stderr, code := runLogProcess(t, tt.args...)
		if code != tt.code || !strings.Contains(stderr, tt.want) {
			t.Errorf("flywheel log %v = %d %q, want %d and %q", tt.args, code, stderr, tt.code, tt.want)
		}
	}
	for _, n := range []string{"one", "two", "three"} {
		if err := flywheel.AppendEvent(dir, flywheel.Event{Kind: "note", Note: n}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	b, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if err := os.WriteFile(path, []byte(lines[0]+"\n"+lines[2]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if stderr, code := runLogProcess(t, "--reanchor", "--note", "x", "--dir", dir); code != 6 || !strings.Contains(stderr, "--force") {
		t.Errorf("unforced removed reanchor = %d %q, want 6 naming --force", code, stderr)
	}
	if stderr, code := runLogProcess(t, "--reanchor", "--force", "--note", "x", "--dir", dir); code != 0 {
		t.Errorf("forced reanchor = %d %q, want 0", code, stderr)
	}
}

// shardTestClock is the fixed instant the migration tests stamp seals with.
var shardTestClock = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// TestLogShardKeepsLayoutWhenConfigFails checks that nothing irreversible
// happens when the config cannot be read: the fence must never be missing
// from a migrated repository (#351 review).
func TestLogShardKeepsLayoutWhenConfigFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{{Task: "t1", Kind: "planned", Brief: "b.txt"}}, true)
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := runLogShard(dir, os.Stdout, os.Stderr, shardTestClock); err == nil {
		t.Fatal("runLogShard() succeeded with a malformed config, want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "events")); !os.IsNotExist(err) {
		t.Errorf("the layout changed although the config could not be read (stat err = %v)", err)
	}
}

// TestLogShardAddsLocksToExistingGitignore checks that a migrated repository
// ignores the transient shard locks (#351 review).
func TestLogShardAddsLocksToExistingGitignore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	appendEvents(dir, []flywheel.Event{{Task: "t1", Kind: "planned", Brief: "b.txt"}}, true)
	ignore := filepath.Join(dir, ".flywheel", ".gitignore")
	if err := os.WriteFile(ignore, []byte("runs/\nworktrees/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := runLogShard(dir, os.Stdout, os.Stderr, shardTestClock); err != nil {
			t.Fatalf("runLogShard() error = %v", err)
		}
	}
	b, err := os.ReadFile(ignore)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if got := strings.Count(string(b), "locks/"); got != 1 {
		t.Errorf(".gitignore = %q, want exactly one locks/ line", b)
	}
	if !strings.Contains(string(b), "runs/") || !strings.Contains(string(b), "worktrees/") {
		t.Errorf(".gitignore = %q, want the existing rules kept", b)
	}
}

// TestLogShardWarnsForNestedFactory checks that the pinned-CI warning finds
// the workflow at the repository root, not under a nested factory (#351
// review).
func TestLogShardWarnsForNestedFactory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	gitInitRepo(t, root)
	workflows := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(workflows, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflows, "flywheel-audit.yml"), []byte("go install github.com/suzworx/flywheel/cmd/flywheel@v0.17.0\n"), 0o644); err != nil {
		t.Fatalf("write workflow: %v", err)
	}
	nested := filepath.Join(root, "services", "api")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	appendEvents(nested, []flywheel.Event{{Task: "t1", Kind: "planned", Brief: "b.txt"}}, true)
	var stderrBuf strings.Builder
	if err := runLogShard(nested, os.Stdout, &stderrBuf, shardTestClock); err != nil {
		t.Fatalf("runLogShard() error = %v", err)
	}
	if !strings.Contains(stderrBuf.String(), "pins a flywheel release") {
		t.Errorf("stderr = %q, want the pinned-release warning for the root workflow", stderrBuf.String())
	}
}

// TestNoteEventLogged checks flywheel log --kind note records a journal line
// through the generic append path, with or without a task (issue #409).
func TestNoteEventLogged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if stderr, code := runLogProcess(t, "--kind", "note", "--note", "action: dispatched t1", "--no-state", "--dir", dir); code != 0 {
		t.Fatalf("task-less note: exit %d; stderr:\n%s", code, stderr)
	}
	if stderr, code := runLogProcess(t, "--kind", "note", "--task", "t1", "--session", "s1", "--note", "result: merged", "--no-state", "--dir", dir); code != 0 {
		t.Fatalf("note on t1: exit %d; stderr:\n%s", code, stderr)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Kind != "note" || events[0].Task != "" || events[0].Note != "action: dispatched t1" ||
		events[1].Task != "t1" || events[1].Session != "s1" || events[1].Note != "result: merged" {
		t.Errorf("events = %+v", events)
	}
	if len(flywheel.Learnings(events)) != 0 {
		t.Error("a note became a learning")
	}
}

// TestLogReplanWarning checks --kind planned on an id with a dispatched
// attempt warns and still records the event, --replan silences it, a fresh id
// gets no warning, and --replan with another kind is a usage error (issue #476).
func TestLogReplanWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("brief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const warn = "warning: task T already has 1 attempt(s); this starts a new plan, the old attempts stay in the ledger (--replan silences this)"
	planned := func(extra ...string) (string, int) {
		return runLogProcess(t, append([]string{"--task", "T", "--kind", "planned", "--brief", "b.md", "--no-state", "--dir", dir}, extra...)...)
	}
	if stderr, code := planned(); code != 0 || strings.Contains(stderr, "already has") {
		t.Fatalf("fresh id: exit %d; stderr:\n%s", code, stderr)
	}
	if err := flywheel.AppendEvent(dir, flywheel.Event{Task: "T", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatal(err)
	}
	if stderr, code := planned(); code != 0 || !strings.Contains(stderr, warn) {
		t.Errorf("re-plan: exit %d, want 0 and %q; stderr:\n%s", code, warn, stderr)
	}
	if stderr, code := planned("--replan"); code != 0 || strings.Contains(stderr, "already has") {
		t.Errorf("--replan: exit %d, want 0 and no warning; stderr:\n%s", code, stderr)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == "planned" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("planned events = %d, want 3 (the warning never refuses)", n)
	}
	stderr, code := runLogProcess(t, "--task", "T", "--kind", "blocked", "--replan", "--no-state", "--dir", dir)
	if code != 2 || !strings.Contains(stderr, "--replan applies to --kind planned only") {
		t.Errorf("--replan with blocked: exit %d, want 2; stderr:\n%s", code, stderr)
	}
}

// TestLogWithdrawn checks --kind withdrawn (issue #479): --note is required
// (exit 2), a noted withdrawal is recorded, and one on a dispatched attempt is
// refused (exit 6) before anything is written.
func TestLogWithdrawn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := flywheel.AppendEvent(dir, flywheel.Event{Task: "T", Kind: "planned", Brief: "b.md"}); err != nil {
		t.Fatal(err)
	}
	if stderr, code := runLogProcess(t, "--task", "T", "--kind", "withdrawn", "--no-state", "--dir", dir); code != 2 || !strings.Contains(stderr, "--kind withdrawn requires --note <why>") {
		t.Errorf("no --note: exit %d, want 2; stderr:\n%s", code, stderr)
	}
	if stderr, code := runLogProcess(t, "--task", "T", "--kind", "withdrawn", "--note", "another root owns T", "--no-state", "--dir", dir); code != 0 {
		t.Fatalf("withdrawn: exit %d, want 0; stderr:\n%s", code, stderr)
	}
	if err := flywheel.AppendEvent(dir, flywheel.Event{Task: "U", Kind: "dispatched", Attempt: "r1"}); err != nil {
		t.Fatal(err)
	}
	if stderr, code := runLogProcess(t, "--task", "U", "--kind", "withdrawn", "--note", "dup", "--no-state", "--dir", dir); code != 6 || !strings.Contains(stderr, "W1") {
		t.Errorf("withdrawn on dispatched U: exit %d, want 6 naming W1; stderr:\n%s", code, stderr)
	}
	events, err := flywheel.ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range events {
		if e.Kind == "withdrawn" {
			got = append(got, e.Task+":"+e.Note)
		}
	}
	if len(got) != 1 || got[0] != "T:another root owns T" {
		t.Errorf("withdrawn events = %v, want [T:another root owns T]", got)
	}
}

// TestLogPlannedBranchWarning checks --kind planned warns when branch fw/<id>
// already exists (issue #479), names the worktree that has it checked out,
// and --replan silences it.
func TestLogPlannedBranchWarning(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitInitRepo(t, dir)
	git := func(args ...string) {
		t.Helper()
		full := append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)
		if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("commit", "-q", "--allow-empty", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("brief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	planned := func(extra ...string) (string, int) {
		return runLogProcess(t, append([]string{"--task", "T", "--kind", "planned", "--brief", "b.md", "--no-state", "--dir", dir}, extra...)...)
	}
	if stderr, code := planned(); code != 0 || strings.Contains(stderr, "fw/T") {
		t.Fatalf("no branch: exit %d, want 0 and no warning; stderr:\n%s", code, stderr)
	}
	git("branch", "fw/T")
	const bare = "warning: branch fw/T already exists; another root may own this id (--replan silences this)"
	if stderr, code := planned(); code != 0 || !strings.Contains(stderr, bare) {
		t.Errorf("branch: exit %d, want 0 and %q; stderr:\n%s", code, bare, stderr)
	}
	sib := filepath.Join(t.TempDir(), "sibling-root")
	git("worktree", "add", "-q", sib, "fw/T")
	if stderr, code := planned(); code != 0 || !strings.Contains(stderr, "warning: branch fw/T already exists (checked out in ") ||
		!strings.Contains(stderr, "sibling-root); another root may own this id (--replan silences this)") {
		t.Errorf("checked out: exit %d, want 0 and a warning naming %s; stderr:\n%s", code, sib, stderr)
	}
	if stderr, code := planned("--replan"); code != 0 || strings.Contains(stderr, "fw/T") {
		t.Errorf("--replan: exit %d, want 0 and no warning; stderr:\n%s", code, stderr)
	}
}

// logRebasedRepo returns a temp git repo with one commit and HEAD's full sha.
func logRebasedRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	gitInitRepo(t, dir)
	commit := exec.Command("git", "-C", dir, "-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com",
		"commit", "-q", "--allow-empty", "-m", "init")
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return dir, strings.TrimSpace(string(out))
}

// TestLogRebasedBase checks flywheel log --kind rebased --base records a hand
// rebase (issue #498): a ref is stored as the full sha it resolves to in --dir,
// a full 40-hex sha passes through as given.
func TestLogRebasedBase(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, base, want string }{
		{"ref", "HEAD", ""},
		{"full sha", strings.Repeat("ab", 20), strings.Repeat("ab", 20)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir, head := logRebasedRepo(t)
			want := tt.want
			if want == "" {
				want = head
			}
			if stderr, code := runLogProcess(t, "--task", "T", "--kind", "rebased", "--base", tt.base, "--note", "x", "--no-state", "--dir", dir); code != 0 {
				t.Fatalf("exit %d, want 0; stderr:\n%s", code, stderr)
			}
			events, err := flywheel.ReadEvents(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Kind != "rebased" || events[0].Task != "T" || events[0].Base != want || events[0].Note != "x" {
				t.Errorf("events = %+v, want one rebased event on T with base %s", events, want)
			}
		})
	}
}

// TestLogRebasedRefusesWithoutBase checks --kind rebased without --base and
// --base with another kind are usage refusals (exit 2), and a ref that does
// not resolve is an error (exit 1) naming the ref (issue #498).
func TestLogRebasedRefusesWithoutBase(t *testing.T) {
	t.Parallel()
	dir, _ := logRebasedRepo(t)
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--task", "T", "--kind", "rebased", "--note", "x"}, 2, "--kind rebased requires --base <ref>"},
		{[]string{"--task", "T", "--kind", "note", "--base", "HEAD", "--note", "x"}, 2, "--base applies to --kind rebased only"},
		{[]string{"--task", "T", "--kind", "rebased", "--base", "no-such-ref", "--note", "x"}, 1, `--base "no-such-ref" does not resolve`},
	} {
		stderr, code := runLogProcess(t, append(tt.args, "--no-state", "--dir", dir)...)
		if code != tt.code || !strings.Contains(stderr, tt.want) {
			t.Errorf("flywheel log %v = %d %q, want %d and %q", tt.args, code, stderr, tt.code, tt.want)
		}
	}
	if events, err := flywheel.ReadEvents(dir); err != nil || len(events) != 0 {
		t.Errorf("events = %+v (err %v), want none written", events, err)
	}
}
