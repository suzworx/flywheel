package flywheel

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/suzworx/flywheel/internal/term"
)

// TUIIO is what the live loop talks to; tests fake every part.
type TUIIO struct {
	Keys  <-chan term.Key  // decoded key presses; closed when input ends
	Ticks <-chan time.Time // refresh ticks
	Size  func() (width, height int)
	Out   io.Writer
	Color bool
	// Stop ends the loop cleanly when it is closed or receives (a signal
	// from another process, #346 review); nil never stops.
	Stop <-chan struct{}
}

// RunTUILoop drives the interactive factory until the model quits or Keys is
// closed: fetch the data, draw a frame, then wait for a key (Update, and
// fetch again when the drill-down it Wants changed) or a tick (fetch again),
// and draw again. Each frame is written as "\x1b[H" + View(...) with every
// line ended by "\x1b[K" (clear to end of line) and the frame followed by
// "\x1b[J" (clear below). A fetch error ends the loop with that error; a
// quit key or Stop ends it at once, without another fetch.
func RunTUILoop(tio TUIIO, fetch func(m *TUI) (TUIData, error)) error {
	m := NewTUI()
	for {
		// Fetch data.
		data, err := fetch(m)
		if err != nil {
			return err
		}

		// Draw the frame in one write (no flicker): home, each line cleared
		// to its end, then everything below cleared.
		w, h := tio.Size()
		var frame strings.Builder
		frame.WriteString("\x1b[H")
		frame.WriteString(strings.ReplaceAll(m.View(data, w, h, tio.Color), "\n", "\x1b[K\r\n"))
		frame.WriteString("\x1b[K\x1b[J")
		if _, err := io.WriteString(tio.Out, frame.String()); err != nil {
			return fmt.Errorf("draw the factory: %w", err)
		}

		// Check if the user quit.
		if m.Quit() {
			return nil
		}

		// Wait for next key or tick.
		select {
		case k, ok := <-tio.Keys:
			if !ok {
				// Keys closed, exit.
				return nil
			}
			m.Update(k, data)
			if m.Quit() {
				// No refresh on the way out: a failing read must not turn a
				// clean quit into an error (#346 review).
				return nil
			}
		case <-tio.Ticks:
			// Tick: fetch again.
		case <-tio.Stop:
			return nil
		}
	}
}

// TUIFetcher returns the fetch function the live factory uses for dir: it
// refreshes a Watcher (w.Refresh(dir, now())), reads the event log
// (ReadEvents) and fills Events with the last 200 events as HumanLine,
// and Detail when m.Wants(): "explain" → RenderExplanation(Explain(events,
// task)) into a buffer, split into lines (an Explain error becomes the one
// line "explain: <error>"); "log" → HumanLine of every event of that task,
// in order.
func TUIFetcher(dir string, now func() time.Time) func(m *TUI) (TUIData, error) {
	w := NewWatcher()
	return func(m *TUI) (TUIData, error) {
		floor, err := w.Refresh(dir, now())
		if err != nil {
			return TUIData{}, err
		}

		// The watcher already holds the whole log, read incrementally by
		// Refresh: no second full read on every key press.
		events := w.events

		// Recent events (last 200) as HumanLine.
		var eventLines []string
		start := len(events) - 200
		if start < 0 {
			start = 0
		}
		for i := start; i < len(events); i++ {
			eventLines = append(eventLines, HumanLine(events[i]))
		}

		data := TUIData{
			Floor:  floor,
			Events: eventLines,
			Detail: nil,
		}

		// Fill Detail if Wants drill-down.
		kind, task, ok := m.Wants()
		if ok {
			switch kind {
			case "explain":
				explain, err := Explain(events, task)
				if err != nil {
					data.Detail = []string{fmt.Sprintf("explain: %v", err)}
				} else {
					buf := &bytes.Buffer{}
					if err := RenderExplanation(buf, explain); err != nil {
						data.Detail = []string{fmt.Sprintf("explain: %v", err)}
					} else {
						// Split rendered explanation into lines.
						for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
							data.Detail = append(data.Detail, string(line))
						}
					}
				}
			case "log":
				// All events of the task in order.
				for _, e := range events {
					if e.Task == task {
						data.Detail = append(data.Detail, HumanLine(e))
					}
				}
			}
		}

		return data, nil
	}
}

// RunTUI runs the interactive factory on a real terminal: MakeRaw(stdin),
// the alternate screen ("\x1b[?1049h", left with "\x1b[?1049l"), a hidden
// cursor ("\x1b[?25l", shown again with "\x1b[?25h"), a goroutine reading
// term.NewReader(stdin, 0).ReadKey() into Keys (closing it on error), a
// time.Ticker at interval for Ticks, Size from term.Size(stdout) (80x24 on
// error), Color from term.EnableVT(stdout). It restores the terminal mode,
// cursor and screen on every return path, a panic included (defer), and on
// SIGINT or SIGTERM from another process, which stop the loop instead of
// killing the process with the terminal still raw (#346 review). A failed
// restoration is returned when nothing else failed first.
func RunTUI(stdin, stdout *os.File, dir string, interval time.Duration, now func() time.Time) (err error) {
	restore, err := term.MakeRaw(stdin)
	if err != nil {
		return fmt.Errorf("raw mode on %s: %w", stdin.Name(), err)
	}
	defer func() {
		if rerr := restore(); rerr != nil && err == nil {
			err = fmt.Errorf("restore the terminal mode: %w", rerr)
		}
	}()

	// Enter alternate screen and hide cursor; leave both on the way out.
	if _, err := fmt.Fprint(stdout, "\x1b[?1049h\x1b[?25l"); err != nil {
		return fmt.Errorf("enter the alternate screen: %w", err)
	}
	defer func() {
		if _, werr := fmt.Fprint(stdout, "\x1b[?25h\x1b[?1049l"); werr != nil && err == nil {
			err = fmt.Errorf("leave the alternate screen: %w", werr)
		}
	}()

	// A signal from another process stops the loop so the defers run.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	stop := make(chan struct{})
	go func() {
		if _, ok := <-sigs; ok {
			close(stop)
		}
	}()

	// Set up input channel.
	keys := make(chan term.Key)
	go func() {
		r := term.NewReader(stdin, 0)
		defer close(keys)
		for {
			k, err := r.ReadKey()
			if err != nil {
				return
			}
			keys <- k
		}
	}()

	// Set up ticker.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run the loop.
	tio := TUIIO{
		Keys:  keys,
		Ticks: ticker.C,
		Size: func() (int, int) {
			w, h, err := term.Size(stdout)
			if err != nil {
				return 80, 24
			}
			return w, h
		},
		Out:   stdout,
		Color: term.EnableVT(stdout),
		Stop:  stop,
	}
	return RunTUILoop(tio, TUIFetcher(dir, now))
}
