package flywheel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// LintResult is every problem and warning found in one brief. Problems make
// the lint command exit 1; warnings alone keep it at 0.
type LintResult struct {
	Problems []string
	Warnings []string
}

// LintBrief checks one brief file for the problems that broke briefs before:
// a missing owns line, no gate line, no # TASK goal and no ## Checks report
// contract, and owns paths that do not exist under dir. A (new) owns entry
// skips the existence check; an entry ending in "/" must exist as a
// directory. An entry containing '*', '?' or '[' is a pattern (the same
// syntax ownsContains matches at validate time) and is checked with
// filepath.Glob instead of os.Stat: a pattern filepath.Glob rejects is
// invalid syntax, while a valid pattern matching nothing is reported like a
// missing path, with the (new) remedy. A (new) entry skips only the
// existence check; a (new) pattern still has its syntax validated. A negated
// entry ("!path", issue #388) is never existence-checked. Warnings cover the
// missing write rule, a missing needs line and a negated entry no positive
// entry covers.
//
// Two more warnings guard the gates and owns against what CI catches later
// (issue #462). No gate matching the full-suite pattern (config
// lint.full_suite, else go test over ./... when dir has go.mod, else a
// package manager's test script when package.json defines one) is a warning;
// an invalid lint.full_suite is a problem. When dir has go.mod and
// lint.importers is not false, go list finds each owned Go package's direct
// importers, and one whose tests owns does not cover is a warning.
func LintBrief(dir, path string) (LintResult, error) {
	return lintBrief(dir, path, goList)
}

// lintBrief is LintBrief with the go list call injected, for tests.
func lintBrief(dir, path string, list func(string) (string, error)) (LintResult, error) {
	res, err := lintStructure(dir, path)
	if err != nil {
		return res, err
	}
	header, err := ParseBriefHeader(path)
	if err != nil {
		return res, fmt.Errorf("parse brief %s: %w", path, err)
	}
	cfg, _, err := LoadConfig(dir)
	if err != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("config not read, lint defaults apply: %v", err))
	}
	lc := cfg.Lint
	if lc == nil {
		lc = &LintConfig{}
	}
	pattern := lc.FullSuite
	if pattern == "" {
		pattern = defaultFullSuite(dir)
	}
	if pattern != "" {
		if re, err := regexp.Compile(pattern); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("config lint.full_suite %q is not a valid regular expression: %v", pattern, err))
		} else if len(header.Gates) > 0 && !slices.ContainsFunc(header.Gates, re.MatchString) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("no gate runs the full suite (want a gate matching %s; set lint.full_suite to change it)", pattern))
		}
	}
	if fileExists(filepath.Join(dir, "go.mod")) && (lc.Importers == nil || *lc.Importers) {
		b, err := os.ReadFile(path)
		if err != nil {
			return res, fmt.Errorf("read brief %s: %w", path, err)
		}
		res.Warnings = append(res.Warnings, importerWarnings(dir, header.Owns, ownsEntries(string(b)), list)...)
	}
	return res, nil
}

