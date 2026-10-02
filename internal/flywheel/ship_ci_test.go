package flywheel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// ciShip ships the fixture with ff and a 1ms Poll, returning the ci step.
func ciShip(t *testing.T, f shipFixture, ff *fakeForge, o ShipOptions) (ShipStep, error) {
	t.Helper()
	o.Forge, o.Poll = ff, time.Millisecond
	if o.CITimeout == 0 {
		o.CITimeout = time.Hour
	}
	res, out, err := f.ship(t, o)
	for _, s := range res.Steps {
		if s.Step == "ci" {
			return s, err
		}
	}
	t.Fatalf("no ci step: %s, %v\n%s", shipSteps(res), err, out)
	return ShipStep{}, err
}

// TestShipCIWaitsForExpected (issue #640): the first poll has only an
// external status that passed at once; the last merged PR's head reported
// test-a and test-b, so ci polls on until both are on the PR and passed. The
// unfixed code returned ok at poll 1.
func TestShipCIWaitsForExpected(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{mergedHeads: []string{"m1"}, commitChecks: map[string][]string{"m1": {"test-b", "test-a"}}, checks: []ChecksState{
		{Passed: []string{"ext"}},
		{Passed: []string{"ext"}},
		{Passed: []string{"ext"}, Pending: []string{"test-a", "test-b"}},
		{Passed: []string{"ext", "test-a", "test-b"}},
	}}
	ci, err := ciShip(t, f, ff, ShipOptions{})
	if err != nil || ci.Result != "ok" || ci.Note != "3 check(s) passed on #7 (expected: test-a, test-b) (marked ready for review)" || ff.checkCalls != 4 || ff.merges != 1 {
		t.Fatalf("ci = %+v, %v, checks called %d, merges %d", ci, err, ff.checkCalls, ff.merges)
	}
	if !slices.Equal(ff.mergedAsks, []string{"main/3"}) || !slices.Equal(ff.commits, []string{"m1"}) {
		t.Errorf("MergedPRHeads asked %q, CommitCheckRuns asked %q; want main/3 and m1", ff.mergedAsks, ff.commits)
	}
}

// TestShipCIIgnoresPushOnly (issue #640 c1): the integration head carries
// checks of push-only workflows (deploy, release-please) that never run on a
// PR; the merged PR heads carry only the PR checks, so ci passes once those
// pass instead of timing out on "missing: deploy, ...".
func TestShipCIIgnoresPushOnly(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	main := shipGit(t, f.dir, "rev-parse", "origin/main")
	pr := []string{"checks", "test (ubuntu-latest)"}
	ff := &fakeForge{mergedHeads: []string{"m1", "m2", "m3"}, commitChecks: map[string][]string{
		main: append([]string{"deploy", "release-please"}, pr...), "m1": pr, "m2": pr, "m3": pr,
	}, checks: []ChecksState{{Passed: pr}}}
	ci, err := ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond})
	if err != nil || ci.Note != "2 check(s) passed on #7 (expected: checks, test (ubuntu-latest)) (marked ready for review)" || ff.merges != 1 {
		t.Fatalf("ci = %+v, %v, merges %d", ci, err, ff.merges)
	}
}

// TestShipCIIntersectsMergedHeads (issue #640 c1): a name reported on only
// one of the last three merged PR heads is not expected.
func TestShipCIIntersectsMergedHeads(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{mergedHeads: []string{"m1", "m2", "m3", "m4"}, commitChecks: map[string][]string{
		"m1": {"test", "flaky-once"}, "m2": {"test"}, "m3": {"test", "old"}, "m4": {"test", "older"},
	}, checks: []ChecksState{{Passed: []string{"test"}}}}
	ci, err := ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond})
	if err != nil || ci.Note != "1 check(s) passed on #7 (expected: test) (marked ready for review)" || len(ff.commits) != 3 {
		t.Fatalf("ci = %+v, %v, CommitCheckRuns asked %q", ci, err, ff.commits)
	}
}

// TestShipCISkippedExpected (issue #640 c1): an expected check that concluded
// SKIPPED on the PR head is present, not missing, so it does not block.
func TestShipCISkippedExpected(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{mergedHeads: []string{"m1"}, commitChecks: map[string][]string{"m1": {"test", "docs"}},
		checks: []ChecksState{{Passed: []string{"test"}, Skipped: []string{"docs"}}}}
	ci, err := ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond})
	if err != nil || ci.Note != "1 check(s) passed on #7 (expected: docs, test) (marked ready for review)" || ff.merges != 1 {
		t.Fatalf("ci = %+v, %v, merges %d", ci, err, ff.merges)
	}
}

