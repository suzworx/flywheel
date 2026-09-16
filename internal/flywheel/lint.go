package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// filepath.Glob instead of os.Stat: no match is one problem, and a (new)
// pattern skips the check like any other (new) entry. Warnings cover the
// missing write rule and a missing needs line.
func LintBrief(dir, path string) (LintResult, error) {
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
		if e.annotation == "new" {
			continue
		}
		if isOwnsPattern(e.path) {
			matches, err := filepath.Glob(filepath.Join(dir, e.path))
			if err != nil || len(matches) == 0 {
				res.Problems = append(res.Problems, fmt.Sprintf("owns pattern %s matches no file", e.path))
			}
			continue
		}
		st, err := os.Stat(filepath.Join(dir, e.path))
		if err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("owns path %s does not exist", e.path))
			continue
		}
		if strings.HasSuffix(e.path, "/") && !st.IsDir() {
			res.Problems = append(res.Problems, fmt.Sprintf("owns path %s is not a directory", e.path))
		}
	}
	if len(header.Needs) == 0 {
		res.Warnings = append(res.Warnings, "no needs: line")
	}
	if !strings.Contains(content, "At most one write per response") {
		res.Warnings = append(res.Warnings, `write rule "At most one write per response" is absent`)
	}
	return res, nil
}

// isOwnsPattern reports whether an owns entry is a shell pattern rather than
// a literal path: it contains '*', '?' or '['. ownsContains (gauges.go)
// already matches these with path.Match at validate time; lint checks them
// with filepath.Glob against dir instead of os.Stat.
func isOwnsPattern(p string) bool {
	return strings.ContainsAny(p, "*?[")
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
