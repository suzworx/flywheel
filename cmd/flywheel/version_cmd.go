package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func init() {
	register("version", "print the flywheel version", runVersion)
	registerHelp("version", "flywheel version", nil)
}

func runVersion(args []string) {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "flywheel version: %v\n", err)
		usage(os.Stderr)
		os.Exit(2)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "flywheel version: unexpected argument %q\n", fs.Arg(0))
		usage(os.Stderr)
		os.Exit(2)
	}
	fmt.Println("flywheel " + version)
	warnStaleSkills(os.Stderr, ".", version)
}

// semverRe matches a release version: exactly X.Y.Z. A dev build's version
// ("dev") never matches, so it is skipped rather than compared.
var semverRe = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// parseSemver splits a validated X.Y.Z string into its integer components.
// ok is false when s does not match semverRe.
func parseSemver(s string) (maj, min, patch int, ok bool) {
	if !semverRe.MatchString(s) {
		return 0, 0, 0, false
	}
	parts := strings.SplitN(s, ".", 3)
	maj, _ = strconv.Atoi(parts[0])
	min, _ = strconv.Atoi(parts[1])
	patch, _ = strconv.Atoi(parts[2])
	return maj, min, patch, true
}

// olderVersion reports whether skill version sv is numerically older than
// binary version bv. Both must parse as X.Y.Z; if either does not, it
// reports false (nothing to warn about) rather than guessing.
func olderVersion(sv, bv string) bool {
	sMaj, sMin, sPatch, ok := parseSemver(sv)
	if !ok {
		return false
	}
	bMaj, bMin, bPatch, ok := parseSemver(bv)
	if !ok {
		return false
	}
	if sMaj != bMaj {
		return sMaj < bMaj
	}
	if sMin != bMin {
		return sMin < bMin
	}
	return sPatch < bPatch
}

// parseFrontmatterLine parses one "key: value" frontmatter line (arbitrary
// leading indentation). ok is false when the line has no colon. A trailing
// "# comment" on the value — such as release-please's generic-updater
// annotation "0.3.0 # x-release-please-version" — is stripped before
// returning, so downstream semver matching sees only the bare value.
func parseFrontmatterLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	value = strings.TrimSpace(line[i+1:])
	if j := strings.Index(value, "#"); j >= 0 {
		value = strings.TrimSpace(value[:j])
	}
	return key, value, true
}

// skillNameVersion scans up to the first 10 lines of a SKILL.md's
// frontmatter for "name:" and "version:" keys (metadata's version is
// indented, so this matches key: value regardless of indentation) and
// returns the first value found for each. ok is false unless both a name
// and a version that parses as X.Y.Z were found.
func skillNameVersion(lines []string) (name, version string, ok bool) {
	for i, line := range lines {
		if i >= 10 {
			break
		}
		key, value, lineOK := parseFrontmatterLine(line)
		if !lineOK {
			continue
		}
		switch key {
		case "name":
			if name == "" {
				name = value
			}
		case "version":
			if version == "" {
				version = value
			}
		}
	}
	if name == "" || !semverRe.MatchString(version) {
		return "", "", false
	}
	return name, version, true
}

// warnStaleSkills scans dir/skills/*/SKILL.md and prints one warning line to
// w per skill whose frontmatter version is numerically older than
// binVersion. binVersion "dev" (or anything not X.Y.Z) is skipped entirely.
func warnStaleSkills(w io.Writer, dir, binVersion string) {
	if !semverRe.MatchString(binVersion) {
		return
	}
	matches, err := filepath.Glob(filepath.Join(dir, "skills", "*", "SKILL.md"))
	if err != nil {
		return
	}
	sort.Strings(matches)
	for _, m := range matches {
		lines, err := readFirstLines(m, 10)
		if err != nil {
			continue
		}
		name, ver, ok := skillNameVersion(lines)
		if !ok {
			continue
		}
		if olderVersion(ver, binVersion) {
			fmt.Fprintf(w, "warning: skill %s is version %s, older than flywheel %s\n", name, ver, binVersion)
		}
	}
}

// readFirstLines reads up to n lines from path.
func readFirstLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for len(lines) < n && sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}
