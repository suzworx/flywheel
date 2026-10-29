package flywheel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var backupNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// backupLedger builds a small real ledger in a temp dir: chained events plus
// the given extra files (paths relative to .flywheel).
func backupLedger(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"one", "two", "three"} {
		if err := AppendEvent(dir, Event{Kind: "note", Note: n}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	for rel, body := range extra {
		p := filepath.Join(dir, ".flywheel", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ledgerDirBackup is a repo whose config ledger.dir is ext, backed up to a
// fresh dest after build ran against it.
func ledgerDirBackup(t *testing.T, ext string, build func(dir string)) (string, string, BackupResult, error) {
	t.Helper()
	dir := ledgerDirRepo(t)
	writeLedgerConfig(t, dir, ledgerConfigBody(ext))
	build(dir)
	dest := filepath.Join(t.TempDir(), "bk")
	res, err := Backup(dir, dest, backupNow)
	return dir, dest, res, err
}

// not parallel: clears FLYWHEEL_LEDGER_DIR.
func TestBackupLedgerDirCopiesExternalLog(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	ext := t.TempDir()
	dir, dest, res, err := ledgerDirBackup(t, ext, func(dir string) {
		appendNotes(t, dir, "a", "b", "c")
		for rel, body := range map[string]string{"locks/x.lock": "x", "events.jsonl.tmp": "partial", "other.txt": "not the log"} {
			p := filepath.Join(ext, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	root, _, err := EventLogDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	src, err := VerifyLogChain(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.LedgerDir != root || !res.ChainOK || res.ChainLines != src.Lines || src.Lines != 3 {
		t.Fatalf("Backup = %+v, want ledger_dir %s and an ok chain of %d lines", res, root, src.Lines)
	}
	live, err := os.ReadFile(filepath.Join(ext, "events.jsonl"))
	if err != nil {
		t.Fatalf("the live external log is gone: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(dest, ".flywheel", "events.jsonl"))
	if err != nil || !bytes.Equal(copied, live) {
		t.Fatalf("copy of events.jsonl = %q, %v; want the external log %q", copied, err, live)
	}
	for _, rel := range []string{"locks", "events.jsonl.tmp", "other.txt"} {
		if _, err := os.Lstat(filepath.Join(dest, ".flywheel", rel)); err == nil {
			t.Errorf("backup copied %s from the ledger dir", rel)
		}
	}
	m := readBackupManifest(t, dest)
	found := false
	for _, f := range m.Files {
		found = found || f.Path == ".flywheel/events.jsonl"
	}
	if m.LedgerDir != root || !found || !m.Chain.OK || m.Chain.Lines != 3 {
		t.Fatalf("manifest = %+v, want ledger_dir %s and .flywheel/events.jsonl", m, root)
	}
	if b, err := os.ReadFile(filepath.Join(ext, "other.txt")); err != nil || string(b) != "not the log" {
		t.Errorf("the live ledger dir was changed: %q, %v", b, err)
	}
	if _, err := Backup(dir, filepath.Join(ext, "bk"), backupNow); err == nil {
		t.Error("Backup into the ledger dir succeeded, want a refusal")
	}
}

// not parallel: clears FLYWHEEL_LEDGER_DIR.
func TestBackupLedgerDirSharded(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, dest, res, err := ledgerDirBackup(t, t.TempDir(), func(dir string) {
		appendNotes(t, dir, "a", "b")
		if _, _, err := EnableShards(dir, time.Now()); err != nil {
			t.Fatal(err)
		}
		appendNotes(t, dir, "c")
	})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	src, err := VerifyLogChain(dir)
	if err != nil || !src.OK() {
		t.Fatalf("source chain = %+v, %v", src, err)
	}
	if !res.ChainOK || res.ChainLines != src.Lines {
		t.Fatalf("Backup = %+v, want an ok chain of %d lines", res, src.Lines)
	}
	if info, err := os.Stat(filepath.Join(dest, ".flywheel", "events", floorShard)); err != nil || info.IsDir() {
		t.Fatalf("no copied shard: %v", err)
	}
}

// not parallel: clears FLYWHEEL_LEDGER_DIR.
func TestBackupLedgerDirMissing(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	_, _, res, err := ledgerDirBackup(t, filepath.Join(t.TempDir(), "absent"), func(string) {})
	if err != nil || res.LedgerDir == "" || !res.ChainOK || res.ChainLines != 0 {
		t.Fatalf("Backup = %+v, %v; want ledger_dir set and no events", res, err)
	}
}

func readBackupManifest(t *testing.T, dest string) backupManifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dest, "flywheel-backup.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m backupManifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return m
}

func TestBackupCopiesLedger(t *testing.T) {
	t.Parallel()
	dir := backupLedger(t, map[string]string{"state.json": `{"s":1}`, "config.json": `{"c":1}`, "briefs/x.txt": "brief", "runs/x.jsonl": "{}\n{\"partial"})
	dest := filepath.Join(t.TempDir(), "bak")
	res, err := Backup(dir, dest, backupNow)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if !res.ChainOK || res.ChainLines != 3 || res.PartialTailBytes != 0 {
		t.Fatalf("result = %+v, want chain ok over 3 lines, no partial tail", res)
	}
	m := readBackupManifest(t, dest)
	if m.Created != "2026-09-26T12:00:00Z" || !m.Chain.OK {
		t.Fatalf("manifest = %+v", m)
	}
	listed := map[string]BackupFile{}
	for _, f := range m.Files {
		listed[f.Path] = f
	}
	for _, rel := range []string{"events.jsonl", "state.json", "config.json", "briefs/x.txt", "runs/x.jsonl"} {
		want, err := os.ReadFile(filepath.Join(dir, ".flywheel", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dest, ".flywheel", filepath.FromSlash(rel)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: copy differs (err %v)", rel, err)
		}
		sum := sha256.Sum256(want)
		if f := listed[".flywheel/"+rel]; f.SHA256 != hex.EncodeToString(sum[:]) || f.Size != int64(len(want)) {
			t.Fatalf("%s: manifest entry %+v", rel, f)
		}
	}
	if chain, err := VerifyLogChain(dest); err != nil || !chain.OK() {
		t.Fatalf("VerifyLogChain(dest) = %+v, %v", chain, err)
	}
}

func TestBackupSkipsWorktreesAndLocks(t *testing.T) {
	t.Parallel()
	dir := backupLedger(t, map[string]string{"worktrees/T/file": "x", "locks/l": "x", "a.tmp": "x", "runs/keep.tmpl": "x"})
	dest := filepath.Join(t.TempDir(), "bak")
	if _, err := Backup(dir, dest, backupNow); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	for _, rel := range []string{"worktrees", "locks", "a.tmp"} {
		if _, err := os.Stat(filepath.Join(dest, ".flywheel", rel)); !os.IsNotExist(err) {
			t.Fatalf("%s was copied (err %v)", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, ".flywheel", "runs", "keep.tmpl")); err != nil {
		t.Fatalf("runs/keep.tmpl missing: %v", err)
	}
}

func TestBackupPartialTail(t *testing.T) {
	t.Parallel()
	dir := backupLedger(t, nil)
	path := filepath.Join(dir, ".flywheel", "events.jsonl")
	complete, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	partial := `{"partial`
	if err := os.WriteFile(path, append(append([]byte{}, complete...), partial...), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "bak")
	res, err := Backup(dir, dest, backupNow)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dest, ".flywheel", "events.jsonl"))
	if !bytes.Equal(got, complete) || res.PartialTailBytes != len(partial) || !res.ChainOK {
		t.Fatalf("copy %q, result %+v; want the complete lines, %d partial bytes, chain ok", got, res, len(partial))
	}
}

func TestBackupRefusesExistingDest(t *testing.T) {
	t.Parallel()
	dir := backupLedger(t, nil)
	parent := t.TempDir()
	dest := filepath.Join(parent, "bak")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(dir, dest, backupNow); err == nil || !bytes.Contains([]byte(err.Error()), []byte("is not empty")) {
		t.Fatalf("Backup into a non-empty dest: err = %v, want not empty", err)
	}
	entries, _ := os.ReadDir(dest)
	if b, _ := os.ReadFile(filepath.Join(dest, "keep")); len(entries) != 1 || string(b) != "mine" {
		t.Fatalf("dest changed: %d entries, keep = %q", len(entries), b)
	}
	if left, _ := filepath.Glob(filepath.Join(parent, ".flywheel-backup-*")); len(left) != 0 {
		t.Fatalf("temp dirs left behind: %v", left)
	}
	empty := filepath.Join(parent, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if res, err := Backup(dir, empty, backupNow); err != nil || !res.ChainOK {
		t.Fatalf("Backup into an empty dest = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(empty, "flywheel-backup.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestBackupRefusesDestInsideLedger(t *testing.T) {
	t.Parallel()
	dir := backupLedger(t, nil)
	if _, err := Backup(dir, filepath.Join(dir, ".flywheel", "bak"), backupNow); err == nil {
		t.Fatal("Backup into the ledger succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "bak")); !os.IsNotExist(err) {
		t.Fatalf("dest inside the ledger was created (err %v)", err)
	}
}

func TestBackupShardedVerifies(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, _, err := EnableShards(dir, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("EnableShards: %v", err)
	}
	for _, e := range []Event{{Task: "T1", Kind: "planned"}, {Task: "T1", Kind: "dispatched"}, {Task: "T2", Kind: "planned"}} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	res, err := Backup(dir, filepath.Join(t.TempDir(), "bak"), backupNow)
	if err != nil || !res.ChainOK {
		t.Fatalf("Backup of a sharded ledger = %+v, %v; want chain ok", res, err)
	}
	shard := filepath.Join(dir, ".flywheel", "events", "T1.jsonl")
	b, err := os.ReadFile(shard)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shard, bytes.Replace(b, []byte(`"planned"`), []byte(`"blocked"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "bak")
	res, err = Backup(dir, dest, backupNow)
	if err != nil {
		t.Fatalf("Backup of a tampered ledger: %v", err)
	}
	if res.ChainOK || res.ChainBreak == "" {
		t.Fatalf("result = %+v, want a broken chain", res)
	}
	if m := readBackupManifest(t, dest); m.Chain.OK || m.Chain.Break != res.ChainBreak {
		t.Fatalf("manifest chain = %+v, want the break %q", m.Chain, res.ChainBreak)
	}
}
