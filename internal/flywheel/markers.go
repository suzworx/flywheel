package flywheel

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxMarkerEntries caps conflictMarkers' list; the rest are counted in one
// final "... and N more" entry.
const maxMarkerEntries = 20

// isConflictMarkerLine reports whether line is a git conflict marker once one
// trailing "\r" is stripped (issue #698: a CRLF file split on LF still holds
// them): it starts with "<<<<<<< " or ">>>>>>> ", or is exactly "<<<<<<<",
// ">>>>>>>" or "=======". ParseEvents shares it for the event log.
func isConflictMarkerLine(line string) bool {
	line = strings.TrimSuffix(line, "\r")
	switch line {
	case "<<<<<<<", ">>>>>>>", "=======":
		return true
	}
	return strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ")
}

// conflictMarkers returns, sorted, "<path>:<line>" for every conflict marker
// line (isConflictMarkerLine) in the files of paths read from wd, capped at
// maxMarkerEntries plus "... and N more". A missing or deleted file, a
// directory, and a binary file (a NUL byte in its first 8000 bytes) are
// skipped (issue #698).
func conflictMarkers(wd string, paths []string) []string {
	type hit struct {
		path string
		line int
	}
	var hits []hit
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		b, err := os.ReadFile(filepath.Join(wd, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		head := b
		if len(head) > 8000 {
			head = head[:8000]
		}
		if bytes.IndexByte(head, 0) >= 0 {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 0, 64*1024), len(b)+1)
		for n := 1; sc.Scan(); n++ {
			if isConflictMarkerLine(sc.Text()) {
				hits = append(hits, hit{p, n})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].path != hits[j].path {
			return hits[i].path < hits[j].path
		}
		return hits[i].line < hits[j].line
	})
	var out []string
	for i, h := range hits {
		if i == maxMarkerEntries {
			out = append(out, fmt.Sprintf("... and %d more", len(hits)-maxMarkerEntries))
			break
		}
		out = append(out, fmt.Sprintf("%s:%d", h.path, h.line))
	}
	return out
}
