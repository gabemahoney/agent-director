package api_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/internal/store"
)

// scriptedPauseStore is the seam the pause polling loop sees. The initial
// GetSpawn returns the row at construction time; GetSpawnState walks a
// scripted sequence of state values so a test can simulate "stays
// waiting for a while, then flips to ended" without driving SQLite.
type scriptedPauseStore struct {
	mu       sync.Mutex
	initRow  store.Spawn
	sequence []string // returned in order; last value is sticky
	idx      int
	notFound bool
}

func (f *scriptedPauseStore) GetSpawn(id string) (store.Spawn, error) {
	if f.notFound {
		return store.Spawn{}, store.ErrSpawnNotFound
	}
	return f.initRow, nil
}

func (f *scriptedPauseStore) GetSpawnState(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= len(f.sequence) {
		return f.sequence[len(f.sequence)-1], nil
	}
	v := f.sequence[f.idx]
	f.idx++
	return v, nil
}

// pauseTmuxRecorder is the SendKeys-only fake — pause emits exactly
// one SendKeys call (`/exit` with pressEnter=true). The literal-text-
// then-real-Enter split is owned by *tmux.Client.
type pauseTmuxRecorder struct {
	calls []pauseRecordedSend
	fail  error
}

type pauseRecordedSend struct {
	name       string
	text       string
	pressEnter bool
}

func (p *pauseTmuxRecorder) SendKeys(name, text string, pressEnter bool) error {
	p.calls = append(p.calls, pauseRecordedSend{name: name, text: text, pressEnter: pressEnter})
	return p.fail
}

// withFastPause shrinks the polling cadence + sleeper so the timeout
// tests don't burn wall-clock time. Restores both on test cleanup.
func withFastPause(t *testing.T) {
	t.Helper()
	api.SetPauseTestKnobs(1*time.Millisecond, func(d time.Duration) {})
	t.Cleanup(func() { api.SetPauseTestKnobs(200*time.Millisecond, time.Sleep) })
}

func TestPauseEndedRowIsNoop(t *testing.T) {
	withFastPause(t)
	f := &scriptedPauseStore{initRow: store.Spawn{
		ClaudeInstanceID: "id-p-1", State: store.StateEnded, TmuxSessionName: "cd-1",
	}}
	tmux := &pauseTmuxRecorder{}

	if _, err := api.Pause(context.Background(), f, tmux,
		1, api.PauseParams{ClaudeInstanceID: "id-p-1"}); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if len(tmux.calls) != 0 {
		t.Errorf("ended row triggered tmux call: %v", tmux.calls)
	}
}

func TestPauseMissingRowIsNoop(t *testing.T) {
	withFastPause(t)
	f := &scriptedPauseStore{initRow: store.Spawn{
		ClaudeInstanceID: "id-p-2", State: store.StateMissing, TmuxSessionName: "cd-2",
	}}
	tmux := &pauseTmuxRecorder{}

	if _, err := api.Pause(context.Background(), f, tmux,
		1, api.PauseParams{ClaudeInstanceID: "id-p-2"}); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if len(tmux.calls) != 0 {
		t.Errorf("missing row triggered tmux call: %v", tmux.calls)
	}
}

func TestPauseWaitingTransitionsToEnded(t *testing.T) {
	withFastPause(t)
	// Sequence: a few "waiting" polls, then "ended" — proves the loop
	// keeps polling until the state flips.
	f := &scriptedPauseStore{
		initRow: store.Spawn{
			ClaudeInstanceID: "id-p-3", State: store.StateWaiting, TmuxSessionName: "cd-3",
		},
		sequence: []string{"waiting", "waiting", "waiting", "ended"},
	}
	tmux := &pauseTmuxRecorder{}

	if _, err := api.Pause(context.Background(), f, tmux,
		10, api.PauseParams{ClaudeInstanceID: "id-p-3"}); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	want := []pauseRecordedSend{
		{name: "cd-3", text: "/exit", pressEnter: true},
	}
	if len(tmux.calls) != len(want) {
		t.Fatalf("tmux calls = %v; want %v", tmux.calls, want)
	}
	for i := range want {
		if tmux.calls[i] != want[i] {
			t.Errorf("tmux.calls[%d] = %v; want %v", i, tmux.calls[i], want[i])
		}
	}
}

