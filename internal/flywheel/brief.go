package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// BriefHeader is the parsed key: value block at the top of a brief file, plus
// the SHA-256 of the whole file. Keys: owns, needs, needs-state, gate,
// exclusive, review.
type BriefHeader struct {
	Owns  []string // comma-separated, annotations stripped
	Needs []string
	// NeedsState lists repo-relative paths or directories (a trailing '/' for
	// a directory) that the gates need but an isolated --workdir will not
	// have: a database, a stack, git-ignored env files (issue #136).
	NeedsState []string // comma-separated, accumulated across repeated lines
	Gates      []string // one shell command per line, order kept
	Exclusive  []string
	Review     []string
	SHA256     string
}

// ParseBriefHeader reads the header block at the top of a brief: the lines
// before the first blank line that is followed by a '#' heading, or the first
// 40 lines, whichever comes first. owns values are comma-separated and may
// continue on indented following lines; a trailing parenthesised annotation
// such as "(new)" is stripped from each entry. gate lines are one command per
// line and keep their order. The returned header carries the SHA-256 of the
// whole brief file.
func ParseBriefHeader(path string) (BriefHeader, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return BriefHeader{}, err
	}
	sum := sha256.Sum256(b)
	h := BriefHeader{SHA256: hex.EncodeToString(sum[:])}
	raw := strings.Split(string(b), "\n")
	var owns []string
	lastKey := ""
	for i := 0; i < len(raw) && i < 40; i++ {
		line := strings.TrimSuffix(raw[i], "\r")
		if strings.TrimSpace(line) == "" {
			if j := i + 1; j < len(raw) && strings.HasPrefix(strings.TrimSpace(raw[j]), "#") {
				break
			}
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if lastKey == "owns" {
				owns = append(owns, line)
			}
			continue
		}
		key, val, ok := cutKey(line)
		if !ok {
			continue
		}
		lastKey = key
		switch key {
		case "owns":
			owns = append(owns, val)
		case "needs":
			h.Needs = append(h.Needs, val)
		case "needs-state":
			for _, entry := range strings.Split(val, ",") {
				if e := strings.TrimSpace(entry); e != "" {
					h.NeedsState = append(h.NeedsState, e)
				}
			}
		case "gate":
			h.Gates = append(h.Gates, val)
		case "exclusive":
			h.Exclusive = append(h.Exclusive, val)
		case "review":
			h.Review = append(h.Review, val)
		}
	}
	for _, part := range owns {
		for _, entry := range strings.Split(part, ",") {
			if e := stripAnnotation(strings.TrimSpace(entry)); e != "" {
				h.Owns = append(h.Owns, e)
			}
		}
	}
	return h, nil
}

// cutKey splits a "key: value" line. It reports false for lines without a
// colon or with an empty key.
func cutKey(line string) (key, val string, ok bool) {
	k, v, found := strings.Cut(line, ":")
	if !found || strings.TrimSpace(k) == "" {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

// stripAnnotation removes a trailing parenthesised annotation: "a.go (new)"
// becomes "a.go". Lines without one pass through unchanged.
func stripAnnotation(s string) string {
	if i := strings.Index(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		return strings.TrimSpace(s[:i])
	}
	return s
}
