package flywheel

import (
	"encoding/json"
	"strings"
	"testing"
)

// sigForge is a fakeForge that records the body a PR is opened with.
type sigForge struct {
	*fakeForge
	body string
}

func (f *sigForge) CreatePR(base, head, title, body string) (PullRequest, error) {
	f.body = body
	return f.fakeForge.CreatePR(base, head, title, body)
}

const (
	sigFooter  = "Shipped by [flywheel](https://github.com/suzworx/flywheel) 1.2.3 · unit `T` · 2/2 gates · 1 correction(s)"
	sigTrailer = "Shipped-by: flywheel 1.2.3 (unit T, attempt c1, 2/2 gates)"
)

// sigShip ships a fixture whose unit passed on its one correction c1, with
// both gates green on c1's latest tree after a red reading on an older one,
// and an uncommitted owned change for commit; it returns the forge.
func sigShip(t *testing.T, o ShipOptions) (shipFixture, *sigForge) {
	t.Helper()
	f := newShipFixture(t, "exit 0", true)
	zero, one := 0, 1
	if err := AppendEvents(f.dir, []Event{
		{TS: "2026-01-01T00:05:00Z", Task: "T", Kind: "dispatched", Attempt: "c1"},
		{TS: "2026-01-01T00:06:00Z", Task: "T", Kind: "finished", Attempt: "c1", Reason: "stop"},
		{TS: "2026-01-01T00:07:00Z", Task: "T", Kind: "validated", Attempt: "c1", Gate: "1", Tree: "t1", RC: &one},
		{TS: "2026-01-01T00:08:00Z", Task: "T", Kind: "validated", Attempt: "c1", Gate: "1", Tree: "t2", RC: &zero},
		{TS: "2026-01-01T00:08:01Z", Task: "T", Kind: "validated", Attempt: "c1", Gate: "2", Tree: "t2", RC: &zero},
		{TS: "2026-01-01T00:09:00Z", Task: "T", Kind: "inspected", Attempt: "c1", Verdict: "pass", Session: "lead"},
	}); err != nil {
		t.Fatal(err)
	}
	shipWrite(t, f.wt, "src/b.go", "package src // b\n")
	ff := &sigForge{fakeForge: &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}}
	o.Forge = ff
	if res, out, err := f.ship(t, o); err != nil || !strings.HasSuffix(shipSteps(res), "merge=ok landed=ok closed=skip") {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	return f, ff
}

