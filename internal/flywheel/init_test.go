package flywheel

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestInitCreatesScaffold(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	got, pieces, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	if got != dir {
		t.Fatalf("InitSeeded() = %q, want %q", got, dir)
	}
	wantPaths := []string{"flywheel.md", ".flywheel/state.json", ".flywheel/events.jsonl", ".flywheel/config.json", ".flywheel/.gitignore", ".flywheel/.gitattributes", ".flywheel/briefs/"}
	if len(pieces) != len(wantPaths) {
		t.Fatalf("InitSeeded() pieces = %+v, want %d entries", pieces, len(wantPaths))
	}
	for i, p := range pieces {
		if p.Path != wantPaths[i] {
			t.Errorf("pieces[%d].Path = %q, want %q", i, p.Path, wantPaths[i])
		}
		if !p.Added {
			t.Errorf("pieces[%d] (%s) Added = false, want true on a fresh init", i, p.Path)
		}
	}

	for _, f := range []string{"flywheel.md", ".flywheel/state.json", ".flywheel/events.jsonl", ".flywheel/.gitignore", ".flywheel/.gitattributes", ".flywheel/briefs"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("InitSeeded() did not create %s: %v", f, err)
		}
	}
}

// TestInitAddsOnlyMissingGitattributes covers the issue #121 scenario: an
// adopted directory that already has everything except .gitattributes gets
// exactly that piece added, and every other piece is left byte-identical.
func TestInitAddsOnlyMissingGitattributes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	mdPath := filepath.Join(dir, "flywheel.md")
	configPath := filepath.Join(dir, ".flywheel", "config.json")
	eventsPath := filepath.Join(dir, ".flywheel", "events.jsonl")
	gitattributesPath := filepath.Join(dir, ".flywheel", ".gitattributes")

	mdBefore, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	eventsBefore, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if err := os.Remove(gitattributesPath); err != nil {
		t.Fatalf("remove .gitattributes: %v", err)
	}

	_, pieces, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	for _, p := range pieces {
		want := p.Path == ".flywheel/.gitattributes"
		if p.Added != want {
			t.Errorf("piece %s Added = %v, want %v", p.Path, p.Added, want)
		}
	}

	if b, err := os.ReadFile(mdPath); err != nil || string(b) != string(mdBefore) {
		t.Errorf("flywheel.md changed: got %q, err %v, want %q", b, err, mdBefore)
	}
	if b, err := os.ReadFile(configPath); err != nil || string(b) != string(configBefore) {
		t.Errorf("config.json changed: got %q, err %v, want %q", b, err, configBefore)
	}
	if b, err := os.ReadFile(eventsPath); err != nil || string(b) != string(eventsBefore) {
		t.Errorf("events.jsonl changed: got %q, err %v, want %q", b, err, eventsBefore)
	}
	if b, err := os.ReadFile(gitattributesPath); err != nil || string(b) != gitattributesDefault {
		t.Errorf(".gitattributes = %q, err %v, want it recreated with the default", b, err)
	}
}

func TestInitSeededOnInitializedDirCreatesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitSeeded(dir, false, "", "", false); err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}

	_, pieces, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() second call error = %v, want idempotent success", err)
	}
	for _, p := range pieces {
		if p.Added {
			t.Errorf("InitSeeded() second call added %s, want a fully initialized directory to add nothing", p.Path)
		}
	}
}

func TestInitWritesMarkdownHeadings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "flywheel.md"))
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	content := string(b)

	for _, h := range []string{"## Status", "## Main session", "## Workers", "## Roles", "## Task log"} {
		if !strings.Contains(content, h) {
			t.Errorf("flywheel.md missing heading %q", h)
		}
	}
	if !strings.Contains(content, "status: initialized") {
		t.Error("flywheel.md missing 'status: initialized'")
	}
}

