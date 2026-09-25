package flywheel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLinesValidate checks that Config.Validate catches line errors: duplicate
// names, unknown workers, empty names.
func TestLinesValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "good config with one line",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "default", Adapter: "sim", Model: "model"}},
				Lines: []Line{
					{Name: "cli", Worker: "default", Owns: []string{"cmd/", "internal/"}},
				},
			},
			wantErr: false,
		},
		{
			name: "duplicate line names",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "default", Adapter: "sim", Model: "model"}},
				Lines: []Line{
					{Name: "cli", Worker: "default"},
					{Name: "cli", Worker: "default"},
				},
			},
			wantErr: true,
			errMsg:  "duplicate name",
		},
		{
			name: "line references unknown worker",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "default", Adapter: "sim", Model: "model"}},
				Lines: []Line{
					{Name: "cli", Worker: "unknown"},
				},
			},
			wantErr: true,
			errMsg:  "worker \"unknown\" is not in workers[]",
		},
		{
			name: "empty line name",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "default", Adapter: "sim", Model: "model"}},
				Lines: []Line{
					{Name: "", Worker: "default"},
				},
			},
			wantErr: true,
			errMsg:  "name must not be empty",
		},
		{
			name: "line name with invalid characters",
			cfg: Config{
				Version: 1,
				Workers: []Worker{{Name: "default", Adapter: "sim", Model: "model"}},
				Lines: []Line{
					{Name: "CLI", Worker: "default"},
				},
			},
			wantErr: true,
			errMsg:  "must match ^[a-z0-9][a-z0-9-]*$",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr && err == nil {
				t.Error("Validate() error = nil, want non-nil")
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("Validate() error = %v, want to contain %q", err, tt.errMsg)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("Validate() error = %v, want nil", err)
			}
		})
	}
}

// TestLinesExplicitHeaderWins checks that a header's explicit line: wins over
// matching by owns:.
func TestLinesExplicitHeaderWins(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "cli-worker", Adapter: "sim", Model: "model"},
			{Name: "docs-worker", Adapter: "sim", Model: "model"},
		},
		Lines: []Line{
			{Name: "cli", Worker: "cli-worker", Owns: []string{"internal/"}},
			{Name: "docs", Worker: "docs-worker", Owns: []string{"docs/"}},
		},
	}

	// Header owns internal/ (would match cli), but line: docs says use docs.
	h := BriefHeader{
		Line: "docs",
		Owns: []string{"internal/a.go"},
	}
	line, ok, err := cfg.LineFor(h)
	if err != nil {
		t.Errorf("LineFor() error = %v, want nil", err)
	}
	if !ok {
		t.Error("LineFor() ok = false, want true")
	}
	if line.Name != "docs" {
		t.Errorf("LineFor() line.Name = %q, want %q", line.Name, "docs")
	}
}

// TestLinesMatchByOwns checks that LineFor finds a line by owns: matching when
// no line: is specified.
func TestLinesMatchByOwns(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "cli-worker", Adapter: "sim", Model: "model"},
			{Name: "docs-worker", Adapter: "sim", Model: "model"},
		},
		Lines: []Line{
			{Name: "cli", Worker: "cli-worker", Owns: []string{"internal/"}},
			{Name: "docs", Worker: "docs-worker", Owns: []string{"docs/"}},
		},
	}

	h := BriefHeader{
		Owns: []string{"internal/a.go", "internal/b.go"},
	}
	line, ok, err := cfg.LineFor(h)
	if err != nil {
		t.Errorf("LineFor() error = %v, want nil", err)
	}
	if !ok {
		t.Error("LineFor() ok = false, want true")
	}
	if line.Name != "cli" {
		t.Errorf("LineFor() line.Name = %q, want %q", line.Name, "cli")
	}
}

// TestLinesMixedOwnsNoLine checks that a header with owns from multiple
// lines returns no line.
func TestLinesMixedOwnsNoLine(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "cli-worker", Adapter: "sim", Model: "model"},
			{Name: "docs-worker", Adapter: "sim", Model: "model"},
		},
		Lines: []Line{
			{Name: "cli", Worker: "cli-worker", Owns: []string{"internal/"}},
			{Name: "docs", Worker: "docs-worker", Owns: []string{"docs/"}},
		},
	}

	h := BriefHeader{
		Owns: []string{"internal/a.go", "README.md"},
	}
	line, ok, err := cfg.LineFor(h)
	if err != nil {
		t.Errorf("LineFor() error = %v, want nil", err)
	}
	if ok {
		t.Error("LineFor() ok = true, want false")
	}
	if line.Name != "" {
		t.Errorf("LineFor() returned line.Name = %q, want empty", line.Name)
	}
}

