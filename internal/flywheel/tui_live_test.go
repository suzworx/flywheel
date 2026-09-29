package flywheel

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

func TestTUILiveQuitKey(t *testing.T) {
	t.Parallel()
	keys := make(chan term.Key, 1)
	keys <- term.Key{Kind: term.KeyRune, Rune: 'q'}
	close(keys)

	ticks := make(chan time.Time)
	out := &bytes.Buffer{}

	var fetchCount int
	fetch := func(m *TUI) (TUIData, error) {
		fetchCount++
		return makeTestTUIData(), nil
	}

	tio := TUIIO{
		Keys:  keys,
		Ticks: ticks,
		Size:  func() (int, int) { return 100, 20 },
		Out:   out,
		Color: false,
	}

	err := RunTUILoop(tio, fetch)
	if err != nil {
		t.Errorf("RunTUILoop returned error: %v", err)
	}

	outStr := out.String()
	if !strings.HasPrefix(outStr, "\x1b[H") {
		t.Errorf("output doesn't start with home cursor")
	}
	if fetchCount < 1 {
		t.Errorf("fetch not called, count = %d", fetchCount)
	}
}

// TestTUILiveCtrlRReloads checks that the fetch right after Ctrl-R, not the
// next tick, receives the reload request (issue #583).
func TestTUILiveCtrlRReloads(t *testing.T) {
	t.Parallel()
	// The fetches run in the background (issue #583 k6): q is queued by the
	// second, so the loop sees it only once that fetch ran.
	keys := make(chan term.Key, 1)
	keys <- term.Key{Kind: term.KeyCtrl, Rune: 'r'}
	var reloads []bool
	fetch := func(m *TUI) (TUIData, error) {
		reloads = append(reloads, m.TakeRefresh())
		if len(reloads) == 2 {
			keys <- term.Key{Kind: term.KeyRune, Rune: 'q'}
		}
		return makeTestTUIData(), nil
	}
	tio := TUIIO{Keys: keys, Ticks: make(chan time.Time), Size: func() (int, int) { return 100, 20 }, Out: &bytes.Buffer{}}
	if err := RunTUILoop(tio, fetch); err != nil {
		t.Fatalf("RunTUILoop: %v", err)
	}
	if len(reloads) != 2 || reloads[0] || !reloads[1] {
		t.Errorf("reload requests seen by the fetches = %v, want [false true]", reloads)
	}
}

func TestTUILiveClosedKeys(t *testing.T) {
	t.Parallel()
	keys := make(chan term.Key)
	close(keys)

	ticks := make(chan time.Time)
	out := &bytes.Buffer{}

	var fetchCount int
	fetch := func(m *TUI) (TUIData, error) {
		fetchCount++
		return makeTestTUIData(), nil
	}

	tio := TUIIO{
		Keys:  keys,
		Ticks: ticks,
		Size:  func() (int, int) { return 100, 20 },
		Out:   out,
		Color: false,
	}

	err := RunTUILoop(tio, fetch)
	if err != nil {
		t.Errorf("RunTUILoop returned error: %v", err)
	}
	if fetchCount < 1 {
		t.Errorf("fetch not called, count = %d", fetchCount)
	}
}

func TestTUILiveTickRefetches(t *testing.T) {
	t.Parallel()
	// Deterministic order: only the tick is ready at first; the second
	// fetch (the tick's) queues q. A key and a tick ready together would
	// let select pick either.
	keys := make(chan term.Key, 1)
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()

	var fetchCount int
	fetch := func(m *TUI) (TUIData, error) {
		fetchCount++
		if fetchCount == 2 {
			keys <- term.Key{Kind: term.KeyRune, Rune: 'q'}
		}
		return makeTestTUIData(), nil
	}

	tio := TUIIO{
		Keys:  keys,
		Ticks: ticks,
		Size:  func() (int, int) { return 100, 20 },
		Out:   &bytes.Buffer{},
	}
	if err := RunTUILoop(tio, fetch); err != nil {
		t.Errorf("RunTUILoop returned error: %v", err)
	}
	if fetchCount != 2 {
		t.Errorf("fetch called %d times, want 2 (the first frame and the tick)", fetchCount)
	}
}

