package flywheel

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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

// GitGuard runs as `git`: it refuses a history-writing subcommand (exit 1,
// a message on stderr naming the rule) and otherwise runs the real git — the
// first "git" (or "git.exe" on Windows) on PATH outside the guard directory —
// with the same arguments, stdin, stdout and stderr, returning its exit code
// (127 when no real git is found).
func GitGuard(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	refused, sub := GitGuardRefused(args)
	if refused {
		io.WriteString(stderr, "flywheel: workers never commit, stash, reset, checkout or push; \"git "+sub+"\" is refused — under flywheel run, git runs read-only commands only (the lead commits after inspection)\n")
		return 1
	}

	gitPath := findRealGit()
	if gitPath == "" {
		return 127
	}

	cmd := exec.Command(gitPath, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	guardDir := os.Getenv(GitGuardEnv)
	if guardDir != "" {
		guardDirClean := filepath.Clean(guardDir)
		pathList := os.Getenv("PATH")
		newPath := removeGuardDir(pathList, guardDirClean)
		cmd.Env = os.Environ()
		// Drop the guard variable and every PATH entry (Windows names it
		// "Path"; env names are case-insensitive there), then set the
		// guard-free PATH once.
		env := make([]string, 0, len(cmd.Env)+1)
		for _, e := range cmd.Env {
			name, _, _ := strings.Cut(e, "=")
			if envNameIs(name, "PATH") || envNameIs(name, GitGuardEnv) {
				continue
			}
			env = append(env, e)
		}
		cmd.Env = append(env, "PATH="+newPath)
	}

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
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
	if err := linkOrCopy(exePath, gitPath); err != nil {
		os.RemoveAll(binDir)
		return "", nil, err
	}

	if runtime.GOOS == "windows" {
		gitExePath := filepath.Join(binDir, "git.exe")
		if err := linkOrCopy(exePath, gitExePath); err != nil {
			os.RemoveAll(binDir)
			return "", nil, err
		}
	}

	pathEnv := "PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	guardEnv := GitGuardEnv + "=" + binDir

	return binDir, []string{pathEnv, guardEnv}, nil
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
