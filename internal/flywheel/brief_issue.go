package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BriefIssueOptions is the header BriefFromIssue writes: owns (required),
// needs (default none), gates (default the Go gate when dir has go.mod), an
// optional kind, and Force to replace an existing brief.
type BriefIssueOptions struct {
	Owns, Needs, Gates []string
	Kind               string
	Force              bool
}

// defaultGoGate is the gate a brief from an issue gets in a Go module when no
// gate is given.
const defaultGoGate = "go build ./... && go vet ./... && go test ./..."

// BriefFromIssue writes .flywheel/briefs/<task>.txt under dir from the
// tracker issue iss (issue #457): a header built from o, the issue's title,
// URL and body, and a Checks section. The file is written atomically; an
// existing one is refused unless o.Force. The written brief is linted and
// removed again when lint reports problems (warnings do not count). It
// returns the brief's path relative to dir.
func BriefFromIssue(dir, task string, iss TrackerIssue, o BriefIssueOptions) (string, error) {
	if !taskOK(task) {
		return "", fmt.Errorf("task id %q does not match ^[A-Za-z0-9._-]+$", task)
	}
	if len(o.Owns) == 0 {
		return "", fmt.Errorf("owns is required: pass --owns")
	}
	gates := o.Gates
	if len(gates) == 0 {
		if !fileExists(filepath.Join(dir, "go.mod")) {
			return "", fmt.Errorf("no gate given and %s has no go.mod to default one from: pass --gate <cmd>", dir)
		}
		gates = []string{defaultGoGate}
	}
	needs := o.Needs
	if len(needs) == 0 {
		needs = []string{"none"}
	}
	rel := filepath.ToSlash(filepath.Join(".flywheel", "briefs", task+".txt"))
	path := filepath.Join(dir, ".flywheel", "briefs", task+".txt")
	if !o.Force {
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("brief %s already exists: pass --force to replace it", rel)
		}
	}
	text := briefIssueText(iss, o.Owns, needs, gates, o.Kind)
	if err := atomicWrite(filepath.Dir(path), task+".txt", task+"-*.txt", []byte(text)); err != nil {
		return "", err
	}
	res, err := LintBrief(dir, path)
	if err == nil && len(res.Problems) > 0 {
		err = fmt.Errorf("lint problems: %s", strings.Join(res.Problems, "; "))
	}
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("brief %s removed: %w", rel, err)
	}
	return rel, nil
}

// briefIssueText renders the brief BriefFromIssue writes, ending in one newline.
func briefIssueText(iss TrackerIssue, owns, needs, gates []string, kind string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "owns: %s\n", strings.Join(owns, ", "))
	fmt.Fprintf(&b, "needs: %s\n", strings.Join(needs, ", "))
	if kind != "" {
		fmt.Fprintf(&b, "kind: %s\n", kind)
	}
	for _, g := range gates {
		fmt.Fprintf(&b, "gate: %s\n", g)
	}
	fmt.Fprintf(&b, "\n# TASK: %s (issue #%d)\n", strings.TrimSpace(iss.Title), iss.Number)
	if iss.URL != "" {
		fmt.Fprintf(&b, "Issue: %s\n", iss.URL)
	}
	b.WriteString("\n## Why (from the issue)\n")
	body := strings.ReplaceAll(iss.Body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	if body = strings.TrimRight(strings.Join(lines, "\n"), "\n"); body != "" {
		b.WriteString(body + "\n")
	}
	b.WriteString("\n## Checks\nRun every gate: line above and report each with its real exit code.\n")
	return b.String()
}
