package flywheel

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

func TestTUILiveQuitKey(t *testing.T) {
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

func TestTUILiveClosedKeys(t *testing.T) {
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

func TestTUILiveEnterFetchesExplain(t *testing.T) {
	keys := make(chan term.Key, 2)
	ticks := make(chan time.Time)

	keys <- term.Key{Kind: term.KeyEnter}
	keys <- term.Key{Kind: term.KeyRune, Rune: 'q'}
	close(keys)
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

	// Expect at least one fetch after Enter, which should Wants explain.
	foundExplain := false
	for _, w := range wantsList {
		if w.kind == "explain" && w.ok && w.task == "T1" {
			foundExplain = true
			break
		}
	}
	if !foundExplain {
		t.Errorf("no Wants(explain, T1, true) found in %d fetches", len(wantsList))
	}
}

func TestTUILiveFetchError(t *testing.T) {
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

	// Create a TUI model in log drill-down mode for T1.
	m := NewTUI()
	m.drillKind = "log"
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

	// Check Events has at least 3 (our 3 events; Init may add more).
	if len(data.Events) < 3 {
		t.Errorf("Events has %d lines, want >= 3", len(data.Events))
	}
}

// TestTUILiveQuitDoesNotRefetch checks that q ends the loop without another
// fetch, so a failing read on the way out cannot turn a clean quit into an
// error (#346 review).
func TestTUILiveQuitDoesNotRefetch(t *testing.T) {
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