func TestPauseWaitingTimesOut(t *testing.T) {
	withFastPause(t)
	// Row stays waiting forever; the verb must give up at the deadline
	// and return ErrPauseTimeout. The deadline math uses real wall clock
	// (1s timeout), but the sleeper is the test's no-op so we don't
	// actually wait — the loop spins polls until time.Now() catches up.
	api.SetPauseTestKnobs(1*time.Millisecond, time.Sleep)
	t.Cleanup(func() { api.SetPauseTestKnobs(200*time.Millisecond, time.Sleep) })

	f := &scriptedPauseStore{
		initRow: store.Spawn{
			ClaudeInstanceID: "id-p-4", State: store.StateWaiting, TmuxSessionName: "cd-4",
		},
		sequence: []string{"waiting"},
	}
	tmux := &pauseTmuxRecorder{}

	start := time.Now()
	_, err := api.Pause(context.Background(), f, tmux,
		1, api.PauseParams{ClaudeInstanceID: "id-p-4"})
	elapsed := time.Since(start)

	if !errors.Is(err, api.ErrPauseTimeout) {
		t.Fatalf("err = %v; want ErrPauseTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("timeout case took %s; should be ~1s", elapsed)
	}
}

func TestPauseUnpausableStatesRejected(t *testing.T) {
	withFastPause(t)
	cases := []struct {
		name  string
		state string
	}{
		{"pending", store.StatePending},
		{"working", store.StateWorking},
		{"ask_user", store.StateAskUser},
		{"check_permission", store.StateCheckPermission},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &scriptedPauseStore{initRow: store.Spawn{
				ClaudeInstanceID: "id-x", State: c.state, TmuxSessionName: "cd-x",
			}}
			tmux := &pauseTmuxRecorder{}

			_, err := api.Pause(context.Background(), f, tmux,
				1, api.PauseParams{ClaudeInstanceID: "id-x"})
			if !errors.Is(err, api.ErrSpawnNotPausable) {
				t.Fatalf("state=%s: err = %v; want ErrSpawnNotPausable", c.state, err)
			}
			if len(tmux.calls) != 0 {
				t.Errorf("state=%s: tmux called for non-pausable state: %v", c.state, tmux.calls)
			}
		})
	}
}

func TestPauseUnknownIdReturnsErrSpawnNotFound(t *testing.T) {
	withFastPause(t)
	f := &scriptedPauseStore{notFound: true}
	tmux := &pauseTmuxRecorder{}

	_, err := api.Pause(context.Background(), f, tmux,
		1, api.PauseParams{ClaudeInstanceID: "absent"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
	if len(tmux.calls) != 0 {
		t.Errorf("unknown id triggered tmux call: %v", tmux.calls)
	}
}

func TestPauseHonorsContextCancel(t *testing.T) {
	withFastPause(t)
	// Use a real-clock sleeper so the goroutine has time to cancel
	// between polls.
	api.SetPauseTestKnobs(20*time.Millisecond, time.Sleep)
	t.Cleanup(func() { api.SetPauseTestKnobs(200*time.Millisecond, time.Sleep) })

	f := &scriptedPauseStore{
		initRow: store.Spawn{
			ClaudeInstanceID: "id-p-c", State: store.StateWaiting, TmuxSessionName: "cd-c",
		},
		sequence: []string{"waiting"},
	}
	tmux := &pauseTmuxRecorder{}

	ctx, cancel := context.WithCancel(context.Background())
	var pollCount atomic.Int32
	// Wrap the state reader to count polls; cancel after the second.
	wrapped := &countingPauseStore{inner: f, polls: &pollCount, cancelAfter: 2, cancel: cancel}

	_, err := api.Pause(ctx, wrapped, tmux,
		30, api.PauseParams{ClaudeInstanceID: "id-p-c"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}

// countingPauseStore wraps another PauseStore and cancels a context
// after a configurable number of GetSpawnState calls. Used to prove
// the polling loop honors ctx.Done() promptly.
type countingPauseStore struct {
	inner       *scriptedPauseStore
	polls       *atomic.Int32
	cancelAfter int32
	cancel      context.CancelFunc
}

func (c *countingPauseStore) GetSpawn(id string) (store.Spawn, error) {
	return c.inner.GetSpawn(id)
}
func (c *countingPauseStore) GetSpawnState(id string) (string, error) {
	n := c.polls.Add(1)
	if n == c.cancelAfter {
		c.cancel()
	}
	return c.inner.GetSpawnState(id)
}

func TestPauseSendKeysFailurePropagates(t *testing.T) {
	// A transport-layer failure on either send-keys call must surface
	// to the caller; no polling should occur because /exit never landed.
	withFastPause(t)
	f := &scriptedPauseStore{initRow: store.Spawn{
		ClaudeInstanceID: "id-p-sk", State: store.StateWaiting, TmuxSessionName: "cd-sk",
	}}
	sentinel := errors.New("tmux down")
	tmux := &pauseTmuxRecorder{fail: sentinel}

	_, err := api.Pause(context.Background(), f, tmux,
		5, api.PauseParams{ClaudeInstanceID: "id-p-sk"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v; want sentinel chain", err)
	}
	// Exactly one SendKeys attempt — the tmux client owns the
	// literal-text-then-Enter split internally, so api-side recording
	// sees a single (failed) call.
	if len(tmux.calls) != 1 {
		t.Errorf("send-keys calls = %d; want 1 (single /exit attempt)", len(tmux.calls))
	}
}
