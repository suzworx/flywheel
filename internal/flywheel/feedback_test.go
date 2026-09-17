package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLearningsAssignsSequentialIDs(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:00Z", Task: "t1", Kind: "learning",
		Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "runs/t1.r1.jsonl", Ask: "repeat",
		Signals: []string{"slow", "terse"}}); err != nil {
		t.Fatalf("AppendEvent() learning 1 error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:01Z", Task: "t1", Kind: "learning",
		Severity: "P2", Title: "Cache", Observed: "misses", Evidence: "runs", Ask: "warm"}); err != nil {
		t.Fatalf("AppendEvent() learning 2 error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	views := Learnings(events)
	if len(views) != 2 {
		t.Fatalf("Learnings() = %d views, want 2", len(views))
	}
	if views[0].ID != "L-01" || views[0].Severity != "P1" || views[0].Title != "Terse" {
		t.Errorf("Learnings()[0] = %+v, want L-01 P1 Terse", views[0])
	}
	if views[1].ID != "L-02" || views[1].Severity != "P2" || views[1].Title != "Cache" {
		t.Errorf("Learnings()[1] = %+v, want L-02 P2 Cache", views[1])
	}
	if got := NextLearningID(events); got != "L-03" {
		t.Errorf("NextLearningID() = %q, want L-03", got)
	}
}

func TestLearningsDismissMarksWithoutRenumbering(t *testing.T) {
	dir := t.TempDir()
	for i, e := range []Event{
		{TS: "2026-09-16T00:00:00Z", Task: "t1", Kind: "learning", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"},
		{TS: "2026-09-16T00:00:01Z", Task: "t1", Kind: "learning", Severity: "P2", Title: "Cache", Observed: "misses", Evidence: "e2", Ask: "a2"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent() learning %d error = %v", i, err)
		}
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:02Z", Task: "t1", Kind: "dismissed", ID: "L-01", Note: "fixed"}); err != nil {
		t.Fatalf("AppendEvent() dismissed error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	views := Learnings(events)
	if len(views) != 2 {
		t.Fatalf("Learnings() = %d views, want 2 (dismiss must not renumber)", len(views))
	}
	if !views[0].Dismissed || views[0].Reason != "fixed" {
		t.Errorf("Learnings()[0] = %+v, want dismissed with reason fixed", views[0])
	}
	if views[1].ID != "L-02" || views[1].Dismissed {
		t.Errorf("Learnings()[1] = %+v, want L-02 untouched", views[1])
	}
}

func TestLearningsDismissUnknownIDIsNoop(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:00Z", Task: "t1", Kind: "learning",
		Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"}); err != nil {
		t.Fatalf("AppendEvent() learning error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:01Z", Task: "t1", Kind: "dismissed", ID: "L-09", Note: "x"}); err != nil {
		t.Fatalf("AppendEvent() dismissed error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	views := Learnings(events)
	if len(views) != 1 || views[0].Dismissed {
		t.Errorf("Learnings() = %+v, want the sole learning untouched by an unknown dismiss target", views)
	}
}

func TestWriteLearningsFile(t *testing.T) {
	dir := t.TempDir()
	views := []LearningView{
		{ID: "L-01", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "runs/t1.r1.jsonl",
			Ask: "repeat", Signals: []string{"slow", "terse"}, Dismissed: true, Reason: "fixed"},
		{ID: "L-02", Severity: "P2", Title: "Cache", Observed: "misses", Evidence: "runs", Ask: "warm"},
	}
	if err := WriteLearningsFile(dir, views); err != nil {
		t.Fatalf("WriteLearningsFile() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "learnings.md"))
	if err != nil {
		t.Fatalf("read learnings.md: %v", err)
	}
	want := "# Learnings\n" +
		"\n## L-01 — Terse\n" +
		"severity: P1\n" +
		"observed: slow\n" +
		"evidence: runs/t1.r1.jsonl\n" +
		"ask: repeat\n" +
		"signals: slow, terse\n" +
		"dismissed: fixed\n" +
		"\n## L-02 — Cache\n" +
		"severity: P2\n" +
		"observed: misses\n" +
		"evidence: runs\n" +
		"ask: warm\n"
	if string(got) != want {
		t.Errorf("learnings.md =\n%s\nwant\n%s", got, want)
	}
}

func TestSanitise(t *testing.T) {
	cases := []struct{ in, want string }{
		{`run D:\secret-co\proj\scripts\validate.sh now`, "run <path> now"},
		{`see C:\Users\me\file.txt`, "see <path>"},
		{`copy D:/tools/build.exe`, "copy <path>"},
		{`log /home/me/proj/x.json`, "log <path>"},
		{`token ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA leaked`, "token <redacted> leaked"},
		{`key gho_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa here`, "key <redacted> here"},
		{`fetch ghs_012345678901234567890123`, "fetch <redacted>"},
		{`sk-abcdefghijklmnopqrstuvwxyz`, "<redacted>"},
		{`Authorization: Bearer abcdefghijklmnopqrstuvwxyz`, "Authorization: <redacted>"},
		{`password = "hunter2"`, "<redacted>"},
		{`token: abc123def456`, "<redacted>"},
		{`secret=super-secret-value`, "<redacted>"},
		{`key = 'quoted value'`, "<redacted>"},
		{`evidence .flywheel/runs/t1.r1.jsonl`, "evidence .flywheel/runs/t1.r1.jsonl"},
		{`path internal/foo/bar.go`, "path internal/foo/bar.go"},
		{`see https://github.com/suzworx/flywheel/issues/40`, "see https://github.com/suzworx/flywheel/issues/40"},
	}
	for _, c := range cases {
		if got := Sanitise(c.in); got != c.want {
			t.Errorf("Sanitise(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFeedbackReportExportsUndismissedSanitised(t *testing.T) {
	dir := t.TempDir()
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:00Z", Task: "t1", Kind: "learning", Severity: "P1",
		Title:    "Gate rots unobserved",
		Observed: "the harness at D:\\secret-co\\proj\\scripts\\validate.sh failed for days; token ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA was in the log",
		Evidence: ".flywheel/runs/t1.r1.jsonl", Ask: "record the commit a gate last passed at"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:01Z", Task: "t1", Kind: "learning", Severity: "P2",
		Title: "Dismissed leak", Observed: `C:\Users\owner\secret\x`, Evidence: "e2", Ask: "a2"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := AppendEvent(dir, Event{TS: "2026-09-16T00:00:02Z", Task: "t1", Kind: "dismissed", ID: "L-02", Note: "done"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	report := FeedbackReport("dev", Learnings(events))
	if !strings.Contains(report, "## L-01 — Gate rots unobserved") {
		t.Errorf("report does not include the undismissed learning:\n%s", report)
	}
	if strings.Contains(report, "Dismissed leak") {
		t.Errorf("report includes a dismissed learning:\n%s", report)
	}
	for _, leak := range []string{"secret-co", "ghp_AAAA", "C:\\Users\\owner"} {
		if strings.Contains(report, leak) {
			t.Errorf("report leaked %q:\n%s", leak, report)
		}
	}
	if !strings.Contains(report, "<path>") || !strings.Contains(report, "<redacted>") {
		t.Errorf("report lacks the redaction markers:\n%s", report)
	}
	if !strings.Contains(report, "observed: the harness at <path> failed for days; token <redacted> was in the log") {
		t.Errorf("report did not sanitise observed in place:\n%s", report)
	}
	if !strings.Contains(report, ".flywheel/runs/t1.r1.jsonl") {
		t.Errorf("report mangled the repo-relative evidence path:\n%s", report)
	}
}

func TestWriteFeedbackReportMatchesPrint(t *testing.T) {
	dir := t.TempDir()
	views := []LearningView{{ID: "L-01", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"}}
	report := FeedbackReport("dev", views)
	path := filepath.Join(dir, "out", "report.md")
	if err := WriteFeedbackReport(path, report); err != nil {
		t.Fatalf("WriteFeedbackReport() error = %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(b) != report {
		t.Errorf("file content = %q, want the printed report %q", b, report)
	}
}

func TestFeedbackSubmitNeverIsRuleRefusal(t *testing.T) {
	cfg := Config{Version: 1, Workers: []Worker{{Name: "d", Adapter: "sim", Model: "m"}},
		Feedback: Feedback{Upstream: "owner/repo", Submit: "never"}}
	_, err := FeedbackSubmit(t.TempDir(), cfg, "dev", nil, FeedbackSubmitOptions{Yes: true})
	if !IsRuleRefusal(err) {
		t.Fatalf("FeedbackSubmit() error = %v, want a RuleRefusal", err)
	}
	if !strings.Contains(err.Error(), "never") {
		t.Errorf("RuleRefusal %q does not name the feedback.submit setting", err)
	}
}

func TestFeedbackSubmitNoUpstreamIsUsage(t *testing.T) {
	cfg := Config{Version: 1, Workers: []Worker{{Name: "d", Adapter: "sim", Model: "m"}},
		Feedback: Feedback{Submit: "ask"}}
	_, err := FeedbackSubmit(t.TempDir(), cfg, "dev", nil, FeedbackSubmitOptions{Yes: true})
	if !errors.Is(err, ErrFeedbackNoUpstream) {
		t.Fatalf("FeedbackSubmit() error = %v, want ErrFeedbackNoUpstream", err)
	}
}

func TestFeedbackSubmitWithoutYesSendsNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "d", Adapter: "sim", Model: "m"}},
		Feedback: Feedback{Upstream: "owner/repo", Submit: "ask"}}
	views := []LearningView{{ID: "L-01", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"}}
	called := false
	res, err := FeedbackSubmit(dir, cfg, "dev", views, FeedbackSubmitOptions{
		Yes: false, Gh: func(...string) error { called = true; return nil },
	})
	if err != nil {
		t.Fatalf("FeedbackSubmit() error = %v", err)
	}
	if !res.NotSent {
		t.Errorf("FeedbackSubmit() = %+v, want consent withheld", res)
	}
	if res.Sent || res.Outbox != "" {
		t.Errorf("FeedbackSubmit() = %+v, want nothing sent and no outbox", res)
	}
	if called {
		t.Error("gh was called without consent")
	}
	if !strings.Contains(res.Report, "L-01") {
		t.Errorf("Report %q does not show the learning it would send", res.Report)
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "feedback", "outbox")); !os.IsNotExist(err) {
		t.Error("outbox was created without consent")
	}
}

func TestFeedbackSubmitParksReportWhenSendFails(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "d", Adapter: "sim", Model: "m"}},
		Feedback: Feedback{Upstream: "owner/repo", Submit: "ask"}}
	views := []LearningView{{ID: "L-01", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"}}
	now := time.Date(2026, 9, 16, 10, 30, 0, 0, time.UTC)
	res, err := FeedbackSubmit(dir, cfg, "dev", views, FeedbackSubmitOptions{
		Yes: true,
		Gh:  func(...string) error { return errors.New("gh missing") },
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("FeedbackSubmit() error = %v", err)
	}
	if res.Sent {
		t.Errorf("FeedbackSubmit() = %+v, want the send to have failed without an issue", res)
	}
	if res.Outbox == "" {
		t.Fatal("FeedbackSubmit() did not park the report in the outbox")
	}
	if want := filepath.Join(dir, ".flywheel", "feedback", "outbox", "20260916-103000-L-01.md"); res.Outbox != want {
		t.Errorf("Outbox = %q, want %q", res.Outbox, want)
	}
	b, err := os.ReadFile(res.Outbox)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if string(b) != res.Report {
		t.Errorf("outbox content = %q, want the exact report", b)
	}
}

func TestFeedbackSubmitSendsViaGH(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Version: 1, Workers: []Worker{{Name: "d", Adapter: "sim", Model: "m"}},
		Feedback: Feedback{Upstream: "owner/repo", Submit: "ask"}}
	views := []LearningView{{ID: "L-01", Severity: "P1", Title: "Terse", Observed: "slow", Evidence: "e1", Ask: "a1"}}
	var got []string
	res, err := FeedbackSubmit(dir, cfg, "dev", views, FeedbackSubmitOptions{
		Yes: true, Gh: func(args ...string) error { got = append(got, args...); return nil },
	})
	if err != nil {
		t.Fatalf("FeedbackSubmit() error = %v", err)
	}
	if !res.Sent || res.Outbox != "" {
		t.Errorf("FeedbackSubmit() = %+v, want sent with no outbox", res)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{"issue", "create", "--repo", "owner/repo", "--title", "--body-file"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh args %q do not contain %q", got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".flywheel", "feedback", "outbox")); !os.IsNotExist(err) {
		t.Error("outbox was created on a successful send")
	}
}
