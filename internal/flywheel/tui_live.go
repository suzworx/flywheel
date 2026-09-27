package flywheel

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
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
	Skin  Skin // the colours when Color is on; the zero Skin is DefaultSkin
	// Stop ends the loop cleanly when it is closed or receives (a signal
	// from another process, #346 review); nil never stops.
	Stop <-chan struct{}
}

// fetchResult is what a background fetch hands the loop: the data, the
// fetchKey of the model it was fetched for, and its error.
type fetchResult struct {
	data TUIData
	key  string
	err  error
}

// fetchKey names what a fetch for the model reads beyond the floor: the
// view, the drill-down, the search and the metrics window. A key that
// changes it starts a fetch.
func (m *TUI) fetchKey() string {
	return strings.Join([]string{m.view, m.drillKind, m.drillTask, m.SearchQuery(), m.MetricsWindow()}, "\x00")
}

// RunTUILoop drives the interactive factory until the model quits or Keys is
// closed (issue #583 k6: k9s's instant keys). The first frame waits for the
// first fetch; after it, this goroutine alone owns the model, and fetches run
// in a background goroutine on a copy of it, so a slow fetch never delays a
// key. A key updates the model and redraws at once from the last data; when
// it changed what the fetch reads (fetchKey) or asked for a reload (Ctrl-R),
// a fetch starts. A tick starts a fetch. At most one fetch runs at a time: a
// request while one runs starts another when it ends. A finished fetch
// becomes the data (Observe marks the rows it changed) and redraws; until a
// fetch for the drill-down shown arrives, the drill-down says "loading…".
// Each frame is written as "\x1b[H" + View(...) with every line ended by
// "\x1b[K" (clear to end of line) and the frame followed by "\x1b[J" (clear
// below). A fetch error ends the loop with that error; a quit key, closed
// Keys or Stop end it at once, without waiting for a fetch or starting one.
func RunTUILoop(tio TUIIO, fetch func(m *TUI) (TUIData, error)) error {
	m := NewTUI()
	if tio.Skin.Name != "" {
		m.skin = tio.Skin
	}
	data, err := fetch(m)
	if err != nil {
		return err
	}
	m.Observe(data)
	dataKey := m.fetchKey()

	// Buffered, so a fetch still running when the loop returns never blocks.
	results := make(chan fetchResult, 1)
	running, again := false, false
	start := func() {
		if running {
			again = true
			return
		}
		running = true
		snap := *m // the fetch reads its copy; the model stays this goroutine's
		m.refresh = false
		go func() {
			d, err := fetch(&snap)
			results <- fetchResult{data: d, key: snap.fetchKey(), err: err}
		}()
	}

	ticks := tio.Ticks
	for {
		// Draw the frame in one write (no flicker): home, each line cleared
		// to its end, then everything below cleared.
		shown := data
		if _, _, ok := m.Wants(); ok && dataKey != m.fetchKey() {
			shown.Detail = []string{"loading…"}
		}
		w, h := tio.Size()
		var frame strings.Builder
		frame.WriteString("\x1b[H")
		frame.WriteString(strings.ReplaceAll(m.View(shown, w, h, tio.Color), "\n", "\x1b[K\r\n"))
		frame.WriteString("\x1b[K\x1b[J")
		if _, err := io.WriteString(tio.Out, frame.String()); err != nil {
			return fmt.Errorf("draw the factory: %w", err)
		}

		select {
		case k, ok := <-tio.Keys:
			if !ok {
				return nil
			}
			before := m.fetchKey()
			m.Update(k, data)
			if m.Quit() {
				// No refresh on the way out: a failing read must not turn a
				// clean quit into an error (#346 review).
				return nil
			}
			if m.refresh || m.fetchKey() != before {
				start()
			}
		case _, ok := <-ticks:
			if !ok {
				ticks = nil // a closed tick channel never ticks again
				continue
			}
			start()
		case r := <-results:
			running = false
			if r.err != nil {
				return r.err
			}
			data, dataKey = r.data, r.key
			m.Observe(data)
			if again {
				again = false
				start()
			}
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
// in order. The header's context (issue #583) comes from the same events —
// FactorySuspended, the paused models, LatestHealth — plus IntegrationBranch
// and the pause threshold from the config, read once and again on a reload,
// and tuiVersion. A reload (m.TakeRefresh, Ctrl-R) starts a new Watcher, so
// the whole ledger is read afresh.
func TUIFetcher(dir string, now func() time.Time) func(m *TUI) (TUIData, error) {
	w := NewWatcher()
	var branch string
	var pauseAt float64
	var stall time.Duration
	loaded := false
	var (
		lastView   string
		cpCache    []TUICheckpoint
		cpLoaded   bool
		searchQ    string
		searchDone bool
		hits       []SearchHit
		capped     bool
		cpTask     string // the unit whose checkpoints unitCPs holds
		unitCPs    []string
		cfg        Config
		metrics    metricsCache // issue #583 k4: at most every metricsEvery
	)
	return func(m *TUI) (TUIData, error) {
		if m.TakeRefresh() {
			w = NewWatcher()
			loaded, cpLoaded, searchDone, cpTask = false, false, false, ""
			metrics = metricsCache{}
		}
		if !loaded {
			branch, _ = IntegrationBranch(dir)
			pauseAt = Limits{}.RateLimitPauseThreshold()
			cfg = Config{}
			if c, _, err := LoadConfig(dir); err == nil {
				cfg = c
				pauseAt = cfg.Limits.RateLimitPauseThreshold()
				stall = cfg.DefaultWorker().stallTimeoutDuration()
			}
			loaded = true
		}
		at := now()
		floor, err := w.Refresh(dir, at)
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
			Floor:   floor,
			Events:  eventLines,
			Detail:  nil,
			Branch:  branch,
			Suspend: FactorySuspended(events, at),
			Version: tuiVersion(),
		}
		models, pauses := pausedModels(events, at, pauseAt)
		for _, model := range models {
			data.Paused = append(data.Paused, TUIPause{Model: model, Until: pauses[model].Until})
		}
		if _, t, ok := LatestHealth(events); ok {
			data.HealthAt = t
		}

		// The navigation views (issue #583 k2). The checkpoints are read
		// from git as the view opens (and on a reload), the search runs as
		// its query changes (and on a reload): never on every key press.
		data.Needs = unitNeeds(events)
		data.Learnings = Learnings(events)
		for _, e := range events {
			if e.Kind == "health" && e.Health != nil {
				data.Health = append(data.Health, TUIHealth{TS: e.TS, Snap: *e.Health})
			}
		}
		data.Health = data.Health[max(0, len(data.Health)-200):]
		view := m.ViewName()
		if view == "checkpoints" && (lastView != "checkpoints" || !cpLoaded) {
			cps, err := ListCheckpoints(dir, "")
			if err != nil {
				return TUIData{}, fmt.Errorf("list checkpoints: %w", err)
			}
			cpCache, cpLoaded = checkpointAges(cps, events, at), true
		}
		lastView = view
		data.Checkpoints = cpCache
		if q := m.SearchQuery(); q != "" && (q != searchQ || !searchDone) {
			docs, err := searchDocs(dir, events)
			if err != nil {
				return TUIData{}, err
			}
			hits, capped = SearchDocs(docs, q, searchLimit)
			searchQ, searchDone = q, true
		}
		data.Search, data.SearchCapped = hits, capped

		// The metrics (issue #583 k4): the last 24h for the header, and the
		// window the metrics views show, each cached for metricsEvery.
		data.Metrics = map[string]TUIMetrics{}
		for _, name := range []string{"24h", m.MetricsWindow()} {
			if tm, ok := metrics.get(name, at, events, cfg); ok {
				data.Metrics[name] = tm
			}
		}

		// Every unit's why (issue #583 k3), from what the floor measured.
		data.Why = map[string]string{}
		for _, u := range floor.Units {
			ctx := WhyContext{RunState: u.RunState, Steps: u.Steps, StallTimeout: stall, PauseAt: pauseAt}
			if u.NeedsOwner > 0 {
				ctx.NeedsOwner = needsOwnerFindings(dir, events, u.Task)
			}
			data.Why[u.Task] = UnitWhy(events, u.Task, at, ctx)
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
			case "why":
				data.Detail = timelineLines(UnitTimeline(events, task))
			case "brief":
				data.Detail = briefLines(dir, events, task)
			case "unitcp":
				// Read from git as the tab opens (and on a reload), not on
				// every key press.
				if cpTask != task {
					cps, err := ListCheckpoints(dir, task)
					if err != nil {
						unitCPs = []string{fmt.Sprintf("checkpoints: %v", err)}
					} else {
						unitCPs = checkpointLines(cps)
					}
					cpTask = task
				}
				data.Detail = unitCPs
			case "findings":
				data.Detail = findingLines(events, task)
			case "unitevents":
				data.Detail = unitEventLines(events, task)
			}
		}

		return data, nil
	}
}

// briefLines is the text of task's brief, the file its latest planned,
// amended or dispatched event names.
func briefLines(dir string, events []Event, task string) []string {
	path := ""
	for _, e := range events {
		if e.Task == task && e.Brief != "" && (e.Kind == "planned" || e.Kind == "amended" || e.Kind == "dispatched") {
			path = e.Brief
		}
	}
	if path == "" {
		return []string{"brief: no event names " + task + "'s brief"}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("brief: %v", err)}
	}
	return strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
}

