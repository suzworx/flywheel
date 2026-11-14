package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/suzworx/flywheel/internal/flywheel"
)

// TestConfigSetDashValue pins that a lone -- ends config's flag parsing, so a
// value beginning with - reaches Set (issue #543).
func TestConfigSetDashValue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args    []string
		dir     string
		want    []string
		wantErr bool
	}{
		{args: []string{"k", "--", "-x"}, dir: ".", want: []string{"k", "-x"}},
		{args: []string{"--", "k", "-x"}, dir: ".", want: []string{"k", "-x"}},
		{args: []string{"--dir", "D", "k", "--", "--dir", "E"}, dir: "D", want: []string{"k", "--dir", "E"}},
		{args: []string{"k", "--", "--"}, dir: ".", want: []string{"k", "--"}},
		{args: []string{"k", "-x"}, wantErr: true},
	} {
		dir, pos, err := parseConfigArgs(tc.args)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseConfigArgs(%q) = nil error, want undefined-flag error", tc.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseConfigArgs(%q) error = %v", tc.args, err)
			continue
		}
		if dir != tc.dir || !reflect.DeepEqual(pos, tc.want) {
			t.Errorf("parseConfigArgs(%q) = %q, %q; want %q, %q", tc.args, dir, pos, tc.dir, tc.want)
		}
	}
}

// configSetHelperEnv carries runConfigSet's arguments to the re-executed test
// binary, separated by \x1f, so a test can observe its streams and exit code.
const configSetHelperEnv = "FLYWHEEL_TEST_CONFIGSET_ARGS"

// TestConfigSetWorkerToolsWarns checks `config set workers.<name>.allowed_tools`
// on a claude worker: a list without Bash persists, exits 0 and warns on
// stderr; a list with Bash prints no warning (issue #956).
func TestConfigSetWorkerToolsWarns(t *testing.T) {
	t.Parallel()
	if v, ok := os.LookupEnv(configSetHelperEnv); ok {
		runConfigSet(strings.Split(v, "\x1f"))
		os.Exit(0)
	}
	dir := t.TempDir()
	cfg := flywheel.DefaultConfig()
	cfg.Workers[0].Adapter = "claude"
	if err := flywheel.WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}
	key := "workers." + cfg.Workers[0].Name + ".allowed_tools"
	set := func(value string) (string, string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigSetWorkerToolsWarns$")
		cmd.Env = append(os.Environ(), configSetHelperEnv+"="+strings.Join([]string{"--dir", dir, key, value}, "\x1f"))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var e *exec.ExitError
		if errors.As(err, &e) {
			return stdout.String(), stderr.String(), e.ExitCode()
		}
		if err != nil {
			t.Fatalf("run child: %v", err)
		}
		return stdout.String(), stderr.String(), 0
	}
	stdout, stderr, code := set("WebFetch")
	if code != 0 {
		t.Fatalf("config set %s WebFetch = %d\n%s%s", key, code, stdout, stderr)
	}
	if want := key + " = WebFetch\n"; !strings.Contains(stdout, want) {
		t.Errorf("stdout lacks %q:\n%s", want, stdout)
	}
	if !strings.Contains(stderr, "flywheel config set: warning: ") || !strings.Contains(stderr, "Bash") {
		t.Errorf("stderr lacks the Bash warning:\n%s", stderr)
	}
	loaded, _, err := flywheel.LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if v, _ := loaded.Get(key); v != "WebFetch" {
		t.Errorf("persisted %s = %q, want WebFetch", key, v)
	}
	if stdout, stderr, code := set("Bash;;WebFetch"); code != 0 || strings.Contains(stderr, "warning") {
		t.Errorf("config set %s Bash;;WebFetch = %d, stderr %q; want 0 and no warning\n%s", key, code, stderr, stdout)
	}
}

// TestConfigUsageKeys pins that config's help mentions -- and keeps no
// hand-written subset of settable keys (issue #543).
func TestConfigUsageKeys(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	configUsage(&b)
	out := b.String()
	if !strings.Contains(out, "set <key> -- <value>") || !strings.Contains(out, "every settable key") {
		t.Errorf("configUsage lacks -- or the settable-key pointer:\n%s", out)
	}
	for _, stale := range []string{"limits.per_host)", "feedback.upstream", "max_parallel,"} {
		if strings.Contains(out, stale) {
			t.Errorf("configUsage contains stale key list %q:\n%s", stale, out)
		}
	}
}
