package flywheel

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestResourcesParse checks resources: lines accumulate, each name once, in
// order, and that a comma list of gate markers records both indices while a
// lone [quiet] marker keeps its meaning (issue #697).
func TestResourcesParse(t *testing.T) {
	t.Parallel()
	h, err := ParseBriefHeaderBytes([]byte("resources: e2e, dev-db\nresources: e2e\ngate: true\ngate[quiet,resources]: e2e\nlive-gate[resources]: probe\ngate[quiet]: q\n\n# TASK x\n"))
	if err != nil {
		t.Fatalf("ParseBriefHeaderBytes() error = %v", err)
	}
	if want := []string{"e2e", "dev-db"}; !slices.Equal(h.Resources, want) {
		t.Errorf("Resources = %q, want %q", h.Resources, want)
	}
	if want := []string{"true", "e2e", "q"}; !slices.Equal(h.Gates, want) {
		t.Errorf("Gates = %q, want %q", h.Gates, want)
	}
	if want := []int{2, 3}; !slices.Equal(h.QuietGates, want) {
		t.Errorf("QuietGates = %v, want %v", h.QuietGates, want)
	}
	if want := []int{2}; !slices.Equal(h.ResourceGates, want) {
		t.Errorf("ResourceGates = %v, want %v", h.ResourceGates, want)
	}
	if want := []int{1}; !slices.Equal(h.ResourceLiveGates, want) {
		t.Errorf("ResourceLiveGates = %v, want %v", h.ResourceLiveGates, want)
	}
	if h.resourcesEmpty || len(h.resourcesInvalid) != 0 {
		t.Errorf("empty=%v invalid=%q, want neither", h.resourcesEmpty, h.resourcesInvalid)
	}
}

// TestResourcesLint checks an invalid name, an empty resources: line and a
// [resources] marker without a resources: line are lint problems, and that
// a clean resources brief has none (issue #697).
func TestResourcesLint(t *testing.T) {
	t.Parallel()
	const tail = "\n# TASK: x\n## Checks\nAt most one write per response\nreport\n"
	cases := []struct {
		name, header string
		want         []string
	}{
		{"clean", "resources: e2e, dev-db\ngate[quiet,resources]: true\n", nil},
		{"invalid", "resources: E2E, ok\ngate: true\n", []string{`resources name "E2E" is not a valid resource name (lower-case letters, digits, '.', '_' and '-', starting with a letter or digit)`}},
		{"empty", "resources:\ngate: true\n", []string{"resources: line is empty; name the shared resources the gates use or remove the line"}},
		{"marker alone", "gate[resources]: true\n", []string{"a gate is marked [resources] but the brief has no resources: line; name the shared resources or drop the marker"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := lintCheck(t, t.TempDir(), []string{"a.go"}, "owns: a.go\nneeds: none\n"+tc.header+tail)
			if !slices.Equal(res.Problems, tc.want) {
				t.Errorf("Problems = %q, want %q", res.Problems, tc.want)
			}
			if len(res.Warnings) != 0 {
				t.Errorf("Warnings = %q, want none", res.Warnings)
			}
		})
	}
}

// TestResourcesSharedPath checks two worktrees of one repository resolve the
// same lock file for one resource, under the git common dir (issue #697).
func TestResourcesSharedPath(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "repo")
	initGitRepoAt(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-q", "-m", "init"})
	wt := filepath.Join(filepath.Dir(dir), "wt")
	git(t, dir, []string{"worktree", "add", "-q", wt})
	a := resourceLockPath(resourceLockDir(dir), "e2e")
	b := resourceLockPath(resourceLockDir(wt), "e2e")
	if !sameTree(a, b) {
		t.Errorf("lock paths differ: %s (repo) vs %s (worktree)", a, b)
	}
	if want := filepath.Join("flywheel-locks", "resource-e2e.lock"); !strings.HasSuffix(a, want) || !strings.Contains(a, ".git") {
		t.Errorf("lock path = %s, want <git common dir>/%s", a, want)
	}
	// outside a repository the lock lives under the tree's .flywheel/locks.
	out := t.TempDir()
	if got, want := resourceLockPath(resourceLockDir(out), "e2e"), filepath.Join(out, ".flywheel", "locks", "resource-e2e.lock"); got != want {
		t.Errorf("outside a repository: %s, want %s", got, want)
	}
}

