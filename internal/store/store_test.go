package store

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentOpenOrInitWithWrites is the regression gate for b.x2m
// (burst-spawned hooks fail with SQLITE_BUSY because every new connection
// re-ran PRAGMA journal_mode=WAL and the DSN had no busy_timeout).
//
// N goroutines simultaneously OpenOrInit the same DB file, then each
// performs an insert + state transition (the exact pattern a SessionStart
// hook executes). All must succeed; any SQLITE_BUSY surfacing here means
// the fix has regressed.
func TestConcurrentOpenOrInitWithWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	const workers = 20

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(workers)
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer done.Done()
			start.Wait()

			s, err := OpenOrInit(path)
			if err != nil {
				errs <- fmt.Errorf("worker %d: OpenOrInit: %w", i, err)
				return
			}
			defer s.Close()

			sp := Spawn{
				ClaudeInstanceID: fmt.Sprintf("burst-worker-%02d", i),
				State:            StatePending,
				CWD:              "/tmp",
				TmuxSessionName:  fmt.Sprintf("cd-burst-%02d", i),
				RelayMode:        "off",
			}
			if err := s.InsertPending(sp); err != nil {
				errs <- fmt.Errorf("worker %d: InsertPending: %w", i, err)
				return
			}
			if err := s.ApplyHookTransition(sp.ClaudeInstanceID, StateWaiting, false); err != nil {
				errs <- fmt.Errorf("worker %d: ApplyHookTransition: %w", i, err)
				return
			}
		}()
	}

	start.Done()
	done.Wait()
	close(errs)

	var failed int
	for err := range errs {
		t.Error(err)
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d/%d concurrent OpenOrInit+write attempts failed", failed, workers)
	}

	// Sanity check: every worker's row landed in the waiting state.
	s, err := OpenOrInit(path)
	if err != nil {
		t.Fatalf("post-burst OpenOrInit: %v", err)
	}
	defer s.Close()
	for i := 0; i < workers; i++ {
		id := fmt.Sprintf("burst-worker-%02d", i)
		state, err := s.GetSpawnState(id)
		if err != nil {
			t.Errorf("GetSpawnState(%s): %v", id, err)
			continue
		}
		if state != StateWaiting {
			t.Errorf("%s: state = %q, want %q", id, state, StateWaiting)
		}
	}
}