func TestInitStateJSONContract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}

	var state map[string]json.RawMessage
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatalf("state.json is not valid JSON: %v", err)
	}

	for _, k := range []string{"version", "updated_at", "tasks", "counts"} {
		if _, ok := state[k]; !ok {
			t.Errorf("state.json missing key %q (have %v)", k, keys(state))
		}
	}
	if len(state) != 4 {
		t.Errorf("state.json has %d keys, want exactly 4 (version, updated_at, tasks, counts)", len(state))
	}

	var version float64
	_ = json.Unmarshal(state["version"], &version)
	if version != 2 {
		t.Errorf("state.json version = %v, want 2", version)
	}

	var tasks []any
	_ = json.Unmarshal(state["tasks"], &tasks)
	if tasks == nil || len(tasks) != 0 {
		t.Errorf("state.json tasks = %#v, want empty array", tasks)
	}

	var counts map[string]json.RawMessage
	_ = json.Unmarshal(state["counts"], &counts)
	if len(counts) != 0 {
		t.Errorf("state.json counts = %v, want empty object", counts)
	}
}

func TestInitRepeatedCallCreatesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	// An already-initialized directory has nothing to do: success, nothing
	// added, nothing refused.
	_, pieces, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatalf("InitSeeded() second call error = %v, want idempotent success", err)
	}
	for _, p := range pieces {
		if p.Added {
			t.Errorf("InitSeeded() second call added %s, want none", p.Path)
		}
	}

	// Nothing changed: flywheel.md still present, state.json still valid.
	if _, err := os.Stat(filepath.Join(dir, "flywheel.md")); err != nil {
		t.Errorf("flywheel.md missing after repeated init: %v", err)
	}
}

func TestInitForceOverwrites(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "flywheel.md")); err != nil {
		t.Errorf("flywheel.md missing after --force: %v", err)
	}
}

func TestInitCreatesMissingParentDirs(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() into nested dirs error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "state.json")); err != nil {
		t.Errorf("state.json missing after nested Init: %v", err)
	}
}

func TestInitLeavesJunkStateUntouchedAndAddsMarkdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	statePath := filepath.Join(dir, ".flywheel", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	junk := []byte(`{"version":99,"status":"corrupt","tasks":[1]}`)
	if err := os.WriteFile(statePath, junk, 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	// state.json already existing no longer stops init: it's left alone and
	// the missing pieces, including flywheel.md, are filled in around it.
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v, want success around an existing state.json", err)
	}
	b, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if string(b) != string(junk) {
		t.Errorf("state.json changed: got %q, want untouched junk %q", b, junk)
	}
	if _, err := os.Stat(filepath.Join(dir, "flywheel.md")); err != nil {
		t.Errorf("flywheel.md not created: %v", err)
	}

	// state.json is never overwritten, even with --force: only flywheel.md
	// responds to force.
	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}
	b, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json after force: %v", err)
	}
	if string(b) != string(junk) {
		t.Errorf("--force changed state.json: got %q, want untouched junk %q", b, junk)
	}
}

func TestInitLeavesExistingMarkdownUntouchedAndAddsState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "flywheel.md")
	custom := []byte("keep me")
	if err := os.WriteFile(mdPath, custom, 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}

	// flywheel.md already existing no longer stops init: it's reported
	// present and the missing pieces, including state.json, are added.
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v, want success around an existing flywheel.md", err)
	}

	b, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	if string(b) != string(custom) {
		t.Errorf("flywheel.md changed: got %q, want %q", b, custom)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "state.json")); err != nil {
		t.Errorf("state.json not created: %v", err)
	}
}

func TestInitInvalidStateDestinationLeavesNoMarkdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Block .flywheel/state.json by making .flywheel a regular file.
	dotFlywheel := filepath.Join(dir, ".flywheel")
	if err := os.WriteFile(dotFlywheel, []byte("obstruction"), 0o644); err != nil {
		t.Fatalf("write obstruction: %v", err)
	}

	if _, err := Init(dir, false); err == nil {
		t.Fatal("Init() with invalid state destination: got nil error, want failure")
	}

	// No markdown was left behind to block a retry.
	if _, err := os.Stat(filepath.Join(dir, "flywheel.md")); !os.IsNotExist(err) {
		t.Errorf("flywheel.md exists after failed Init, want absent")
	}

	// Remove the obstruction; the same call now succeeds.
	if err := os.Remove(dotFlywheel); err != nil {
		t.Fatalf("remove obstruction: %v", err)
	}
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() after obstruction removed error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "state.json")); err != nil {
		t.Errorf("state.json missing after retry: %v", err)
	}
}