func TestTUILiveEnterFetchesWhy(t *testing.T) {
	t.Parallel()
	// Enter starts a background fetch (issue #583 k6), which queues q.
	keys := make(chan term.Key, 1)
	ticks := make(chan time.Time)

	keys <- term.Key{Kind: term.KeyEnter}
	close(ticks)

	out := &bytes.Buffer{}

	var wantsList []struct {
		kind, task string
		ok         bool
	}

	fetch := func(m *TUI) (TUIData, error) {
		kind, task, ok := m.Wants()
		wantsList = append(wantsList, struct {
			kind, task string
			ok         bool
		}{kind, task, ok})
		if len(wantsList) == 2 {
			keys <- term.Key{Kind: term.KeyRune, Rune: 'q'}
		}
		return makeTestTUIData(), nil
	}

	tio := TUIIO{
		Keys:  keys,
		Ticks: ticks,
		Size:  func() (int, int) { return 100, 20 },
		Out:   out,
		Color: false,
	}

	err := RunTUILoop(tio, fetch)
	if err != nil {
		t.Errorf("RunTUILoop returned error: %v", err)
	}

	// Expect at least one fetch after Enter, which should Want the why tab.
	foundExplain := false
	for _, w := range wantsList {
		if w.kind == "why" && w.ok && w.task == "T1" {
			foundExplain = true
			break
		}
	}
	if !foundExplain {
		t.Errorf("no Wants(why, T1, true) found in %d fetches", len(wantsList))
	}
}

func TestTUILiveFetchError(t *testing.T) {
	t.Parallel()
	keys := make(chan term.Key)
	ticks := make(chan time.Time)
	close(keys)
	close(ticks)

	out := &bytes.Buffer{}

	expectedErr := errors.New("test error")
	fetch := func(m *TUI) (TUIData, error) {
		return TUIData{}, expectedErr
	}

	tio := TUIIO{
		Keys:  keys,
		Ticks: ticks,
		Size:  func() (int, int) { return 100, 20 },
		Out:   out,
		Color: false,
	}

	err := RunTUILoop(tio, fetch)
	if err != expectedErr {
		t.Errorf("expected %v, got %v", expectedErr, err)
	}
}

func TestTUILiveFetcherLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Initialize the flywheel state.
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	// Write events: Init (auto-created), then T1 r1 start+finish, T2 r1 start+finish.
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{TS: now.Format(time.RFC3339), Task: "T1", Kind: "started", Session: "s1"},
		{TS: now.Add(1 * time.Second).Format(time.RFC3339), Task: "T1", Kind: "finished", Attempt: "r1", RC: intPtr(0), Session: "s1"},
		{TS: now.Add(2 * time.Second).Format(time.RFC3339), Task: "T2", Kind: "started", Session: "s2"},
	}
	for _, e := range events {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}

	// The events tab keeps the unit's ledger events (issue #583 k7).
	m := NewTUI()
	m.drillKind = "unitevents"
	m.drillTask = "T1"

	// Get the fetcher and call it.
	fetcher := TUIFetcher(dir, func() time.Time { return now })
	data, err := fetcher(m)
	if err != nil {
		t.Fatalf("TUIFetcher failed: %v", err)
	}

	// Check Detail has exactly 2 lines (T1's two events).
	if len(data.Detail) != 2 {
		t.Errorf("Detail has %d lines, want 2 (T1 started and finished); got:\n%v", len(data.Detail), data.Detail)
	}

	// The log tab is T2's run, once a dispatch names its attempt and adapter.
	m.drillKind, m.drillTask = "log", "T2"
	if data, err = fetcher(m); err != nil {
		t.Fatalf("TUIFetcher failed: %v", err)
	}
	if len(data.Detail) != 1 || !strings.Contains(data.Detail[0], "not been dispatched") {
		t.Errorf("log of an undispatched unit = %q, want the no-run line", data.Detail)
	}
	if err := AppendEvent(dir, Event{TS: now.Add(3 * time.Second).Format(time.RFC3339), Task: "T2", Kind: "dispatched",
		Attempt: "r1", Model: "claude-opus-5-5", Adapter: "claude", Session: "s2"}); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}
	run := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"a.go"}}]},"session_id":"s"}` + "\n"
	if err := os.MkdirAll(filepath.Join(dir, ".flywheel", "runs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "runs", "T2.r1.jsonl"), []byte(run), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err = fetcher(m); err != nil {
		t.Fatalf("TUIFetcher failed: %v", err)
	}
	if got := strings.Join(data.Detail, "\n"); !strings.Contains(got, "attempt r1 on claude-opus-5-5 (claude)") || !strings.Contains(got, "#1 read a.go") {
		t.Errorf("log of T2 = %q, want its dispatch and its read step", data.Detail)
	}

	// Check Events has at least 3 (our 3 events; Init may add more).
	if len(data.Events) < 3 {
		t.Errorf("Events has %d lines, want >= 3", len(data.Events))
	}
}

// TestTUILiveFetcherSearch checks the live fetch (issue #583 k2): the needs
// and learnings come from the ledger, and `:s` searches the events, a run
// log, a report and a brief, naming each file's task and attempt.
func TestTUILiveFetcherSearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, e := range []Event{
		{TS: now.Format(time.RFC3339), Task: "T2", Kind: "planned", Needs: []string{"T1"}},
		{TS: now.Format(time.RFC3339), Task: "T2", Kind: "learning", Severity: "P2", Title: "Quota gone", Observed: "o", Evidence: "e", Ask: "a"},
	} {
		if err := AppendEvent(dir, e); err != nil {
			t.Fatalf("AppendEvent failed: %v", err)
		}
	}
	files := map[string]string{
		"runs/T2.3.jsonl":     "{\"a\":1}\r\n{\"text\":\"quota gone at step 4\"}\r\n",
		"runs/T2.3.report.md": "# report\nquota gone\n",
		"briefs/T2.txt":       "owns: x\n\nwhen the QUOTA GONE, stop\n",
	}
	for name, body := range files {
		path := filepath.Join(dir, ".flywheel", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := NewTUI()
	m.runCommand("s quota gone")
	data, err := TUIFetcher(dir, func() time.Time { return now })(m)
	if err != nil {
		t.Fatalf("TUIFetcher failed: %v", err)
	}
	if got := data.Needs["T2"]; len(got) != 1 || got[0] != "T1" {
		t.Errorf("needs of T2 = %v, want [T1]", got)
	}
	if len(data.Learnings) != 1 || data.Learnings[0].Title != "Quota gone" {
		t.Errorf("learnings = %+v", data.Learnings)
	}
	var got []string
	for _, h := range data.Search {
		got = append(got, fmt.Sprintf("%s %s %s %d", h.Source, h.Task, h.Attempt, h.Line))
	}
	joined := strings.Join(got, ", ")
	for _, want := range []string{"log T2 3 2", "report T2 3 2", "brief T2  3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("search results %q lack %q", joined, want)
		}
	}
	if !strings.HasPrefix(joined, "event T2 ") {
		t.Errorf("search results %q do not start with the learning event", joined)
	}
}

// TestTUILiveQuitDoesNotRefetch checks that q ends the loop without another
// fetch, so a failing read on the way out cannot turn a clean quit into an
// error (#346 review).
func TestTUILiveQuitDoesNotRefetch(t *testing.T) {
	t.Parallel()
	keys := make(chan term.Key, 1)
	keys <- term.Key{Kind: term.KeyRune, Rune: 'q'}
	calls := 0
	fetch := func(m *TUI) (TUIData, error) {
		calls++
		if calls > 1 {
			return TUIData{}, errors.New("log is being rotated")
		}
		return makeTestTUIData(), nil
	}
	tio := TUIIO{Keys: keys, Size: func() (int, int) { return 80, 20 }, Out: &bytes.Buffer{}}
	if err := RunTUILoop(tio, fetch); err != nil {
		t.Errorf("RunTUILoop() = %v, want nil after q", err)
	}
	if calls != 1 {
		t.Errorf("fetch called %d times, want 1", calls)
	}
}

// TestTUILiveStopEndsLoop checks that closing Stop (a signal from another
// process) ends the loop cleanly so RunTUI's restoration runs (#346 review).
func TestTUILiveStopEndsLoop(t *testing.T) {
	t.Parallel()
	stop := make(chan struct{})
	close(stop)
	tio := TUIIO{Keys: make(chan term.Key), Size: func() (int, int) { return 80, 20 }, Out: &bytes.Buffer{}, Stop: stop}
	done := make(chan error, 1)
	go func() { done <- RunTUILoop(tio, func(m *TUI) (TUIData, error) { return makeTestTUIData(), nil }) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("RunTUILoop() = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunTUILoop did not stop")
	}
}

// frameWriter hands every frame RunTUILoop writes to the test.
type frameWriter chan string

func (w frameWriter) Write(p []byte) (int, error) { w <- string(p); return len(p), nil }

// TestTUIInstantKeysWhileFetching checks issue #583 k6's instant keys: while
// a fetch blocks, a key still updates the model and redraws at once; Ctrl-R
// during it waits for it rather than fetching alongside; the fetch's data,
// when released, redraws; and no two fetches ever run together.
func TestTUIInstantKeysWhileFetching(t *testing.T) {
	t.Parallel()
	keys, ticks := make(chan term.Key), make(chan time.Time)
	frames, release := make(frameWriter, 16), make(chan TUIData)
	var calls, running, overlap atomic.Int32
	fetch := func(m *TUI) (TUIData, error) {
		n := calls.Add(1)
		if running.Add(1) > 1 {
			overlap.Add(1)
		}
		defer running.Add(-1)
		if n == 2 {
			return <-release, nil // the tick's fetch blocks until released
		}
		return makeTestTUIData(), nil
	}
	done := make(chan error, 1)
	go func() {
		done <- RunTUILoop(TUIIO{Keys: keys, Ticks: ticks, Size: func() (int, int) { return 100, 20 }, Out: frames}, fetch)
	}()
	// Hang guards only: nothing below waits on the clock.
	guard := func() <-chan time.Time { return time.After(10 * time.Second) }
	next := func(what string) string {
		t.Helper()
		select {
		case f := <-frames:
			return f
		case <-guard():
			t.Fatalf("no frame after %s", what)
		}
		return ""
	}
	send := func(k term.Key) {
		t.Helper()
		select {
		case keys <- k:
		case <-guard():
			t.Fatalf("the loop did not read the key %q", k.Rune)
		}
	}

	next("the first fetch")
	select {
	case ticks <- time.Now():
	case <-guard():
		t.Fatal("the loop did not read the tick")
	}
	next("the tick")
	send(term.Key{Kind: term.KeyRune, Rune: 'j'})
	if f := next("j while the fetch blocks"); !strings.Contains(f, "> T2") {
		t.Errorf("j while a fetch blocks: the cursor did not move to T2:\n%q", f)
	}
	send(term.Key{Kind: term.KeyCtrl, Rune: 'r'})
	next("Ctrl-R while the fetch blocks")

	d := makeTestTUIData()
	d.Floor.Units = append(d.Floor.Units, Unit{Task: "T4", Stage: "planned"})
	select {
	case release <- d:
	case <-guard():
		t.Fatal("the tick's fetch never ran")
	}
	if f := next("the released fetch"); !strings.Contains(f, "T4") {
		t.Errorf("the released fetch's data was not drawn:\n%q", f)
	}
	next("the reload's fetch")
	send(term.Key{Kind: term.KeyRune, Rune: 'q'})
	if err := <-done; err != nil {
		t.Errorf("RunTUILoop() = %v, want nil", err)
	}
	if calls.Load() != 3 || overlap.Load() != 0 {
		t.Errorf("fetches = %d with %d overlapping, want 3 (first, tick, reload) and none overlapping", calls.Load(), overlap.Load())
	}
}

// TestTUIAndonNextFetchFast checks the andon fetch on a large ledger (issue
// #583 k7 c1): 5,000 events, and the fetch ends well within a hang guard
// with NEXT decided from the events alone, never by Recover's world checks.
// The guard is 30s: this test is parallel, so it shares a host that other
// suites may saturate too (issue #619), and it still trips on a hang, which
// is what it guards; the fetch normally ends in well under a second.
func TestTUIAndonNextFetchFast(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	reasons := []string{"rate-limited", "error", "stop", "silent"}
	// 100 units of 25 attempts each, as a long-running factory's ledger
	// holds them; each unit's last attempt ends as reasons says.
	var b strings.Builder
	for n := 0; n < 2500; n++ {
		i, attempt := n%100, n/100+1
		task := fmt.Sprintf("T%04d", i)
		reason := "error"
		if attempt == 25 {
			reason = reasons[i%len(reasons)]
		}
		ts := now.Add(time.Duration(n) * time.Second)
		for _, e := range []Event{
			{TS: ts.Format(time.RFC3339), Task: task, Kind: "dispatched", Attempt: fmt.Sprintf("r%d", attempt), Model: "m", Adapter: "claude", Session: "s"},
			{TS: ts.Add(time.Second / 2).Format(time.RFC3339Nano), Task: task, Kind: "finished", Attempt: fmt.Sprintf("r%d", attempt), RC: intPtr(0), Reason: reason, Session: "s"},
		} {
			line, err := marshalEvent(e)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(line)
			b.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".flywheel", "events.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewTUI()
	typed(m, TUIData{}, ":a")
	fetch := TUIFetcher(dir, func() time.Time { return now.Add(3 * time.Hour) })
	type res struct {
		d   TUIData
		err error
	}
	done := make(chan res, 1)
	go func() { d, err := fetch(m); done <- res{d, err} }()
	var r res
	select {
	case r = <-done:
	case <-time.After(budget(30 * time.Second)):
		t.Fatalf("the andon fetch did not end within %s (race %v) on a 5,000-event ledger", budget(30*time.Second), raceEnabled)
	}
	if r.err != nil {
		t.Fatalf("fetch: %v", r.err)
	}
	_, rows := m.Rows(r.d)
	next := map[string]string{}
	for _, row := range rows {
		next[row[0]] = row[len(row)-1]
	}
	if got := next["T0000"]; got != "flywheel run T0000 --resume" {
		t.Errorf("rate-limited T0000's NEXT = %q, want flywheel run T0000 --resume (andon %d rows)", got, len(rows))
	}
	if got := next["T0001"]; !strings.Contains(got, "correct or dispatch again") {
		t.Errorf("failed T0001's NEXT = %q, want the correct-or-dispatch step", got)
	}
}
