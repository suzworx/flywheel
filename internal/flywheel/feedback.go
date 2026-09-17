package flywheel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// LearningView is one learning: its stable id, the task it was observed on,
// its curation fields, and its dismissal state.
type LearningView struct {
	ID        string   `json:"id"`
	Task      string   `json:"task"`
	Severity  string   `json:"severity"`
	Title     string   `json:"title"`
	Observed  string   `json:"observed"`
	Evidence  string   `json:"evidence"`
	Ask       string   `json:"ask"`
	Signals   []string `json:"signals,omitempty"`
	Dismissed bool     `json:"dismissed"`
	Reason    string   `json:"reason,omitempty"`
}

// Learnings folds the event log into one LearningView per learning event, in
// log order, assigning ids L-01, L-02, ... in the order learning events
// appear. A later dismissed event marks the learning it targets by id;
// dismissing never removes or renumbers a learning.
func Learnings(events []Event) []LearningView {
	var out []LearningView
	index := map[string]int{}
	n := 0
	for _, e := range events {
		if e.Kind != "learning" {
			continue
		}
		n++
		id := fmt.Sprintf("L-%02d", n)
		index[id] = len(out)
		out = append(out, LearningView{
			ID: id, Task: e.Task, Severity: e.Severity, Title: e.Title,
			Observed: e.Observed, Evidence: e.Evidence, Ask: e.Ask, Signals: e.Signals,
		})
	}
	for _, e := range events {
		if e.Kind != "dismissed" {
			continue
		}
		if i, ok := index[e.ID]; ok {
			out[i].Dismissed = true
			out[i].Reason = e.Note
		}
	}
	return out
}

// NextLearningID returns the next L-NN id in log order.
func NextLearningID(events []Event) string {
	n := 0
	for _, e := range events {
		if e.Kind == "learning" {
			n++
		}
	}
	return fmt.Sprintf("L-%02d", n+1)
}

// WriteLearningsFile rewrites .flywheel/learnings.md atomically (temp file plus
// rename) from views, in log order: a heading, then one section per learning
// with its severity, observed, evidence, ask, signals (when given) and
// dismissed reason (when dismissed). Every free-text field is sanitised
// before it is written, so a path or token never leaks into the artifact. The
// file lives under .flywheel/ so a repo's existing .flywheel handling covers
// it; a stale <dir>/learnings.md from an older version is removed only after
// the new file is written, so a failed write never orphans the old copy.
func WriteLearningsFile(dir string, views []LearningView) error {
	var b strings.Builder
	b.WriteString("# Learnings\n")
	for _, v := range views {
		fmt.Fprintf(&b, "\n## %s — %s\n", v.ID, Sanitise(v.Title))
		fmt.Fprintf(&b, "severity: %s\n", v.Severity)
		fmt.Fprintf(&b, "observed: %s\n", Sanitise(v.Observed))
		fmt.Fprintf(&b, "evidence: %s\n", Sanitise(v.Evidence))
		fmt.Fprintf(&b, "ask: %s\n", Sanitise(v.Ask))
		if len(v.Signals) > 0 {
			sigs := make([]string, len(v.Signals))
			for i, s := range v.Signals {
				sigs[i] = Sanitise(s)
			}
			fmt.Fprintf(&b, "signals: %s\n", strings.Join(sigs, ", "))
		}
		if v.Dismissed {
			fmt.Fprintf(&b, "dismissed: %s\n", Sanitise(v.Reason))
		}
	}
	if err := atomicWrite(filepath.Join(dir, ".flywheel"), "learnings.md", "learnings-*.md", []byte(b.String())); err != nil {
		return err
	}
	old := filepath.Join(dir, "learnings.md")
	if _, err := os.Stat(old); err == nil {
		if err := os.Remove(old); err != nil {
			return fmt.Errorf("remove %s: %w", old, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", old, err)
	}
	return nil
}

// sanitise regexes. Each rule is independent of the others; over-redacting a
// word costs a maintainer nothing, a leaked token costs the owner.
var (
	// key/token/secret/password assignments: `key = "..."`, `token: abc`,
	// `secret=value`, quoted or unquoted. No leading word boundary so common
	// spellings like `api_key=...` are caught too.
	secretAssignRe = regexp.MustCompile(`(?i)(?:key|token|secret|password)\s*[=:]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`)
	// GitHub tokens: ghp_/gho_/ghs_ followed by 20+ characters.
	ghTokenRe = regexp.MustCompile(`\bgh[ops]_[A-Za-z0-9]{20,}`)
	// OpenAI-style keys: sk- followed by 20+ characters.
	skTokenRe = regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}`)
	// Authorization: Bearer <token>.
	bearerRe = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._-]{12,}`)
	// Absolute Windows paths (C:\x\y, D:/x/y, either slash), bounded so a URL
	// scheme like `https:` is not mistaken for a drive letter.
	windowsAbsPathRe = regexp.MustCompile(`(^|[^\w:])[A-Za-z]:[\\/][^\s"';,()]*`)
	// Absolute POSIX paths (/home/x/y). The boundary excludes a word char,
	// slash, colon or dot so a repo-relative path like `internal/foo/bar.go`
	// or `.flywheel/runs/t1.r1.jsonl` survives untouched, and a URL scheme's
	// `//` never matches.
	posixAbsPathRe = regexp.MustCompile(`(^|[^\w/:.])(/[\w.-]+(?:/[\w.-]+)+)`)
)