func TestInitFailedForceRestoresMarkdown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	mdPath := filepath.Join(dir, "flywheel.md")

	// Simulate a preexisting markdown a failed force update must restore.
	custom := []byte("custom markdown that must survive a failed force update")
	if err := os.WriteFile(mdPath, custom, 0o644); err != nil {
		t.Fatalf("write custom flywheel.md: %v", err)
	}

	// Turn .gitattributes into a directory so the force call fails on a
	// later piece, after it has already force-reset flywheel.md.
	gitattributesPath := filepath.Join(dir, ".flywheel", ".gitattributes")
	if err := os.Remove(gitattributesPath); err != nil {
		t.Fatalf("remove .gitattributes: %v", err)
	}
	if err := os.Mkdir(gitattributesPath, 0o755); err != nil {
		t.Fatalf("mkdir .gitattributes obstruction: %v", err)
	}

	if _, err := Init(dir, true); err == nil {
		t.Skip("directory obstruction did not fail the write; skipping restore assertion")
	}

	// flywheel.md's preexisting bytes were restored.
	b, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	if string(b) != string(custom) {
		t.Errorf("flywheel.md not restored after failed force: got %q, want %q", b, custom)
	}

	// Unblock and verify a retry succeeds and resets flywheel.md.
	if err := os.Remove(gitattributesPath); err != nil {
		t.Fatalf("remove obstruction: %v", err)
	}
	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() retry after unblock error = %v", err)
	}
	b, err = os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	if string(b) != markdownTemplate {
		t.Errorf("flywheel.md not reset by successful force: got %q", b)
	}
}

func TestInitForceResetsMarkdownOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "flywheel.md")
	statePath := filepath.Join(dir, ".flywheel", "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatalf("mkdir .flywheel: %v", err)
	}
	if err := os.WriteFile(mdPath, []byte("junk markdown"), 0o644); err != nil {
		t.Fatalf("write flywheel.md: %v", err)
	}
	junkState := []byte("junk state")
	if err := os.WriteFile(statePath, junkState, 0o644); err != nil {
		t.Fatalf("write state.json: %v", err)
	}

	// --force resets flywheel.md but leaves every other piece, including
	// state.json, untouched.
	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}

	b, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatalf("read flywheel.md: %v", err)
	}
	if string(b) != markdownTemplate {
		t.Errorf("flywheel.md not reset: got %q", b)
	}
	b, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if string(b) != string(junkState) {
		t.Errorf("--force overwrote state.json: got %q, want untouched junk %q", b, junkState)
	}
}

func TestInitForceRejectsDirectoryDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mdPath := filepath.Join(dir, "flywheel.md")
	if err := os.Mkdir(mdPath, 0o755); err != nil {
		t.Fatalf("mkdir flywheel.md: %v", err)
	}

	if _, err := Init(dir, true); err == nil {
		t.Fatal("Init() --force with directory at flywheel.md: got nil error, want refusal")
	}

	// The directory is untouched (not deleted).
	info, err := os.Stat(mdPath)
	if err != nil {
		t.Fatalf("stat flywheel.md: %v", err)
	}
	if !info.IsDir() {
		t.Error("flywheel.md directory was removed by refused Init")
	}
}

func TestInitForceRejectsSymlinkDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(target, []byte("do not touch"), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "flywheel.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	if _, err := Init(dir, true); err == nil {
		t.Fatal("Init() --force with symlink at flywheel.md: got nil error, want refusal")
	}
	b, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read symlink target: %v", err)
	}
	if string(b) != "do not touch" {
		t.Errorf("symlink target modified: got %q", b)
	}
}

func TestInitForcePreservesUnrelatedFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Pre-existing unrelated content, including a brief inside .flywheel/briefs.
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("unrelated"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	briefs := filepath.Join(dir, ".flywheel", "briefs")
	if err := os.MkdirAll(briefs, 0o755); err != nil {
		t.Fatalf("mkdir briefs: %v", err)
	}
	brief := filepath.Join(briefs, "keep.txt")
	if err := os.WriteFile(brief, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}

	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}

	for p, want := range map[string]string{notes: "unrelated", brief: "keep"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("read %s: %v", p, err)
			continue
		}
		if string(b) != want {
			t.Errorf("%s changed: got %q, want %q", p, b, want)
		}
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestInitCreatesEventLogAndGitignore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	eventsPath := filepath.Join(dir, ".flywheel", "events.jsonl")
	gitignorePath := filepath.Join(dir, ".flywheel", ".gitignore")

	b, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(b) != 0 {
		t.Errorf("events.jsonl = %q, want empty", b)
	}

	b, err = os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(b) != "runs/\nworktrees/\nlocks/\n" {
		t.Errorf(".gitignore = %q, want runs/\\nworktrees/\\nlocks/", b)
	}
}

func TestInitWritesGitattributes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".flywheel", ".gitattributes"))
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	if string(b) != gitattributesDefault {
		t.Errorf(".gitattributes = %q, want %q", b, gitattributesDefault)
	}
}

// TestInitGitattributesUnion checks init's .gitattributes marks the event log,
// legacy and sharded, merge=union (issue #436), and that an existing file is
// not rewritten to add it.
func TestInitGitattributesUnion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	path := filepath.Join(dir, ".flywheel", ".gitattributes")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .gitattributes: %v", err)
	}
	for _, want := range []string{"* text eol=lf\n", "\nevents.jsonl merge=union\n", "\nevents/*.jsonl merge=union\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf(".gitattributes = %q, want it to carry %q", b, want)
		}
	}
	if err := os.WriteFile(path, []byte("* text eol=lf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "* text eol=lf\n" {
		t.Errorf("an existing .gitattributes was rewritten: %q", b)
	}
}

func TestInitForceLeavesExistingGitattributesUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	path := filepath.Join(dir, ".flywheel", ".gitattributes")
	custom := []byte("*.txt eol=crlf\n")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatalf("write custom .gitattributes: %v", err)
	}
	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read .gitattributes: %v", err)
	}
	if string(b) != string(custom) {
		t.Error("--force overwrote an existing .gitattributes")
	}
}

func TestInitForceLeavesExistingEventLogUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	eventsPath := filepath.Join(dir, ".flywheel", "events.jsonl")

	// Simulate a real log with one appended event.
	if err := AppendEvent(dir, Event{TS: "2026-09-12T00:00:00Z", Task: "T1", Kind: "planned"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	b, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("test setup: event log is empty")
	}

	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}

	b2, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("re-read events.jsonl: %v", err)
	}
	if string(b2) != string(b) {
		t.Error("--force overwrote or truncated the event log")
	}
}

func TestInitWritesDefaultConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	cfg, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !exists {
		t.Fatal("Init() did not write .flywheel/config.json")
	}
	if !reflect.DeepEqual(cfg, DefaultConfig()) {
		t.Errorf("config = %+v, want the built-in default %+v", cfg, DefaultConfig())
	}
}

func TestInitForceKeepsExistingConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	configPath := filepath.Join(dir, ".flywheel", "config.json")
	custom := []byte(`{"version":1,"workers":[{"name":"sim","adapter":"sim","model":"f.jsonl"}]}`)
	if err := os.WriteFile(configPath, custom, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	if _, err := Init(dir, true); err != nil {
		t.Fatalf("Init() --force error = %v", err)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if string(b) != string(custom) {
		t.Error("--force overwrote the existing config.json")
	}
}

func TestInitRollbackRemovesCreatedConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Block .flywheel/.gitignore with a directory so Init fails after the
	// config.json it created.
	block := filepath.Join(dir, ".flywheel", ".gitignore")
	if err := os.MkdirAll(block, 0o755); err != nil {
		t.Fatalf("mkdir block: %v", err)
	}
	if _, err := Init(dir, false); err == nil {
		t.Skip("Init() did not fail; skipping rollback assertion")
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "config.json")); !os.IsNotExist(err) {
		t.Error("config.json left behind after rollback")
	}
	if _, err := os.Stat(filepath.Join(dir, "flywheel.md")); !os.IsNotExist(err) {
		t.Error("flywheel.md left behind after rollback")
	}
}

