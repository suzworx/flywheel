package flywheel

import (
	"errors"
	"fmt"
	"io"
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

// AddLearning appends a learning event and rewrites .flywheel/learnings.md,
// holding .flywheel/feedback.lock across the whole mutation — read, append,
// reread, render — so two concurrent feedback commands serialise instead of
// interleaving (issue #260). The critical section is short and contains no
// worker run, so the lock is held for the entire mutation. The append error
// and the render error are returned separately: a failure after a successful
// append is not an append failure — the event is durable in the append-only
// log and the artifact is derived from it, so the learning is not lost.
// Because the mutation is serialised, the id computed from the events read
// under the lock is the id the append actually writes.
func AddLearning(dir, task, severity, title, observed, evidence, ask string, signals []string) (id, titleOut string, appendErr, renderErr error) {
	release, err := acquireRepoLock(dir, "feedback.lock")
	if err != nil {
		return "", "", err, nil
	}
	defer release()
	events, err := ReadEvents(dir)
	if err != nil {
		return "", "", err, nil
	}
	id = NextLearningID(events)
	if err := AppendEvent(dir, Event{
		Task: task, Kind: "learning", Severity: severity, Title: title,
		Observed: observed, Evidence: evidence, Ask: ask, Signals: signals,
	}); err != nil {
		return "", "", err, nil
	}
	events, err = ReadEvents(dir)
	if err != nil {
		return id, "", nil, err
	}
	views := Learnings(events)
	last := views[len(views)-1]
	if err := WriteLearningsFile(dir, views); err != nil {
		return last.ID, last.Title, nil, err
	}
	return last.ID, last.Title, nil, nil
}

// DismissLearning appends a dismissed event for an existing learning id and
// rewrites .flywheel/learnings.md, holding the feedback lock across the whole
// mutation exactly as AddLearning does (issue #260). An id the log does not
// know records nothing and returns an error naming it.
func DismissLearning(dir, id, reason string) (appendErr, renderErr error) {
	release, err := acquireRepoLock(dir, "feedback.lock")
	if err != nil {
		return err, nil
	}
	defer release()
	events, err := ReadEvents(dir)
	if err != nil {
		return err, nil
	}
	views := Learnings(events)
	var task string
	found := false
	for _, l := range views {
		if l.ID == id {
			task = l.Task
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown learning %q", id), nil
	}
	if _, err := CheckLearningsOwned(dir); err != nil {
		return err, nil
	}
	if err := AppendEvent(dir, Event{Task: task, Kind: "dismissed", ID: id, Note: reason}); err != nil {
		return err, nil
	}
	events, err = ReadEvents(dir)
	if err != nil {
		return nil, err
	}
	if err := WriteLearningsFile(dir, Learnings(events)); err != nil {
		return nil, err
	}
	return nil, nil
}

// RegenLearnings rebuilds .flywheel/learnings.md from the event log,
// appending nothing (issue #254): the artifact is derived from the log, so
// when the log is right and the file is wrong — for instance after a render
// failure — this reconciles them. The feedback lock is held across read and
// rewrite, the same critical section AddLearning and DismissLearning protect,
// so a regen never interleaves with a concurrent mutation. It succeeds when
// the file already matches, and refuses a hand-maintained file exactly as
// the other paths do.
func RegenLearnings(dir string) error {
	release, err := acquireRepoLock(dir, "feedback.lock")
	if err != nil {
		return err
	}
	defer release()
	events, err := ReadEvents(dir)
	if err != nil {
		return err
	}
	return WriteLearningsFile(dir, Learnings(events))
}

// learningsMarker is the line that proves flywheel generated a learnings.md:
// written immediately after the "# Learnings" title and matched on before any
// overwrite or delete. The filename alone never proves ownership — the first
// feedback add used to regenerate .flywheel/learnings.md and remove
// <dir>/learnings.md blindly, destroying hand-maintained files.
const learningsMarker = "<!-- generated by flywheel feedback; do not edit by hand -->"

// LearningsOwnership is the proof CheckLearningsOwned collected on
// .flywheel/learnings.md: the identity of the object the marker was read from.
// A nil info means the file did not exist at check time.
type LearningsOwnership struct {
	info os.FileInfo
}

// checkDotLearningsOwned proves .flywheel/learnings.md is flywheel's (absent or
// marked) and returns the identity of the object the marker was read from,
// taken from the same open handle the bytes came from.
func checkDotLearningsOwned(dir string) (*LearningsOwnership, error) {
	dot := filepath.Join(dir, ".flywheel", "learnings.md")
	f, err := os.Open(dot)
	if err != nil {
		if os.IsNotExist(err) {
			return &LearningsOwnership{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", dot, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dot, err)
	}
	if !strings.Contains(string(b), learningsMarker) {
		return nil, fmt.Errorf("%s exists without the flywheel marker and looks hand-maintained; move it aside (or delete it) and re-run — flywheel will not overwrite it", dot)
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dot, err)
	}
	return &LearningsOwnership{info: fi}, nil
}

// CheckLearningsOwned preflights feedback add and dismiss: .flywheel/learnings.md
// must be absent or marked (the error names the path and tells the operator to
// move it aside or delete it and re-run, so a refusal records nothing), and
// <dir>/learnings.md, when present, must be readable, so no write can fail on
// it after the event is appended. It returns the ownership proof; callers that
// only need the verdict ignore it.
func CheckLearningsOwned(dir string) (*LearningsOwnership, error) {
	own, err := checkDotLearningsOwned(dir)
	if err != nil {
		return nil, err
	}
	old := filepath.Join(dir, "learnings.md")
	if _, err := os.ReadFile(old); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", old, err)
	}
	return own, nil
}

// WriteLearningsFile rewrites .flywheel/learnings.md atomically (temp file plus
// rename) from views, in log order: a heading, the flywheel marker, then one
// section per learning with its severity, observed, evidence, ask, signals
// (when given) and dismissed reason (when dismissed). Every free-text field is
// sanitised before it is written, so a path or token never leaks into the
// artifact. The file lives under .flywheel/ so a repo's existing .flywheel
// handling covers it. A file flywheel did not generate is never touched: a
// hand-maintained .flywheel/learnings.md blocks the write with an error, and
// <dir>/learnings.md is removed only when the object at that path is the very
// one whose marker was read (re-checked with os.SameFile immediately before the
// remove; a replaced file is another writer's and is left alone). The
// .flywheel/learnings.md identity is re-checked immediately before the rename,
// and an unreadable <dir>/learnings.md is treated as not ours and left alone.
func WriteLearningsFile(dir string, views []LearningView) error {
	return writeLearningsFile(dir, views, os.Lstat)
}

// writeLearningsFile is WriteLearningsFile with the identity-check seam
// injected per operation, so a test can interleave a replacement between the
// temp write and the rename without touching a package global.
func writeLearningsFile(dir string, views []LearningView, lstat func(string) (os.FileInfo, error)) error {
	own, err := checkDotLearningsOwned(dir)
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# Learnings\n")
	b.WriteString(learningsMarker + "\n")
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
	dot := filepath.Join(dir, ".flywheel", "learnings.md")
	// The ownership proof is re-checked inside the atomic write, immediately
	// before the os.Rename, so nothing but the rename sits between the proof
	// and the mutation. A two-syscall window between the Lstat and the rename
	// is the best the standard library offers, and that is acceptable.
	if err := atomicWriteChecked(filepath.Join(dir, ".flywheel"), "learnings.md", "learnings-*.md", []byte(b.String()), func() error {
		return requireDotStillOwned(dot, own, lstat)
	}); err != nil {
		return err
	}
	return removeRootIfStillMarked(dir, lstat)
}

// requireDotStillOwned re-checks .flywheel/learnings.md immediately before the
// rename: the object must still be the one CheckLearningsOwned proved, or —
// when no file existed at check time — must still be absent, or now carry the
// marker. A file that appeared or was replaced in between belongs to another
// writer and is never clobbered. lstat is the identity-check seam.
func requireDotStillOwned(dot string, own *LearningsOwnership, lstat func(string) (os.FileInfo, error)) error {
	fi, err := lstat(dot)
	if own.info == nil {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stat %s: %w", dot, err)
		}
		b, err := os.ReadFile(dot)
		if err != nil {
			return fmt.Errorf("read %s: %w", dot, err)
		}
		if strings.Contains(string(b), learningsMarker) {
			return nil
		}
		return fmt.Errorf("%s appeared since the check without the flywheel marker and looks hand-maintained; move it aside (or delete it) and re-run — flywheel will not overwrite it", dot)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s disappeared between check and write; re-run to regenerate it", dot)
		}
		return fmt.Errorf("stat %s: %w", dot, err)
	}
	if !os.SameFile(own.info, fi) {
		return fmt.Errorf("%s changed between check and write; another writer owns it now — move it aside (or delete it) and re-run — flywheel will not overwrite it", dot)
	}
	return nil
}

