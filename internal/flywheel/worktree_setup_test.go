package flywheel

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestNeedsStateLink checks linkNeedsState (issue #430): a target directory
// in root is reachable through the link in the worktree, a second call is a
// no-op, and a missing target is an error naming it.
func TestNeedsStateLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	wt := t.TempDir()
	target := filepath.Join(root, "apps", "web", "node_modules")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "dep.txt"), []byte("dep"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := []string{"apps/web/node_modules/"}
	if err := linkNeedsState(root, wt, paths); err != nil {
		t.Fatalf("linkNeedsState() error = %v", err)
	}
	through := filepath.Join(wt, "apps", "web", "node_modules", "dep.txt")
	b, err := os.ReadFile(through)
	if err != nil || string(b) != "dep" {
		t.Fatalf("read through link = %q, %v, want dep", b, err)
	}
	if err := linkNeedsState(root, wt, paths); err != nil {
		t.Fatalf("second linkNeedsState() error = %v, want a no-op", err)
	}
	if b, err := os.ReadFile(through); err != nil || string(b) != "dep" {
		t.Fatalf("after second call, read through link = %q, %v, want dep", b, err)
	}
	err = linkNeedsState(root, wt, []string{"ghost/"})
	if err == nil || !strings.Contains(err.Error(), "ghost/") {
		t.Fatalf("linkNeedsState(missing) error = %v, want one naming ghost/", err)
	}
}

// TestRunWorktreeSetupEnvAndTail checks runWorktreeSetup runs in the
// worktree with the FLYWHEEL_* variables and returns the exit code and tail.
func TestRunWorktreeSetupEnvAndTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wt := t.TempDir()
	rc, tail, _, err := runWorktreeSetup(dir, wt, "t1", `echo "task=$FLYWHEEL_TASK" && echo ok > marker && exit 3`, time.Minute)
	if err != nil {
		t.Fatalf("runWorktreeSetup() error = %v", err)
	}
	if rc != 3 {
		t.Errorf("rc = %d, want 3", rc)
	}
	if !strings.Contains(tail, "task=t1") {
		t.Errorf("tail = %q, want it to carry task=t1", tail)
	}
	if _, err := os.Stat(filepath.Join(wt, "marker")); err != nil {
		t.Errorf("marker not written in the worktree: %v", err)
	}
}

