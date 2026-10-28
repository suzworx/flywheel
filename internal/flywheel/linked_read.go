package flywheel

import (
	"path/filepath"
	"runtime"
	"strings"
)

// claudeAbsPattern is an absolute path in Claude Code's "//" permission-rule
// form, the same on every OS: backslashes become "/", a leading drive "X:"
// becomes "/x" (lower-case), and the result is "/" + that, so C:\Users\a is
// //c/Users/a and /home/a is //home/a. A trailing slash is trimmed.
func claudeAbsPattern(p string) string {
	p = strings.TrimRight(strings.ReplaceAll(p, `\`, "/"), "/")
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		p = "/" + strings.ToLower(p[:1]) + p[2:]
	}
	return "/" + p
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// linkedReadRules are the read-only claude allow rules for a worktree's
// needs-state "(link)" paths (issue #886). Claude Code resolves a link before
// its working-directory check and applies an allow rule to a linked path only
// when BOTH the requested path and the resolved target match one, so each
// link gets Read, Grep and Glob rules for itself and for its target. An entry
// that is missing or not a link gets none. Never Edit or Write: writes into
// the main checkout stay refused.
func linkedReadRules(wt string, links []string) []string {
	var rules []string
	seen := map[string]bool{}
	for _, entry := range links {
		rel := strings.Trim(entry, `/\`)
		if rel == "" {
			continue
		}
		p := filepath.Join(wt, filepath.FromSlash(rel))
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		// linkTarget, not EvalSymlinks alone: EvalSymlinks leaves a Windows
		// junction unresolved.
		resolved, ok := linkTarget(p)
		if !ok {
			continue
		}
		// The link's own path with its parents resolved: equal to the
		// resolved path when p itself is not a link.
		link := p
		if parent, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
			link = filepath.Join(parent, filepath.Base(p))
		}
		if sameCleanPath(link, resolved) {
			continue
		}
		for _, path := range []string{link, resolved} {
			for _, tool := range []string{"Read", "Grep", "Glob"} {
				rule := tool + "(" + claudeAbsPattern(path) + "/**)"
				if !seen[rule] {
					seen[rule] = true
					rules = append(rules, rule)
				}
			}
		}
	}
	return rules
}

// sameCleanPath compares two already-resolved paths, case-insensitively on
// Windows.
func sameCleanPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
