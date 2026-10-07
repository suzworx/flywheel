package flywheel

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
)

// gateEnvEssentials are the variables a gate inherits under gates.env_allow
// whatever the allow-list says (issue #809): what a shell and the OS need to
// find programs, home and temp directories, and the locale.
var gateEnvEssentials = []string{
	"PATH", "PATHEXT", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
	"TEMP", "TMP", "TMPDIR", "SystemRoot", "SystemDrive", "windir", "ComSpec",
	"LOCALAPPDATA", "APPDATA", "ProgramData", "ProgramFiles", "ProgramFiles(x86)",
	"USER", "USERNAME", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TERM",
}

// gateEnv filters environ to what a gate inherits (issue #809): nil allow
// returns environ unchanged; otherwise it keeps, in order, the variables
// whose name matches an allow entry (an exact name, or a prefix ending in
// "*") or a gateEnvEssentials name, and Windows' "=C:=C:\..." entries.
func gateEnv(environ []string, allow []string) []string {
	return gateEnvFor(environ, allow, runtime.GOOS == "windows")
}

// gateEnvFor is gateEnv with name matching case-insensitive when fold is set,
// as Windows env names are.
func gateEnvFor(environ []string, allow []string, fold bool) []string {
	if allow == nil {
		return environ
	}
	eq := func(a, b string) bool {
		if fold {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	match := func(name string) bool {
		for _, e := range gateEnvEssentials {
			if eq(name, e) {
				return true
			}
		}
		for _, a := range allow {
			if p, ok := strings.CutSuffix(a, "*"); ok {
				if len(name) >= len(p) && eq(name[:len(p)], p) {
					return true
				}
			} else if eq(name, a) {
				return true
			}
		}
		return false
	}
	out := []string{}
	for _, kv := range environ {
		if strings.HasPrefix(kv, "=") {
			out = append(out, kv)
			continue
		}
		name, _, _ := strings.Cut(kv, "=")
		if match(name) {
			out = append(out, kv)
		}
	}
	return out
}

// gateAllowFor is the allow-list a brief's gates run under: nil allow stays
// nil (inherit everything); otherwise a new slice of allow plus the brief's
// `needs-env:` names (issue #534) not already in it, so a variable the brief
// declares is never stripped by gates.env_allow.
func gateAllowFor(allow, needsEnv []string) []string {
	if allow == nil {
		return nil
	}
	out := slices.Clone(allow)
	for _, n := range needsEnv {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// gateEnvAllowFor is gateAllowFor over the flywheel dir's gates.env_allow.
func gateEnvAllowFor(dir string, needsEnv []string) ([]string, error) {
	allow, err := gateEnvAllow(dir)
	if err != nil {
		return nil, err
	}
	return gateAllowFor(allow, needsEnv), nil
}

// gateEnvAllow is the flywheel dir's gates.env_allow, for a gate runner that
// has no config at hand.
func gateEnvAllow(dir string) ([]string, error) {
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		return nil, fmt.Errorf("gates.env_allow from %s/.flywheel/%s: %w", dir, configFileName, err)
	}
	return cfg.GateEnvAllow(), nil
}