// resourcesTask is initTask with header as the brief's gate and resources
// lines; it returns the root (the validated tree) and its lock directory.
func resourcesTask(t *testing.T, header string) (dir, lockDir string) {
	t.Helper()
	dir, err := initTask(t, nil)
	if err != nil {
		t.Fatalf("initTask() error = %v", err)
	}
	brief := "owns: a.go\nneeds: none\n" + header + "\n# TASK: resources\n"
	if err := os.WriteFile(filepath.Join(dir, "brief.txt"), []byte(brief), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}
	git(t, dir, []string{"add", "-A"})
	git(t, dir, []string{"commit", "-m", "resources brief"})
	return dir, resourceLockDir(dir)
}

// fakeLockClock returns a resource-timings hook on a fake clock: every retry
// sleep advances it, wait becomes the budget, and onPoll (may be nil) runs
// at every poll with the clock's time. No test sleeps for real.
func fakeLockClock(wait time.Duration, onPoll func(time.Time)) func(*repoLockTimings) {
	clock := time.Now()
	return func(tm *repoLockTimings) {
		tm.wait = wait
		tm.now = func() time.Time { return clock }
		tm.sleep = func(d time.Duration) { clock = clock.Add(d) }
		tm.poll = func() {
			if onPoll != nil {
				onPoll(clock)
			}
		}
	}
}

// holdResource writes another holder's lock file at p, as validate o9 gate 1.
func holdResource(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("othertoken\npid 4242 host h locked 2026-09-30T00:00:00Z cmd validate o9 gate 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// resourceEvents returns the validated events of the ledger at dir.
func resourceEvents(t *testing.T, dir string) []Event {
	t.Helper()
	evs, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == "validated" {
			out = append(out, e)
		}
	}
	return out
}

// TestResourcesWaitsForHolder checks a gate waits while another holder keeps
// its resource lock fresh, runs once the holder releases, and records how
// long it waited for which resource and holder (issue #697).
func TestResourcesWaitsForHolder(t *testing.T) {
	t.Parallel()
	ran := filepath.Join(t.TempDir(), "ran")
	dir, lockDir := resourcesTask(t, "resources: e2e\ngate: touch '"+filepath.ToSlash(ran)+"'\n")
	p := resourceLockPath(lockDir, "e2e")
	holdResource(t, p)
	polls := 0
	tune := fakeLockClock(30*time.Second, func(c time.Time) {
		polls++
		if polls == 5 {
			_ = os.Remove(p)
		} else {
			_ = os.Chtimes(p, c, c)
		}
	})
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, resourceTimings: tune})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	want := "waited 1s for resource e2e (validate o9 gate 1 (pid 4242))"
	if len(res.Gates) != 1 || res.Gates[0].RC != 0 || res.Gates[0].Inconclusive || res.Gates[0].Note != want {
		t.Fatalf("gates = %+v, want one passing gate noting %q", res.Gates, want)
	}
	if evs := resourceEvents(t, dir); len(evs) != 1 || evs[0].Note != want || evs[0].RC == nil || *evs[0].RC != 0 {
		t.Errorf("validated events = %+v, want one rc 0 noting %q", evs, want)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Errorf("the gate never ran: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("resource lock still present after the gate (stat err %v)", err)
	}
}

// TestResourcesBusyInconclusive checks a lock held past the wait budget
// leaves the gate unrun and its reading inconclusive, naming the resource
// and its holder, while the holder's lock is left alone (issue #697).
func TestResourcesBusyInconclusive(t *testing.T) {
	t.Parallel()
	ran := filepath.Join(t.TempDir(), "ran")
	dir, lockDir := resourcesTask(t, "resources: e2e\ngate: touch '"+filepath.ToSlash(ran)+"'\n")
	p := resourceLockPath(lockDir, "e2e")
	holdResource(t, p)
	tune := fakeLockClock(2*time.Second, func(c time.Time) { _ = os.Chtimes(p, c, c) })
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, resourceTimings: tune})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	want := "resource busy: e2e held by validate o9 gate 1 (pid 4242)"
	if len(res.Gates) != 1 || !res.Gates[0].Inconclusive || res.Gates[0].RC != -1 || res.Gates[0].Note != want {
		t.Fatalf("gates = %+v, want one inconclusive gate noting %q", res.Gates, want)
	}
	if res.GatesOK {
		t.Error("GatesOK = true, want false: an unmeasured reading is not a pass")
	}
	if evs := resourceEvents(t, dir); len(evs) != 1 || evs[0].Reason != "inconclusive" || evs[0].Note != want || evs[0].RC != nil {
		t.Errorf("validated events = %+v, want one inconclusive, no rc, noting %q", evs, want)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Errorf("the gate ran while the resource was busy (stat err %v)", err)
	}
	if holder := repoLockHolder(p); holder != "othertoken" {
		t.Errorf("holder's lock token = %q, want othertoken (left alone)", holder)
	}
}

