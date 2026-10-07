package flywheel

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// workdirPromptMessage is the lead message adapter a sends for r: the claude
// stdin's first line, opencode's argument before --file, else the last
// argument (issue #805).
func workdirPromptMessage(t *testing.T, a Adapter, r RunRequest) string {
	t.Helper()
	if a.Name() == "claude" {
		first, _, _ := strings.Cut(claudeStdin(t, a, r), "\n")
		return first
	}
	_, args := a.Command(r)
	if a.Name() == "opencode" {
		return args[len(args)-3]
	}
	return args[len(args)-1]
}

// TestWithWorkdirPrompt checks withWorkdir leaves the message alone without a
// Workdir and otherwise appends one line naming the tree and the root.
func TestWithWorkdirPrompt(t *testing.T) {
	t.Parallel()
	if got := withWorkdir(freshMessage, RunRequest{}); got != freshMessage {
		t.Errorf("withWorkdir(no Workdir) = %q, want %q", got, freshMessage)
	}
	got := withWorkdir(freshMessage, RunRequest{Workdir: "/w/tree", Root: "/w/root"})
	if !strings.HasPrefix(got, freshMessage+" ") || !strings.Contains(got, "/w/tree") || !strings.Contains(got, "/w/root is the flywheel root") {
		t.Errorf("withWorkdir() = %q, want freshMessage then the tree and root", got)
	}
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("withWorkdir() = %q, want one line", got)
	}
	if got := withWorkdir(resumeMessage, RunRequest{Workdir: "/w/tree"}); strings.Contains(got, "flywheel root") || !strings.Contains(got, "/w/tree") {
		t.Errorf("withWorkdir(no Root) = %q, want the tree and no root clause", got)
	}
}

// TestAdaptersWorkdirPrompt checks every adapter's fresh and resumed lead
// message names Workdir when set and is unchanged when it is empty.
func TestAdaptersWorkdirPrompt(t *testing.T) {
	t.Parallel()
	brief := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(brief, []byte("do it\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"opencode", "claude", "codex", "pi"} {
		a, err := AdapterFor(name)
		if err != nil {
			t.Fatalf("AdapterFor(%s) error = %v", name, err)
		}
		for _, resume := range []bool{false, true} {
			r := RunRequest{Task: "T1", Model: "m", PromptFile: brief, Resume: resume}
			want := freshMessage
			if resume {
				r.Session, want = "s-1", resumeMessage
			}
			if got := workdirPromptMessage(t, a, r); !strings.HasPrefix(got, want) || strings.Contains(got, "working directory") {
				t.Errorf("%s resume=%v without Workdir: message = %q, want %q unchanged", name, resume, got, want)
			}
			r.Workdir, r.Root = filepath.FromSlash("/w/tree"), filepath.FromSlash("/w/root")
			if got := workdirPromptMessage(t, a, r); !strings.HasPrefix(got, want) || !strings.Contains(got, r.Workdir) {
				t.Errorf("%s resume=%v with Workdir: message = %q, want %q naming %s", name, resume, got, want, r.Workdir)
			}
		}
	}
}

// TestAdaptersWorkdirPromptCmdMetachar checks a Workdir holding a cmd.exe
// metacharacter drops the sentence from an argument-passing adapter on
// Windows only, while claude's stdin always carries it.
func TestAdaptersWorkdirPromptCmdMetachar(t *testing.T) {
	t.Parallel()
	r := RunRequest{Task: "T1", Model: "m", PromptFile: "brief.md", Workdir: "/w/a&b", Root: "/w/root"}
	if cmdSafe(r.Workdir) || !cmdSafe(r.Root) {
		t.Fatalf("cmdSafe(%q) / cmdSafe(%q) wrong", r.Workdir, r.Root)
	}
	msg := workdirPromptMessage(t, opencodeAdapter{}, r)
	if has := strings.Contains(msg, "working directory"); has == (runtime.GOOS == "windows") {
		t.Errorf("opencode message on %s = %q: sentence present = %v", runtime.GOOS, msg, has)
	}
	stdin, err := os.CreateTemp(t.TempDir(), "brief")
	if err != nil {
		t.Fatal(err)
	}
	stdin.Close()
	r.PromptFile = stdin.Name()
	if got := workdirPromptMessage(t, claudeAdapter{}, r); !strings.Contains(got, "/w/a&b") {
		t.Errorf("claude stdin message = %q, want it naming the workdir", got)
	}
}

// TestRunWorkdirPromptRequest checks Run's RunRequest carries Workdir and Root
// on a --workdir dispatch and on the delta continuing there, and neither on a
// plain dispatch; the fake claude's saved stdin names the tree.
func TestRunWorkdirPromptRequest(t *testing.T) {
	// not parallel: sets the package-level commandHook and t.Setenv PATH
	dir, wt := workdirRepo(t)
	plain, _ := workdirRepo(t)
	cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}}}
	for _, d := range []string{dir, plain} {
		if err := WriteConfig(d, cfg); err != nil {
			t.Fatal(err)
		}
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
	stream := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(stream, []byte(`{"type":"system","subtype":"init","session_id":"ses_wp"}`+"\n"+
		`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":"ses_wp","total_cost_usd":0.01}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeClaudeEnv, stream)
	t.Setenv(fakeClaudeStdinEnv, "stdin.marker")
	var got []RunRequest
	commandHook = func(r RunRequest) { got = append(got, r) }
	t.Cleanup(func() { commandHook = nil })

	if _, err := Run(dir, RunOptions{Task: "T1", Workdir: wt}); err != nil {
		t.Fatalf("Run(Workdir) error = %v", err)
	}
	delta := filepath.Join(t.TempDir(), "delta.txt")
	if err := os.WriteFile(delta, []byte("owns: a.go\nneeds: none\n\n# TASK: fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1", Resume: true, DeltaPath: delta}); err != nil {
		t.Fatalf("Run(delta) error = %v", err)
	}
	if _, err := Run(plain, RunOptions{Task: "T1"}); err != nil {
		t.Fatalf("Run(plain) error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("commandHook captured %d requests, want 3", len(got))
	}
	for i, r := range got[:2] {
		if !samePath(r.Workdir, wt) || !samePath(r.Root, dir) || !filepath.IsAbs(r.Root) {
			t.Errorf("request %d: Workdir, Root = %q, %q, want %s, %s", i, r.Workdir, r.Root, wt, dir)
		}
	}
	if !got[1].Resume {
		t.Errorf("delta request Resume = false, want a correction")
	}
	if got[2].Workdir != "" || got[2].Root != "" {
		t.Errorf("plain request: Workdir, Root = %q, %q, want both empty", got[2].Workdir, got[2].Root)
	}
	b, err := os.ReadFile(filepath.Join(wt, "stdin.marker"))
	if err != nil {
		t.Fatalf("read the worker's saved stdin: %v", err)
	}
	if first, _, _ := strings.Cut(string(b), "\n"); !strings.Contains(first, "Your working directory is "+got[1].Workdir) {
		t.Errorf("worker stdin lead = %q, want it naming %s", first, got[1].Workdir)
	}
}
