package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestGateEnvFilter checks gateEnvFor keeps the essentials, exact and prefix
// allow entries and Windows' "=C:" entries in order, drops the rest, and
// returns environ unchanged for a nil allow-list.
func TestGateEnvFilter(t *testing.T) {
	t.Parallel()
	environ := []string{"=C:=C:\\work", "PATH=/bin", "FW_STRAY=1", "GOPATH=/go", "GOFLAGS=-mod=mod", "HOME=/h", "MY_TOKEN=x", "KEEP=1", "KEEPER=2"}
	got := gateEnvFor(environ, []string{"GO*", "KEEP"}, false)
	want := []string{"=C:=C:\\work", "PATH=/bin", "GOPATH=/go", "GOFLAGS=-mod=mod", "HOME=/h", "KEEP=1"}
	if !slices.Equal(got, want) {
		t.Errorf("gateEnvFor = %q, want %q", got, want)
	}
	if got := gateEnvFor(environ, nil, false); !slices.Equal(got, environ) {
		t.Errorf("nil allow = %q, want environ unchanged", got)
	}
	if got := gateEnvFor(environ, []string{}, false); !slices.Equal(got, []string{"=C:=C:\\work", "PATH=/bin", "HOME=/h"}) {
		t.Errorf("empty allow = %q, want the essentials only", got)
	}
}

// TestGateEnvCaseFold checks names match case-insensitively only when fold
// is set (Windows), for essentials, exact and prefix entries.
func TestGateEnvCaseFold(t *testing.T) {
	t.Parallel()
	environ := []string{"Path=C:\\bin", "go_x=1", "keep=1", "systemroot=C:\\Windows"}
	allow := []string{"GO*", "KEEP"}
	if got := gateEnvFor(environ, allow, true); !slices.Equal(got, environ) {
		t.Errorf("fold = %q, want all kept", got)
	}
	if got := gateEnvFor(environ, allow, false); len(got) != 0 {
		t.Errorf("no fold = %q, want none kept", got)
	}
}

// TestGateEnvConfigValidate checks Config.Validate refuses an empty entry, an
// entry with '=' and a '*' anywhere but last, naming gates.env_allow[i], and
// accepts good entries.
func TestGateEnvConfigValidate(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		entry, want string
	}{
		{"", "gates.env_allow[1]"},
		{" ", "gates.env_allow[1]"},
		{"A=B", "gates.env_allow[1]"},
		{"*GO", "gates.env_allow[1]"},
		{"G*O", "gates.env_allow[1]"},
		{"GO**", "gates.env_allow[1]"},
	} {
		cfg := DefaultConfig()
		cfg.Gates = &GatesConfig{EnvAllow: []string{"OK", c.entry}}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("entry %q: Validate() = %v, want an error naming %s", c.entry, err, c.want)
		}
	}
	cfg := DefaultConfig()
	cfg.Gates = &GatesConfig{EnvAllow: []string{"GO*", "CI", "NODE_OPTIONS"}}
	if err := cfg.Validate(); err != nil {
		t.Errorf("good entries: Validate() = %v, want nil", err)
	}
	if got := DefaultConfig().GateEnvAllow(); got != nil {
		t.Errorf("unset GateEnvAllow() = %q, want nil", got)
	}
}

