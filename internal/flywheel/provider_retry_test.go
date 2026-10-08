package flywheel

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestProviderErrorRetry checks that a provider-error finish is resumed once
// after a fixed 2m wait with a .error-<n>.txt delta, and only once, and never
// when limits.provider_error_retries is 0 or the attempt stopped (issue #830).
func TestProviderErrorRetry(t *testing.T) {
	// not parallel: each subtest sets PATH to a fake claude and the package's commandHook
	const session = "ses_err_001"
	head := fmt.Sprintf(`{"type":"system","subtype":"init","session_id":%q}`+"\n", session) +
		fmt.Sprintf(`{"type":"assistant","session_id":%q,"message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"a.go"}}]}}`+"\n", session)
	fail := head + fmt.Sprintf(`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":500,"session_id":%q,"result":"Internal server error"}`+"\n", session)
	stop := head + fmt.Sprintf(`{"type":"result","subtype":"success","stop_reason":"end_turn","session_id":%q,"total_cost_usd":0.01}`+"\n", session)
	zero := 0
	for _, tc := range []struct {
		name       string
		retries    *int
		streams    []string
		wantReason string
		wantDisp   int
	}{
		{"error then stop", nil, []string{fail, stop}, "stop", 2},
		{"error twice", nil, []string{fail, fail, stop}, "error", 2},
		{"retries 0", &zero, []string{fail, stop}, "error", 1},
		{"stop", nil, []string{stop, stop}, "stop", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupTask(t)
			brief := "owns: a.go\nneeds: none\ngate: go vet ./...\n\n# TASK: x\n"
			if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(brief), 0o644); err != nil {
				t.Fatalf("write brief: %v", err)
			}
			cfg := Config{Version: 1, Workers: []Worker{{Name: "claude", Adapter: "claude", Model: "claude-sonnet-5"}},
				Limits: Limits{ProviderErrorRetries: tc.retries}}
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
			for i, s := range tc.streams {
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
			var sleeps []time.Duration
			var buf bytes.Buffer
			res, err := RunResumingLimits(dir, RunOptions{Task: "T1", Progress: &buf},
				func(d time.Duration) { sleeps = append(sleeps, d) }, time.Now)
			if err != nil {
				t.Fatalf("RunResumingLimits() error = %v; progress:\n%s", err, buf.String())
			}
			if res.Reason != tc.wantReason {
				t.Errorf("result = %+v, want reason %s; progress:\n%s", res, tc.wantReason, buf.String())
			}
			evs, err := ReadEvents(dir)
			if err != nil {
				t.Fatalf("ReadEvents() error = %v", err)
			}
			var disp []Event
			for _, e := range evs {
				if e.Kind == "dispatched" && e.Task == "T1" {
					disp = append(disp, e)
				}
			}
			if len(disp) != tc.wantDisp || len(reqs) != tc.wantDisp {
				t.Fatalf("%d dispatched events, %d requests, want %d; progress:\n%s", len(disp), len(reqs), tc.wantDisp, buf.String())
			}
			if tc.wantDisp == 1 {
				if len(sleeps) != 0 {
					t.Errorf("sleeps = %v, want none", sleeps)
				}
				return
			}
			if len(sleeps) != 1 || sleeps[0] != 2*time.Minute {
				t.Errorf("sleeps = %v, want one of 2m", sleeps)
			}
			if !reqs[1].Resume || reqs[1].Session != session || !strings.HasSuffix(filepath.ToSlash(reqs[1].PromptFile), ".flywheel/briefs/T1.error-1.txt") {
				t.Errorf("retry request = %+v, want a resume of %s with .flywheel/briefs/T1.error-1.txt", reqs[1], session)
			}
			want := "owns: a.go\nneeds: none\ngate: go vet ./...\n\n" + errorContinue
			delta, err := os.ReadFile(filepath.Join(dir, ".flywheel", "briefs", "T1.error-1.txt"))
			if err != nil || string(delta) != want {
				t.Errorf("delta = %q (%v), want %q", delta, err, want)
			}
			// The retry's dispatched event records Run's per-attempt snapshot of
			// the delta (the same bytes), not the source path.
			snap, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(disp[1].Brief)))
			if err != nil || string(snap) != want {
				t.Errorf("retry dispatched brief %s = %q (%v), want the error-1 delta", disp[1].Brief, snap, err)
			}
			if !strings.Contains(buf.String(), "T1 provider-error; retrying once in 2m0s (limits.provider_error_retries)") {
				t.Errorf("progress = %q, want the retry line", buf.String())
			}
		})
	}
}
