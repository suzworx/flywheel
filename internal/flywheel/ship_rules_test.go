package flywheel

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// hostRulesShip ships the fixture's passed unit T with a fakeForge answering
// rules and rulesErr from HostRules, returning the result, progress and forge.
func hostRulesShip(t *testing.T, rules HostRules, rulesErr error, o ShipOptions) (ShipResult, string, *fakeForge) {
	t.Helper()
	f := newShipFixture(t, "exit 0", true)
	shipWrite(t, f.wt, "src/a.go", "package src // task\n")
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}, rules: rules, rulesErr: rulesErr}
	o.Forge = ff
	res, out, err := f.ship(t, o)
	if err != nil {
		t.Fatalf("Ship: %v\n%s", err, out)
	}
	if len(res.Steps) == 0 || res.Steps[0].Step != "preflight" || res.Steps[0].Result != "ok" {
		t.Fatalf("steps = %+v, want preflight ok first\n%s", res.Steps, out)
	}
	return res, out, ff
}

// TestShipHostRulesAllOn: every rule on is a host-rules line ending
// "up to date on" on the preflight note and no warning line.
func TestShipHostRulesAllOn(t *testing.T) {
	t.Parallel()
	res, out, ff := hostRulesShip(t, HostRules{PullRequest: true, RequiredChecks: true, Strict: true, Checks: []string{"build"}}, nil, ShipOptions{})
	if want := "status passed; host rules: main: pull request on, required checks on, up to date on"; res.Steps[0].Note != want {
		t.Errorf("preflight note = %q, want %q", res.Steps[0].Note, want)
	}
	if strings.Contains(out, "warning:") {
		t.Errorf("progress has a warning with every rule on:\n%s", out)
	}
	if !slices.Equal(ff.rulesAsks, []string{"main"}) {
		t.Errorf("HostRules asks = %v, want [main]", ff.rulesAsks)
	}
}

// TestShipHostRulesStrictOff: strict off is "up to date off" on the note and
// one warning line naming the setting to enable.
func TestShipHostRulesStrictOff(t *testing.T) {
	t.Parallel()
	res, out, _ := hostRulesShip(t, HostRules{PullRequest: true, RequiredChecks: true}, nil, ShipOptions{})
	if want := "status passed; host rules: main: pull request on, required checks on, up to date off"; res.Steps[0].Note != want {
		t.Errorf("preflight note = %q, want %q", res.Steps[0].Note, want)
	}
	if !strings.Contains(out, "ship T: warning: main does not require branches to be up to date before merging") ||
		!strings.Contains(out, `enable "Require branches to be up to date before merging" (strict)`) {
		t.Errorf("progress lacks the strict warning:\n%s", out)
	}
	if n := strings.Count(out, "warning:"); n != 1 {
		t.Errorf("%d warning lines, want 1:\n%s", n, out)
	}
}

// TestShipHostRulesNone: no rules warns for the pull-request rule and the
// required status checks.
func TestShipHostRulesNone(t *testing.T) {
	t.Parallel()
	res, out, _ := hostRulesShip(t, HostRules{}, nil, ShipOptions{})
	if want := "status passed; host rules: main: pull request off, required checks off, up to date off"; res.Steps[0].Note != want {
		t.Errorf("preflight note = %q, want %q", res.Steps[0].Note, want)
	}
	for _, w := range []string{"ship T: warning: main has no pull-request rule", "ship T: warning: main has no required status checks"} {
		if !strings.Contains(out, w) {
			t.Errorf("progress lacks %q:\n%s", w, out)
		}
	}
}

// TestShipHostRulesForgeError: an unreadable host is the inconclusive line,
// a warning naming the error, and preflight still ok.
func TestShipHostRulesForgeError(t *testing.T) {
	t.Parallel()
	res, out, _ := hostRulesShip(t, HostRules{}, errors.New("gh api: HTTP 403 boom"), ShipOptions{})
	if want := "status passed; host rules: main: inconclusive"; res.Steps[0].Note != want {
		t.Errorf("preflight note = %q, want %q", res.Steps[0].Note, want)
	}
	if !strings.Contains(out, "ship T: warning: host rules for main are inconclusive (gh api: HTTP 403 boom)") {
		t.Errorf("progress lacks the inconclusive warning:\n%s", out)
	}
}

// TestShipHostRulesNoMerge: nothing merges with NoMerge, so the host is not
// read and the note stays "status passed".
func TestShipHostRulesNoMerge(t *testing.T) {
	t.Parallel()
	res, out, ff := hostRulesShip(t, HostRules{}, nil, ShipOptions{NoMerge: true})
	if res.Steps[0].Note != "status passed" || len(ff.rulesAsks) != 0 || strings.Contains(out, "warning:") {
		t.Errorf("NoMerge: note %q, HostRules asks %v\n%s", res.Steps[0].Note, ff.rulesAsks, out)
	}
}

// TestShipHostRulesRefusal: a T5 refusal returns before any host call.
func TestShipHostRulesRefusal(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", false)
	ff := &fakeForge{checks: []ChecksState{{Passed: []string{"build"}}}}
	_, out, err := f.ship(t, ShipOptions{Forge: ff})
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "T5" {
		t.Fatalf("Ship error = %v, want a T5 refusal\n%s", err, out)
	}
	if len(ff.rulesAsks) != 0 {
		t.Errorf("HostRules asked %v before a T5 refusal", ff.rulesAsks)
	}
}

// TestShipHostRulesGhRepo: GhTracker with Repo o/r puts it in the api paths.
func TestShipHostRulesGhRepo(t *testing.T) {
	t.Parallel()
	var calls []string
	g := GhTracker{Repo: "o/r", Run: func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if strings.HasSuffix(args[len(args)-1], "/protection") {
			return nil, errors.New("HTTP 404")
		}
		return []byte(`[{"type":"pull_request"}]`), nil
	}}
	r, err := g.HostRules("main")
	if err != nil || !r.PullRequest || r.RequiredChecks {
		t.Fatalf("HostRules = %+v, %v", r, err)
	}
	if want := []string{"api repos/o/r/rules/branches/main", "api repos/o/r/branches/main/protection"}; !slices.Equal(calls, want) {
		t.Errorf("gh calls = %q, want %q", calls, want)
	}
}
