package flywheel

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func ledgerGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "core.autocrlf=false", "-c", "user.name=test", "-c", "user.email=test@example.com", "-C", dir}, args...)
	if out, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ledgerDirRepo is a git repo with one commit holding README.md.
func ledgerDirRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ledgerGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ledgerGit(t, dir, "add", "README.md")
	ledgerGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func writeLedgerConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", configFileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ledgerConfigBody(value string) string {
	return `{"version":1,"ledger":{"dir":"` + filepath.ToSlash(value) + `"}}`
}

func appendNotes(t *testing.T, dir string, notes ...string) {
	t.Helper()
	for _, n := range notes {
		if err := AppendEvent(dir, Event{Kind: "note", Note: n}); err != nil {
			t.Fatal(err)
		}
	}
}

func readCount(t *testing.T, dir string, want int) {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != want {
		t.Fatalf("ReadEvents(%s) = %d events, want %d", dir, len(events), want)
	}
}

func noInTreeLog(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{legacyLogName, shardDirName} {
		if _, err := os.Lstat(filepath.Join(dir, ".flywheel", name)); err == nil {
			t.Fatalf("in-tree %s exists with ledger.dir set", name)
		}
	}
}

// not parallel: FLYWHEEL_LEDGER_DIR must be unset for the default path.
func TestLedgerDirUnsetIsInTree(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir := t.TempDir()
	root, err := ledgerEventsRoot(dir)
	if err != nil || root != filepath.Join(dir, ".flywheel") {
		t.Fatalf("ledgerEventsRoot = %q, %v; want %q", root, err, filepath.Join(dir, ".flywheel"))
	}
	appendNotes(t, dir, "a", "b")
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	readCount(t, dir, 2)
}

// not parallel: clears FLYWHEEL_LEDGER_DIR.
func TestLedgerDirAbsolute(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, ext := ledgerDirRepo(t), t.TempDir()
	writeLedgerConfig(t, dir, ledgerConfigBody(ext))
	appendNotes(t, dir, "a", "b")
	if _, err := os.Stat(filepath.Join(ext, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	noInTreeLog(t, dir)
	readCount(t, dir, 2)
	if c, err := VerifyLogChain(dir); err != nil || !c.OK() || c.Lines != 2 {
		t.Fatalf("VerifyLogChain = %+v, %v", c, err)
	}
	if _, _, err := EnableShards(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(ext, shardDirName)); err != nil || !info.IsDir() {
		t.Fatalf("no sharded log in the ledger dir: %v", err)
	}
	appendNotes(t, dir, "c")
	noInTreeLog(t, dir)
	readCount(t, dir, 4) // a, b, the sharded seal, c
	if c, err := VerifyLogChain(dir); err != nil || !c.OK() {
		t.Fatalf("sharded VerifyLogChain = %+v, %v", c, err)
	}
	if _, err := os.Stat(filepath.Join(ext, "locks")); err != nil {
		t.Fatalf("shard locks not under the ledger dir: %v", err)
	}
}

// not parallel: clears FLYWHEEL_LEDGER_DIR.
func TestLedgerDirSurvivesBranchSwitch(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir, ext := ledgerDirRepo(t), t.TempDir()
	writeLedgerConfig(t, dir, ledgerConfigBody(ext))
	ledgerGit(t, dir, "add", ".flywheel/config.json")
	ledgerGit(t, dir, "commit", "-q", "-m", "ledger")
	appendNotes(t, dir, "a", "b", "c")
	logPath := filepath.Join(ext, "events.jsonl")
	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	ledgerGit(t, dir, "checkout", "-q", "-b", "other")
	ledgerGit(t, dir, "checkout", "-q", "-")
	ledgerGit(t, dir, "checkout", "-q", "HEAD~1")
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", configFileName)); err == nil {
		t.Fatal("config survived the checkout of the commit without it")
	}
	ledgerGit(t, dir, "checkout", "-q", "other")
	if after, err := os.ReadFile(logPath); err != nil || string(after) != string(before) {
		t.Fatalf("external log changed by checkouts: %v", err)
	}
	readCount(t, dir, 3)
}

// not parallel: clears FLYWHEEL_LEDGER_DIR.
func TestLedgerDirRefusals(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir := ledgerDirRepo(t)
	writeLedgerConfig(t, dir, ledgerConfigBody("rel/ledger"))
	if _, _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "ledger.dir") || !strings.Contains(err.Error(), configFileName) {
		t.Fatalf("LoadConfig relative = %v", err)
	}
	writeLedgerConfig(t, dir, `{"version":1,"ledger":{"dir":""}}`)
	if _, _, err := LoadConfig(dir); err == nil || !strings.Contains(err.Error(), "ledger.dir") {
		t.Fatalf("LoadConfig empty = %v", err)
	}
	writeLedgerConfig(t, dir, ledgerConfigBody(filepath.Join(dir, "sub")))
	if _, err := ReadEvents(dir); err == nil || !strings.Contains(err.Error(), "ledger.dir") {
		t.Fatalf("ReadEvents with ledger.dir in the work tree = %v", err)
	}
	inTree := t.TempDir()
	appendNotes(t, inTree, "a")
	ext := t.TempDir()
	writeLedgerConfig(t, inTree, ledgerConfigBody(ext))
	_, err := ReadEvents(inTree)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(inTree, ".flywheel", "events.jsonl")) || !strings.Contains(err.Error(), filepath.Clean(ext)) {
		t.Fatalf("two live logs = %v", err)
	}
}

