package flywheel

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fleetGit runs git in dir for a fleet test, failing t on error.
func fleetGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com", "-C", dir}
	if out, err := exec.Command("git", append(base, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fleetRepo makes a git repository with one commit under a new temp dir.
func fleetRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "main")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fleetGit(t, dir, "init", "-q")
	fleetGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

// fleetLedger initialises a flywheel ledger in dir.
func fleetLedger(t *testing.T, dir string) {
	t.Helper()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init %s: %v", dir, err)
	}
}

// TestFleetRegistryRoundTrip: add, save, load, list and remove round-trip;
// the same path twice is refused naming the entry; default names are made
// unique; a path with no .flywheel/ that is not a git repository is refused.
func TestFleetRegistryRoundTrip(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "cfg", "fleet.json")
	f, err := LoadFleet(file)
	if err != nil || len(f.Roots) != 0 {
		t.Fatalf("LoadFleet(missing) = %+v, %v; want empty", f, err)
	}
	a := fleetRepo(t)
	b := fleetRepo(t) // same base name "main"
	ra, err := f.AddRoot(a, "")
	if err != nil || ra.Name != "main" {
		t.Fatalf("AddRoot a = %+v, %v; want name main", ra, err)
	}
	rb, err := f.AddRoot(b, "")
	if err != nil || rb.Name != "main-2" {
		t.Fatalf("AddRoot b = %+v, %v; want name main-2", rb, err)
	}
	if _, err := f.AddRoot(a+string(filepath.Separator), "x"); err == nil || !strings.Contains(err.Error(), `"main"`) {
		t.Errorf("AddRoot same path = %v, want a refusal naming main", err)
	}
	plain := t.TempDir()
	if _, err := f.AddRoot(plain, ""); err == nil {
		t.Errorf("AddRoot non-repo %s succeeded, want a refusal", plain)
	}
	fleetLedger(t, plain)
	if _, err := f.AddRoot(plain, "led"); err != nil {
		t.Errorf("AddRoot ledger-only dir: %v", err)
	}
	if err := SaveFleet(file, f); err != nil {
		t.Fatal(err)
	}
	g, err := LoadFleet(file)
	if err != nil || len(g.Roots) != 3 || g.Roots[1] != rb || g.Version != FleetVersion {
		t.Fatalf("LoadFleet = %+v, %v; want the 3 saved roots", g, err)
	}
	if err := g.RemoveRoot("main"); err != nil {
		t.Fatal(err)
	}
	if err := g.RemoveRoot("main"); err == nil {
		t.Error("RemoveRoot twice succeeded, want an error")
	}
	if len(g.Roots) != 2 || g.Roots[0].Name != "main-2" {
		t.Errorf("after remove roots = %+v", g.Roots)
	}
}

// fleetAppend appends raw events to dir's events.jsonl.
func fleetAppend(t *testing.T, dir string, events ...Event) {
	t.Helper()
	fh, err := os.OpenFile(filepath.Join(dir, ".flywheel", "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, e := range events {
		b, _ := json.Marshal(e)
		if _, err := fh.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

// TestFleetDiscovery: discovery finds the root, a sibling git worktree, a
// .flywheel/worktrees unit and a .claude/worktrees ledger; skips a worktree
// without a ledger; keeps one entry for a path reached both as a git
// worktree and under .flywheel/worktrees; and reports a missing root.
func TestFleetDiscovery(t *testing.T) {
	t.Parallel()
	root := fleetRepo(t)
	fleetLedger(t, root)
	sib := filepath.Join(filepath.Dir(root), "sib")
	fleetGit(t, root, "worktree", "add", "-q", "--detach", sib)
	fleetLedger(t, sib)
	fleetGit(t, root, "worktree", "add", "-q", "--detach", filepath.Join(filepath.Dir(root), "bare"))
	unit := filepath.Join(root, ".flywheel", "worktrees", "u1")
	fleetGit(t, root, "worktree", "add", "-q", "--detach", unit) // reached twice
	fleetLedger(t, unit)
	claude := filepath.Join(root, ".claude", "worktrees", "c1")
	fleetLedger(t, claude)
	if err := os.MkdirAll(filepath.Join(root, ".claude", "worktrees", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	f := Fleet{Roots: []FleetRoot{{Name: "main", Path: root}, {Name: "gone", Path: gone}}}
	got := FleetLedgers(f)
	var kinds []string
	for _, l := range got {
		kinds = append(kinds, l.Name+"="+l.Kind+"|"+l.Error)
	}
	want := []string{"main=root|", "main/c1=claude-worktree|", "main/u1=git-worktree|", "main/sib=git-worktree|", "gone=root|root missing"}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Errorf("FleetLedgers =\n %v\nwant\n %v", kinds, want)
	}
}

// TestFleetStatus: a suspended ledger shows SUSPENDED, a paused model shows
// "paused: m1" with its andon counted, the latest event and health ages are
// set, and a ledger that fails to read carries an error without failing the
// others.
func TestFleetStatus(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	base := t.TempDir()
	mk := func(name string) string {
		d := filepath.Join(base, name)
		fleetLedger(t, d)
		return d
	}
	sus, paused, bad := mk("sus"), mk("paused"), mk("bad")
	if err := Suspend(sus, "lead", "freeze", time.Time{}); err != nil {
		t.Fatal(err)
	}
	ts := now.Add(-time.Minute).Format(time.RFC3339Nano)
	fleetAppend(t, paused, Event{TS: ts, Kind: "finished", Task: "T1", Model: "m1", Reason: "rate-limited",
		ResetAt: now.Add(time.Hour).Format(time.RFC3339)})
	if err := os.WriteFile(filepath.Join(bad, ".flywheel", shardDirName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := Fleet{Roots: []FleetRoot{{Name: "sus", Path: sus}, {Name: "paused", Path: paused}, {Name: "bad", Path: bad}}}
	rows := FleetStatus(f, now)
	if len(rows) != 3 {
		t.Fatalf("FleetStatus = %d rows, want 3: %+v", len(rows), rows)
	}
	if r := rows[0]; r.State() != "SUSPENDED" || r.Error != "" || r.LastAge == nil {
		t.Errorf("sus row = %+v state %q, want SUSPENDED with a last age", r, r.State())
	}
	r := rows[1]
	rep, err := Status(paused, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.State() != "paused: m1" || r.Andon != rep.Andon || r.Andon == 0 || r.LastAge == nil || *r.LastAge != 60 {
		t.Errorf("paused row = %+v state %q, want paused: m1 with andon %d", r, r.State(), rep.Andon)
	}
	if r.HealthAge != nil {
		t.Errorf("paused row health age = %d, want none recorded", *r.HealthAge)
	}
	if rows[2].Error == "" || rows[2].Name != "bad" {
		t.Errorf("bad row = %+v, want a read error", rows[2])
	}
}