// TestLinesUnknownLineErrors checks that a header naming an unknown line:
// returns an error.
func TestLinesUnknownLineErrors(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "default", Adapter: "sim", Model: "model"},
		},
		Lines: []Line{
			{Name: "cli", Worker: "default"},
		},
	}

	h := BriefHeader{
		Line: "nope",
	}
	line, ok, err := cfg.LineFor(h)
	if err == nil {
		t.Error("LineFor() error = nil, want non-nil")
	}
	if ok {
		t.Error("LineFor() ok = true, want false")
	}
	if line.Name != "" {
		t.Errorf("LineFor() returned line.Name = %q, want empty", line.Name)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("LineFor() error = %v, want to mention %q", err, "nope")
	}
}

// TestLinesParseHeader checks that ParseBriefHeader parses line: headers.
func TestLinesParseHeader(t *testing.T) {
	t.Parallel()
	briefContent := `owns: docs/
line: docs
gate: some command

# Brief

Some content here.
`
	h, err := ParseBriefHeaderBytes([]byte(briefContent))
	if err != nil {
		t.Errorf("ParseBriefHeaderBytes() error = %v", err)
	}
	if h.Line != "docs" {
		t.Errorf("ParseBriefHeaderBytes() line = %q, want %q", h.Line, "docs")
	}
	if len(h.Owns) != 1 || h.Owns[0] != "docs/" {
		t.Errorf("ParseBriefHeaderBytes() owns = %v, want [docs/]", h.Owns)
	}
}

// TestLinesRunUsesLineWorker checks that Run uses the line's worker when
// --worker is not specified.
func TestLinesRunUsesLineWorker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	briefContent := `owns: docs/x.md
line: docs

# Brief

Do the thing.
`
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(briefContent), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "default", Adapter: "sim", Model: fixturePath("clean.jsonl", t)},
			{Name: "docsw", Adapter: "sim", Model: fixturePath("clean.jsonl", t)},
		},
		Lines: []Line{
			{Name: "docs", Worker: "docsw", Owns: []string{"docs/"}},
		},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	res, err := Run(dir, RunOptions{Task: "T1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Attempt == "" {
		t.Fatal("Run() returned empty attempt")
	}

	// Check that the dispatched event has Line "docs".
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	var dispatchedEvent *Event
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "dispatched" {
			dispatchedEvent = &e
			break
		}
	}
	if dispatchedEvent == nil {
		t.Fatal("Run() did not record a dispatched event")
	}
	if dispatchedEvent.Line != "docs" {
		t.Errorf("dispatched event Line = %q, want %q", dispatchedEvent.Line, "docs")
	}
	// Verify the docsw worker was used by checking its model in the event.
	docsWorker, _ := cfg.Worker("docsw")
	if dispatchedEvent.Model != docsWorker.Model {
		t.Errorf("dispatched event Model = %q, want %q", dispatchedEvent.Model, docsWorker.Model)
	}
}