func TestInitRollbackRemovesCreatedGitattributes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Block .flywheel/.gitattributes with a directory so Init fails after
	// the config.json it created.
	block := filepath.Join(dir, ".flywheel", ".gitattributes")
	if err := os.MkdirAll(block, 0o755); err != nil {
		t.Fatalf("mkdir block: %v", err)
	}
	if _, err := Init(dir, false); err == nil {
		t.Skip("Init() did not fail; skipping rollback assertion")
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "config.json")); !os.IsNotExist(err) {
		t.Error("config.json left behind after rollback")
	}
	if _, err := os.Stat(filepath.Join(dir, "flywheel.md")); !os.IsNotExist(err) {
		t.Error("flywheel.md left behind after rollback")
	}
}

func TestInitSeedsModelAndVariant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitSeeded(dir, false, "x/y", "max", false); err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	cfg, exists, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if !exists {
		t.Fatal("InitSeeded() did not write .flywheel/config.json")
	}
	w, ok := cfg.Worker("default")
	if !ok {
		t.Fatal("seeded config has no default worker")
	}
	if w.Model != "x/y" {
		t.Errorf("seeded model = %q, want x/y", w.Model)
	}
	if w.Variant != "max" {
		t.Errorf("seeded variant = %q, want max", w.Variant)
	}
}

func TestInitSeedsVariantOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitSeeded(dir, false, "", "max", false); err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	w, ok := cfg.Worker("default")
	if !ok {
		t.Fatal("seeded config has no default worker")
	}
	if w.Model != DefaultConfig().Workers[0].Model {
		t.Errorf("variant-only seed changed model: got %q, want %q", w.Model, DefaultConfig().Workers[0].Model)
	}
	if w.Variant != "max" {
		t.Errorf("seeded variant = %q, want max", w.Variant)
	}
}

func TestInitSeededKeepsExistingConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	configPath := filepath.Join(dir, ".flywheel", "config.json")
	custom := []byte(`{"version":1,"workers":[{"name":"sim","adapter":"sim","model":"f.jsonl"}]}`)
	if err := os.WriteFile(configPath, custom, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	if _, _, err := InitSeeded(dir, true, "x/y", "max", false); err != nil {
		t.Fatalf("InitSeeded() --force error = %v", err)
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if string(b) != string(custom) {
		t.Error("InitSeeded() --force overwrote the existing config.json")
	}
}

// gitInit makes dir a throwaway git repository; no commit is needed because
// the ignore check reads the work tree.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", append(gitInitFlags(), "init", "-q")...)
	cmd.Dir = dir
	if _, err := cmd.Output(); err != nil {
		var e *exec.ExitError
		if errors.As(err, &e) {
			t.Fatalf("git init: %v", err)
		}
		t.Skipf("git unavailable: %v", err)
	}
}