// removeRootIfStillMarked deletes the migrated <dir>/learnings.md only when the
// object at that path is the very one whose marker was read: the identity is
// captured from the same open handle the bytes came from and os.SameFile is
// required against an Lstat taken immediately before the remove. A file
// replaced in between belongs to another writer and is left alone (not an
// error); a small window between the Lstat and the Remove is unavoidable with
// the standard library. An unreadable file is treated as not ours. lstat is
// the identity-check seam.
func removeRootIfStillMarked(dir string, lstat func(string) (os.FileInfo, error)) error {
	old := filepath.Join(dir, "learnings.md")
	f, err := os.Open(old)
	if err != nil {
		return nil // absent or unreadable: nothing to remove, or not ours
	}
	b, err := io.ReadAll(f)
	if err != nil {
		f.Close()
		return nil // unreadable: not ours, leave it untouched
	}
	if !strings.Contains(string(b), learningsMarker) {
		f.Close()
		return nil
	}
	fi, err := f.Stat()
	f.Close()
	if err != nil {
		return fmt.Errorf("stat %s: %w", old, err)
	}
	now, err := lstat(old)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // already gone; nothing to remove
		}
		return fmt.Errorf("stat %s: %w", old, err)
	}
	if !os.SameFile(fi, now) {
		return nil // replaced: another writer owns the path, leave it alone
	}
	if err := os.Remove(old); err != nil {
		return fmt.Errorf("remove %s: %w", old, err)
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
