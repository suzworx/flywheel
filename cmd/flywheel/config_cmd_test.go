package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
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