// not parallel: sets FLYWHEEL_LEDGER_DIR.
func TestLedgerDirEnvOverride(t *testing.T) {
	dir, cfgDir, envDir := ledgerDirRepo(t), t.TempDir(), t.TempDir()
	writeLedgerConfig(t, dir, ledgerConfigBody(cfgDir))
	t.Setenv(ledgerDirEnv, envDir)
	appendNotes(t, dir, "a")
	if _, err := os.Stat(filepath.Join(envDir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "events.jsonl")); err == nil {
		t.Fatal("config ledger.dir won over FLYWHEEL_LEDGER_DIR")
	}
	readCount(t, dir, 1)
}

// not parallel: sets HOME, USERPROFILE and FLYWHEEL_LEDGER_DIR.
func TestLedgerDirHome(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := ledgerDirRepo(t)
	writeLedgerConfig(t, dir, ledgerConfigBody("home"))
	root, external, err := EventLogDir(dir)
	if err != nil || !external {
		t.Fatalf("EventLogDir = %q, %v, %v", root, external, err)
	}
	if filepath.Dir(root) != filepath.Join(home, ".flywheel", "ledgers") || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(filepath.Base(root)) {
		t.Fatalf("home ledger dir = %q", root)
	}
	wt := filepath.Join(t.TempDir(), "wt")
	ledgerGit(t, dir, "worktree", "add", "-q", "-b", "wtb", wt)
	writeLedgerConfig(t, wt, ledgerConfigBody("home"))
	if root2, _, err := EventLogDir(wt); err != nil || root2 != root {
		t.Fatalf("worktree EventLogDir = %q, %v; want %q", root2, err, root)
	}
	appendNotes(t, dir, "a")
	readCount(t, wt, 1)
}

// not parallel: clears FLYWHEEL_LEDGER_DIR.
func TestLedgerDirBrokenConfigStillReads(t *testing.T) {
	t.Setenv(ledgerDirEnv, "")
	dir := t.TempDir()
	appendNotes(t, dir, "a", "b")
	writeLedgerConfig(t, dir, `{"version":1,"no_such_key":true}`)
	if _, _, err := LoadConfig(dir); err == nil {
		t.Fatal("LoadConfig accepted an unknown key")
	}
	readCount(t, dir, 2)
}