// TestLinesRunUnknownLineRefused checks that Run refuses a brief with an
// unknown line: header.
func TestLinesRunUnknownLineRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	briefContent := `owns: docs/x.md
line: nope

# Brief

Do the thing.
`
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte(briefContent), 0o644); err != nil {
		t.Fatalf("write brief: %v", err)
	}

	if err := AppendEvent(dir, Event{TS: "2026-09-19T00:00:00Z", Task: "T1", Kind: "planned", Brief: "b.txt"}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	cfg := Config{
		Version: 1,
		Workers: []Worker{
			{Name: "default", Adapter: "sim", Model: fixturePath("clean.jsonl", t)},
		},
		Lines: []Line{
			{Name: "docs", Worker: "default"},
		},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	_, err := Run(dir, RunOptions{Task: "T1"})
	if err == nil {
		t.Fatal("Run() error = nil, want non-nil (RuleRefusal for unknown line)")
	}
	var refusal *RuleRefusal
	if !errors.As(err, &refusal) {
		t.Errorf("Run() error = %v, want RuleRefusal", err)
	}
	if refusal != nil && refusal.Rule != "line" {
		t.Errorf("Run() error rule = %q, want %q", refusal.Rule, "line")
	}
}

// linesRunDir plans T1 with a brief owning docs/x.md on a config whose two sim
// workers replay different fixture paths, so the dispatched Model tells which
// worker ran; docs is the line of worker docsw.
func linesRunDir(t *testing.T) (dir, defaultModel, docsModel string) {
	t.Helper()
	dir = t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("owns: docs/x.md\n\n# TASK: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RecordPlanned(dir, "T1", "b.txt"); err != nil {
		t.Fatal(err)
	}
	defaultModel = fixturePath("clean.jsonl", t)
	b, err := os.ReadFile(defaultModel)
	if err != nil {
		t.Fatal(err)
	}
	docsModel = filepath.Join(t.TempDir(), "docs-clean.jsonl")
	if err := os.WriteFile(docsModel, b, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1,
		Workers: []Worker{{Name: "default", Adapter: "sim", Model: defaultModel}, {Name: "docsw", Adapter: "sim", Model: docsModel}},
		Lines:   []Line{{Name: "docs", Worker: "docsw", Owns: []string{"docs/"}}},
	}
	if err := WriteConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	return dir, defaultModel, docsModel
}

// dispatchedT1 returns T1's dispatched event.
func dispatchedT1(t *testing.T, dir string) Event {
	t.Helper()
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Task == "T1" && e.Kind == "dispatched" {
			return e
		}
	}
	t.Fatal("no dispatched event for T1")
	return Event{}
}

// TestLinesRunStaffsFromLineByOwns checks that a unit matched to a line by its
// owns runs on the line's worker (its model is the one dispatched).
func TestLinesRunStaffsFromLineByOwns(t *testing.T) {
	t.Parallel()
	dir, _, docsModel := linesRunDir(t)
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatal(err)
	}
	if e := dispatchedT1(t, dir); e.Line != "docs" || e.Model != docsModel {
		t.Errorf("dispatched line %q model %q, want docs on %q", e.Line, e.Model, docsModel)
	}
}

// TestLinesExplicitWorkerKeepsLine checks that --worker wins over the line's
// worker, and the dispatch still records the unit's line.
func TestLinesExplicitWorkerKeepsLine(t *testing.T) {
	t.Parallel()
	dir, defaultModel, _ := linesRunDir(t)
	if _, err := Run(dir, RunOptions{Task: "T1", Worker: "default"}); err != nil {
		t.Fatal(err)
	}
	if e := dispatchedT1(t, dir); e.Line != "docs" || e.Model != defaultModel {
		t.Errorf("dispatched line %q model %q, want docs on the explicit worker's %q", e.Line, e.Model, defaultModel)
	}
}

// TestLinesUnknownLineWithoutConfiguredLines checks that a brief naming a line
// is refused when the config has no lines at all (#340 review).
func TestLinesUnknownLineWithoutConfiguredLines(t *testing.T) {
	t.Parallel()
	dir := setupTask(t)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("owns: a.go\nline: docs\n\n# TASK: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatal(err)
	}
	_, err := Run(dir, RunOptions{Task: "T1"})
	var r *RuleRefusal
	if !errors.As(err, &r) || r.Rule != "line" {
		t.Errorf("Run with line: docs and no lines = %v, want rule line", err)
	}
}

// TestLinesEditedBriefUsesCurrentLine checks that the line comes from the
// brief as it is now, not the header recorded when it was planned (#340
// review): planned on no line, edited to line: docs before the run.
func TestLinesEditedBriefUsesCurrentLine(t *testing.T) {
	t.Parallel()
	dir, _, docsModel := linesRunDir(t)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("owns: other/x.md\nline: docs\n\n# TASK: t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, RunOptions{Task: "T1"}); err != nil {
		t.Fatal(err)
	}
	if e := dispatchedT1(t, dir); e.Line != "docs" || e.Model != docsModel {
		t.Errorf("dispatched line %q model %q, want docs on %q", e.Line, e.Model, docsModel)
	}
}