// tuiVersion is the running binary's module version as the build stamped
// it (a tagged build or go install), "dev" for a local build.
func tuiVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

// unitNeeds is each unit's needs, from its latest planned, amended or
// dispatched event (the parsed brief header when it carries one).
func unitNeeds(events []Event) map[string][]string {
	needs := map[string][]string{}
	for _, e := range events {
		switch e.Kind {
		case "planned", "amended", "dispatched":
			if e.Header != nil {
				needs[e.Task] = e.Header.Needs
			} else {
				needs[e.Task] = e.Needs
			}
		}
	}
	return needs
}

// checkpointAges pairs each checkpoint with its age at now: the time of the
// event that recorded its commit, -1 when none did.
func checkpointAges(cps []Checkpoint, events []Event, now time.Time) []TUICheckpoint {
	taken := map[string]time.Time{}
	for _, e := range events {
		if e.Checkpoint == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, e.TS); err == nil {
			taken[e.Checkpoint] = t
		}
	}
	out := make([]TUICheckpoint, len(cps))
	for i, c := range cps {
		out[i] = TUICheckpoint{Checkpoint: c, Age: -1}
		if t, ok := taken[c.SHA]; ok {
			out[i].Age = max(0, int(now.Sub(t).Seconds()))
		}
	}
	return out
}