// lintStructure is LintBrief's structural checks: the header lines, headings,
// owns paths and gate quoting.
func lintStructure(dir, path string) (LintResult, error) {
	var res LintResult
	b, err := os.ReadFile(path)
	if err != nil {
		return res, fmt.Errorf("read brief %s: %w", path, err)
	}
	header, err := ParseBriefHeader(path)
	if err != nil {
		return res, fmt.Errorf("parse brief %s: %w", path, err)
	}
	content := string(b)
	if !hasHeading(content, "# TASK") {
		res.Problems = append(res.Problems, "no # TASK heading")
	}
	if len(header.Gates) == 0 {
		res.Problems = append(res.Problems, "no gate: line")
	}
	if !strings.Contains(content, "## Checks") {
		res.Problems = append(res.Problems, "no ## Checks section")
	}
	entries := ownsEntries(content)
	if len(entries) == 0 {
		res.Problems = append(res.Problems, "missing owns: line")
	}
	for _, e := range entries {
		if neg, ok := negatedEntry(e.path); ok {
			if !negationExcludes(entries, neg) {
				res.Warnings = append(res.Warnings, fmt.Sprintf("owns: !%s excludes nothing: no positive entry covers it", neg))
			}
			continue
		}
		if isOwnsPattern(e.path) {
			matches, err := filepath.Glob(filepath.Join(dir, e.path))
			if err != nil {
				res.Problems = append(res.Problems, fmt.Sprintf("owns pattern %s is invalid: %v; correct the pattern", e.path, err))
			} else if e.annotation != "new" && len(matches) == 0 {
				res.Problems = append(res.Problems, fmt.Sprintf("owns pattern %s matches no file; if the unit creates it, annotate it: %s (new)", e.path, e.path))
			}
			continue
		}
		if e.annotation == "new" {
			continue
		}
		st, err := os.Stat(filepath.Join(dir, e.path))
		if err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("owns path %s does not exist; if the unit creates it, annotate it: %s (new)", e.path, e.path))
			continue
		}
		if strings.HasSuffix(e.path, "/") && !st.IsDir() {
			res.Problems = append(res.Problems, fmt.Sprintf("owns path %s is not a directory", e.path))
		}
	}
	for i, lg := range header.LiveGates {
		for j, g := range header.Gates {
			if lg == g {
				res.Problems = append(res.Problems, fmt.Sprintf("live-gate %d repeats gate %d; a live gate must run the real path, not the mocked one", i+1, j+1))
				break
			}
		}
	}
	for _, e := range header.Exclusive {
		if strings.TrimSpace(e) == "" {
			res.Problems = append(res.Problems, "exclusive: entry is empty")
		}
	}
	for i, g := range header.Gates {
		if gateBacktickInDoubleQuotes(g) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("gate %d has a backtick inside double quotes: bash runs it as command substitution; use single quotes or a script file", i+1))
		}
	}
	for i, lg := range header.LiveGates {
		if gateBacktickInDoubleQuotes(lg) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("live-gate %d has a backtick inside double quotes: bash runs it as command substitution; use single quotes or a script file", i+1))
		}
	}
	// gate[quiet]: and live-gate[quiet]: are the known markers (issue #411);
	// any other marker parses as a plain gate, so it is flagged, not refused.
	for i, line := range strings.Split(content, "\n") {
		if i >= 40 {
			break
		}
		if key, _, ok := cutKey(strings.TrimSuffix(line, "\r")); ok {
			if base, marker, found := gateMarker(key); found && marker != "quiet" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s has unknown marker [%s]; the known marker is [quiet], and the line runs as a plain %s", key, marker, base))
			}
		}
	}
	if !header.NeedsDeclared {
		res.Warnings = append(res.Warnings, "no needs: line")
	}
	if !strings.Contains(content, "At most one write per response") {
		res.Warnings = append(res.Warnings, `write rule "At most one write per response" is absent`)
	}
	return res, nil
}

// gateBacktickInDoubleQuotes reports whether a gate has an unescaped backtick
// inside a double-quoted span (issue #366). Gates run under `bash -c`, where
// such a backtick is command substitution, so  node -e "... `x` ..."  fails on
// correct work. One pass tracks quote state: outside quotes, ' opens a
// single-quoted span that ends at the next ' (no escapes) and " opens a
// double-quoted span; inside it \ escapes the next character and " closes it.
// "$(...)" is not flagged: the repository's own gates rely on it.
func gateBacktickInDoubleQuotes(gate string) bool {
	inSingle, inDouble := false, false
	for i := 0; i < len(gate); i++ {
		c := gate[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			switch c {
			case '\\':
				i++
			case '"':
				inDouble = false
			case '`':
				return true
			}
		case c == '\'':
			inSingle = true
		case c == '"':
			inDouble = true
		}
	}
	return false
}

// isOwnsPattern reports whether an owns entry is a shell pattern rather than
// a literal path: it contains '*', '?' or '['. ownsContains (gauges.go)
// already matches these with path.Match at validate time; lint checks them
// with filepath.Glob against dir instead of os.Stat.
func isOwnsPattern(p string) bool {
	return strings.ContainsAny(p, "*?[")
}

// negationExcludes reports whether some positive owns entry could contain the
// negated entry neg (issue #388): the positive entry matches neg itself (a
// "dir/" negation also without its '/'), neg covers the positive entry, or,
// when either is a pattern, one literal prefix (the text before the first
// '*', '?' or '[') starts with the other.
func negationExcludes(entries []ownsEntry, neg string) bool {
	for _, e := range entries {
		pos := e.path
		if _, n := negatedEntry(pos); n {
			continue
		}
		if ownsEntryMatches(pos, neg) || ownsEntryMatches(pos, strings.TrimSuffix(neg, "/")) || ownsEntryMatches(neg, pos) {
			return true
		}
		if isOwnsPattern(neg) || isOwnsPattern(pos) {
			np, pp := literalPrefix(neg), literalPrefix(pos)
			if strings.HasPrefix(np, pp) || strings.HasPrefix(pp, np) {
				return true
			}
		}
	}
	return false
}

// literalPrefix returns an owns entry up to its first '*', '?' or '['.
func literalPrefix(p string) string {
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		return p[:i]
	}
	return p
}

// defaultFullSuite is the full-suite gate pattern for dir's toolchain (issue
// #462): go test over ./... when go.mod exists, else a package manager's test
// script when package.json has one, else "" (no check).
func defaultFullSuite(dir string) string {
	if fileExists(filepath.Join(dir, "go.mod")) {
		return `go test\b.*\./\.\.\.`
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pj struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pj) == nil && strings.TrimSpace(pj.Scripts["test"]) != "" {
		return `\b(npm|pnpm|yarn|bun)( run)? test\b`
	}
	return ""
}