// Sanitise replaces anything unsafe in s: absolute paths become <path> and
// secret-shaped tokens become <redacted>, leaving repo-relative paths and
// ordinary prose alone. It is applied to every free-text field of a learning
// before it is written or sent.
func Sanitise(s string) string {
	s = secretAssignRe.ReplaceAllString(s, "<redacted>")
	s = ghTokenRe.ReplaceAllString(s, "<redacted>")
	s = skTokenRe.ReplaceAllString(s, "<redacted>")
	s = bearerRe.ReplaceAllString(s, "<redacted>")
	s = windowsAbsPathRe.ReplaceAllString(s, "${1}<path>")
	s = posixAbsPathRe.ReplaceAllString(s, "${1}<path>")
	return s
}

// FeedbackReport renders a Markdown report of every undismissed learning for
// export or submission: a short header naming the flywheel version, then one
// section per learning with its id, severity, title and sanitised observed,
// evidence, ask and signals. Dismissed learnings are excluded.
func FeedbackReport(version string, views []LearningView) string {
	if version == "" {
		version = "dev"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# flywheel learnings (v%s)\n", version)
	for _, v := range views {
		if v.Dismissed {
			continue
		}
		fmt.Fprintf(&b, "\n## %s — %s\n", v.ID, Sanitise(v.Title))
		fmt.Fprintf(&b, "severity: %s\n", v.Severity)
		fmt.Fprintf(&b, "observed: %s\n", Sanitise(v.Observed))
		fmt.Fprintf(&b, "evidence: %s\n", Sanitise(v.Evidence))
		fmt.Fprintf(&b, "ask: %s\n", Sanitise(v.Ask))
		if len(v.Signals) > 0 {
			sigs := make([]string, len(v.Signals))
			for i, s := range v.Signals {
				sigs[i] = Sanitise(s)
			}
			fmt.Fprintf(&b, "signals: %s\n", strings.Join(sigs, ", "))
		}
	}
	return b.String()
}

// WriteFeedbackReport writes report to path via a temp file plus rename,
// creating the destination directory if missing.
func WriteFeedbackReport(path, report string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return atomicWrite(dir, filepath.Base(path), "feedback-*.md", []byte(report))
}

// ErrFeedbackNoUpstream is the usage error submit returns when
// feedback.upstream is unset: there is nowhere to send a report.
var ErrFeedbackNoUpstream = errors.New("feedback.upstream is unset in .flywheel/config.json; set it to owner/repo to submit")

// GhRunner runs `gh <args...>`. The CLI passes nil to run the real gh binary;
// tests inject a runner so sending is never performed against a real host.
type GhRunner func(args ...string) error

// FeedbackSubmitOptions configures one submit.
type FeedbackSubmitOptions struct {
	Yes bool
	Gh  GhRunner         // nil runs the real gh binary
	Now func() time.Time // nil means time.Now
}

// FeedbackSubmitResult reports what a submit did.
type FeedbackSubmitResult struct {
	Sent    bool   // the issue was created upstream
	NotSent bool   // consent withheld: the report was shown but nothing was sent
	Outbox  string // report parked here when the send failed
	Report  string // the exact text that would be sent
}

// FeedbackSubmit sends the sanitised report of undismissed learnings upstream
// as a gh issue. It refuses with a RuleRefusal when feedback.submit is
// "never", and with ErrFeedbackNoUpstream when feedback.upstream is unset.
// Without --yes it shows the exact text it would send and sends nothing.
// With --yes it runs `gh issue create --repo <upstream> --title ... --body-file ...`;
// if gh is missing or fails, the report is parked in
// .flywheel/feedback/outbox/<timestamp>-<id>.md and the call succeeds.
func FeedbackSubmit(dir string, cfg Config, version string, views []LearningView, o FeedbackSubmitOptions) (FeedbackSubmitResult, error) {
	if cfg.Feedback.Submit == "never" {
		return FeedbackSubmitResult{}, &RuleRefusal{Rule: "feedback", Fix: fmt.Sprintf("feedback.submit is %q; set it to \"ask\" in .flywheel/config.json to allow sending", cfg.Feedback.Submit)}
	}
	if cfg.Feedback.Upstream == "" {
		return FeedbackSubmitResult{}, ErrFeedbackNoUpstream
	}
	report := FeedbackReport(version, views)
	if !o.Yes {
		return FeedbackSubmitResult{NotSent: true, Report: report}, nil
	}
	gh := o.Gh
	if gh == nil {
		gh = runGh
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	body, err := os.CreateTemp("", "flywheel-feedback-*.md")
	if err != nil {
		return FeedbackSubmitResult{}, fmt.Errorf("create body file: %w", err)
	}
	bodyName := body.Name()
	defer os.Remove(bodyName)
	if _, err := body.WriteString(report); err != nil {
		body.Close()
		return FeedbackSubmitResult{}, fmt.Errorf("write body file %s: %w", bodyName, err)
	}
	if err := body.Close(); err != nil {
		return FeedbackSubmitResult{}, fmt.Errorf("close body file %s: %w", bodyName, err)
	}
	args := []string{"issue", "create", "--repo", cfg.Feedback.Upstream, "--title", fmt.Sprintf("flywheel learnings (v%s)", version), "--body-file", bodyName}
	if err := gh(args...); err != nil {
		outbox, werr := writeOutbox(dir, views, report, now())
		if werr != nil {
			return FeedbackSubmitResult{}, fmt.Errorf("send failed (%v) and the report could not be parked: %w", err, werr)
		}
		return FeedbackSubmitResult{Outbox: outbox, Report: report}, nil
	}
	return FeedbackSubmitResult{Sent: true, Report: report}, nil
}

// runGh runs the real gh binary, returning an error naming it and its output.
func runGh(args ...string) error {
	cmd := exec.Command("gh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gh %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeOutbox parks report in dir/.flywheel/feedback/outbox/<timestamp>-<id>.md
// atomically and returns the written path.
func writeOutbox(dir string, views []LearningView, report string, now time.Time) (string, error) {
	id := "L-00"
	for _, v := range views {
		if !v.Dismissed {
			id = v.ID
			break
		}
	}
	name := now.Format("20060102-150405") + "-" + id + ".md"
	outboxDir := filepath.Join(dir, ".flywheel", "feedback", "outbox")
	if err := atomicWrite(outboxDir, name, "outbox-*.md", []byte(report)); err != nil {
		return "", err
	}
	return filepath.Join(outboxDir, name), nil
}