// TestShipCISettles (issue #640): with no expected checks the kept set grows
// over three polls; ci passes only once two consecutive polls agree.
func TestShipCISettles(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	ff := &fakeForge{checks: []ChecksState{
		{Passed: []string{"ext"}},
		{Passed: []string{"ext", "build"}},
		{Passed: []string{"ext", "build", "lint"}},
		{Passed: []string{"ext", "build", "lint"}},
	}}
	ci, err := ciShip(t, f, ff, ShipOptions{})
	if err != nil || ci.Result != "ok" || ci.Note != "3 check(s) passed on #7 (marked ready for review)" || ff.checkCalls != 4 {
		t.Fatalf("ci = %+v, %v, checks called %d; want ok at poll 4", ci, err, ff.checkCalls)
	}
}

// TestShipCIHeadOnly (issue #640): checks reported on a previous head commit
// are not counted, so they neither pass ci nor count as reported.
func TestShipCIHeadOnly(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	head := shipGit(t, f.dir, "rev-parse", "fw/T")
	old := strings.Repeat("0", 40)
	ff := &fakeForge{checks: []ChecksState{
		{Head: old, Passed: []string{"build"}},
		{Head: old, Passed: []string{"build"}},
		{Head: head, Pending: []string{"build"}},
		{Head: head, Passed: []string{"build"}},
	}}
	ci, err := ciShip(t, f, ff, ShipOptions{})
	if err != nil || ci.Result != "ok" || ff.checkCalls != 4 {
		t.Fatalf("ci = %+v, %v, checks called %d; want ok at poll 4", ci, err, ff.checkCalls)
	}
	f = newShipFixture(t, "exit 0", true)
	ff = &fakeForge{checks: []ChecksState{{Head: old, Passed: []string{"build"}}}}
	ci, err = ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond})
	if !errors.Is(err, ErrShipCI) || ci.Note != "timed out, no checks reported" || ff.merges != 0 {
		t.Fatalf("old-head checks only: ci = %+v, %v, merges %d", ci, err, ff.merges)
	}
}

// setRequiredChecks writes a config with ship.required_checks names.
func setRequiredChecks(t *testing.T, dir string, names ...string) {
	t.Helper()
	c := DefaultConfig()
	c.Ship = &ShipConfig{RequiredChecks: names}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", configFileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestShipCIRequiredConfig (issue #640): ship.required_checks replaces the
// integration head's checks and --ignore-check removes a name from it; a
// required check missing at the timeout fails ci naming it.
func TestShipCIRequiredConfig(t *testing.T) {
	t.Parallel()
	f := newShipFixture(t, "exit 0", true)
	setRequiredChecks(t, f.dir, "test-a", "lint")
	ff := &fakeForge{mergedHeads: []string{"m1"}, commitChecks: map[string][]string{"m1": {"never"}}, checks: []ChecksState{{Passed: []string{"test-a"}}}}
	ci, err := ciShip(t, f, ff, ShipOptions{IgnoreChecks: []string{"lint"}})
	if err != nil || ci.Note != "1 check(s) passed on #7 (expected: test-a) (marked ready for review)" || len(ff.mergedAsks)+len(ff.commits) != 0 {
		t.Fatalf("ci = %+v, %v, MergedPRHeads asked %q, CommitCheckRuns asked %q", ci, err, ff.mergedAsks, ff.commits)
	}
	f = newShipFixture(t, "exit 0", true)
	setRequiredChecks(t, f.dir, "test-a", "deploy")
	ff = &fakeForge{checks: []ChecksState{{Passed: []string{"test-a"}}}}
	ci, err = ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond})
	if !errors.Is(err, ErrShipCI) || ci.Note != "timed out, missing: deploy" || ff.merges != 0 {
		t.Fatalf("missing deploy: ci = %+v, %v, merges %d", ci, err, ff.merges)
	}
}

// requiredSkipShip ships a fixture whose ship.required_checks is required
// against the PR head states in checks, returning the ci step, forge and error.
func requiredSkipShip(t *testing.T, required []string, checks ...ChecksState) (ShipStep, *fakeForge, error) {
	t.Helper()
	f := newShipFixture(t, "exit 0", true)
	setRequiredChecks(t, f.dir, required...)
	ff := &fakeForge{checks: checks}
	ci, err := ciShip(t, f, ff, ShipOptions{})
	return ci, ff, err
}