// TestWorktreeSetupResolveCommand checks resolveSetupCommand (issue #471): a
// relative script missing from the worktree but present in the root resolves
// against the root; every other command is unchanged.
func TestWorktreeSetupResolveCommand(t *testing.T) {
	t.Parallel()
	wt, root, spaced := filepath.FromSlash("/w"), filepath.FromSlash("/r"), filepath.FromSlash("/my root")
	have := map[string]bool{
		filepath.Join(root, ".flywheel", "briefs", "setup.mjs"): true,
		filepath.Join(root, "setup.sh"):                         true,
		filepath.Join(root, "both.sh"):                          true,
		filepath.Join(wt, "both.sh"):                            true,
		filepath.Join(spaced, "setup.sh"):                       true,
	}
	exists := func(p string) bool { return have[p] }
	abs := func(r, rel string) string { return filepath.ToSlash(filepath.Join(r, filepath.FromSlash(rel))) }
	cases := []struct{ root, in, want string }{
		{root, "node .flywheel/briefs/setup.mjs", "node " + abs(root, ".flywheel/briefs/setup.mjs")},
		{root, "./setup.sh --x", abs(root, "setup.sh") + " --x"},
		{root, "bash setup.sh && npm ci", "bash " + abs(root, "setup.sh") + " && npm ci"},
		{spaced, "sh setup.sh", `sh "` + abs(spaced, "setup.sh") + `"`},
		{root, "sh both.sh", "sh both.sh"},
		{root, "node /abs/setup.mjs", "node /abs/setup.mjs"},
		{root, "npm ci", "npm ci"},
		{root, "node --version", "node --version"},
		{root, "node missing.mjs", "node missing.mjs"},
		{root, `node "setup.sh"`, `node "setup.sh"`},
		{root, "./setup.sh|tee log", "./setup.sh|tee log"},
		{root, "", ""},
	}
	for _, c := range cases {
		if got := resolveSetupCommand(c.in, wt, c.root, exists); got != c.want {
			t.Errorf("resolveSetupCommand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWorktreeSetupRunsRootScript checks runWorktreeSetup runs a script that
// exists only in the root, with cwd still the worktree.
func TestWorktreeSetupRunsRootScript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "setup.sh"), []byte("echo from-root > marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, tail, _, err := runWorktreeSetup(dir, wt, "t1", "sh setup.sh", time.Minute)
	if err != nil || rc != 0 {
		t.Fatalf("runWorktreeSetup() = %d, %v; tail %q", rc, err, tail)
	}
	if _, err := os.Stat(filepath.Join(wt, "marker")); err != nil {
		t.Errorf("marker not written in the worktree: %v", err)
	}
}

// testLink links link to target: a symlink, or on Windows without the
// symlink privilege a junction, the way linkNeedsState does; it skips the
// test when neither works.
func testLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	} else if runtime.GOOS != "windows" {
		t.Skipf("symlink %s: %v", link, err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("neither a symlink nor a junction for %s: %v: %s", link, err, out)
	}
}

// escapeFixture builds a workspace root whose node_modules holds a scoped
// link into packages/ (escapes), a real directory, a link into the .pnpm
// store, a broken link and a link into the worktree wt under root.
func escapeFixture(t *testing.T) (root, wt string) {
	t.Helper()
	root = t.TempDir()
	wt = filepath.Join(root, ".flywheel", "worktrees", "T1")
	nm := filepath.Join(root, "node_modules")
	for _, d := range []string{
		filepath.Join(root, "packages", "web"), filepath.Join(wt, "packages", "own"),
		filepath.Join(nm, "@acme"), filepath.Join(nm, "left-pad"), filepath.Join(nm, ".pnpm", "x"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testLink(t, filepath.Join(root, "packages", "web"), filepath.Join(nm, "@acme", "web"))
	testLink(t, filepath.Join(wt, "packages", "own"), filepath.Join(nm, "@acme", "own"))
	testLink(t, filepath.Join(nm, ".pnpm", "x"), filepath.Join(nm, "y"))
	testLink(t, filepath.Join(root, "nowhere"), filepath.Join(nm, "broken"))
	return root, wt
}

// TestEscapingLinks checks escapingLinks (issue #460): only the workspace
// link into packages/ is reported; the .pnpm store link, the real directory,
// the broken link and the link into the worktree are not.
func TestEscapingLinks(t *testing.T) {
	t.Parallel()
	root, wt := escapeFixture(t)
	got, err := escapingLinks(root, wt, "node_modules/")
	if err != nil {
		t.Fatalf("escapingLinks() error = %v", err)
	}
	if want := []string{"node_modules/@acme/web"}; !slices.Equal(got, want) {
		t.Errorf("escapingLinks() = %q, want %q", got, want)
	}
	if _, err := escapingLinks(root, wt, "ghost/"); err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("escapingLinks(missing) error = %v, want one naming ghost", err)
	}
}

// TestWorktreeSetupEscape checks prepareWorktree (issue #460) records the
// escaping links on the worktree_setup event and warns, and that with
// worktree.strict_links it refuses with rule "setup" and still records it.
func TestWorktreeSetupEscape(t *testing.T) {
	t.Parallel()
	for _, strict := range []bool{false, true} {
		root, wt := escapeFixture(t)
		cfg := Config{Worktree: &WorktreeConfig{StrictLinks: strict}}
		warnings, err := prepareWorktree(root, wt, "T1", "r1", cfg, []string{"node_modules/"}, nil, nil)
		var rr *RuleRefusal
		if strict && (!errors.As(err, &rr) || rr.Rule != "setup" || !strings.Contains(rr.Fix, "node_modules/@acme/web")) {
			t.Errorf("strict: prepareWorktree() error = %v, want a setup RuleRefusal naming node_modules/@acme/web", err)
		}
		if !strict && err != nil {
			t.Errorf("prepareWorktree() error = %v, want nil", err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "warning: needs-state link node_modules/ holds links into the main checkout (1: node_modules/@acme/web)") {
			t.Errorf("strict=%v: warnings = %q", strict, warnings)
		}
		evs, err := ReadEvents(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].Kind != "worktree_setup" || !slices.Equal(evs[0].Escaped, []string{"node_modules/@acme/web"}) {
			t.Fatalf("strict=%v: events = %+v, want one worktree_setup with Escaped", strict, evs)
		}
		if strict != strings.Contains(evs[0].Note, "refused") {
			t.Errorf("strict=%v: note = %q, want refused only when strict", strict, evs[0].Note)
		}
	}
}

// copyFixture is a root and a worktree, both git repositories whose
// .gitignore lists .env; wt has tracked.txt committed.
func copyFixture(t *testing.T) (root, wt string) {
	t.Helper()
	root, wt = t.TempDir(), t.TempDir()
	for _, d := range []string{root, wt} {
		initGitRepoAt(t, d)
		if err := os.WriteFile(filepath.Join(d, ".gitignore"), []byte(".env\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, wt, []string{"add", "tracked.txt", ".gitignore"})
	git(t, wt, []string{"commit", "-q", "-m", "init"})
	for name, body := range map[string]string{".env": "KEY=one\n", "tracked.txt": "root copy\n", "notes.txt": "n\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, wt
}

// TestPrepareWorktreeCopies checks prepareWorktree's "(copy)" paths (issue
// #471): copied with their content and overwritten on the next call, named on
// the event's Copied; a missing source or a path tracked in the worktree is a
// setup RuleRefusal with a noted event; a path git does not ignore is copied
// with a warning.
func TestPrepareWorktreeCopies(t *testing.T) {
	t.Parallel()
	root, wt := copyFixture(t)
	for _, want := range []string{"KEY=one\n", "KEY=two\n"} {
		if err := os.WriteFile(filepath.Join(root, ".env"), []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		warnings, err := prepareWorktree(root, wt, "T1", "r1", Config{}, nil, []string{".env"}, nil)
		if err != nil || len(warnings) != 0 {
			t.Fatalf("prepareWorktree() = %q, %v, want no warnings and nil", warnings, err)
		}
		if got, err := os.ReadFile(filepath.Join(wt, ".env")); err != nil || string(got) != want {
			t.Errorf("wt .env = %q, %v, want %q", got, err, want)
		}
	}
	evs, err := ReadEvents(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[1].Kind != "worktree_setup" || !slices.Equal(evs[1].Copied, []string{".env"}) {
		t.Fatalf("events = %+v, want two worktree_setup with Copied [.env]", evs)
	}

	for _, tc := range []struct{ path, want string }{
		{"ghost.env", "not found under"},
		{"tracked.txt", "tracked by git"},
	} {
		root, wt := copyFixture(t)
		_, err := prepareWorktree(root, wt, "T1", "r1", Config{}, nil, []string{tc.path}, nil)
		var rr *RuleRefusal
		if !errors.As(err, &rr) || rr.Rule != "setup" || !strings.Contains(rr.Fix, tc.path) || !strings.Contains(rr.Fix, tc.want) {
			t.Errorf("prepareWorktree(%s) error = %v, want a setup RuleRefusal naming it and %q", tc.path, err, tc.want)
		}
		evs, err := ReadEvents(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 || evs[0].Note == "" || len(evs[0].Copied) != 0 {
			t.Errorf("%s: events = %+v, want one worktree_setup with a Note and no Copied", tc.path, evs)
		}
		if tc.path == "tracked.txt" {
			if got, _ := os.ReadFile(filepath.Join(wt, "tracked.txt")); string(got) != "committed\n" {
				t.Errorf("tracked.txt overwritten: %q", got)
			}
		}
	}

	root, wt = copyFixture(t)
	warnings, err := prepareWorktree(root, wt, "T1", "r1", Config{}, nil, []string{"notes.txt"}, nil)
	if err != nil || len(warnings) != 1 || warnings[0] != "warning: needs-state copy notes.txt is not git-ignored in the worktree; add it to .gitignore so it is never committed" {
		t.Errorf("prepareWorktree(notes.txt) = %q, %v, want the not-ignored warning", warnings, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "notes.txt")); err != nil {
		t.Errorf("notes.txt not copied: %v", err)
	}
}

// TestInstallCommand checks installCommand's lockfile detection (issue #460):
// each lockfile, Yarn Berry vs Classic, precedence, and no lockfile.
func TestInstallCommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		files         []string
		manager, want string
	}{
		{[]string{"pnpm-lock.yaml"}, "pnpm", "pnpm install --offline --frozen-lockfile"},
		{[]string{"bun.lock"}, "bun", "bun install --frozen-lockfile"},
		{[]string{"bun.lockb"}, "bun", "bun install --frozen-lockfile"},
		{[]string{"yarn.lock", ".yarnrc.yml"}, "yarn", "yarn install --immutable"},
		{[]string{"yarn.lock"}, "yarn", "yarn install --frozen-lockfile --offline"},
		{[]string{"package-lock.json"}, "npm", "npm ci --prefer-offline --no-audit"},
		{[]string{"npm-shrinkwrap.json"}, "npm", "npm ci --prefer-offline --no-audit"},
		{[]string{"package-lock.json", "pnpm-lock.yaml"}, "pnpm", "pnpm install --offline --frozen-lockfile"},
	} {
		exists := func(p string) bool { return slices.Contains(tc.files, filepath.Base(p)) && filepath.Dir(p) == "wt" }
		m, c, err := installCommand("wt", exists)
		if err != nil || m != tc.manager || c != tc.want {
			t.Errorf("installCommand(%v) = %q, %q, %v, want %q, %q", tc.files, m, c, err, tc.manager, tc.want)
		}
	}
	if _, _, err := installCommand("wt", func(string) bool { return false }); err == nil || !strings.Contains(err.Error(), "pnpm-lock.yaml") || !strings.Contains(err.Error(), "package-lock.json") {
		t.Errorf("installCommand(none) error = %v, want one naming the lockfiles looked for", err)
	}
}

// TestPrepareWorktreeInstall checks prepareWorktree's "(install)" paths (issue
// #460) with a fake installRunner, so it is not parallel: one run for two
// paths and a marker, a skip when the marker and dirs match, a rerun on a
// changed lockfile, and setup refusals for a failed install, no lockfile and
// a path both linked and installed.
func TestPrepareWorktreeInstall(t *testing.T) {
	calls, rc := 0, 0
	paths := []string{"node_modules/", "packages/web/node_modules/"}
	old := installRunner
	defer func() { installRunner = old }()
	installRunner = func(dir, wt, task, command string, timeout time.Duration) (int, string, time.Duration, error) {
		calls++
		for _, p := range paths {
			if err := os.MkdirAll(filepath.Join(wt, filepath.FromSlash(p)), 0o755); err != nil {
				return -1, "", 0, err
			}
		}
		return rc, "fake tail", 0, nil
	}
	root, wt := t.TempDir(), t.TempDir()
	lock := filepath.Join(wt, "pnpm-lock.yaml")
	marker := filepath.Join(wt, ".flywheel", "install.sha256")
	if err := os.WriteFile(lock, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i, want := range []struct {
		calls   int
		install string
	}{{1, "pnpm install --offline --frozen-lockfile"}, {1, "up to date (pnpm-lock.yaml)"}, {2, "pnpm install --offline --frozen-lockfile"}} {
		if i == 2 {
			if err := os.WriteFile(lock, []byte("v2\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := prepareWorktree(root, wt, "T1", "r1", Config{}, nil, nil, paths); err != nil {
			t.Fatalf("dispatch %d: prepareWorktree() error = %v", i+1, err)
		}
		evs, err := ReadEvents(root)
		if err != nil {
			t.Fatal(err)
		}
		ev := evs[len(evs)-1]
		if calls != want.calls || ev.Install != want.install || !slices.Equal(ev.Installed, paths) {
			t.Errorf("dispatch %d: calls = %d, event Install %q Installed %v, want %d, %q, %v", i+1, calls, ev.Install, ev.Installed, want.calls, want.install, paths)
		}
		if b, err := os.ReadFile(marker); err != nil || !strings.HasPrefix(string(b), "pnpm-lock.yaml ") {
			t.Errorf("dispatch %d: marker = %q, %v", i+1, b, err)
		}
	}

	var wt2 string
	refused := func(name string, links, installs []string, want string) {
		t.Helper()
		root := t.TempDir()
		_, err := prepareWorktree(root, wt2, "T1", "r1", Config{}, links, nil, installs)
		var rr *RuleRefusal
		if !errors.As(err, &rr) || rr.Rule != "setup" || !strings.Contains(rr.Fix, want) {
			t.Errorf("%s: error = %v, want a setup RuleRefusal naming %q", name, err, want)
		}
		if evs, err := ReadEvents(root); err != nil || len(evs) != 1 || evs[0].Note == "" {
			t.Errorf("%s: events = %+v, %v, want one worktree_setup with a Note", name, evs, err)
		}
	}
	rc = 1
	wt2 = t.TempDir()
	if err := os.WriteFile(filepath.Join(wt2, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refused("exit 1", nil, paths, "npm ci --prefer-offline --no-audit")
	if _, err := os.Stat(filepath.Join(wt2, ".flywheel", "install.sha256")); err == nil {
		t.Error("exit 1: marker written")
	}
	wt2 = t.TempDir()
	refused("no lockfile", nil, paths, "package-lock.json")
	refused("link and install", []string{"node_modules"}, paths, "both (link) and (install)")
}

// TestOutputTailKeepsLastLines checks the tail keeps the last n lines.
func TestOutputTailKeepsLastLines(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("line\r\n")
	}
	sb.WriteString("last\r\n")
	got := outputTail(sb.String(), 20)
	lines := strings.Split(got, "\n")
	if len(lines) != 20 || lines[19] != "last" {
		t.Errorf("outputTail() = %d lines ending %q, want 20 ending last", len(lines), lines[len(lines)-1])
	}
}