// TestDoctorLedgerDir: with ledger.dir set doctor skips the committed-ledger
// check and warns only about an unusable directory; a bad value, two live logs
// and an interrupted ledger move are each a warning.
func TestDoctorLedgerDir(t *testing.T) {
	t.Parallel()
	t.Run("external log", func(t *testing.T) {
		t.Parallel()
		dir, ext := ledgerDirRepo(t), t.TempDir()
		writeLedgerConfig(t, dir, ledgerConfigBody(ext))
		appendNotes(t, dir, "a")
		if got := DoctorLedgerWarning(dir); got != "" {
			t.Errorf("DoctorLedgerWarning(external) = %q, want empty", got)
		}
		ents, _ := os.ReadDir(ext)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".doctor-probe") {
				t.Errorf("doctor left its probe %s in the ledger dir", e.Name())
			}
		}
	})
	t.Run("missing dir", func(t *testing.T) {
		t.Parallel()
		dir := ledgerDirRepo(t)
		writeLedgerConfig(t, dir, ledgerConfigBody(filepath.Join(t.TempDir(), "later")))
		if got := DoctorLedgerWarning(dir); got != "" {
			t.Errorf("DoctorLedgerWarning(missing ledger dir) = %q, want empty", got)
		}
	})
	t.Run("regular file", func(t *testing.T) {
		t.Parallel()
		dir, file := ledgerDirRepo(t), filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeLedgerConfig(t, dir, ledgerConfigBody(file))
		if got := DoctorLedgerWarning(dir); !strings.Contains(got, "not a directory") {
			t.Errorf("DoctorLedgerWarning(file) = %q, want a not-a-directory warning", got)
		}
	})
	t.Run("two live logs", func(t *testing.T) {
		t.Parallel()
		dir := ledgerDirRepo(t)
		appendNotes(t, dir, "a")
		writeLedgerConfig(t, dir, ledgerConfigBody(t.TempDir()))
		if got := DoctorLedgerWarning(dir); !strings.Contains(got, "ledger move") {
			t.Errorf("DoctorLedgerWarning(two logs) = %q, want it to name ledger move", got)
		}
	})
	t.Run("interrupted move", func(t *testing.T) {
		t.Parallel()
		dir := ledgerDirRepo(t)
		writeLedgerConfig(t, dir, ledgerConfigBody(t.TempDir()))
		if err := os.MkdirAll(filepath.Join(dir, ".flywheel", ledgerMoveAside), 0o755); err != nil {
			t.Fatal(err)
		}
		if got := DoctorLedgerWarning(dir); !strings.Contains(got, "interrupted") || !strings.Contains(got, ledgerMoveAside) {
			t.Errorf("DoctorLedgerWarning(aside) = %q, want the interrupted-move warning", got)
		}
	})
}

// TestFleetLedgerDir: two worktrees sharing one ledger.dir are one ledger; an
// in-tree ledger alongside is still listed, and so is a bad ledger.dir.
func TestFleetLedgerDir(t *testing.T) {
	t.Parallel()
	dir, ext := ledgerDirRepo(t), t.TempDir()
	writeLedgerConfig(t, dir, ledgerConfigBody(ext))
	wt := filepath.Join(t.TempDir(), "wt")
	ledgerGit(t, dir, "worktree", "add", "-q", "--detach", wt)
	writeLedgerConfig(t, wt, ledgerConfigBody(ext))
	appendNotes(t, dir, "a")
	plain := t.TempDir()
	appendNotes(t, plain, "b")
	bad := t.TempDir()
	writeLedgerConfig(t, bad, ledgerConfigBody("rel/ledger"))
	f := Fleet{Roots: []FleetRoot{{Name: "main", Path: dir}, {Name: "wt", Path: wt}, {Name: "plain", Path: plain}, {Name: "bad", Path: bad}}}
	var names []string
	for _, l := range FleetLedgers(f) {
		names = append(names, l.Name)
	}
	if strings.Join(names, " ") != "main plain bad" {
		t.Errorf("FleetLedgers = %v, want [main plain bad]", names)
	}
	rows := FleetStatus(f, time.Now())
	if len(rows) != 3 || rows[2].Error == "" || !strings.Contains(rows[2].Error, "ledger.dir") {
		t.Errorf("bad ledger.dir row = %+v, want an Error naming ledger.dir", rows)
	}
}

// TestInitLedgerDir: with ledger.dir set init creates no in-tree log and
// reports no events.jsonl piece; a bad ledger.dir fails init.
func TestInitLedgerDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeLedgerConfig(t, dir, ledgerConfigBody(t.TempDir()))
	_, pieces, err := InitSeeded(dir, false, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	noInTreeLog(t, dir)
	for _, p := range pieces {
		if p.Path == ".flywheel/events.jsonl" {
			t.Errorf("pieces include %s with ledger.dir set", p.Path)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".flywheel", ".gitignore")); err != nil || !strings.Contains(string(b), "claims/\nleases/\n") {
		t.Errorf(".gitignore = %q, %v; want claims/ and leases/", b, err)
	}
	bad := t.TempDir()
	writeLedgerConfig(t, bad, ledgerConfigBody("rel/ledger"))
	if _, err := Init(bad, false); err == nil || !strings.Contains(err.Error(), "ledger.dir") {
		t.Errorf("Init(bad ledger.dir) = %v, want a ledger.dir error", err)
	}
}
