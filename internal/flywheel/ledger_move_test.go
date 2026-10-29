package flywheel

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ledgerMoveRepo is a git repo whose in-tree log holds notes (sealed into the
// sharded layout, then more appended, when shard), with ledger.dir then set
// to a fresh dir: the repo, the ledger dir and the in-tree files' bytes.
func ledgerMoveRepo(t *testing.T, shard bool, notes ...string) (string, string, map[string][]byte) {
	t.Helper()
	dir := ledgerDirRepo(t)
	appendNotes(t, dir, notes...)
	if shard {
		if _, _, err := EnableShards(dir, time.Now()); err != nil {
			t.Fatal(err)
		}
		appendNotes(t, dir, "after-seal")
	}
	files, _, err := inTreeLogFiles(filepath.Join(dir, ".flywheel"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]byte{}
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(dir, ".flywheel", filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		want[f] = b
	}
	ext := filepath.Join(t.TempDir(), "ledger")
	writeLedgerConfig(t, dir, ledgerConfigBody(ext))
	return dir, ext, want
}

// ledgerTreeHolds fails unless root holds exactly want's files with want's bytes.
func ledgerTreeHolds(t *testing.T, root string, want map[string][]byte) {
	t.Helper()
	for f, b := range want {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil || string(got) != string(b) {
			t.Fatalf("%s in %s: %v, bytes equal %v", f, root, err, string(got) == string(b))
		}
	}
}

func eventNotes(t *testing.T, dir string) []string {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, e := range events {
		notes = append(notes, e.Note)
	}
	return notes
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerMoveLegacy(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, ext, want := ledgerMoveRepo(t, false, "a", "b", "c")
	res, err := LedgerMove(dir)
	if err != nil || !res.Moved || res.Resumed || !res.ChainOK || res.Tracked || !reflect.DeepEqual(res.Files, []string{legacyLogName}) || res.Bytes != int64(len(want[legacyLogName])) || res.Destination != ext {
		t.Fatalf("LedgerMove = %+v, %v", res, err)
	}
	noInTreeLog(t, dir)
	ledgerTreeHolds(t, ext, want)
	if got := eventNotes(t, dir); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("events after the move = %q", got)
	}
	appendNotes(t, dir, "d")
	noInTreeLog(t, dir)
	readCount(t, dir, 4)
	if c, err := VerifyLogChain(dir); err != nil || !c.OK() {
		t.Fatalf("VerifyLogChain = %+v, %v", c, err)
	}
	if anyExists(filepath.Join(dir, ".flywheel", ledgerMoveAside)) {
		t.Fatal("the aside dir survived the move")
	}
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerMoveSharded(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, ext, want := ledgerMoveRepo(t, true, "a", "b")
	if want[shardDirName+"/"+floorShard] == nil || want[legacyLogName] == nil {
		t.Fatalf("sharded in-tree log = %v files", len(want))
	}
	res, err := LedgerMove(dir)
	if err != nil || !res.Moved || !res.ChainOK || len(res.Files) != len(want) {
		t.Fatalf("LedgerMove = %+v, %v", res, err)
	}
	noInTreeLog(t, dir)
	ledgerTreeHolds(t, ext, want)
	if sharded, err := ShardedLayout(dir); err != nil || !sharded {
		t.Fatalf("ShardedLayout after the move = %v, %v", sharded, err)
	}
	readCount(t, dir, 4) // a, b, the seal, after-seal
	appendNotes(t, dir, "c")
	if c, err := VerifyLogChain(dir); err != nil || !c.OK() {
		t.Fatalf("VerifyLogChain = %+v, %v", c, err)
	}
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerMoveUsage(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir := ledgerDirRepo(t)
	appendNotes(t, dir, "a")
	var usage *LedgerUsageError
	if _, err := LedgerMove(dir); !errors.As(err, &usage) || !strings.Contains(err.Error(), "ledger.dir is not set") {
		t.Fatalf("LedgerMove with ledger.dir unset = %v", err)
	}
	writeLedgerConfig(t, dir, ledgerConfigBody("rel/ledger"))
	if _, err := LedgerMove(dir); !errors.As(err, &usage) {
		t.Fatalf("LedgerMove with a relative ledger.dir = %v", err)
	}
	if !anyExists(filepath.Join(dir, ".flywheel", legacyLogName)) {
		t.Fatal("a refused move changed the in-tree log")
	}
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerMoveNothing(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, ext := ledgerDirRepo(t), filepath.Join(t.TempDir(), "ledger")
	writeLedgerConfig(t, dir, ledgerConfigBody(ext))
	res, err := LedgerMove(dir)
	if err != nil || res.Moved || !strings.Contains(res.Note, "nothing to move") {
		t.Fatalf("LedgerMove with no in-tree log = %+v, %v", res, err)
	}
	if anyExists(ext) {
		t.Fatal("LedgerMove with nothing to move created the ledger dir")
	}
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerMoveRefusesOtherLog(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, ext, want := ledgerMoveRepo(t, false, "a", "b")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	other := map[string][]byte{legacyLogName: []byte("{\"kind\":\"note\"}\n")}
	if err := os.WriteFile(filepath.Join(ext, legacyLogName), other[legacyLogName], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LedgerMove(dir)
	var rr *RuleRefusal
	if !errors.As(err, &rr) || rr.Rule != "two-live-logs" {
		t.Fatalf("LedgerMove onto another log = %v", err)
	}
	ledgerTreeHolds(t, filepath.Join(dir, ".flywheel"), want)
	ledgerTreeHolds(t, ext, other)
	if entries, err := os.ReadDir(filepath.Dir(ext)); err != nil || len(entries) != 1 {
		t.Fatalf("beside the ledger dir: %d entries, %v; want only the ledger dir", len(entries), err)
	}
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerMoveResume(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	for _, aside := range []bool{false, true} {
		dir, ext, want := ledgerMoveRepo(t, true, "a")
		src := filepath.Join(dir, ".flywheel")
		for f, b := range want {
			p := filepath.Join(ext, filepath.FromSlash(f))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if aside { // a crash after setting the in-tree log aside
			if err := os.MkdirAll(filepath.Join(src, ledgerMoveAside), 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{legacyLogName, shardDirName} {
				if err := os.Rename(filepath.Join(src, name), filepath.Join(src, ledgerMoveAside, name)); err != nil {
					t.Fatal(err)
				}
			}
		}
		res, err := LedgerMove(dir)
		if err != nil || !res.Moved || !res.Resumed || !res.ChainOK {
			t.Fatalf("aside %v: LedgerMove = %+v, %v", aside, res, err)
		}
		noInTreeLog(t, dir)
		ledgerTreeHolds(t, ext, want)
		if anyExists(filepath.Join(src, ledgerMoveAside)) {
			t.Fatalf("aside %v: the aside dir survived", aside)
		}
	}
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerMoveTrackedKeepsLocks(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, ext, _ := ledgerMoveRepo(t, false, "a")
	ledgerGit(t, dir, "add", ".flywheel/events.jsonl")
	ledgerGit(t, dir, "commit", "-q", "-m", "log")
	if err := os.MkdirAll(filepath.Join(ext, "locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := LedgerMove(dir)
	if err != nil || !res.Moved || !res.Tracked {
		t.Fatalf("LedgerMove = %+v, %v", res, err)
	}
	if !anyExists(filepath.Join(ext, "locks")) || !anyExists(filepath.Join(ext, legacyLogName)) {
		t.Fatal("the ledger dir lost locks/ or did not get the log")
	}
	out, err := exec.Command("git", "-C", dir, "ls-files", ".flywheel/events.jsonl").Output()
	if err != nil || strings.TrimSpace(string(out)) != ".flywheel/events.jsonl" {
		t.Fatalf("the index lost the tracked log: %q, %v", out, err)
	}
}
