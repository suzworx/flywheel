package flywheel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// draftShip ships a fixture with an uncommitted owned change, cfg written as
// its config when set, and returns the result, the progress and the forge.
func draftShip(t *testing.T, cfg *Config, o ShipOptions, ff *fakeForge) (ShipResult, string, error) {
	t.Helper()
	f := newShipFixture(t, "exit 0", true)
	if cfg != nil {
		b, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		shipWrite(t, f.dir, ".flywheel/config.json", string(b))
	}
	shipWrite(t, f.wt, "src/a.go", "package src // task\n")
	o.Forge = ff
	return f.ship(t, o)
}

// shipNote is res's note for step, "" when the step did not run.
func shipNote(res ShipResult, step string) string {
	for _, s := range res.Steps {
		if s.Step == step {
			return s.Note
		}
	}
	return ""
}

// TestShipDraftReadyBeforeMerge (issue #765, a): by default the PR is opened
// as a draft and marked ready when ci passes, before the merge.
func TestShipDraftReadyBeforeMerge(t *testing.T) {
	t.Parallel()
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	res, out, err := draftShip(t, nil, ShipOptions{}, ff)
	if err != nil {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if !ff.draft || ff.created != 1 {
		t.Errorf("CreatePR draft = %v (%d call(s)), want one draft PR", ff.draft, ff.created)
	}
	if got := strings.Join(ff.calls, " "); !strings.HasPrefix(got, "ready") || !strings.HasSuffix(got, "merge") || ff.merges != 1 {
		t.Errorf("forge calls = %q, want ready before the one merge", got)
	}
	if note := shipNote(res, "ci"); !strings.Contains(note, "(marked ready for review)") {
		t.Errorf("ci note = %q, want it to say marked ready", note)
	}
	if note := shipNote(res, "pr"); !strings.HasPrefix(note, "opened draft #7") {
		t.Errorf("pr note = %q, want opened draft #7", note)
	}
}

// TestShipDraftOff (issue #765, b): ship.draft false opens the PR ready and
// never marks it ready.
func TestShipDraftOff(t *testing.T) {
	t.Parallel()
	cfg, off := DefaultConfig(), false
	cfg.Ship = &ShipConfig{Draft: &off}
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	res, out, err := draftShip(t, &cfg, ShipOptions{}, ff)
	if err != nil {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if ff.draft || ff.readies != 0 || ff.merges != 1 {
		t.Errorf("draft = %v, readies = %d, merges = %d; want a ready PR, no Ready, one merge", ff.draft, ff.readies, ff.merges)
	}
	if note := shipNote(res, "ci"); strings.Contains(note, "ready") {
		t.Errorf("ci note = %q, want no ready clause", note)
	}
}

// TestShipDraftReadyFails (issue #765, c): a Ready error fails ci and nothing
// is merged.
func TestShipDraftReadyFails(t *testing.T) {
	t.Parallel()
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}, readyErr: errors.New("ready refused")}
	res, out, err := draftShip(t, nil, ShipOptions{}, ff)
	if err == nil || !strings.Contains(err.Error(), "ready refused") {
		t.Fatalf("Ship error = %v, want the Ready error\n%s", err, out)
	}
	if got := shipSteps(res); !strings.HasSuffix(got, "ci=fail") {
		t.Errorf("steps = %s, want ci to fail last", got)
	}
	if ff.merges != 0 {
		t.Errorf("merged %d time(s) after Ready failed", ff.merges)
	}
}

// TestShipDraftNoMerge (issue #765, d): --no-merge stops after ci with the
// PR marked ready once and no merge.
func TestShipDraftNoMerge(t *testing.T) {
	t.Parallel()
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	res, out, err := draftShip(t, nil, ShipOptions{NoMerge: true}, ff)
	if err != nil {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	if ff.readies != 1 || ff.merges != 0 {
		t.Errorf("readies = %d, merges = %d; want 1 and 0", ff.readies, ff.merges)
	}
}

// TestShipDraftConfig (issue #765, e): ShipDraft is true unless ship.draft
// is false.
func TestShipDraftConfig(t *testing.T) {
	t.Parallel()
	on, off := true, false
	for _, c := range []struct {
		ship *ShipConfig
		want bool
	}{{nil, true}, {&ShipConfig{}, true}, {&ShipConfig{Draft: &on}, true}, {&ShipConfig{Draft: &off}, false}} {
		if got := (Config{Ship: c.ship}).ShipDraft(); got != c.want {
			t.Errorf("ShipDraft(%+v) = %v, want %v", c.ship, got, c.want)
		}
	}
}
