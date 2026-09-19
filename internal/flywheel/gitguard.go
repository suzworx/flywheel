package flywheel

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// GitGuardEnv names the environment variable that holds the guard directory
// (the directory on the worker's PATH that contains the guard binary); the
// guard skips that directory when it looks for the real git.
const GitGuardEnv = "FLYWHEEL_GIT_GUARD"

// GitGuardRefused reports whether args (git's arguments, without "git") run
// a history-writing subcommand a worker may not run, and which. Global
// options before the subcommand are skipped: -C <path>, -c <k=v>,
// --git-dir=<p>, --work-tree=<p>, --namespace=<n>, -P, -p, --no-pager,
// --paginate, --bare, --no-replace-objects, --literal-pathspecs and any other
// argument starting with "-" (a "-C"/"-c" consumes the next argument too).
func GitGuardRefused(args []string) (refused bool, sub string) {
	refusedCommands := map[string]bool{
		"commit":      true,
		"push":        true,
		"stash":       true,
		"reset":       true,
		"checkout":    true,
		"switch":      true,
		"restore":     true,
		"rebase":      true,
		"merge":       true,
		"cherry-pick": true,
		"revert":      true,
		"am":          true,
		"pull":        true,
		"tag":         true,
		"update-ref":  true,
		"worktree":    true,
	}

	i := 0
	for i < len(args) {
		arg := args[i]

		if !strings.HasPrefix(arg, "-") {
			break
		}

		if arg == "-C" || arg == "-c" {
			i += 2
			continue
		}

		if arg == "--git-dir" || arg == "--work-tree" || arg == "--namespace" || arg == "--exec-path" || arg == "--config-env" {
			i += 2
			continue
		}

		if strings.HasPrefix(arg, "--git-dir=") || strings.HasPrefix(arg, "--work-tree=") ||
			strings.HasPrefix(arg, "--namespace=") {
			i++
			continue
		}

		if arg == "-P" || arg == "-p" || arg == "--no-pager" || arg == "--paginate" ||
			arg == "--bare" || arg == "--no-replace-objects" || arg == "--literal-pathspecs" {
			i++
			continue
		}

		i++
	}

	if i >= len(args) {
		return false, ""
	}

	subcommand := args[i]

	if refusedCommands[subcommand] {
		return true, subcommand
	}

	if subcommand == "branch" {
		i++
		for i < len(args) {
			arg := args[i]
			if arg == "-D" || arg == "-d" || arg == "-m" || arg == "-M" ||
				arg == "-c" || arg == "-C" || arg == "-f" || arg == "--delete" ||
				arg == "--move" || arg == "--copy" || arg == "--force" {
				return true, "branch"
			}
			if !strings.HasPrefix(arg, "-") {
				return true, "branch"
			}
			i++
		}
		return false, ""
	}

	return false, ""
}

// GitGuard runs as `git`: it refuses a history-writing subcommand (exit 1,
// a message on stderr naming the rule) and otherwise runs the real git — the
// first "git" (or "git.exe" on Windows) on PATH outside the guard directory —
// with the same arguments, stdin, stdout and stderr, returning its exit code
// (127 when no real git is found).
func GitGuard(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	refused, sub := GitGuardRefused(args)
	if refused {
		io.WriteString(stderr, "flywheel: workers never commit, stash, reset, checkout or push; \"git "+sub+"\" is refused (the lead commits after inspection)\n")
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