// searchDocs gathers what `:s` searches: every event (its line number in
// the ledger), the run logs (.flywheel/runs/<task>.<attempt>.jsonl), the
// reports (<task>.<attempt>.report.md) and the briefs (.flywheel/briefs/
// <task>*.txt). A missing directory has nothing; a file removed since the
// listing is skipped.
func searchDocs(dir string, events []Event) ([]SearchDoc, error) {
	var docs []SearchDoc
	for i, e := range events {
		docs = append(docs, SearchDoc{Source: "event", Task: e.Task, Attempt: e.Attempt, First: i + 1, Lines: []string{HumanLine(e)}})
	}
	read := func(sub string, doc func(name string) (SearchDoc, bool)) error {
		path := filepath.Join(dir, ".flywheel", sub)
		entries, err := os.ReadDir(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("search %s: %w", path, err)
		}
		for _, ent := range entries {
			d, ok := doc(ent.Name())
			if ent.IsDir() || !ok {
				continue
			}
			data, err := os.ReadFile(filepath.Join(path, ent.Name()))
			if err != nil {
				continue
			}
			d.First = 1
			d.Lines = strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
			docs = append(docs, d)
		}
		return nil
	}
	err := read("runs", func(name string) (SearchDoc, bool) {
		source := "report"
		stem, ok := strings.CutSuffix(name, ".report.md")
		if !ok {
			source = "log"
			if stem, ok = strings.CutSuffix(name, ".jsonl"); !ok {
				return SearchDoc{}, false
			}
		}
		task, attempt := stem, ""
		if i := strings.LastIndex(stem, "."); i > 0 {
			task, attempt = stem[:i], stem[i+1:]
		}
		return SearchDoc{Source: source, Task: task, Attempt: attempt}, true
	})
	if err != nil {
		return nil, err
	}
	err = read("briefs", func(name string) (SearchDoc, bool) {
		if !strings.HasSuffix(name, ".txt") {
			return SearchDoc{}, false
		}
		task, _, _ := strings.Cut(name, ".")
		return SearchDoc{Source: "brief", Task: task}, true
	})
	return docs, err
}

// RunTUI runs the interactive factory on a real terminal: MakeRaw(stdin),
// the alternate screen ("\x1b[?1049h", left with "\x1b[?1049l"), a hidden
// cursor ("\x1b[?25l", shown again with "\x1b[?25h"), a goroutine reading
// term.NewReader(stdin, 0).ReadKey() into Keys (closing it on error), a
// time.Ticker at interval for Ticks, Size from term.Size(stdout) (80x24 on
// error), Color from term.EnableVT(stdout) and the skin factory.skin names
// (issue #583 k6; "none" draws no colour). It restores the terminal mode,
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

	// The skin factory.skin names; an unreadable config keeps the default,
	// and "none" turns colour off.
	skin := skins[DefaultSkin]
	if c, _, err := LoadConfig(dir); err == nil {
		if s, err := SkinFor(c.FactorySkin()); err == nil {
			skin = s
		}
	}

	// Run the loop.
	tio := TUIIO{
		Skin:  skin,
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
