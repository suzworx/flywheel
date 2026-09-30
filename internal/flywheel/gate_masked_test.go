package flywheel

import (
	"reflect"
	"testing"
)

// skipUnlessBash skips a test when gates do not run under bash: sh and cmd
// have no PIPESTATUS, so nothing is measured there (issue #704).
func skipUnlessBash(t *testing.T) {
	t.Helper()
	if !isBashArgv(ShellArgv("true")) {
		t.Skip("gates do not run under bash on this host")
	}
}

// TestGateMaskedStageTable checks the pure masking decision.
func TestGateMaskedStageTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		rc          int
		stages      []int
		idx, status int
		masked      bool
	}{
		{"first stage failed", 0, []int{1, 0}, 1, 1, true},
		{"sigpipe", 0, []int{141, 0}, 0, 0, false},
		{"all passed", 0, []int{0, 0}, 0, 0, false},
		{"middle stage failed", 0, []int{0, 2, 0}, 2, 2, true},
		{"gate failed", 1, []int{1, 1}, 0, 0, false},
		{"no stages", 0, nil, 0, 0, false},
		{"single stage", 0, []int{0}, 0, 0, false},
	}
	for _, c := range cases {
		idx, status, masked := maskedStage(c.rc, c.stages)
		if idx != c.idx || status != c.status || masked != c.masked {
			t.Errorf("%s: maskedStage(%d, %v) = %d, %d, %v; want %d, %d, %v",
				c.name, c.rc, c.stages, idx, status, masked, c.idx, c.status, c.masked)
		}
	}
}

// TestGateMaskedRunGateBase checks runGateBase reads a masked pipeline as its
// failing stage and leaves every other shape of gate alone.
func TestGateMaskedRunGateBase(t *testing.T) {
	t.Parallel()
	skipUnlessBash(t)
	cases := []struct {
		gate string
		rc   int
	}{
		{"sh -c 'exit 3' | cat", 3},
		{"echo hi | grep hi", 0},
		{"set -o pipefail; false | cat", 1},
		{"exit 4", 4},
		{"yes | head -1", 0},
	}
	for _, c := range cases {
		rc, _, _, err := runGateBase(t.TempDir(), c.gate, "")
		if err != nil {
			t.Fatalf("runGateBase(%q) error = %v", c.gate, err)
		}
		if rc != c.rc {
			t.Errorf("runGateBase(%q) rc = %d, want %d", c.gate, rc, c.rc)
		}
	}
}

// TestGateMaskedStagesRecorded checks the trailer sees the gate's last
// pipeline in both expansions (the rc and PIPESTATUS), that a gate exiting
// before its end records no stages, and that the output is unchanged.
func TestGateMaskedStagesRecorded(t *testing.T) {
	t.Parallel()
	skipUnlessBash(t)
	cases := []struct {
		gate   string
		rc     int
		stages []int
		out    string
	}{
		{"sh -c 'exit 3' | cat", 0, []int{3, 0}, ""},
		{"echo hi | grep hi", 0, []int{0, 0}, "hi\n"},
		{"true | false", 1, []int{0, 1}, ""},
		{"false | true | sh -c 'exit 5'", 5, []int{1, 0, 5}, ""},
		{"echo out; exit 4", 4, nil, "out\n"},
	}
	for _, c := range cases {
		rc, _, out, stages, err := runGateStages(t.TempDir(), c.gate, "base")
		if err != nil {
			t.Fatalf("runGateStages(%q) error = %v", c.gate, err)
		}
		if rc != c.rc || !reflect.DeepEqual(stages, c.stages) || string(out) != c.out {
			t.Errorf("runGateStages(%q) = rc %d, stages %v, out %q; want %d, %v, %q",
				c.gate, rc, stages, out, c.rc, c.stages, c.out)
		}
	}
}

// TestGateMaskedReadPipestatus checks a missing or garbage file yields no stages.
func TestGateMaskedReadPipestatus(t *testing.T) {
	t.Parallel()
	if got := readPipestatus(t.TempDir() + "/missing"); got != nil {
		t.Errorf("missing file stages = %v, want nil", got)
	}
	if got := maskedNote(1, 2, 3); got != "masked: pipeline stage 1 of 2 exited 3; the gate's own status was 0 (the filter's)" {
		t.Errorf("maskedNote = %q", got)
	}
}

// TestGateMaskedValidate checks a masked gate fails validate and records
// Reason "masked" with its note on the validated event.
func TestGateMaskedValidate(t *testing.T) {
	t.Parallel()
	skipUnlessBash(t)
	dir, err := initTask(t, []string{"sh -c 'exit 3' | cat"})
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	want := "masked: pipeline stage 1 of 2 exited 3; the gate's own status was 0 (the filter's)"
	g := res.Gates[0]
	if res.GatesOK || !g.Masked || g.RC != 3 || g.Note != want {
		t.Fatalf("GatesOK/Masked/RC/Note = %v/%v/%d/%q, want false/true/3/%q", res.GatesOK, g.Masked, g.RC, g.Note, want)
	}
	evs := kindEvents(t, dir, "validated")
	last := evs[len(evs)-1]
	if last.Reason != "masked" || last.Note != want || last.RC == nil || *last.RC != 3 {
		t.Errorf("validated event reason/note/rc = %q/%q/%v, want masked/%q/3", last.Reason, last.Note, last.RC, want)
	}
}