// TestShipSignature: a generated and a given body each get the footer, the
// merge message ends in the trailer, and the ship commit carries it beside
// Flywheel-Task:, with version, unit, attempt c1 and 2/2 gates.
func TestShipSignature(t *testing.T) {
	t.Parallel()
	f, ff := sigShip(t, ShipOptions{Version: "1.2.3"})
	if !strings.HasPrefix(ff.body, "Task T: T\n") || !strings.HasSuffix(ff.body, ", landed, closed.\n\n"+sigFooter+"\n") {
		t.Errorf("generated body = %q", ff.body)
	}
	if want := ff.body + "\n" + sigTrailer + "\n"; ff.mergeMsg != want {
		t.Errorf("merge message = %q, want %q", ff.mergeMsg, want)
	}
	if msg := shipGit(t, f.dir, "log", "-1", "--format=%B", "fw/T"); !strings.HasSuffix(msg, "\n\nFlywheel-Task: T\n"+sigTrailer) {
		t.Errorf("ship commit message = %q", msg)
	}
	// A given body is signed in TestShipSignatureTrailer; a dev build's
	// version reads dev. The ledger is cut where ship started, before its
	// gates step added a reading of the merged tree.
	events, err := ReadEvents(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range events {
		if e.Kind == "shipped" {
			events = events[:i]
			break
		}
	}
	if tr, ft := shipSignature(events, "T", "c1", ""); tr != strings.Replace(sigTrailer, "1.2.3", "dev", 1) || ft != strings.Replace(sigFooter, "1.2.3", "dev", 1) {
		t.Errorf("dev signature = %q, %q", tr, ft)
	}
}

// TestShipSignatureTrailer: a given body ending in a Co-Authored-By: trailer
// gets the footer above that paragraph and Shipped-by: appended inside it.
func TestShipSignatureTrailer(t *testing.T) {
	t.Parallel()
	_, ff := sigShip(t, ShipOptions{Version: "1.2.3", Body: "Summary.\n\nCo-Authored-By: X <x@y>\n"})
	if want := "Summary.\n\n" + sigFooter + "\n\nCo-Authored-By: X <x@y>\n"; ff.body != want {
		t.Errorf("body = %q, want %q", ff.body, want)
	}
	if want := "Summary.\n\n" + sigFooter + "\n\nCo-Authored-By: X <x@y>\n" + sigTrailer + "\n"; ff.mergeMsg != want {
		t.Errorf("merge message = %q, want %q", ff.mergeMsg, want)
	}
}

// TestShipSignatureOnce: a body already signed (a re-run of ship, a reused
// PR) gets no second footer and no second trailer.
func TestShipSignatureOnce(t *testing.T) {
	t.Parallel()
	signed := "S.\n\n" + sigFooter + "\n\n" + sigTrailer + "\n"
	if got := withTrailer(withFooter(signed, sigFooter), sigTrailer); got != signed {
		t.Errorf("signed body signed again = %q, want %q", got, signed)
	}
	once := withTrailer(withFooter("S.\n", sigFooter), sigTrailer)
	if twice := withTrailer(withFooter(once, sigFooter), sigTrailer); twice != once {
		t.Errorf("signed again = %q, want %q", twice, once)
	}
	// A resumed ship: the first run stops after ci, the second reuses its PR.
	f := newShipFixture(t, "exit 0", true)
	shipWrite(t, f.wt, "src/b.go", "package src // b\n")
	sf := &sigForge{fakeForge: &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}}
	for _, noMerge := range []bool{true, false} {
		if res, out, err := f.ship(t, ShipOptions{Forge: sf, Version: "1.2.3", NoMerge: noMerge}); err != nil {
			t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
		}
	}
	log := shipGit(t, f.dir, "log", "--format=%B", "fw/T")
	if sf.created != 1 || sf.merges != 1 || strings.Count(sf.mergeMsg, "Shipped by [flywheel]") != 1 ||
		strings.Count(sf.mergeMsg, "Shipped-by: flywheel") != 1 || strings.Count(log, "Shipped-by: flywheel") != 1 {
		t.Errorf("resumed ship: forge %+v, merge message %q, fw/T log %q", sf.fakeForge, sf.mergeMsg, log)
	}
}

// TestShipSignatureOff: config ship.signature false and NoSignature (the
// --no-signature flag) each leave the body, the merge message and the ship
// commit unsigned.
func TestShipSignatureOff(t *testing.T) {
	t.Parallel()
	_, ff := sigShip(t, ShipOptions{Version: "1.2.3", Body: "Plain.\n", NoSignature: true})
	if ff.body != "Plain.\n" || ff.mergeMsg != "Plain.\n" {
		t.Errorf("--no-signature: body = %q, merge message = %q", ff.body, ff.mergeMsg)
	}
	cfg, off := DefaultConfig(), false
	cfg.Ship = &ShipConfig{Signature: &off}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f := newShipFixture(t, "exit 0", true)
	shipWrite(t, f.dir, ".flywheel/config.json", string(b))
	shipWrite(t, f.wt, "src/b.go", "package src // b\n")
	sf := &sigForge{fakeForge: &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}}
	if res, out, err := f.ship(t, ShipOptions{Forge: sf, Version: "1.2.3"}); err != nil {
		t.Fatalf("Ship = %s, %v\n%s", shipSteps(res), err, out)
	}
	msg := shipGit(t, f.dir, "log", "-1", "--format=%B", "fw/T")
	if strings.Contains(sf.body+sf.mergeMsg+msg, "hipped-by") || strings.Contains(sf.body, "Shipped by [flywheel]") || sf.mergeMsg != sf.body {
		t.Errorf("ship.signature false: body %q, merge message %q, commit %q", sf.body, sf.mergeMsg, msg)
	}
}