func TestIgnoredStateFilesReportsGitIgnored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/*\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	gitInit(t, dir)

	ignored := IgnoredStateFiles(dir)
	sort.Strings(ignored)
	want := []string{".flywheel/config.json", ".flywheel/events.jsonl", ".flywheel/events/@floor.jsonl", ".flywheel/state.json"}
	if !reflect.DeepEqual(ignored, want) {
		t.Errorf("IgnoredStateFiles() = %v, want %v", ignored, want)
	}
}

func TestIgnoredStateFilesReportsNothingWhenNotIgnored(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gitInit(t, dir)

	if ignored := IgnoredStateFiles(dir); len(ignored) != 0 {
		t.Errorf("IgnoredStateFiles() = %v, want none", ignored)
	}
}

func TestIgnoredStateFilesReportsNothingOutsideGit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if ignored := IgnoredStateFiles(dir); len(ignored) != 0 {
		t.Errorf("IgnoredStateFiles() = %v, want none", ignored)
	}
}

func TestIgnoreMarkdownCreatesFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	added, err := IgnoreMarkdown(dir)
	if err != nil {
		t.Fatalf("IgnoreMarkdown() error = %v", err)
	}
	if !added {
		t.Error("IgnoreMarkdown() added = false, want true for a missing .gitignore")
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(b) != "flywheel.md\n" {
		t.Errorf(".gitignore = %q, want %q", b, "flywheel.md\n")
	}
}

func TestIgnoreMarkdownAppendsAfterOtherLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("runs/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	added, err := IgnoreMarkdown(dir)
	if err != nil {
		t.Fatalf("IgnoreMarkdown() error = %v", err)
	}
	if !added {
		t.Error("IgnoreMarkdown() added = false, want true")
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	if string(b) != "runs/\nflywheel.md\n" {
		t.Errorf(".gitignore = %q, want %q", b, "runs/\nflywheel.md\n")
	}
}

func TestIgnoreMarkdownNeverDuplicates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := IgnoreMarkdown(dir); err != nil {
		t.Fatalf("IgnoreMarkdown() first call error = %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	added, err := IgnoreMarkdown(dir)
	if err != nil {
		t.Fatalf("IgnoreMarkdown() second call error = %v", err)
	}
	if added {
		t.Error("IgnoreMarkdown() second call added = true, want false (already present)")
	}
	after, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("re-read .gitignore: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf(".gitignore changed on repeat call: got %q, want %q", after, before)
	}
}

func TestIgnoreMarkdownLeavesExistingLineAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	custom := []byte("node_modules/\nflywheel.md\ndist/\n")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), custom, 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	added, err := IgnoreMarkdown(dir)
	if err != nil {
		t.Fatalf("IgnoreMarkdown() error = %v", err)
	}
	if added {
		t.Error("IgnoreMarkdown() added = true, want false for an existing exact line")
	}
	b, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("re-read .gitignore: %v", err)
	}
	if string(b) != string(custom) {
		t.Errorf(".gitignore changed: got %q, want untouched %q", b, custom)
	}
}

func TestInitAgentsMDWritesBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitSeeded(dir, false, "", "", true); err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(b)
	for _, want := range []string{
		agentsMDStart, agentsMDEnd,
		"npx skills add suzworx/flywheel --skill <name>",
		"- flywheel: lead",
		"- flywheel-worker: worker",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("AGENTS.md missing %q, got %q", want, content)
		}
	}
}

func TestInitAgentsMDRerunReplacesBlockNotDuplicate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitSeeded(dir, false, "", "", true); err != nil {
		t.Fatalf("InitSeeded() error = %v", err)
	}
	path := filepath.Join(dir, "AGENTS.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}

	_, pieces, err := InitSeeded(dir, false, "", "", true)
	if err != nil {
		t.Fatalf("InitSeeded() second call error = %v", err)
	}
	for _, p := range pieces {
		if p.Path == "AGENTS.md" && p.Added {
			t.Error("AGENTS.md piece Added = true on an unchanged rerun, want false")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read AGENTS.md: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("AGENTS.md changed on an idempotent rerun: got %q, want %q", after, before)
	}
	if n := strings.Count(string(after), agentsMDStart); n != 1 {
		t.Errorf("AGENTS.md has %d start markers, want exactly 1", n)
	}
}

func TestInitAgentsMDKeepsExistingContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("own text\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	if _, _, err := InitSeeded(dir, false, "", "", true); err != nil {
		t.Fatalf("InitSeeded() --agents-md error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	content := string(b)
	if !strings.Contains(content, "own text") {
		t.Errorf("AGENTS.md lost its existing content: got %q", content)
	}
	if !strings.Contains(content, "- flywheel-worker: worker") {
		t.Errorf("AGENTS.md missing the persona block: got %q", content)
	}
}

// TestInitHooksWritesBothFilesOnce checks InitHooks creates both hook files
// on a fresh call and reports them added.
func TestInitHooksWritesBothFilesOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, pieces, err := InitHooks(dir)
	if err != nil {
		t.Fatalf("InitHooks() error = %v", err)
	}
	want := map[string]bool{".claude/settings.json": true, ".opencode/plugin/flywheel-session.mjs": true}
	if len(pieces) != len(want) {
		t.Fatalf("InitHooks() pieces = %+v, want %d entries", pieces, len(want))
	}
	for _, p := range pieces {
		if !want[p.Path] {
			t.Errorf("InitHooks() unexpected piece %q", p.Path)
		}
		if !p.Added {
			t.Errorf("InitHooks() piece %s Added = false, want true on a fresh call", p.Path)
		}
	}
	for path, wantSub := range map[string]string{
		".claude/settings.json":                 "SessionStart",
		".opencode/plugin/flywheel-session.mjs": "session_start",
	} {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(b), wantSub) {
			t.Errorf("%s missing %q, got %q", path, wantSub, b)
		}
	}
}

// TestInitHooksSecondRunByteIdentical checks a rerun leaves both hook files
// byte-identical: like every other init piece, they're created only once.
func TestInitHooksSecondRunByteIdentical(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitHooks(dir); err != nil {
		t.Fatalf("InitHooks() error = %v", err)
	}
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	pluginPath := filepath.Join(dir, ".opencode", "plugin", "flywheel-session.mjs")
	before1, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	before2, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}

	_, pieces, err := InitHooks(dir)
	if err != nil {
		t.Fatalf("InitHooks() second call error = %v", err)
	}
	for _, p := range pieces {
		if p.Added {
			t.Errorf("InitHooks() second call added %s, want none", p.Path)
		}
	}
	after1, err := os.ReadFile(settingsPath)
	if err != nil || string(after1) != string(before1) {
		t.Errorf("settings.json changed on rerun: got %q, err %v, want %q", after1, err, before1)
	}
	after2, err := os.ReadFile(pluginPath)
	if err != nil || string(after2) != string(before2) {
		t.Errorf("plugin changed on rerun: got %q, err %v, want %q", after2, err, before2)
	}
}

// TestInitHooksLeavesExistingSettingsUntouched checks a preexisting
// .claude/settings.json is never overwritten.
func TestInitHooksLeavesExistingSettingsUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	custom := []byte(`{"own":"settings"}`)
	if err := os.WriteFile(settingsPath, custom, 0o644); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	_, pieces, err := InitHooks(dir)
	if err != nil {
		t.Fatalf("InitHooks() error = %v", err)
	}
	for _, p := range pieces {
		if p.Path == ".claude/settings.json" && p.Added {
			t.Error("InitHooks() reported settings.json added over an existing file")
		}
	}
	b, err := os.ReadFile(settingsPath)
	if err != nil || string(b) != string(custom) {
		t.Errorf("settings.json changed: got %q, err %v, want untouched %q", b, err, custom)
	}
	if _, err := os.Stat(filepath.Join(dir, ".opencode", "plugin", "flywheel-session.mjs")); err != nil {
		t.Errorf("plugin not created alongside an existing settings.json: %v", err)
	}
}

// TestInitHooksRollbackRemovesCreatedFile checks that when the plugin file
// can't be written, the settings.json this same call already created is
// rolled back so a retry starts clean.
func TestInitHooksRollbackRemovesCreatedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Block .opencode/plugin/flywheel-session.mjs with a directory so the
	// call fails after settings.json is already written.
	block := filepath.Join(dir, ".opencode", "plugin", "flywheel-session.mjs")
	if err := os.MkdirAll(block, 0o755); err != nil {
		t.Fatalf("mkdir block: %v", err)
	}
	if _, _, err := InitHooks(dir); err == nil {
		t.Skip("InitHooks() did not fail; skipping rollback assertion")
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("settings.json left behind after rollback")
	}
}

// TestInitHooksStopRunsGate checks InitHooks creates a Stop hook that runs
// flywheel gate and blocks ending the session while work is left unjudged.
func TestInitHooksStopRunsGate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := InitHooks(dir); err != nil {
		t.Fatalf("InitHooks() error = %v", err)
	}

	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}

	var cfg claudeSettings
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("unmarshal settings.json: %v", err)
	}

	if len(cfg.Hooks.Stop) != 1 {
		t.Fatalf("Stop hook groups = %d, want 1", len(cfg.Hooks.Stop))
	}
	if len(cfg.Hooks.Stop[0].Hooks) != 1 {
		t.Fatalf("Stop hook commands = %d, want 1", len(cfg.Hooks.Stop[0].Hooks))
	}

	cmd := cfg.Hooks.Stop[0].Hooks[0].Command
	if cmd != claudeStopGateHook {
		t.Errorf("Stop hook command = %q, want %q", cmd, claudeStopGateHook)
	}

	for _, want := range []string{"flywheel gate", "stop_hook_active", "exit 2"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("Stop hook missing %q, got %q", want, cmd)
		}
	}
}

func TestInitAgentsMDRollbackRestoresPreexisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	path := filepath.Join(dir, "AGENTS.md")
	custom := []byte("custom AGENTS.md that must survive a failed init step\n")
	if err := os.WriteFile(path, custom, 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	// Block .gitattributes so InitSeeded fails on a later piece, after it has
	// already rewritten AGENTS.md (no markers means the write is not a no-op).
	gitattributesPath := filepath.Join(dir, ".flywheel", ".gitattributes")
	if err := os.Remove(gitattributesPath); err != nil {
		t.Fatalf("remove .gitattributes: %v", err)
	}
	if err := os.Mkdir(gitattributesPath, 0o755); err != nil {
		t.Fatalf("mkdir .gitattributes obstruction: %v", err)
	}

	if _, _, err := InitSeeded(dir, false, "", "", true); err == nil {
		t.Skip("directory obstruction did not fail the write; skipping restore assertion")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if string(b) != string(custom) {
		t.Errorf("AGENTS.md not restored after failed init: got %q, want %q", b, custom)
	}
}

// TestInitHooksStopHookRunsGate executes the installed Stop command with a
// stub flywheel on PATH (#296 review): gate's exit 6 blocks (exit 2) with the
// report on stderr and gate pointed at CLAUDE_PROJECT_DIR; a clear floor and
// a gate error both allow the stop; stop_hook_active allows it without
// running gate at all.
func TestInitHooksStopHookRunsGate(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on PATH")
	}
	bin := t.TempDir()
	stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$STUB_ARGS\"\necho gate-report\nexit ${STUB_RC:-0}\n"
	if err := os.WriteFile(filepath.Join(bin, "flywheel"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	project := t.TempDir()
	run := func(rc, stdin string) (int, string, string) {
		t.Helper()
		argsFile := filepath.Join(t.TempDir(), "args")
		cmd := exec.Command(sh, "-c", claudeStopGateHook)
		cmd.Env = append(os.Environ(),
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"STUB_RC="+rc, "STUB_ARGS="+argsFile, "CLAUDE_PROJECT_DIR="+project)
		cmd.Stdin = strings.NewReader(stdin)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("run hook: %v", err)
			}
			code = ee.ExitCode()
		}
		args, _ := os.ReadFile(argsFile)
		return code, stderr.String(), string(args)
	}
	code, stderr, args := run("6", `{"session_id":"s1","stop_hook_active":false}`)
	if code != 2 || !strings.Contains(stderr, "gate-report") {
		t.Errorf("gate exit 6: hook exit %d stderr %q, want 2 with the report", code, stderr)
	}
	if !strings.Contains(args, "gate --dir "+project) {
		t.Errorf("gate args = %q, want gate --dir %s", args, project)
	}
	if code, _, _ := run("0", `{"stop_hook_active":false}`); code != 0 {
		t.Errorf("gate exit 0: hook exit %d, want 0", code)
	}
	if code, _, _ := run("1", `{"stop_hook_active":false}`); code != 0 {
		t.Errorf("gate error (exit 1): hook exit %d, want 0 — an error must never trap the session", code)
	}
	code, _, args = run("6", `{"session_id":"s1","stop_hook_active": true}`)
	if code != 0 || args != "" {
		t.Errorf("stop_hook_active: hook exit %d, gate args %q, want 0 without running gate", code, args)
	}
}
