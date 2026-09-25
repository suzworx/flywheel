package flywheel

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// GitGuardEnv names the environment variable that holds the guard directory
// (the directory on the worker's PATH that contains the guard binary); the
// guard skips that directory when it looks for the real git.
const GitGuardEnv = "FLYWHEEL_GIT_GUARD"

// gitReadOnly lists the git subcommands a worker may run through the guard:
// reads that change neither history, refs, the index nor the working tree
// (#325 review). Everything else is refused — an allow-list, so a new or
// aliased command cannot slip through (git never lets an alias shadow a
// builtin, so an alias name is simply not on this list).
var gitReadOnly = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "rev-parse": true,
	"ls-files": true, "ls-tree": true, "grep": true, "blame": true, "annotate": true,
	"describe": true, "shortlog": true, "cat-file": true, "rev-list": true,
	"show-ref": true, "name-rev": true, "merge-base": true, "for-each-ref": true,
	"diff-tree": true, "diff-index": true, "diff-files": true, "check-ignore": true,
	"check-attr": true, "var": true, "count-objects": true, "whatchanged": true,
	"help": true, "version": true, "range-diff": true, "cherry": true,
}

// GitGuardRefused reports whether args (git's arguments, without "git") may
// not run through the worker's guard, and the subcommand. Global options
// before the subcommand are skipped (-C <path>, -c <k=v>, --git-dir[=]<p>,
// --work-tree[=]<p>, --namespace[=]<n>, --exec-path, --config-env, -P, -p,
// --no-pager, --paginate, --bare, --no-replace-objects, --literal-pathspecs
// and any other argument starting with "-"). A subcommand is allowed only
// when it is on gitReadOnly, or is one of the read-only forms of branch, tag,
// config, remote, reflog, stash or worktree; everything else — commit, push,
// add, rm, mv, clean, checkout, update-index, an alias, … — is refused (issue
// #319, #325 review). No args (plain "git") and --version/--help are allowed.
func GitGuardRefused(args []string) (refused bool, sub string) {
	i := 0
	for i < len(args) {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			break
		}
		switch arg {
		case "--version", "--help", "-h":
			return false, arg
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env":
			i += 2
			continue
		}
		i++
	}
	if i >= len(args) {
		return false, ""
	}
	sub = args[i]
	rest := args[i+1:]
	if gitReadOnly[sub] {
		return false, sub
	}
	switch sub {
	case "branch":
		return !branchReadOnly(rest), sub
	case "tag":
		return !listOnly(rest, "-l", "--list", "-n", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at", "--sort", "--format", "--column", "--no-column", "-i", "--ignore-case"), sub
	case "config":
		return !hasAny(rest, "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "get", "list"), sub
	case "remote":
		return !(len(rest) == 0 || (len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose")) || (len(rest) >= 1 && (rest[0] == "show" || rest[0] == "get-url"))), sub
	case "reflog":
		return !(len(rest) == 0 || rest[0] == "show" || strings.HasPrefix(rest[0], "-")), sub
	case "stash":
		return !(len(rest) >= 1 && (rest[0] == "list" || rest[0] == "show")), sub
	case "worktree":
		return !(len(rest) >= 1 && rest[0] == "list"), sub
	}
	return true, sub
}

// branchReadOnly reports whether git branch's arguments only list or query
// branches: no write flag, and positional arguments only while a query mode
// (--list, --contains, --merged, --points-at, …) is active.
func branchReadOnly(args []string) bool {
	write := map[string]bool{"-d": true, "-D": true, "-m": true, "-M": true, "-c": true, "-C": true,
		"--delete": true, "--move": true, "--copy": true, "-f": true, "--force": true, "-u": true,
		"--set-upstream-to": true, "--unset-upstream": true, "--edit-description": true, "-t": true,
		"--track": true, "--no-track": true, "--create-reflog": true}
	query := map[string]bool{"-l": true, "--list": true, "--contains": true, "--no-contains": true,
		"--merged": true, "--no-merged": true, "--points-at": true}
	queryMode := false
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if write[name] || strings.HasPrefix(name, "--set-upstream-to") {
			return false
		}
		if query[name] {
			queryMode = true
			continue
		}
		if !strings.HasPrefix(a, "-") && !queryMode {
			return false // a positional outside a query mode creates a branch
		}
	}
	return true
}

// listOnly reports whether args hold no positional argument, or a listing
// flag among flags (so "tag" lists and "tag v1" creates).
func listOnly(args []string, flags ...string) bool {
	listing := hasAny(args, "-l", "--list")
	for _, a := range args {
		if !strings.HasPrefix(a, "-") && !listing {
			return false
		}
		name, _, _ := strings.Cut(a, "=")
		if strings.HasPrefix(a, "-") && !slices.Contains(flags, name) {
			return false
		}
	}
	return true
}

// hasAny reports whether args contain any of names.
func hasAny(args []string, names ...string) bool {
	for _, a := range args {
		if slices.Contains(names, a) {
			return true
		}
	}
	return false
}

// GitGuard runs as `git`: it refuses a non-read-only subcommand in the unit's repository (exit 1,
// a message on stderr naming the rule) and otherwise runs the real git — the
// first "git" (or "git.exe" on Windows) on PATH outside the guard directory —
// with the same arguments, stdin, stdout and stderr, returning its exit code
// (127 when no real git is found).
func GitGuard(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	gitPath := realGit()
	if gitPath == "" {
		return 127
	}
	env := guardFreeEnv()

	if refused, sub := GitGuardRefused(args); refused {
		if stillRefused(gitPath, env, args, sub) {
			logGitGuard("refused " + sub)
			io.WriteString(stderr, "flywheel: workers never commit, stash, reset, checkout or push; \"git "+sub+"\" is refused in this unit's repository — under flywheel run, git runs read-only commands only there (the lead commits after inspection)\n")
			return 1
		}
		logGitGuard("allowed " + sub)
	}

	cmd := exec.Command(gitPath, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

// gitGuardLogPath is the guard's write log for the bin directory binDir: a
// sibling file (binDir + ".log"), so it survives the removal of binDir (#361).
func gitGuardLogPath(binDir string) string {
	return filepath.Clean(binDir) + ".log"
}

// logGitGuard appends line to the guard's write log (GitGuardEnv names the bin
// directory). Every error is ignored: logging never changes what the guard does.
func logGitGuard(line string) {
	binDir := os.Getenv(GitGuardEnv)
	if binDir == "" {
		return
	}
	f, err := os.OpenFile(gitGuardLogPath(binDir), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

// readGitGuardLog returns the write-class subcommands the guard refused and
// allowed during an attempt, from binDir's log; a missing log reads as none.
func readGitGuardLog(binDir string) (refused, allowed []string) {
	b, err := os.ReadFile(gitGuardLogPath(binDir))
	if err != nil {
		return nil, nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		verdict, sub, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		switch verdict {
		case "refused":
			refused = append(refused, sub)
		case "allowed":
			allowed = append(allowed, sub)
		}
	}
	return refused, allowed
}

// GitRealEnv names the environment variable holding the absolute path of the
// git flywheel run resolved before it put the guard on PATH. The guard runs
// that git directly: a PATH search could find another shim that itself calls
// "git" from PATH — the guard again — and the two would chain (#325: a test
// installing its own git shim did exactly that).
const GitRealEnv = "FLYWHEEL_GIT_REAL"

// realGit returns the git to run: GitRealEnv when it names an existing file,
// else the first git on PATH outside the guard directory.
func realGit() string {
	if p := os.Getenv(GitRealEnv); p != "" {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return findRealGit()
}

// GitGuardRepoEnv names the environment variable holding the absolute git
// common directory of the unit's repository — the one the guard protects.
const GitGuardRepoEnv = "FLYWHEEL_GIT_GUARD_REPO"

// guardFreeEnv is the environment for the real git: the guard variables
// removed and the guard directory dropped from PATH (Windows names it "Path";
// env names are case-insensitive there), so git's own helpers never loop back.
func guardFreeEnv() []string {
	guardDir := filepath.Clean(os.Getenv(GitGuardEnv))
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if envNameIs(name, "PATH") || envNameIs(name, GitGuardEnv) || envNameIs(name, GitGuardRepoEnv) {
			continue
		}
		env = append(env, e)
	}
	return append(env, "PATH="+removeGuardDir(os.Getenv("PATH"), guardDir))
}

// guardedRepo reports whether a command with these global options would run
// in the unit's repository: the git common directory the options (-C, --git-dir, …) and the
// current directory resolve to equals FLYWHEEL_GIT_GUARD_REPO. Git elsewhere —
// a test's temporary repository, a directory that is no repository yet — is
// not the unit's history, so it passes (#325: a gate running go test must be
// able to git init and commit in t.TempDir()). No recorded repository means
// every refused command is refused.
func guardedRepo(gitPath string, env []string, global []string) bool {
	want := os.Getenv(GitGuardRepoEnv)
	if want == "" {
		return true
	}
	probe := append(append([]string{}, global...), "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd := exec.Command(gitPath, probe...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return false // not a repository: nothing of the unit's to protect
	}
	return isPathEqual(filepath.Clean(strings.TrimSpace(string(out))), filepath.Clean(want))
}

// globalOptions returns the global options that precede the subcommand in
// args, with the values -C, -c and the separate-value forms consume.
func globalOptions(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			break
		}
		out = append(out, a)
		switch a {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env":
			if i+1 < len(args) {
				out = append(out, args[i+1])
				i++
			}
		}
	}
	return out
}

func findRealGit() string {
	pathList := os.Getenv("PATH")
	if pathList == "" {
		return ""
	}

	guardDir := ""
	if gd := os.Getenv(GitGuardEnv); gd != "" {
		guardDir = filepath.Clean(gd)
	}

	paths := filepath.SplitList(pathList)
	for _, p := range paths {
		clean := filepath.Clean(p)
		if guardDir != "" && isPathEqual(clean, guardDir) {
			continue
		}

		// On Windows only git.exe/git.cmd can be started directly; an
		// extensionless "git" there is a shell script.
		candidates := []string{"git"}
		if runtime.GOOS == "windows" {
			candidates = []string{"git.exe", "git.cmd"}
		}

		for _, name := range candidates {
			fullPath := filepath.Join(p, name)
			if info, err := os.Stat(fullPath); err == nil && !info.IsDir() {
				return fullPath
			}
		}
	}

	return ""
}

func removeGuardDir(pathList, guardDir string) string {
	paths := filepath.SplitList(pathList)
	var result []string
	for _, p := range paths {
		if !isPathEqual(filepath.Clean(p), guardDir) {
			result = append(result, p)
		}
	}
	return strings.Join(result, string(os.PathListSeparator))
}

// envNameIs compares environment variable names the way the OS does:
// case-insensitively on Windows.
func envNameIs(name, want string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(name, want)
	}
	return name == want
}

func isPathEqual(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// installGitGuard creates <dir>/.flywheel/runs/<task>.<attempt>.bin/ holding
// the running flywheel executable (os.Executable) named git, plus git.exe on
// Windows (a hard link when possible, else a copy), and returns that
// directory and the environment entries that put it first on PATH:
// "PATH=<bin><os.PathListSeparator><current PATH>" and
// "FLYWHEEL_GIT_GUARD=<bin>". The caller removes the directory afterwards.
func installGitGuard(dir, task, attempt string) (bin string, env []string, err error) {
	// Absolute: a relative PATH entry would resolve against whatever
	// directory the worker's shell happens to be in.
	binDir, err := filepath.Abs(filepath.Join(dir, ".flywheel", "runs", task+"."+attempt+".bin"))
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", nil, err
	}

	exePath, err := os.Executable()
	if err != nil {
		os.RemoveAll(binDir)
		return "", nil, err
	}

	gitPath := filepath.Join(binDir, "git")
	if err := linkGuardBinary(exePath, gitPath, guardLinkTries, guardLinkWait, time.Sleep); err != nil {
		os.RemoveAll(binDir)
		return "", nil, err
	}

	if runtime.GOOS == "windows" {
		gitExePath := filepath.Join(binDir, "git.exe")
		if err := linkGuardBinary(exePath, gitExePath, guardLinkTries, guardLinkWait, time.Sleep); err != nil {
			os.RemoveAll(binDir)
			return "", nil, err
		}
	}

	pathEnv := "PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	guardEnv := GitGuardEnv + "=" + binDir
	env = []string{pathEnv, guardEnv}
	// The real git, resolved now — before the guard is on PATH.
	if real, err := exec.LookPath("git"); err == nil {
		if abs, err := filepath.Abs(real); err == nil {
			env = append(env, GitRealEnv+"="+abs)
		}
	}
	// The repository the guard protects: dir's git common directory (shared
	// by all its linked worktrees). Not a repository: nothing to protect.
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir").Output(); err == nil {
		env = append(env, GitGuardRepoEnv+"="+filepath.Clean(strings.TrimSpace(string(out))))
	}

	return binDir, env, nil
}

func linkOrCopy(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err = io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	// A copy (unlike a hard link) does not carry the executable bit.
	return os.Chmod(dst, 0o755)
}

// guardLinkTries and guardLinkWait bound how long installGitGuard waits for
// a flywheel executable that is missing mid-rename (a `flywheel upgrade`
// swapping the binary, issue #461).
const (
	guardLinkTries = 5
	guardLinkWait  = 200 * time.Millisecond
)

// linkGuardBinary links or copies src to dst, retrying up to tries times,
// wait apart (via sleep), while src does not exist; any other failure, or a
// src still missing after the last try, is returned naming src.
func linkGuardBinary(src, dst string, tries int, wait time.Duration, sleep func(time.Duration)) error {
	var err error
	for i := 0; i < tries; i++ {
		if i > 0 {
			sleep(wait)
		}
		if err = linkOrCopy(src, dst); err == nil {
			return nil
		}
		if _, serr := os.Stat(src); !errors.Is(serr, fs.ErrNotExist) {
			break
		}
	}
	return fmt.Errorf("guard binary %s: %w", src, err)
}

// objectOnly lists subcommands that only add objects to the object store —
// never a ref, the shared index or the working tree — so they are harmless
// in any repository (flywheel's own tree hashing uses write-tree).
var objectOnly = map[string]bool{"write-tree": true, "hash-object": true, "mktree": true}

// tempIndexWrites lists index writers that are harmless when GIT_INDEX_FILE
// names a temporary index outside the repository: flywheel's tree hashing
// (add -A, read-tree, update-index into a throwaway index) and any tool that
// follows the same idiom touch only objects and that file.
var tempIndexWrites = map[string]bool{"add": true, "read-tree": true, "update-index": true}

// stillRefused decides a command the allow-list refused (#325): object-only
// commands and temp-index writes pass; a global or system config write is
// refused anywhere; otherwise the command is refused only in the unit's
// repository (git init <dir> is judged by <dir>).
func stillRefused(gitPath string, env []string, args []string, sub string) bool {
	if objectOnly[sub] {
		return false
	}
	rest := afterSubcommand(args)
	if tempIndexWrites[sub] && tempIndexOutsideRepo(gitPath, env, args) {
		return false
	}
	if sub == "config" && hasAny(rest, "--global", "--system") {
		return true
	}
	probe := globalOptions(args)
	if sub == "init" || sub == "clone" {
		if p := lastPositional(rest); p != "" {
			probe = append(probe, "-C", p)
		}
	}
	return guardedRepo(gitPath, env, probe)
}

// tempIndexOutsideRepo reports whether GIT_INDEX_FILE is set to a file
// outside the repository's git directory — a throwaway index, not the one the
// lead stages commits in.
func tempIndexOutsideRepo(gitPath string, env []string, args []string) bool {
	idx := os.Getenv("GIT_INDEX_FILE")
	if idx == "" {
		return false
	}
	abs, err := filepath.Abs(idx)
	if err != nil {
		return false
	}
	cmd := exec.Command(gitPath, append(globalOptions(args), "rev-parse", "--path-format=absolute", "--git-dir")...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return true // not a repository: nothing to protect
	}
	gitDir := filepath.Clean(strings.TrimSpace(string(out)))
	rel, err := filepath.Rel(gitDir, abs)
	return err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// afterSubcommand returns the arguments after the subcommand.
func afterSubcommand(args []string) []string {
	n := len(globalOptions(args))
	if n >= len(args) {
		return nil
	}
	return args[n+1:]
}

// lastPositional returns the last argument that is not an option, or "".
func lastPositional(args []string) string {
	for i := len(args) - 1; i >= 0; i-- {
		if !strings.HasPrefix(args[i], "-") {
			return args[i]
		}
	}
	return ""
}
