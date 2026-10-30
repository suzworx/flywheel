package flywheel

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// fixSubject matches a commit subject that names a fix: "fix: x", "fix(voice):
// x" and "Fix wrong time" count; "prefix: x", "fixture: x" and "feat: x" do not.
var fixSubject = regexp.MustCompile(`(?i)^(fix|fixes|fixed|hotfix|bugfix)\b`)

// removedHunk is a unified diff hunk header's old range. A combined diff's
// "@@@" header (an unmerged path) does not match, so it removes nothing.
var removedHunk = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+`)

// removedFixLines returns, sorted, "<path>: <n> line(s) from fix <sha12>
// <subject>" for every fix commit that added non-blank lines of paths at from
// which the working tree in wd no longer holds (issue #940). It only reads:
// the caller reports the entries, and a deliberate removal still lands. A line
// ending change and a pure rename remove nothing; a rename diffs under its old
// path and a deleted file counts. Subjects are clipped to 80 characters, the
// list capped at maxMarkerEntries plus "... and N more". One read-only git
// process, plus one per file with removed lines.
func removedFixLines(wd, from string, paths []string) ([]string, error) {
	if len(paths) == 0 || from == "" {
		return nil, nil
	}
	args := append([]string{"-c", "core.quotePath=false", "diff", "-M", "--src-prefix=a/", "--dst-prefix=b/", "--ignore-cr-at-eol", "--no-color", "--no-ext-diff", "-U0", from, "--"}, paths...)
	out, err := gitRead(wd, args)
	if err != nil {
		return nil, err
	}
	ranges := map[string][]string{}
	var order []string
	// Only the header lines between "diff --git" and the first "@@" name the
	// file: a removed line "-- a/x" reads "--- a/x" in a hunk. Git ends a path
	// holding whitespace with a tab.
	file, header := "", false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			file, header = "", true
		case strings.HasPrefix(line, "@@"):
			header = false
			m := removedHunk.FindStringSubmatch(line)
			if m == nil || file == "" || m[2] == "0" {
				continue
			}
			count := m[2]
			if count == "" {
				count = "1"
			}
			if _, ok := ranges[file]; !ok {
				order = append(order, file)
			}
			ranges[file] = append(ranges[file], m[1]+",+"+count)
		case header && strings.HasPrefix(line, "--- a/"):
			file = strings.TrimRight(strings.TrimPrefix(line, "--- a/"), "\t\r")
		}
	}
	var hits []string
	for _, p := range order {
		args := []string{"blame", "--porcelain"}
		for _, r := range ranges[p] {
			args = append(args, "-L", r)
		}
		out, err := gitRead(wd, append(args, from, "--", p))
		if err != nil {
			return nil, err
		}
		hits = append(hits, blameFixEntries(p, out)...)
	}
	sort.Strings(hits)
	if len(hits) > maxMarkerEntries {
		hits = append(hits[:maxMarkerEntries], fmt.Sprintf("... and %d more", len(hits)-maxMarkerEntries))
	}
	return hits, nil
}

// blameFixEntries turns one path's git blame --porcelain output into its
// removedFixLines entries. Porcelain prints a commit's summary only on the
// commit's first appearance, so it is remembered by hash; every line starting
// with a tab is content and the line after it starts the next header.
func blameFixEntries(path, out string) []string {
	subjects := map[string]string{}
	counts := map[string]int{}
	commit, fresh := "", true
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case strings.HasPrefix(line, "\t"):
			if strings.TrimSpace(line) != "" && fixSubject.MatchString(subjects[commit]) {
				counts[commit]++
			}
			fresh = true
		case fresh:
			if fields := strings.Fields(line); len(fields) > 0 {
				commit, fresh = fields[0], false
			}
		case strings.HasPrefix(line, "summary "):
			subjects[commit] = strings.TrimPrefix(line, "summary ")
		}
	}
	entries := make([]string, 0, len(counts))
	for c, n := range counts {
		subject := subjects[c]
		if r := []rune(subject); len(r) > 80 {
			subject = string(r[:80])
		}
		entries = append(entries, path+": "+strconv.Itoa(n)+" line(s) from fix "+c[:min(12, len(c))]+" "+subject)
	}
	return entries
}
