package flywheel

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// shellHost is the lookups ShellArgv resolves a shell with, injectable so
// tests cover the Windows logic on every OS (issue #471).
type shellHost struct {
	goos       string
	lookPath   func(string) (string, error)
	stat       func(string) (os.FileInfo, error)
	systemRoot string // %SystemRoot%, for isWSLBash
}

// realShellHost is this process's host.
func realShellHost() shellHost {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return shellHost{goos: runtime.GOOS, lookPath: exec.LookPath, stat: os.Stat, systemRoot: root}
}

// ShellArgv is the argv gates, worktree.setup and run --notify run command
// with: on Windows Git for Windows' bash, else a bash on PATH that is not the
// WSL launcher, else cmd /C; elsewhere bash on PATH, else sh -c.
func ShellArgv(command string) []string {
	return realShellHost().argv(command)
}

// argv is ShellArgv for host h.
func (h shellHost) argv(command string) []string {
	if h.goos == "windows" {
		if bash := h.gitBash(); bash != "" {
			return []string{bash, "-c", command}
		}
		if bash, err := h.lookPath("bash"); err == nil && !isWSLBash(bash, h.systemRoot) {
			return []string{bash, "-c", command}
		}
		return []string{"cmd", "/C", command}
	}
	if bash, err := h.lookPath("bash"); err == nil {
		return []string{bash, "-c", command}
	}
	return []string{"sh", "-c", command}
}

// gitBash returns Git for Windows' bash.exe next to the git.exe on PATH
// (git.exe lives in <Git>\cmd, <Git>\bin or <Git>\mingw64\bin), or "".
func (h shellHost) gitBash() string {
	git, err := h.lookPath("git")
	if err != nil || git == "" {
		return ""
	}
	dir := winDir(git)
	lower := strings.ToLower(dir)
	root := dir
	switch {
	case strings.HasSuffix(lower, `\mingw64\bin`):
		root = winDir(winDir(dir))
	case strings.HasSuffix(lower, `\cmd`), strings.HasSuffix(lower, `\bin`):
		root = winDir(dir)
	}
	for _, c := range []string{root + `\bin\bash.exe`, root + `\usr\bin\bash.exe`} {
		if fi, err := h.stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// wslOnPath returns the WSL launcher when it is PATH's first bash on a
// Windows host, with the shell ShellArgv picks instead; ok is false otherwise.
func (h shellHost) wslOnPath() (wsl, chosen string, ok bool) {
	if h.goos != "windows" {
		return "", "", false
	}
	bash, err := h.lookPath("bash")
	if err != nil || !isWSLBash(bash, h.systemRoot) {
		return "", "", false
	}
	argv := h.argv("")
	chosen = argv[0]
	if chosen == "cmd" {
		chosen = "cmd /C"
	}
	return bash, chosen, true
}

// isWSLBash reports whether the Windows path p is the WSL launcher:
// %SystemRoot%\System32\bash.exe, or any bash in System32, SysWOW64 or a
// WindowsApps directory. Matching is on the cleaned, case-insensitive path.
func isWSLBash(p, systemRoot string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.ReplaceAll(s, "/", `\`))
		for strings.Contains(s, `\\`) {
			s = strings.ReplaceAll(s, `\\`, `\`)
		}
		return strings.TrimSuffix(s, `\`)
	}
	lp := norm(p)
	if systemRoot != "" && lp == norm(systemRoot)+`\system32\bash.exe` {
		return true
	}
	dir := winDir(lp)
	return strings.HasSuffix(dir, `\system32`) || strings.HasSuffix(dir, `\syswow64`) || strings.HasSuffix(dir, `\windowsapps`)
}

// winDir is the directory part of a Windows (or slash) path p.
func winDir(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[:i]
	}
	return ""
}
