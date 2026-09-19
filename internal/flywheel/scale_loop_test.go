package flywheel

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestScaleFactoryLoop drives N tasks through the whole factory loop in one
// ledger (issue #48): the reconciler picks what to dispatch, the sim adapter
// runs it, the gauges measure it, a lead session inspects and lands it, and
// the log is read back as flywheel watch reads it. N is 8, or FLYWHEEL_SCALE
// when set (CI runs 1000); 5 under -short when FLYWHEEL_SCALE is unset.
func TestScaleFactoryLoop(t *testing.T) {
	// Determine N from environment or defaults
	n := 8
	if testing.Short() && os.Getenv("FLYWHEEL_SCALE") == "" {
		n = 5
	} else if env := os.Getenv("FLYWHEEL_SCALE"); env != "" {
		var err error
		n, err = strconv.Atoi(env)
		if err != nil {
			t.Fatalf("FLYWHEEL_SCALE invalid: %v", err)
		}
	}

	start := time.Now()
	dir := t.TempDir()
	if _, err := Init(dir, false); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	initRepo(t, dir)

	// Write .gitignore so flywheel bookkeeping is excluded from the unit tree
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".flywheel/\nflywheel.md\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	git(t, dir, []string{"add", ".gitignore"})
	git(t, dir, []string{"commit", "-m", "init"})
	headSha := git(t, dir, []string{"rev-parse", "HEAD"})

	// Write config with sim adapter and MaxParallel=8
	config := simConfig(fixturePath("clean.jsonl", t))
	config.Workers[0].MaxParallel = 8
	if err := WriteConfig(dir, config); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Plan tasks L0001..L<n>
	for i := 1; i <= n; i++ {
		taskID := fmt.Sprintf("L%04d", i)
		// One brief per task, each owning its own file: concurrent units
		// with overlapping owns are refused by run (and #322 makes next
		// aware of it). The real planning path parses the header. Briefs
		// live under .flywheel/briefs/ as flywheel's own do: outside the
		// unit's tree, so a validation never hashes N brief files (in the
		// repo root they made the loop quadratic and CI timed out at 1,000).
		brief := ".flywheel/briefs/" + taskID + ".txt"
		text := "owns: f" + taskID + ".txt\nneeds: none\ngate: true\n\n# TASK: t\n"
		if err := os.WriteFile(filepath.Join(dir, brief), []byte(text), 0o644); err != nil {
			t.Fatalf("write brief %s: %v", brief, err)
		}
		if err := RecordPlanned(dir, taskID, brief); err != nil {
			t.Fatalf("RecordPlanned(%s) error = %v", taskID, err)
		}
	}

	// Track metrics
	var (
		totalEvents  int
		roundCount   int
		eventsByKind = make(map[string]int)
		mu           sync.Mutex
	)

	// Loop until every task is landed (at most N+10 rounds)
	for round := 0; round < n+10; round++ {
		roundCount++

		// 1. Get next actions
		acts, err := NextActions(dir, time.Now())
		if err != nil {
			t.Fatalf("round %d NextActions error = %v", round, err)
		}

		// Count actions by kind and run DISPATCH actions
		dispatches := make([]string, 0)
		for _, act := range acts {
			mu.Lock()
			eventsByKind[act.Kind]++
			mu.Unlock()
			if act.Kind == "DISPATCH" {
				dispatches = append(dispatches, act.Task)
			}
		}

		// Check if we're done (no more actions)
		if len(acts) == 0 {
			break
		}

		// 2. Run DISPATCH actions concurrently (at most 8 goroutines)
		taskCh := make(chan string, len(dispatches))
		for _, task := range dispatches {
			taskCh <- task
		}
		close(taskCh)

		var wg sync.WaitGroup
		var runErrs []string
		var errsLock sync.Mutex

		for w := 0; w < 8 && w < len(dispatches); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for task := range taskCh {
					if _, err := Run(dir, RunOptions{Task: task}); err != nil {
						errsLock.Lock()
						runErrs = append(runErrs, fmt.Sprintf("%s: %v", task, err))
						errsLock.Unlock()
					}
				}
			}()
		}
		wg.Wait()

		if len(runErrs) > 0 {
			shown := runErrs
			if len(shown) > 5 {
				shown = shown[:5]
			}
			t.Fatalf("round %d: %d of %d runs failed, first: %s", round, len(runErrs), len(dispatches), strings.Join(shown, "; "))
		}

		// 3. For each task just run: validate, inspect, land
		for _, task := range dispatches {
			// Validate task
			res, err := ValidateTask(dir, task, ValidateOptions{Dir: dir})
			if err != nil {
				t.Fatalf("round %d ValidateTask(%s) error = %v", round, task, err)
			}
			if !res.OK() {
				t.Fatalf("round %d ValidateTask(%s) result not OK: GatesOK=%v, OwnsOK=%v, Refused=%q", round, task, res.GatesOK, res.OwnsOK, res.Refused)
			}

			// Inspect task
			if err := InspectTask(dir, task, InspectOptions{Dir: dir, Verdict: "pass", Session: "lead-scale"}); err != nil {
				t.Fatalf("round %d InspectTask(%s) error = %v", round, task, err)
			}

			// Land task
			if err := LandTask(dir, task, headSha, "", false, ""); err != nil {
				t.Fatalf("round %d LandTask(%s) error = %v", round, task, err)
			}
		}
	}

	// Read all events and verify invariants
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	totalEvents = len(events)

	// Invariant (a): exactly one dispatched, one finished, one inspected, one landed per L-task
	dispatchedCount := make(map[string]int)
	finishedCount := make(map[string]int)
	inspectedCount := make(map[string]int)
	landedCount := make(map[string]int)

	for _, e := range events {
		switch e.Kind {
		case "dispatched":
			dispatchedCount[e.Task]++
		case "finished":
			finishedCount[e.Task]++
		case "inspected":
			inspectedCount[e.Task]++
		case "landed":
			landedCount[e.Task]++
		}
	}

	for i := 1; i <= n; i++ {
		taskID := fmt.Sprintf("L%04d", i)
		if dispatchedCount[taskID] != 1 {
			t.Errorf("task %s dispatched count = %d, want 1", taskID, dispatchedCount[taskID])
		}
		if finishedCount[taskID] != 1 {
			t.Errorf("task %s finished count = %d, want 1", taskID, finishedCount[taskID])
		}
		if inspectedCount[taskID] != 1 {
			t.Errorf("task %s inspected count = %d, want 1", taskID, inspectedCount[taskID])
		}
		if landedCount[taskID] != 1 {
			t.Errorf("task %s landed count = %d, want 1", taskID, landedCount[taskID])
		}
	}

	// Invariant (b): every L-task's derived status is "landed"
	derived := Derive(events)
	for i := 1; i <= n; i++ {
		taskID := fmt.Sprintf("L%04d", i)
		found := false
		for _, ts := range derived.Tasks {
			if ts.ID == taskID {
				if ts.Status != "landed" {
					t.Errorf("task %s status = %q, want landed", taskID, ts.Status)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("task %s not in derived state", taskID)
		}
	}

	// Invariant (c): no MARK_LOST, BLOCK or HOLD actions, and final NextActions has no DISPATCH
	if eventsByKind["MARK_LOST"] > 0 {
		t.Errorf("unexpected MARK_LOST actions: %d", eventsByKind["MARK_LOST"])
	}
	if eventsByKind["BLOCK"] > 0 {
		t.Errorf("unexpected BLOCK actions: %d", eventsByKind["BLOCK"])
	}
	if eventsByKind["HOLD"] > 0 {
		t.Errorf("unexpected HOLD actions: %d", eventsByKind["HOLD"])
	}

	finalActs, err := NextActions(dir, time.Now())
	if err != nil {
		t.Errorf("final NextActions error = %v", err)
	}
	for _, act := range finalActs {
		if act.Kind == "DISPATCH" {
			t.Errorf("final NextActions still has DISPATCH action for task %s", act.Task)
		}
	}

	// Invariant (d): VerifyLogChain reports no break
	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Errorf("VerifyLogChain() error = %v", err)
	}
	if chain.BreakLine != 0 {
		t.Errorf("VerifyLogChain break at line %d (prev=%q)", chain.BreakLine, chain.BreakPrev)
	}

	// Invariant (e): TailEvents returns exactly as many events as ReadEvents, HumanLine non-empty
	tailedEvents, _, err := TailEvents(dir, 0)
	if err != nil {
		t.Errorf("TailEvents() error = %v", err)
	}
	if len(tailedEvents) != len(events) {
		t.Errorf("TailEvents returned %d events, ReadEvents returned %d", len(tailedEvents), len(events))
	}
	for i, e := range tailedEvents {
		line := HumanLine(e)
		if line == "" {
			t.Errorf("HumanLine empty for event %d: %+v", i, e)
		}
	}

	// Log summary
	t.Logf("scale loop: %d tasks, %d events, %d rounds, %s", n, totalEvents, roundCount, time.Since(start).Round(time.Millisecond))
}