// TestShipCIRequiredSkipped (issue #653): a required check that concluded
// SKIPPED (a path filter left it out) fails ci at once and nothing merges.
func TestShipCIRequiredSkipped(t *testing.T) {
	t.Parallel()
	ci, ff, err := requiredSkipShip(t, []string{"test", "rules"}, ChecksState{Passed: []string{"test"}, Skipped: []string{"rules"}})
	if !errors.Is(err, ErrShipCI) || ci.Note != "required-check-skipped: rules (SKIPPED)" || ff.merges != 0 || ff.checkCalls != 1 {
		t.Fatalf("ci = %+v, %v, merges %d, polls %d", ci, err, ff.merges, ff.checkCalls)
	}
	if !strings.Contains(err.Error(), "required check(s) did not run: rules (SKIPPED); a path filter or job condition skipped them") {
		t.Errorf("err = %v", err)
	}
}

// TestShipCIRequiredNeutral (issue #653): NEUTRAL on a required check fails
// ci like SKIPPED.
func TestShipCIRequiredNeutral(t *testing.T) {
	t.Parallel()
	ci, ff, err := requiredSkipShip(t, []string{"test", "rules"}, ChecksState{Passed: []string{"test"}, Neutral: []string{"rules"}})
	if !errors.Is(err, ErrShipCI) || ci.Note != "required-check-skipped: rules (NEUTRAL)" || ff.merges != 0 {
		t.Fatalf("ci = %+v, %v, merges %d", ci, err, ff.merges)
	}
}

// TestShipCIRequiredSkippedWhilePending (issue #653): a skip is final, so ci
// fails on the first poll without waiting for the pending check.
func TestShipCIRequiredSkippedWhilePending(t *testing.T) {
	t.Parallel()
	ci, ff, err := requiredSkipShip(t, []string{"test", "rules"},
		ChecksState{Pending: []string{"test"}, Skipped: []string{"rules"}}, ChecksState{Passed: []string{"test"}, Skipped: []string{"rules"}})
	if !errors.Is(err, ErrShipCI) || ci.Note != "required-check-skipped: rules (SKIPPED)" || ff.checkCalls != 1 || ff.merges != 0 {
		t.Fatalf("ci = %+v, %v, polls %d, merges %d", ci, err, ff.checkCalls, ff.merges)
	}
}

// TestShipCINotRequiredSkippedIgnored (issue #653): a skipped check not named
// in ship.required_checks does not block.
func TestShipCINotRequiredSkippedIgnored(t *testing.T) {
	t.Parallel()
	ci, ff, err := requiredSkipShip(t, []string{"test"}, ChecksState{Passed: []string{"test"}, Skipped: []string{"docs"}})
	if err != nil || ci.Result != "ok" || ff.merges != 1 {
		t.Fatalf("ci = %+v, %v, merges %d", ci, err, ff.merges)
	}
}

// TestShipCIIgnoresVanishedStatus (issue #640 c3): the merged PR heads carry
// check runs a and b and a commit status "bot" from an external bot that has
// since stopped posting; the PR head has a and b passed and no "bot". By
// default only check runs are expected, so ci passes; with required_checks
// ["bot"] ci waits for it and fails at the timeout naming it.
func TestShipCIIgnoresVanishedStatus(t *testing.T) {
	t.Parallel()
	merged := func() *fakeForge {
		heads := []string{"m1", "m2", "m3"}
		ff := &fakeForge{mergedHeads: heads, commitChecks: map[string][]string{}, commitStatuses: map[string][]string{},
			checks: []ChecksState{{Passed: []string{"a", "b"}}}}
		for _, h := range heads {
			ff.commitChecks[h], ff.commitStatuses[h] = []string{"a", "b"}, []string{"bot"}
		}
		return ff
	}
	f := newShipFixture(t, "exit 0", true)
	ff := merged()
	ci, err := ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond})
	if err != nil || ci.Note != "2 check(s) passed on #7 (expected: a, b) (marked ready for review)" || ff.merges != 1 {
		t.Fatalf("default: ci = %+v, %v, merges %d", ci, err, ff.merges)
	}
	f = newShipFixture(t, "exit 0", true)
	setRequiredChecks(t, f.dir, "bot")
	ff = merged()
	ci, err = ciShip(t, f, ff, ShipOptions{CITimeout: 5 * time.Millisecond})
	if !errors.Is(err, ErrShipCI) || ci.Note != "timed out, missing: bot" || ff.merges != 0 || ff.checkCalls < 2 {
		t.Fatalf("required bot: ci = %+v, %v, merges %d, polls %d", ci, err, ff.merges, ff.checkCalls)
	}
}
