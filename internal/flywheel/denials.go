package flywheel

import "strings"

// attributeDenial names the deny pattern and the command segment that matched
// a denied Bash call (issue #497): Claude's refusal names only the whole
// compound, so a worker blamed the `cd` prefix when the real match was a
// later `git branch`. command is split on unquoted &&, ||, ;, | and newlines,
// a leading `cd <dir>` segment is dropped and leading VAR=value assignments
// are stripped; the first segment a pattern of disallowed matches gives
// "Bash: <pattern> (<segment>)", else "Bash: unattributed (<command>)" with
// the command clipped to 80 runes.
func attributeDenial(command string, disallowed []string) string {
	segs := commandSegments(command)
	for len(segs) > 0 && (segs[0] == "cd" || strings.HasPrefix(segs[0], "cd ")) {
		segs = segs[1:]
	}
	for _, seg := range segs {
		s := stripEnvAssignments(seg)
		for _, p := range disallowed {
			if denyMatches(p, s) {
				return "Bash: " + p + " (" + s + ")"
			}
		}
	}
	c := []rune(strings.Join(strings.Fields(command), " "))
	if len(c) > 80 {
		c = c[:80]
	}
	return "Bash: unattributed (" + string(c) + ")"
}

// denyMatches reports whether the Claude permission pattern p matches the
// segment s: bare "Bash" matches everything, "Bash(<prefix>:*)" matches s ==
// prefix or s starting with prefix + " ", and "Bash(<exact>)" matches s ==
// exact. Any other pattern matches nothing.
func denyMatches(p, s string) bool {
	if p == "Bash" {
		return true
	}
	if !strings.HasPrefix(p, "Bash(") || !strings.HasSuffix(p, ")") {
		return false
	}
	inner := p[len("Bash(") : len(p)-1]
	if prefix, ok := strings.CutSuffix(inner, ":*"); ok {
		return s == prefix || strings.HasPrefix(s, prefix+" ")
	}
	return s == inner
}

// stripEnvAssignments drops leading VAR=value words from a segment
// (`FOO=1 git push` is `git push`).
func stripEnvAssignments(s string) string {
	for {
		word, rest, _ := strings.Cut(s, " ")
		eq := strings.IndexByte(word, '=')
		if eq <= 0 || !isEnvName(word[:eq]) || rest == "" {
			return s
		}
		s = strings.TrimLeft(rest, " \t")
	}
}

// isEnvName reports whether s is a shell variable name.
func isEnvName(s string) bool {
	for i, r := range s {
		if r != '_' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return s != ""
}

// commandSegments splits a shell command on &&, ||, ;, | and newlines outside
// single and double quotes, trimming each segment and dropping empty ones.
func commandSegments(command string) []string {
	var segs []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	var quote byte
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(command) {
				cur.WriteByte(c)
				i++
				c = command[i]
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && i+1 < len(command):
			cur.WriteByte(c)
			i++
			c = command[i]
		case c == ';' || c == '\n' || c == '|':
			flush()
			if c == '|' && i+1 < len(command) && command[i+1] == '|' {
				i++
			}
			continue
		case c == '&' && i+1 < len(command) && command[i+1] == '&':
			flush()
			i++
			continue
		}
		cur.WriteByte(c)
	}
	flush()
	return segs
}
