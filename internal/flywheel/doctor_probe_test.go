package flywheel

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// probeHelperName is the base name the test binary is installed under to
// stand in for a stdin-prompting CLI, as TestMain's fake claude does; it
// runs before TestMain so the helper needs no edit there. Its one argument
// is the mode: "stdin" prints "STEP stop" when stdin holds a prompt and
// otherwise writes "no input" to stderr and exits 3; "fail" always does the
// latter (issue #637).
const probeHelperName = "doctor-probe"

func init() {
	if !strings.HasPrefix(filepath.Base(os.Args[0]), probeHelperName) || len(os.Args) != 2 {
		return
	}
	in, _ := io.ReadAll(os.Stdin)
	if os.Args[1] == "stdin" && strings.TrimSpace(string(in)) != "" {
		fmt.Println("STEP stop")
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, "no input")
	os.Exit(3)
}

// probeAdapter is a stdin-prompting Adapter that launches the probe helper
// in mode and records the request it was asked to launch.
type probeAdapter struct {
	bin, mode string
	mu        sync.Mutex
	got       []RunRequest
}

func (a *probeAdapter) Name() string { return "fake" }

func (a *probeAdapter) Command(r RunRequest) (string, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, r)
	return a.bin, []string{a.mode}
}

func (a *probeAdapter) Parse(line []byte) (Observation, bool) {
	if s := strings.TrimSpace(string(line)); strings.HasPrefix(s, "STEP ") {
		return Observation{Kind: "step", Reason: strings.TrimPrefix(s, "STEP ")}, true
	}
	return Observation{}, false
}

// Stdin feeds the prompt file, as claudeAdapter does.
func (a *probeAdapter) Stdin(r RunRequest) io.Reader {
	b, _ := os.ReadFile(r.PromptFile)
	return strings.NewReader(string(b))
}

// installProbeHelper copies the test binary into a temp dir as the probe
// helper and returns its path.
func installProbeHelper(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	fake := filepath.Join(t.TempDir(), probeHelperName)
	if runtime.GOOS == "windows" {
		fake += ".exe"
	}
	if err := linkOrCopy(exe, fake); err != nil {
		t.Fatalf("install probe helper: %v", err)
	}
	return fake
}

// TestProbeFeedsStdin checks a probe of a stdin-prompting adapter feeds it
// the probe prompt and launches it with the worker's permission mode and max
// turns 1, so it classifies ok (issue #637: the prompt never reached stdin).
func TestProbeFeedsStdin(t *testing.T) {
	t.Parallel()
	adap := &probeAdapter{bin: installProbeHelper(t), mode: "stdin"}
	worker := Worker{Name: "c", Adapter: "fake", Model: "m", PermissionMode: "acceptEdits"}
	class, detail := probeModel(t.TempDir(), worker, adap, "m")
	if class != ClassOK || detail != "" {
		t.Errorf("probeModel = (%q, %q), want (ok, \"\")", class, detail)
	}
	if len(adap.got) != 1 {
		t.Fatalf("Command called %d times, want 1", len(adap.got))
	}
	r := adap.got[0]
	if r.PermissionMode != "acceptEdits" || r.MaxTurns != 1 || r.Model != "m" || r.PromptFile == "" {
		t.Errorf("request = %+v, want PermissionMode acceptEdits, MaxTurns 1, Model m and a PromptFile", r)
	}
	if _, err := os.Stat(r.PromptFile); !os.IsNotExist(err) {
		t.Errorf("prompt file %s left behind (stat err %v)", r.PromptFile, err)
	}
}

// TestProbeErrorDetail checks a probe whose process exits non-zero without a
// stop classifies error with the exit code and first stderr line (#637).
func TestProbeErrorDetail(t *testing.T) {
	t.Parallel()
	adap := &probeAdapter{bin: installProbeHelper(t), mode: "fail"}
	class, detail := probeModel(t.TempDir(), Worker{Name: "c", Adapter: "fake", Model: "m"}, adap, "m")
	if class != ClassError || !strings.Contains(detail, "exit 3") || !strings.Contains(detail, "no input") {
		t.Errorf("probeModel = (%q, %q), want error with exit 3 and no input", class, detail)
	}
}

// TestProbeRecordDetail checks a recorded probe's note carries its Detail,
// and a probe without one keeps the plain note (#637).
func TestProbeRecordDetail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	probes := []DoctorProbe{{Model: "m1", Class: ClassOK}, {Model: "m2", Class: ClassError, Detail: "exit 3: no input"}}
	if err := RecordProbes(dir, probes); err != nil {
		t.Fatalf("RecordProbes() error = %v", err)
	}
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if len(evs) != 2 || evs[0].Note != "flywheel doctor" || evs[1].Note != "flywheel doctor: exit 3: no input" || evs[1].Reason != ClassError {
		t.Errorf("events = %+v, want notes \"flywheel doctor\" and \"flywheel doctor: exit 3: no input\"", evs)
	}
}

// TestProbeStartDetail checks a binary that cannot start gives its error.
func TestProbeStartDetail(t *testing.T) {
	t.Parallel()
	adap := &probeAdapter{bin: filepath.Join(t.TempDir(), "no-such-cli"), mode: "stdin"}
	class, detail := probeModel(t.TempDir(), Worker{Name: "c", Adapter: "fake", Model: "m"}, adap, "m")
	if class != ClassError || !strings.HasPrefix(detail, "start: ") {
		t.Errorf("probeModel = (%q, %q), want error with a start: detail", class, detail)
	}
}
