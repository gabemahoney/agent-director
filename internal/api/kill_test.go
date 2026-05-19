package api_test

import (
	"errors"
	"testing"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/store"
)

// recordingKillTmux records every kill-session call and optionally
// returns a scripted error. A non-nil failErr proves the verb swallows
// tmux failures (intent: "make sure the session is gone" is satisfied
// regardless of who actually killed it).
type recordingKillTmux struct {
	calls   []string
	failErr error
}

func (r *recordingKillTmux) KillSession(name string) error {
	r.calls = append(r.calls, name)
	return r.failErr
}

func TestKillLiveRowInvokesTmux(t *testing.T) {
	s := openStoreWithRow(t, "id-k-1", "cd-k-1", store.StateWaiting, "off")
	tmux := &recordingKillTmux{}

	if _, err := api.Kill(s, tmux, api.KillParams{ClaudeInstanceID: "id-k-1"}); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if got, want := len(tmux.calls), 1; got != want {
		t.Fatalf("tmux calls = %d; want %d", got, want)
	}
	if tmux.calls[0] != "cd-k-1" {
		t.Errorf("tmux.KillSession arg = %q; want %q", tmux.calls[0], "cd-k-1")
	}
}

func TestKillSwallowsTmuxFailure(t *testing.T) {
	// SRD §5: kill's post-condition is "session is gone". A non-zero
	// tmux exit ("session already gone") satisfies that post-condition,
	// so the verb returns nil rather than forcing callers to distinguish.
	s := openStoreWithRow(t, "id-k-2", "cd-k-2", store.StateWaiting, "off")
	tmux := &recordingKillTmux{failErr: errors.New("can't find session: cd-k-2")}

	if _, err := api.Kill(s, tmux, api.KillParams{ClaudeInstanceID: "id-k-2"}); err != nil {
		t.Fatalf("Kill must swallow tmux errors; got %v", err)
	}
	if got, want := len(tmux.calls), 1; got != want {
		t.Fatalf("tmux calls = %d; want %d", got, want)
	}
}

func TestKillTerminalStateIsNoop(t *testing.T) {
	// Terminal states skip the tmux call entirely. The row's session
	// may or may not still exist on disk; either way, the intent is
	// already satisfied.
	cases := []struct {
		state string
		id    string
		sess  string
	}{
		{store.StateEnded, "id-k-3", "cd-k-3"},
		{store.StateMissing, "id-k-4", "cd-k-4"},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			s := openStoreWithRow(t, tc.id, tc.sess, tc.state, "off")
			tmux := &recordingKillTmux{}
			if _, err := api.Kill(s, tmux, api.KillParams{ClaudeInstanceID: tc.id}); err != nil {
				t.Fatalf("Kill: %v", err)
			}
			if len(tmux.calls) != 0 {
				t.Errorf("%s row triggered tmux call: %v", tc.state, tmux.calls)
			}
		})
	}
}

func TestKillUnknownIdReturnsErrSpawnNotFound(t *testing.T) {
	s := openStoreWithRow(t, "id-k-5", "cd-k-5", store.StateWaiting, "off")
	tmux := &recordingKillTmux{}

	_, err := api.Kill(s, tmux, api.KillParams{ClaudeInstanceID: "absent"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
	if len(tmux.calls) != 0 {
		t.Errorf("absent id triggered tmux call: %v", tmux.calls)
	}
}

func TestKillIsIdempotentAcrossRepeatedCalls(t *testing.T) {
	// The verb does not flip the row's state — find-missing reconciles
	// that out-of-band (Epic 8). Repeated kills must therefore behave
	// identically: each one invokes tmux.KillSession (cheap), each one
	// returns nil, no error sticks.
	s := openStoreWithRow(t, "id-k-6", "cd-k-6", store.StateWaiting, "off")
	tmux := &recordingKillTmux{failErr: errors.New("session gone")}

	for i := 0; i < 3; i++ {
		if _, err := api.Kill(s, tmux, api.KillParams{ClaudeInstanceID: "id-k-6"}); err != nil {
			t.Fatalf("Kill iter %d: %v", i, err)
		}
	}
	if got, want := len(tmux.calls), 3; got != want {
		t.Fatalf("tmux calls = %d; want %d (one per call)", got, want)
	}
}
