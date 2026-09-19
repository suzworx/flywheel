package flywheel

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestScaleWave drives N simulated tasks through Run concurrently in one ledger
// (issue #48): N is 50, or FLYWHEEL_SCALE when set (CI runs 1000).
func TestScaleWave(t *testing.T) {
	// Determine N from environment or defaults
	n := 50
	if testing.Short() && os.Getenv("FLYWHEEL_SCALE") == "" {
		n = 10
	} else if env := os.Getenv("FLYWHEEL_SCALE"); env != "" {
		var err error
		n, err = strconv.Atoi(env)
		if err != nil {
			t.Fatalf("FLYWHEEL_SCALE invalid: %v", err)
		}
	}

	start := time.Now()
	dir := setupTask(t)
	if err := WriteConfig(dir, simConfig(fixturePath("clean.jsonl", t))); err != nil {
		t.Fatalf("WriteConfig() error = %v", err)
	}

	// Plan S0001 through S<n>
	for i := 1; i <= n; i++ {
		taskID := fmt.Sprintf("S%04d", i)
		if err := AppendEvent(dir, Event{
			TS:    "2026-09-12T00:00:00Z",
			Task:  taskID,
			Kind:  "planned",
			Brief: "b.txt",
		}); err != nil {
			t.Fatalf("RecordPlanned(%s) error = %v", taskID, err)
		}
	}

	// Run the tasks with 8 concurrent workers in one ledger: concurrency is
	// where a double dispatch or a lost event would show.
	ids := make(chan string, n)
	for i := 1; i <= n; i++ {
		ids <- fmt.Sprintf("S%04d", i)
	}
	close(ids)
	var (
		mu   sync.Mutex
		errs []string
		wg   sync.WaitGroup
	)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				if _, err := Run(dir, RunOptions{Task: id}); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Sprintf("%s: %v", id, err))
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		shown := errs
		if len(shown) > 5 {
			shown = shown[:5]
		}
		t.Fatalf("%d of %d runs failed, first: %s", len(errs), n, strings.Join(shown, "; "))
	}

	// Read events and verify invariants
	events, err := ReadEvents(dir)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}

	// Invariant (a): exactly one dispatched and one finished per S-task
	dispatchedCount := make(map[string]int)
	finishedCount := make(map[string]int)
	for _, e := range events {
		switch e.Kind {
		case "dispatched":
			dispatchedCount[e.Task]++
		case "finished":
			finishedCount[e.Task]++
		}
	}

	for i := 1; i <= n; i++ {
		taskID := fmt.Sprintf("S%04d", i)
		if dispatchedCount[taskID] != 1 {
			t.Errorf("task %s dispatched count = %d, want 1", taskID, dispatchedCount[taskID])
		}
		if finishedCount[taskID] != 1 {
			t.Errorf("task %s finished count = %d, want 1", taskID, finishedCount[taskID])
		}
	}

	// Invariant (b): every S-task's derived status is finished
	derived := Derive(events)
	for i := 1; i <= n; i++ {
		taskID := fmt.Sprintf("S%04d", i)
		found := false
		for _, ts := range derived.Tasks {
			if ts.ID == taskID {
				if ts.Status != "finished" {
					t.Errorf("task %s status = %q, want finished", taskID, ts.Status)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("task %s not in derived state", taskID)
		}
	}

	// Invariant (c): VerifyLogChain is OK and chained count correct
	chain, err := VerifyLogChain(dir)
	if err != nil {
		t.Errorf("VerifyLogChain() error = %v", err)
	}
	if chain.BreakLine != 0 {
		t.Errorf("VerifyLogChain break at line %d (prev=%q)", chain.BreakLine, chain.BreakPrev)
	}
	if chain.Chained < len(events)-1 {
		t.Errorf("VerifyLogChain chained = %d, want at least %d (total events - 1)", chain.Chained, len(events)-1)
	}

	// Log summary
	t.Logf("scale: %d tasks, %d events, %s", n, len(events), time.Since(start).Round(time.Millisecond))
}
