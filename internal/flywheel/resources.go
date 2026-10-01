package flywheel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

// Exclusive resources (issue #697): a brief's `resources: e2e, dev-db` line
// names the shared host resources its heavy gates use (ports, one local
// database). While such a gate runs, validate holds an exclusive lock per
// resource at <git common dir>/flywheel-locks/resource-<name>.lock, which
// every worktree of the repository shares, so two units never run those
// gates at once. The locks serialise validate's gates only, never a worker's
// own runs.

// resourceNameRE is the shape of a resource name: lower case, so one name
// maps to one lock file on every file system.
var resourceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// resourceLocksDirName is the directory under the git common dir holding one
// lock file per resource.
const resourceLocksDirName = "flywheel-locks"

// resourceLockDir returns the directory of tree's resource locks, from one
// git process: the repository's git common dir, shared by every worktree, or
// <tree>/.flywheel/locks outside a repository.
func resourceLockDir(tree string) string {
	if _, common, ok := slotTree(tree); ok {
		return filepath.Join(common, resourceLocksDirName)
	}
	return filepath.Join(tree, ".flywheel", "locks")
}

// resourceLockPath is the lock file of resource name in lockDir.
func resourceLockPath(lockDir, name string) string {
	return filepath.Join(lockDir, "resource-"+name+".lock")
}

// holdsResources reports whether gate n (1-based) of a list holds the
// brief's resource locks: a gate marked [resources], or every gate of the
// list when none of it is marked. No resources means no gate holds any.
func holdsResources(resources []string, marked []int, n int) bool {
	if len(resources) == 0 {
		return false
	}
	return len(marked) == 0 || slices.Contains(marked, n)
}

// resourceLockTimings are a resource lock's timings: the dispatch lock's
// heartbeat and stale window, a 250ms retry, and wait (validate's quiet_wait
// budget) for a holder to finish its gate.
func resourceLockTimings(task, gateID string, wait time.Duration) repoLockTimings {
	return repoLockTimings{
		staleAfter: 15 * time.Second,
		wait:       wait,
		retry:      250 * time.Millisecond,
		heartbeat:  5 * time.Second,
		holder:     "validate " + task + " gate " + gateID,
	}
}

// acquireResources takes the lock of every name in lockDir, in sorted name
// order so two units naming the same pair in different orders cannot
// deadlock. release frees them in reverse; it is never nil and always safe
// to call. note says, for each lock that took at least 1s, "waited <dur> for
// resource <name> (<holder>)", joined with "; ". When a lock stays held past
// timings.wait, busy is "resource busy: <name> held by <holder>" and every
// lock taken so far is already released. onWait (may be nil) is called once
// per resource whose lock file is already present when its turn comes, before
// the wait starts, with the holder label ("another command" when the file
// names none); its error releases every lock and is returned.
func acquireResources(lockDir string, names []string, timings repoLockTimings, onWait func(name, holder string) error) (release func(), note, busy string, err error) {
	var releases []func()
	release = func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
		releases = nil
	}
	clock := now
	if timings.now != nil {
		clock = timings.now
	}
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return release, "", "", fmt.Errorf("resource lock: create %s: %w", lockDir, err)
	}
	sorted := slices.Clone(names)
	slices.Sort(sorted)
	for _, name := range slices.Compact(sorted) {
		p := resourceLockPath(lockDir, name)
		holder := repoLockHolderLabel(p)
		if onWait != nil {
			if _, serr := os.Stat(p); serr == nil {
				who := holder
				if who == "" {
					who = "another command"
				}
				if werr := onWait(name, who); werr != nil {
					release()
					return release, "", "", werr
				}
			}
		}
		start := clock()
		rel, aerr := acquireLockFile(p, timings)
		var lb *RepoLockBusy
		if errors.As(aerr, &lb) {
			release()
			who := lb.Holder
			if who == "" {
				who = "another command"
			}
			return release, "", "resource busy: " + name + " held by " + who, nil
		}
		if aerr != nil {
			release()
			return release, "", "", fmt.Errorf("resource lock %s: %w", p, aerr)
		}
		releases = append(releases, rel)
		if waited := clock().Sub(start); waited >= time.Second {
			n := fmt.Sprintf("waited %s for resource %s", waited.Round(time.Second), name)
			if holder != "" {
				n += " (" + holder + ")"
			}
			note = joinNotes(note, n)
		}
	}
	return release, note, "", nil
}

// joinNotes joins two notes with "; ", skipping an empty one.
func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}
