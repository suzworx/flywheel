package flywheel

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// slotGit runs git in dir and fails the test on error.
func slotGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "core.autocrlf=false"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// slotRepo makes a repository with one commit plus n detached worktrees and
// returns the main tree and the worktrees.
func slotRepo(t *testing.T, n int) (string, []string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	slotGit(t, filepath.Dir(repo), "init", "-q", repo)
	slotGit(t, repo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	var wts []string
	for i := 1; i <= n; i++ {
		wts = append(wts, slotWorktree(t, repo, fmt.Sprintf("wt%d", i)))
	}
	return repo, wts
}

// slotWorktree adds a detached worktree named name next to repo.
func slotWorktree(t *testing.T, repo, name string) string {
	t.Helper()
	wt := filepath.Join(filepath.Dir(repo), name)
	slotGit(t, repo, "worktree", "add", "-q", "--detach", wt)
	return wt
}

// mustSlot leases tree's slot and fails the test on error.
func mustSlot(t *testing.T, tree string) int {
	t.Helper()
	n, err := LeaseSlot(tree)
	if err != nil {
		t.Fatalf("LeaseSlot(%s): %v", tree, err)
	}
	return n
}

func TestSlotMainTreeReused(t *testing.T) {
	t.Parallel()
	repo, _ := slotRepo(t, 0)
	if got := mustSlot(t, repo); got != 1 {
		t.Fatalf("first lease = %d, want 1", got)
	}
	if got := mustSlot(t, repo); got != 1 {
		t.Fatalf("second lease = %d, want 1", got)
	}
	entries, err := os.ReadDir(filepath.Join(repo, ".git", slotsDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("lease files = %d, want 1", len(entries))
	}
}

func TestSlotWorktreesDistinct(t *testing.T) {
	t.Parallel()
	repo, wts := slotRepo(t, 2)
	trees := []string{wts[0], wts[1], repo}
	for i, tree := range trees {
		if got := mustSlot(t, tree); got != i+1 {
			t.Fatalf("lease %s = %d, want %d", tree, got, i+1)
		}
	}
	for i, tree := range trees {
		if got := mustSlot(t, tree); got != i+1 {
			t.Fatalf("second lease %s = %d, want %d", tree, got, i+1)
		}
	}
}

func TestSlotReclaimsRemovedTree(t *testing.T) {
	t.Parallel()
	repo, wts := slotRepo(t, 2)
	if mustSlot(t, wts[0]) != 1 || mustSlot(t, wts[1]) != 2 {
		t.Fatal("initial leases are not 1 and 2")
	}
	if err := os.RemoveAll(wts[0]); err != nil {
		t.Fatal(err)
	}
	if got := mustSlot(t, slotWorktree(t, repo, "wt3")); got != 1 {
		t.Fatalf("lease after removal = %d, want the reclaimed 1", got)
	}
	if got := mustSlot(t, wts[1]); got != 2 {
		t.Fatalf("surviving lease = %d, want 2", got)
	}
}

func TestSlotSubdirSameSlot(t *testing.T) {
	t.Parallel()
	repo, _ := slotRepo(t, 0)
	sub := filepath.Join(repo, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	a, b := mustSlot(t, repo), mustSlot(t, sub)
	if a != b {
		t.Fatalf("tree slot %d, subdirectory slot %d; want the same", a, b)
	}
	entries, err := os.ReadDir(filepath.Join(repo, ".git", slotsDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("lease files = %d, want 1", len(entries))
	}
}

// TestSlotReclaimKeepsLiveLease is the reclaim race made deterministic: lease
// 1 was read recording a gone tree, but another racer has since reclaimed it
// and holds it for a live tree; the reclaim must put the live lease back.
func TestSlotReclaimKeepsLiveLease(t *testing.T) {
	t.Parallel()
	repo, _ := slotRepo(t, 0)
	common, err := gitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(common, slotsDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "1")
	live := filepath.Clean(repo)
	if ok, err := createLease(p, live); !ok || err != nil {
		t.Fatalf("createLease = %v, %v", ok, err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if reclaimLease(p, gone) {
		t.Fatal("reclaimLease reclaimed a live lease")
	}
	if got, ok := readLease(p); !ok || got != live {
		t.Fatalf("lease 1 = %q, %v; want %q", got, ok, live)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("slots dir holds %d entries, want only lease 1", len(entries))
	}
	// A lease still recording the gone tree is reclaimed.
	if err := os.WriteFile(filepath.Join(dir, "2"), []byte(gone+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !reclaimLease(filepath.Join(dir, "2"), gone) {
		t.Fatal("reclaimLease did not reclaim a gone tree's lease")
	}
	if _, err := os.Stat(filepath.Join(dir, "2")); !os.IsNotExist(err) {
		t.Fatalf("lease 2 still exists after reclaim: %v", err)
	}
}

func TestSlotNotARepository(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	n, err := LeaseSlot(dir)
	if n != 0 || err != nil {
		t.Fatalf("LeaseSlot(non-repo) = %d, %v; want 0, nil", n, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("non-repo dir gained %d entries", len(entries))
	}
}

func TestSlotConcurrent(t *testing.T) {
	t.Parallel()
	_, wts := slotRepo(t, 8)
	got := make([]int, len(wts))
	errs := make([]error, len(wts))
	var wg sync.WaitGroup
	for i, wt := range wts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = LeaseSlot(wt)
		}()
	}
	wg.Wait()
	seen := map[int]bool{}
	for i, n := range got {
		if errs[i] != nil {
			t.Fatalf("LeaseSlot(%s): %v", wts[i], errs[i])
		}
		if n < 1 || n > 8 || seen[n] {
			t.Fatalf("slots %v: want 8 distinct slots 1..8", got)
		}
		seen[n] = true
	}
}

func TestSlotGateSeesIt(t *testing.T) {
	t.Parallel()
	skipUnlessBash(t)
	repo, _ := slotRepo(t, 0)
	rc, _, out, _, err := runGateStages(repo, `echo "slot=$FLYWHEEL_SLOT"`, "")
	if err != nil || rc != 0 {
		t.Fatalf("runGateStages = rc %d, err %v, out %q", rc, err, out)
	}
	if !strings.Contains(string(out), "slot=1") {
		t.Fatalf("gate output %q, want slot=1", out)
	}
}

func TestSlotEventValidate(t *testing.T) {
	t.Parallel()
	if err := Validate(Event{Task: "T1", Kind: "dispatched", Attempt: "r1", Slot: 2}); err != nil {
		t.Fatalf("dispatched slot 2: %v", err)
	}
	for _, e := range []Event{
		{Task: "T1", Kind: "finished", Attempt: "r1", Slot: 2},
		{Task: "T1", Kind: "dispatched", Attempt: "r1", Slot: -1},
	} {
		if err := Validate(e); err == nil || !strings.Contains(err.Error(), "slot") {
			t.Fatalf("Validate(%s slot %d) = %v, want a slot error", e.Kind, e.Slot, err)
		}
	}
}
