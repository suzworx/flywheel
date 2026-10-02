package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// droppedUpstream returns, sorted, "<path>: <line>" for every non-blank line
// the diff from..to adds to one of paths that no line of the working-tree
// file in wd still holds (both compared without trailing space and "\r"): a
// unit brought up to date by hand that silently reverted lines the merged
// integration commits added (issue #770). A deleted file drops every added
// line. Lines are trimmed and clipped to 80 characters, the list capped at
// maxMarkerEntries plus "... and N more". One read-only git process.
func droppedUpstream(wd, from, to string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	args := append([]string{"-c", "core.quotePath=false", "diff", "--no-renames", "--no-color", "--no-ext-diff", "-U0", from, to, "--"}, paths...)
	out, err := gitRead(wd, args)
	if err != nil {
		return nil, err
	}
	added := map[string][]string{}
	var order []string
	// Only the header lines between "diff --git" and the first "@@" name the
	// file: an added line "++ b/x" reads "+++ b/x" in a hunk.
	file, header := "", false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, header = "", true
		case strings.HasPrefix(line, "@@"):
			header = false
		case header && strings.HasPrefix(line, "--- a/"):
			file = strings.TrimSuffix(strings.TrimPrefix(line, "--- a/"), "\r")
		case header && strings.HasPrefix(line, "+++ b/"):
			file = strings.TrimSuffix(strings.TrimPrefix(line, "+++ b/"), "\r")
		case header:
		case strings.HasPrefix(line, "+") && file != "":
			l := strings.TrimRight(line[1:], " \t\r")
			if strings.TrimSpace(l) == "" {
				continue
			}
			if _, ok := added[file]; !ok {
				order = append(order, file)
			}
			added[file] = append(added[file], l)
		}
	}
	seen := map[string]bool{}
	var hits []string
	for _, p := range order {
		have := map[string]bool{}
		if b, err := os.ReadFile(filepath.Join(wd, filepath.FromSlash(p))); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				have[strings.TrimRight(l, " \t\r")] = true
			}
		}
		for _, l := range added[p] {
			if have[l] {
				continue
			}
			shown := strings.TrimSpace(l)
			if len(shown) > 80 {
				shown = shown[:80]
			}
			if e := p + ": " + shown; !seen[e] {
				seen[e] = true
				hits = append(hits, e)
			}
		}
	}
	sort.Strings(hits)
	if len(hits) > maxMarkerEntries {
		hits = append(hits[:maxMarkerEntries], fmt.Sprintf("... and %d more", len(hits)-maxMarkerEntries))
	}
	return hits, nil
}

// pendingUpstream returns the warning for integration commits in from..to
// (the branch has not merged them) that touch one of paths or a path owns
// matches, naming up to 5 of them and the files they touch, or "" when there
// are none (issue #770). One read-only git process.
func pendingUpstream(wd, from, to string, paths, owns []string) (string, error) {
	out, err := gitRead(wd, []string{"-c", "core.quotePath=false", "log", "--no-renames", "--no-color", "--format=%x00%h", "--name-only", from + ".." + to})
	if err != nil {
		return "", err
	}
	want := make(map[string]bool, len(paths))
	for _, p := range paths {
		want[p] = true
	}
	var shas []string
	files := map[string]bool{}
	sha, counted := "", false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "\x00") {
			sha, counted = strings.TrimSpace(line[1:]), false
			continue
		}
		if line == "" || sha == "" || (!want[line] && !ownsContains(owns, line)) {
			continue
		}
		files[line] = true
		if !counted {
			counted = true
			shas = append(shas, short7(sha))
		}
	}
	if len(shas) == 0 {
		return "", nil
	}
	named := make([]string, 0, len(files))
	for f := range files {
		named = append(named, f)
	}
	sort.Strings(named)
	shown := shas
	if len(shown) > 5 {
		shown = append(append([]string(nil), shown[:5]...), "...")
	}
	return fmt.Sprintf("%d integration commit(s) since the unit's base touch owned files %s (%s): merge %s before landing", len(shas), strings.Join(named, ", "), strings.Join(shown, ", "), to), nil
}
