package flywheel

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"testing"
	"time"
)

// fakeFile is a regular-file os.FileInfo for injected stat lookups.
type fakeFile struct{}

func (fakeFile) Name() string       { return "bash.exe" }
func (fakeFile) Size() int64        { return 1 }
func (fakeFile) Mode() fs.FileMode  { return 0o755 }
func (fakeFile) ModTime() time.Time { return time.Time{} }
func (fakeFile) IsDir() bool        { return false }
func (fakeFile) Sys() any           { return nil }

// fakeShellHost resolves names from path and stats only the files in present.
func fakeShellHost(goos string, path map[string]string, present ...string) shellHost {
	have := map[string]bool{}
	for _, p := range present {
		have[p] = true
	}
	return shellHost{
		goos: goos,
		lookPath: func(name string) (string, error) {
			if p, ok := path[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		stat: func(p string) (os.FileInfo, error) {
			if have[p] {
				return fakeFile{}, nil
			}
			return nil, fs.ErrNotExist
		},
		systemRoot: `C:\Windows`,
	}
}

func TestShellArgv(t *testing.T) {
	t.Parallel()
	const gitBash = `C:\Program Files\Git\bin\bash.exe`
	cases := []struct {
		name string
		host shellHost
		want []string
	}{
		{"windows git bash beats WSL", fakeShellHost("windows",
			map[string]string{"git": `C:\Program Files\Git\cmd\git.exe`, "bash": `C:\Windows\System32\bash.exe`}, gitBash),
			[]string{gitBash, "-c", "x"}},
		{"windows git in mingw64 usr bash", fakeShellHost("windows",
			map[string]string{"git": `C:\Git\mingw64\bin\git.exe`}, `C:\Git\usr\bin\bash.exe`),
			[]string{`C:\Git\usr\bin\bash.exe`, "-c", "x"}},
		{"windows only WSL no git", fakeShellHost("windows",
			map[string]string{"bash": `C:\Windows\System32\bash.exe`}),
			[]string{"cmd", "/C", "x"}},
		{"windows non-WSL bash no git", fakeShellHost("windows",
			map[string]string{"bash": `C:\msys64\usr\bin\bash.exe`}),
			[]string{`C:\msys64\usr\bin\bash.exe`, "-c", "x"}},
		{"linux bash", fakeShellHost("linux", map[string]string{"bash": "/usr/bin/bash"}),
			[]string{"/usr/bin/bash", "-c", "x"}},
		{"linux no bash", fakeShellHost("linux", nil), []string{"sh", "-c", "x"}},
	}
	for _, c := range cases {
		if got := c.host.argv("x"); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: argv = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestShellIsWSLBash(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		`C:\Windows\System32\bash.exe`:                             true,
		`c:/windows/system32/BASH.EXE`:                             true,
		`C:\Windows\SysWOW64\bash.exe`:                             true,
		`C:\Users\me\AppData\Local\Microsoft\WindowsApps\bash.exe`: true,
		`C:\Program Files\Git\bin\bash.exe`:                        false,
		`C:\Program Files\Git\usr\bin\bash.exe`:                    false,
		`D:\tools\bash.exe`:                                        false,
	}
	for p, want := range cases {
		if got := isWSLBash(p, `C:\Windows`); got != want {
			t.Errorf("isWSLBash(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestShellWSLOnPath(t *testing.T) {
	t.Parallel()
	h := fakeShellHost("windows", map[string]string{"bash": `C:\Windows\System32\bash.exe`})
	if wsl, chosen, ok := h.wslOnPath(); !ok || wsl != `C:\Windows\System32\bash.exe` || chosen != "cmd /C" {
		t.Errorf("wslOnPath = %q, %q, %v", wsl, chosen, ok)
	}
	if _, _, ok := fakeShellHost("linux", map[string]string{"bash": "/bin/bash"}).wslOnPath(); ok {
		t.Errorf("wslOnPath on linux = true")
	}
}