// TestGateEnvEndToEnd runs a gate that fails when a stray variable is set:
// it fails with no allow-list, passes under one that drops the variable,
// fails again when the variable is allowed, and FLYWHEEL_BASE stays visible.
// not parallel: t.Setenv sets FW_STRAY_809 in the process environment.
func TestGateEnvEndToEnd(t *testing.T) {
	t.Setenv("FW_STRAY_809", "1")
	gate := `test -z "$FW_STRAY_809"`
	for _, c := range []struct {
		name   string
		allow  []string
		wantRC bool
	}{
		{"nil allow inherits the stray var", nil, false},
		{"allow GO* drops it", []string{"GO*"}, true},
		{"allowed exactly", []string{"FW_STRAY_809"}, false},
		{"allowed by prefix", []string{"FW_*"}, false},
	} {
		rc, _, out, _, err := runGateStagesEnv(t.TempDir(), gate, "", c.allow)
		if err != nil {
			t.Fatalf("%s: runGateStagesEnv error = %v", c.name, err)
		}
		if (rc == 0) != c.wantRC {
			t.Errorf("%s: rc %d (out %q), want pass=%v", c.name, rc, out, c.wantRC)
		}
	}
	rc, _, out, _, err := runGateStagesEnv(t.TempDir(), `test "$FLYWHEEL_BASE" = base809`, "base809", []string{"GO*"})
	if err != nil || rc != 0 {
		t.Errorf("FLYWHEEL_BASE under an allow-list: rc %d, err %v, out %q; want 0", rc, err, out)
	}
}

// TestGateEnvAllowFor checks gateAllowFor keeps a nil allow-list nil, appends
// the needs-env names not already allowed, and never aliases its input.
func TestGateEnvAllowFor(t *testing.T) {
	t.Parallel()
	if got := gateAllowFor(nil, []string{"A"}); got != nil {
		t.Errorf("gateAllowFor(nil, [A]) = %q, want nil", got)
	}
	allow := make([]string, 2, 8)
	copy(allow, []string{"GO*", "A"})
	got := gateAllowFor(allow, []string{"A", "B", "B"})
	if want := []string{"GO*", "A", "B"}; !slices.Equal(got, want) {
		t.Errorf("gateAllowFor = %q, want %q", got, want)
	}
	got[0] = "CHANGED"
	if allow[0] != "GO*" || slices.Contains(allow[:cap(allow)], "B") {
		t.Errorf("gateAllowFor aliased its input: allow = %q", allow[:cap(allow)])
	}
	if got := gateAllowFor([]string{}, nil); got == nil || len(got) != 0 {
		t.Errorf("gateAllowFor([], nil) = %#v, want an empty non-nil list", got)
	}
}

// TestGateEnvNeedsEnvPassThrough checks a brief's needs-env name reaches its
// gate under an allow-list that does not name it, through gateAllowFor.
// not parallel: t.Setenv sets FW_NEEDED_809 in the process environment.
func TestGateEnvNeedsEnvPassThrough(t *testing.T) {
	t.Setenv("FW_NEEDED_809", "1")
	gate := `test -n "$FW_NEEDED_809"`
	if rc, _, _, _, err := runGateStagesEnv(t.TempDir(), gate, "", []string{"GO*"}); err != nil || rc == 0 {
		t.Errorf("allow [GO*]: rc %d, err %v; want a failure", rc, err)
	}
	if rc, _, out, _, err := runGateStagesEnv(t.TempDir(), gate, "", gateAllowFor([]string{"GO*"}, []string{"FW_NEEDED_809"})); err != nil || rc != 0 {
		t.Errorf("allow [GO*] + needs-env: rc %d, err %v, out %q; want 0", rc, err, out)
	}
}

// TestGateEnvValidateNeedsEnv checks ValidateTask, under gates.env_allow
// ["GO*"], gives a brief's gate its needs-env variable while still dropping
// a stray one.
// not parallel: t.Setenv sets FW_NEEDED_809 and FW_STRAY_809.
func TestGateEnvValidateNeedsEnv(t *testing.T) {
	t.Setenv("FW_NEEDED_809", "1")
	t.Setenv("FW_STRAY_809", "1")
	dir := needsEnvTask(t, "owns: a.go\nneeds: none\nneeds-env: FW_NEEDED_809\ngate: test -n \"$FW_NEEDED_809\" && test -z \"$FW_STRAY_809\"\n\n# TASK x\n")
	initRepo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "brief"})
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Gates = &GatesConfig{EnvAllow: []string{"GO*"}}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.GatesOK || len(res.Gates) != 1 || res.Gates[0].RC != 0 {
		t.Errorf("ValidateTask() gates = %+v (ok %v), want gate 1 to pass", res.Gates, res.GatesOK)
	}
}
