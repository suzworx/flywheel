package flywheel

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

var sgrRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// framesLedger is a small ledger with two units, T1 landed-ish and T2
// running, at a fixed now.
func framesLedger(t *testing.T) (string, time.Time) {
	t.Helper()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, e := range []Event{
		{TS: now.Add(-3 * time.Minute).Format(time.RFC3339), Task: "T1", Kind: "started", Session: "s1"},
		{TS: now.Add(-2 * time.Minute).Format(time.RFC3339), Task: "T1", Kind: "finished", Attempt: "r1", RC: intPtr(0), Session: "s1"},
		{TS: now.Add(-1 * time.Minute).Format(time.RFC3339), Task: "T2", Kind: "started", Session: "s2"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}
	return dir, now
}

func TestTUIFramesKeys(t *testing.T) {
	t.Parallel()
	dir, now := framesLedger(t)
	keys := `j <enter> <esc> : "andon" <enter>`
	frames, err := TUIFrames(dir, keys, 100, 30, now)
	if err != nil {
		t.Fatalf("TUIFrames: %v", err)
	}
	// One frame per token: the quoted run is typed whole, then drawn once.
	want := []string{"", "j", "<enter>", "<esc>", ":", `"andon"`, "<enter>"}
	var got []string
	for _, f := range frames {
		got = append(got, f.Key)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("frame keys = %q, want %q", got, want)
	}
	plain := func(i int) string { return sgrRE.ReplaceAllString(frames[i].Frame, "") }
	if !strings.Contains(frames[0].Frame, "\x1b[") {
		t.Errorf("frame 0 has no colour:\n%s", frames[0].Frame)
	}
	if p := plain(0); !strings.Contains(strings.ToLower(p), "units") || !strings.Contains(p, "T1") || !strings.Contains(p, "T2") {
		t.Errorf("frame 0 is not the units view:\n%s", p)
	}
	if p := plain(2); !strings.Contains(p, "T1") || p == plain(1) {
		t.Errorf("frame after <enter> shows no unit detail:\n%s", p)
	}
	if p := plain(3); !strings.Contains(strings.ToLower(p), "units") {
		t.Errorf("frame after <esc> is not back at units:\n%s", p)
	}
	if p := plain(len(frames) - 1); !strings.Contains(strings.ToLower(firstLines(p, 8)), "andon") {
		t.Errorf("frame after :andon<enter> does not name Andon:\n%s", p)
	}
	again, err := TUIFrames(dir, keys, 100, 30, now)
	if err != nil {
		t.Fatalf("TUIFrames again: %v", err)
	}
	if !reflect.DeepEqual(frames, again) {
		t.Error("two renders at the same now differ")
	}
}

func TestTUIFramesBadKey(t *testing.T) {
	t.Parallel()
	for _, seq := range []string{"<nope>", `"open`, "ab"} {
		if _, err := ParseKeySeq(seq); err == nil {
			t.Errorf("ParseKeySeq(%q) = nil error, want one", seq)
		}
	}
	ks, err := ParseKeySeq(`<ctrl-a> <UP> "é/"`)
	if err != nil || len(ks) != 3 || len(ks[2].Keys) != 2 || ks[2].Keys[0].Rune != 'é' || ks[2].Keys[1].Rune != '/' {
		t.Errorf("ParseKeySeq = %+v, %v", ks, err)
	}
}

// firstLines is the first n lines of s.
func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	return strings.Join(lines[:min(n, len(lines))], "\n")
}
