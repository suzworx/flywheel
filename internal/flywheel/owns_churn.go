package flywheel

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Churn classes churnClass reports for an outside path (issue #647).
const (
	churnLineEndings = "line endings only"
	churnWhitespace  = "whitespace only"
)

// churnClass reports how p's working-tree bytes in wd differ from its blob at
// base (issue #647): "line endings only" when they match once every "\r\n"
// reads "\n", "whitespace only" when they match once every Unicode whitespace
// rune is dropped, and "" otherwise — content differs, the bytes are
// identical, p was added or deleted, base is empty, or git fails. A label for
// the lead's review, never an excuse: the owns check still counts p.
func churnClass(wd, base, p string) string {
	if base == "" || p == "" {
		return ""
	}
	work, err := os.ReadFile(filepath.Join(wd, filepath.FromSlash(p)))
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", wd, "cat-file", "blob", base+":"+p)
	orig, err := cmd.Output()
	if err != nil {
		return ""
	}
	if bytes.Equal(work, orig) {
		return ""
	}
	crlf, lf := []byte("\r\n"), []byte("\n")
	if bytes.Equal(bytes.ReplaceAll(work, crlf, lf), bytes.ReplaceAll(orig, crlf, lf)) {
		return churnLineEndings
	}
	if stripSpace(work) == stripSpace(orig) {
		return churnWhitespace
	}
	return ""
}

// stripSpace returns b as a string with every Unicode whitespace rune removed.
func stripSpace(b []byte) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, string(b))
}

// outsideChurn labels each bare outside path whose change is line endings or
// whitespace only (issue #647); entries that are not a bare path (a sibling
// "<worktree>: <path>", a "(git-ignored)" or "left uncommitted" entry) are
// skipped. nil when nothing is labelled.
func outsideChurn(wd, base string, outside []string) map[string]string {
	var churn map[string]string
	for _, o := range outside {
		if strings.Contains(o, ": ") || strings.HasSuffix(o, " (git-ignored)") || strings.HasPrefix(o, "left uncommitted") {
			continue
		}
		if c := churnClass(wd, base, o); c != "" {
			if churn == nil {
				churn = map[string]string{}
			}
			churn[o] = c
		}
	}
	return churn
}