// TestResourcesMarkedGates checks only a [resources]-marked gate takes the
// locks when one is marked, every gate when none is, and that each lock is
// released after its gate (issue #697).
func TestResourcesMarkedGates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, header string
		want         []string
	}{
		{"one marked", "resources: e2e\ngate: true\ngate[resources]: true\n", []string{"validate T1 gate 2"}},
		{"none marked", "resources: e2e\ngate: true\ngate: true\n", []string{"validate T1 gate 1", "validate T1 gate 2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, lockDir := resourcesTask(t, tc.header)
			var holders []string
			clock := fakeLockClock(time.Minute, nil)
			tune := func(tm *repoLockTimings) {
				holders = append(holders, tm.holder)
				clock(tm)
			}
			res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, resourceTimings: tune})
			if err != nil {
				t.Fatalf("ValidateTask() error = %v", err)
			}
			if !res.GatesOK || len(res.Gates) != 2 {
				t.Errorf("gates = %+v, GatesOK = %v; want two passing gates", res.Gates, res.GatesOK)
			}
			if !slices.Equal(holders, tc.want) {
				t.Errorf("lock holders = %q, want %q", holders, tc.want)
			}
			if _, err := os.Stat(resourceLockPath(lockDir, "e2e")); !os.IsNotExist(err) {
				t.Errorf("resource lock still present after validate (stat err %v)", err)
			}
		})
	}
}

// TestResourcesSortedOrder checks two resources are taken in sorted name
// order, whatever order the brief names them in, so two units naming one
// pair cannot deadlock (issue #697).
func TestResourcesSortedOrder(t *testing.T) {
	t.Parallel()
	dir, lockDir := resourcesTask(t, "resources: zeta, alpha\ngate: true\n")
	var seen []string
	tune := fakeLockClock(time.Minute, func(time.Time) {
		var names []string
		entries, _ := os.ReadDir(lockDir)
		for _, e := range entries {
			names = append(names, e.Name())
		}
		seen = append(seen, strings.Join(names, ","))
	})
	if _, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, resourceTimings: tune}); err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	// one poll per lock: alpha is taken first, zeta while alpha is held.
	if want := []string{"", "resource-alpha.lock"}; !slices.Equal(seen, want) {
		t.Errorf("lock files at each poll = %q, want %q", seen, want)
	}
	if entries, _ := os.ReadDir(lockDir); len(entries) != 0 {
		t.Errorf("%d lock files left after validate, want 0", len(entries))
	}
}

// TestResourcesNoLine checks a brief with no resources: line takes no lock
// and creates no lock directory (issue #697).
func TestResourcesNoLine(t *testing.T) {
	t.Parallel()
	dir, lockDir := resourcesTask(t, "gate: true\ngate: true\n")
	calls := 0
	tune := func(*repoLockTimings) { calls++ }
	res, err := ValidateTask(dir, "T1", ValidateOptions{Dir: dir, resourceTimings: tune})
	if err != nil {
		t.Fatalf("ValidateTask() error = %v", err)
	}
	if !res.GatesOK || calls != 0 {
		t.Errorf("GatesOK = %v, lock acquisitions = %d; want true, 0", res.GatesOK, calls)
	}
	if _, err := os.Stat(lockDir); !os.IsNotExist(err) {
		t.Errorf("%s exists (stat err %v), want no lock directory", lockDir, err)
	}
	// .flywheel/locks holds the host gate lock (issue #411); no resource lock
	// may appear there either.
	if m, _ := filepath.Glob(filepath.Join(dir, ".flywheel", "locks", "resource-*")); len(m) != 0 {
		t.Errorf("resource locks %q created, want none", m)
	}
}
