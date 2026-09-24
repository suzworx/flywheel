package flywheel

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// BriefHeader is the parsed key: value block at the top of a brief file, plus
// the SHA-256 of the whole file. Keys: owns, needs, needs-state, gate,
// live-gate, exclusive, review, line.
type BriefHeader struct {
	Owns  []string // comma-separated, annotations stripped
	Needs []string
	// NeedsDeclared is true when the header has at least one needs: line, even needs: none (issue #307).
	NeedsDeclared bool `json:",omitempty"`
	// NeedsState lists repo-relative paths or directories (a trailing '/' for
	// a directory) that the gates need but an isolated --workdir will not
	// have: a database, a stack, git-ignored env files (issue #136).
	NeedsState []string // comma-separated, accumulated across repeated lines
	Gates      []string // one shell command per line, order kept
	// LiveGates are `live-gate:` lines, one shell command per line, order
	// kept: gates that run only in the lead's verification pass
	// (`flywheel validate <task> --live`), never on a worker's own mocked
	// run (issue #152).
	LiveGates []string
	// QuietGates and QuietLiveGates are the 1-based indices into Gates and
	// LiveGates of the lines marked `[quiet]` (issue #411): gates that wait
	// for an idle host before they run.
	QuietGates     []int `json:",omitempty"`
	QuietLiveGates []int `json:",omitempty"`
	Exclusive      []string
	Review         []string
	Line           string `json:",omitempty"`
	SHA256         string
}

// ParseBriefHeader reads the file at path and parses its header block; it is
// ParseBriefHeaderBytes after the read, the one parsing implementation.
func ParseBriefHeader(path string) (BriefHeader, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return BriefHeader{}, err
	}
	return ParseBriefHeaderBytes(b)
}

// ParseBriefHeaderBytes parses the header block at the top of a brief's bytes:
// the lines before the first blank line that is followed by a '#' heading, or
// the first 40 lines, whichever comes first. owns values are comma-separated
// and may continue on indented following lines; a trailing parenthesised
// annotation such as "(new)" is stripped from each entry. gate lines are one
// command per line and keep their order. The returned header carries the
// SHA-256 of the whole bytes, so a caller that already holds the exact bytes
// it sent (a dispatch hashing its prompt) gets a header and a hash describing
// the same immutable content.
func ParseBriefHeaderBytes(b []byte) (BriefHeader, error) {
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
		// A `gate[quiet]:` or `live-gate[quiet]:` line is a gate that needs the
		// host to itself (issue #411): its command joins Gates/LiveGates like
		// any other and its 1-based index is recorded as quiet. An unknown
		// marker is kept as the plain key's line; flywheel lint warns on it.
		if base, marker, found := gateMarker(key); found {
			key = base
			if marker == "quiet" {
				if base == "gate" {
					h.QuietGates = append(h.QuietGates, len(h.Gates)+1)
				} else {
					h.QuietLiveGates = append(h.QuietLiveGates, len(h.LiveGates)+1)
				}
			}
		}
		switch key {
		case "owns":
			owns = append(owns, val)
		case "needs":
			h.NeedsDeclared = true
			h.Needs = append(h.Needs, NeedTargets(val)...)
		case "needs-state":
			for _, entry := range strings.Split(val, ",") {
				if e := strings.TrimSpace(entry); e != "" {
					h.NeedsState = append(h.NeedsState, e)
				}
			}
		case "gate":
			h.Gates = append(h.Gates, val)
		case "live-gate":
			h.LiveGates = append(h.LiveGates, val)
		case "exclusive":
			h.Exclusive = append(h.Exclusive, val)
		case "review":
			h.Review = append(h.Review, val)
		case "line":
			h.Line = val
		}
	}
	for _, part := range owns {
		for _, entry := range strings.Split(part, ",") {
			if e := stripAnnotation(strings.TrimSpace(entry)); e != "" {
				h.Owns = append(h.Owns, e)
			}
		}
	}
	// One pass over every needs: line, so an id repeated across lines is kept
	// once (#310 review).
	h.Needs = NeedTargets(h.Needs...)
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

// gateMarker splits a `gate[m]` or `live-gate[m]` key into its base key and
// marker m (issue #411). found is false for any other key.
func gateMarker(key string) (base, marker string, found bool) {
	for _, b := range []string{"gate", "live-gate"} {
		if rest, ok := strings.CutPrefix(key, b+"["); ok && strings.HasSuffix(rest, "]") {
			return b, strings.TrimSpace(strings.TrimSuffix(rest, "]")), true
		}
	}
	return "", "", false
}

// isQuiet reports whether the 1-based index n is listed in quiet.
func isQuiet(quiet []int, n int) bool {
	for _, q := range quiet {
		if q == n {
			return true
		}
	}
	return false
}

// stripAnnotation removes a trailing parenthesised annotation: "a.go (new)"
// becomes "a.go". Lines without one pass through unchanged.
func stripAnnotation(s string) string {
	if i := strings.Index(s, " ("); i >= 0 && strings.HasSuffix(s, ")") {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// NeedTargets turns needs: values into task ids (issue #307): each value is
// split on commas and trimmed; empty entries, "-" and "none" (any case) are
// dropped, and a repeated id is kept once, in first-seen order. nil when
// nothing remains.
func NeedTargets(values ...string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, val := range values {
		for _, entry := range strings.Split(val, ",") {
			e := strings.TrimSpace(entry)
			if e == "" || e == "-" || strings.EqualFold(e, "none") {
				continue
			}
			if !seen[e] {
				result = append(result, e)
				seen[e] = true
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
