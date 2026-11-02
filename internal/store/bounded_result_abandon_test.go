package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Idea b.x9o round 1 F1 (CR-01): a deadline may return while the native
// worker still owns its result. Do not receive entered before call returns:
// that would order the worker's earlier writes before the caller's reads and
// hide the terminal-flag race. Release and teardown happen after the assertion.
func checkAbandonedResult(t *testing.T, f *v5Store, observe func(func()) func(), call func(*store.Store) (any, error), zero any, recovered func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	timer := time.AfterFunc(3*time.Second, unblock)
	defer func() { unblock(); timer.Stop() }()
	stop := observe(func() { enterOnce.Do(func() { close(entered); <-release }) })
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	got, err := call(f.s.WithContext(ctx))
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("caller exceeded original 500ms deadline + 1s allowance: %v", elapsed)
	}
	if !errors.Is(err, store.ErrStoreBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("abandoned operation = %v; want ErrStoreBusy wrapping deadline", err)
	}
	if !reflect.DeepEqual(got, zero) {
		t.Errorf("abandoned operation exposed a nonzero %T result", got)
	}
	select {
	case <-entered:
	default:
		t.Error("worker never reached the required native boundary")
	}
	unblock()
	stop()
	// The fresh bounded read admits through the same operation slot, so its
	// completion also waits out the abandoned worker, including disposal.
	recovered()
	if !reflect.DeepEqual(got, zero) {
		t.Errorf("%T result stayed nonzero after worker completion", got)
	}
}

func TestBoundedResultAbandonSlices(t *testing.T) {
	// b.x9o F1: each of these readers appends directly to its captured slice.
	for _, tc := range []struct {
		name string
		read func(*store.Store) (any, error)
		zero any
	}{
		{"ListSpawns", func(s *store.Store) (any, error) { return s.ListSpawns(store.ListFilters{}) }, []store.Spawn(nil)},
		{"RecoveryRows", func(s *store.Store) (any, error) { return s.RecoveryRows(nil, store.SettleRead{}) }, []store.Spawn(nil)},
		{"ProvisionalTranscriptsFor", func(s *store.Store) (any, error) { return s.ProvisionalTranscriptsFor("") }, []store.ProvisionalTranscript(nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			f.seed(store.StateWorking, "first-session")
			f.seed(store.StateWorking, "second-session")
			want, err := tc.read(f.s)
			if err != nil || reflect.ValueOf(want).Len() != 2 {
				t.Fatalf("seeded read = %#v, %v; want both rows", want, err)
			}
			observe := func(hold func()) func() {
				next := 0 // only the worker touches this counter
				return store.ObserveReturningRowsForTest(t, f.s, f.path, func(_ context.Context, q string, err error) {
					if err == nil && strings.Contains(q, "FROM spawns") {
						next++
						if next == 2 {
							// The first row is already appended, while the second
							// remains in the real driver until after abandonment.
							hold()
						}
					}
				})
			}
			checkAbandonedResult(t, f, observe, tc.read, tc.zero, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				fresh, err := tc.read(f.s.WithContext(ctx))
				if err != nil || !reflect.DeepEqual(fresh, want) {
					t.Errorf("reused Store read = %#v, %v; want %#v", fresh, err, want)
				}
			})
		})
	}
}

func TestBoundedResultAbandonRuntime(t *testing.T) {
	// b.x9o F1: even InstanceID must not escape the abandoned struct read.
	f, row, binding := cursorRowBinding(t)
	observe := func(hold func()) func() {
		return store.ObservePromptRowsForTest(t, f.s, f.path, nil, nil, func(_ context.Context, q string) {
			if strings.Contains(q, "FROM cursor_runtimes") {
				// Scan has populated the runtime before rows-close cancellation
				// can order those writes before the caller's abandoned return.
				hold()
			}
		})
	}
	call := func(s *store.Store) (any, error) { return s.ReadCursorRuntimeWithin(row.InstanceID, 5*time.Second) }
	checkAbandonedResult(t, f, observe, call, store.CursorRuntime{}, func() {
		fresh, err := f.s.ReadCursorRuntimeWithin(row.InstanceID, 2*time.Second)
		if err != nil || fresh != binding {
			t.Errorf("reused Store runtime = %+v, %v; want %+v", fresh, err, binding)
		}
	})
}

func TestBoundedResultAbandonTerminalFlag(t *testing.T) {
	// b.x9o F1: Scan used to write the caller's recorded flag. Its return
	// expression read that flag even though an errored mark reported false.
	f := newV5Store(t)
	id := f.seed(store.StateWorking, "", append(rowVersionSeed(), apitest.WithAgent(store.AgentCodex))...)
	row := stampEvidence(t, f, id)
	exit := store.ProcessExit{PID: row.Identity.PanePID, Start: row.Identity.PaneStarttime, AtMillis: evidenceSeedMs + 1000}
	observe := func(hold func()) func() {
		return store.ObservePromptRowsForTest(t, f.s, f.path, nil, nil, func(_ context.Context, q string) {
			if strings.HasPrefix(q, "SELECT process_evidence IS NOT NULL") {
				// Scan has written recorded, but rows close has not yet canceled
				// its child context or reached the transaction's commit mutex.
				hold()
			}
		})
	}
	type result struct {
		prior    string
		applied  store.CondResult
		recorded bool
	}
	call := func(s *store.Store) (any, error) {
		prior, applied, recorded, err := s.RecoveryWrites(row).MarkMissingIfSameLifeFrom(id, row.Snapshot, store.EvidenceProcess, exit)
		return result{prior, applied, recorded}, err
	}
	checkAbandonedResult(t, f, observe, call, result{}, func() {
		fresh, err := f.s.GetSpawnWithin(id, 2*time.Second)
		if err != nil || !reflect.DeepEqual(fresh, row) {
			t.Errorf("precommit abandonment changed row: %+v, %v; want %+v", fresh, err, row)
		}
	})
}

func TestBoundedResultCompletedCursorMissingPartial(t *testing.T) {
	// b.x9o F1: error-first publication must retain a completed read's
	// validated rows when a different row's lost-gateway proof became stale.
	e := newCursorTrailEnv(t)
	row, err := e.f.s.GetSpawn(e.b.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	goodID := e.f.seed(store.StateWaiting, "validated-session")
	if ok, err := e.f.s.MarkCursorRuntimeGone(e.b); err != nil || !ok {
		t.Fatalf("change examined binding: %v, %v", ok, err)
	}
	view := e.f.s.WithCursorMissingReads([]store.CursorMissingProof{{Row: row, Binding: e.b}})
	for _, tc := range []struct {
		name string
		read func() ([]store.Spawn, error)
	}{
		{"ListSpawns", func() ([]store.Spawn, error) { return view.ListSpawns(store.ListFilters{}) }},
		{"RecoveryRows", func() ([]store.Spawn, error) { return view.RecoveryRows(nil, store.SettleRead{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := tc.read()
			var stale *store.CursorMissingReadError
			if !errors.As(err, &stale) || !reflect.DeepEqual(stale.InstanceIDs, []string{row.InstanceID}) {
				t.Fatalf("completed read error = %v; want only stale id %s", err, row.InstanceID)
			}
			if len(rows) != 1 || rows[0].InstanceID != goodID || rows[0].State != store.StateWaiting {
				t.Fatalf("completed partial rows = %+v; want validated row %s", rows, goodID)
			}
		})
	}
}