// fileExists reports whether p exists and is not a directory.
func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// goListFormat is the go list template the importer check parses: one package
// per line, tab-separated, list fields comma-joined.
const goListFormat = "{{.ImportPath}}\t{{.Dir}}\t{{join .Imports \",\"}}\t{{join .TestImports \",\"}}\t{{join .XTestImports \",\"}}\t{{join .TestGoFiles \",\"}}\t{{join .XTestGoFiles \",\"}}"

// goList runs go list over every package under dir, bounded by 60 seconds.
func goList(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-f", goListFormat, "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return "", fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return string(out), err
}

// goPackage is one go list line: the package, everything it and its tests
// import, and its test file names.
type goPackage struct {
	importPath string
	imports    []string
	testFiles  []string
}

// parseGoList parses goListFormat output; a line without seven fields is
// skipped.
func parseGoList(out string) []goPackage {
	split := func(s string) []string { return strings.FieldsFunc(s, func(r rune) bool { return r == ',' }) }
	var pkgs []goPackage
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(f) != 7 {
			continue
		}
		p := goPackage{importPath: f[0]}
		for _, s := range f[2:5] {
			p.imports = append(p.imports, split(s)...)
		}
		for _, s := range f[5:7] {
			p.testFiles = append(p.testFiles, split(s)...)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// goModulePath returns the module path go.mod in dir declares, "" if none.
func goModulePath(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// importerWarnings warns, per owned Go package, about its direct importers
// whose test files owns does not cover (issue #462). Owned packages are the
// directories of literal and (new) non-test .go entries; a test file a negated
// entry covers is a deliberate exclusion. A go list failure is a warning.
func importerWarnings(dir string, owns []string, entries []ownsEntry, list func(string) (string, error)) []string {
	var dirs []string
	for _, e := range entries {
		p := filepath.ToSlash(e.path)
		if _, neg := negatedEntry(p); neg || isOwnsPattern(p) || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			continue
		}
		if d := path.Dir(p); !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	if len(dirs) == 0 {
		return nil
	}
	out, err := list(dir)
	if err != nil {
		return []string{fmt.Sprintf("importer check skipped: go list: %v", err)}
	}
	mod := goModulePath(dir)
	if mod == "" {
		return []string{"importer check skipped: go.mod declares no module"}
	}
	pkgs := parseGoList(out)
	var warns []string
	for _, d := range dirs {
		p := mod
		if d != "." {
			p = mod + "/" + d
		}
		var unowned []string
		for _, q := range pkgs {
			qd, inMod := strings.CutPrefix(q.importPath, mod+"/")
			if q.importPath == mod {
				qd, inMod = ".", true
			}
			if q.importPath == p || !inMod || !slices.Contains(q.imports, p) {
				continue
			}
			for _, f := range q.testFiles {
				if fp := path.Join(qd, f); !ownsNegated(owns, fp) && !ownsContains(owns, fp) {
					unowned = append(unowned, q.importPath)
					break
				}
			}
		}
		if len(unowned) > 0 {
			warns = append(warns, fmt.Sprintf("owns changes %s, imported by %s whose tests are not owned", p, strings.Join(unowned, ", ")))
		}
	}
	return warns
}

// hasHeading reports whether content has a line starting with prefix, e.g. a
// "# TASK" heading.
func hasHeading(content, prefix string) bool {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// ownsEntry is one owns value with its trailing parenthesised annotation kept,
// so (new) entries can skip the existence check.
type ownsEntry struct {
	path       string
	annotation string
}

// ownsEntries parses the owns lines the way ParseBriefHeader does but keeps
// each entry's trailing parenthesised annotation.
func ownsEntries(content string) []ownsEntry {
	lines := strings.Split(content, "\n")
	var owned []string
	lastKey := ""
	for i := 0; i < len(lines) && i < 40; i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			if j := i + 1; j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "#") {
				break
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey == "owns" {
				owned = append(owned, line)
			}
			continue
		}
		key, val, ok := cutKey(line)
		if !ok {
			continue
		}
		lastKey = key
		if key == "owns" {
			owned = append(owned, val)
		}
	}
	var entries []ownsEntry
	for _, part := range owned {
		for _, e := range strings.Split(part, ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			path, annotation := e, ""
			if i := strings.Index(e, " ("); i >= 0 && strings.HasSuffix(e, ")") {
				path = strings.TrimSpace(e[:i])
				annotation = e[i+2 : len(e)-1]
			}
			entries = append(entries, ownsEntry{path: path, annotation: annotation})
		}
	}
	return entries
}
