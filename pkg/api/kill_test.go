package api_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/internal/store"
)

// recordingKillLogger captures every Printf the verb emits so the
// "log the swallowed tmux failure" test can inspect both severity
// and content. The struct is reused across cases that need to
// assert on the recorded lines.
type recordingKillLogger struct {
	lines []string
}

func (r *recordingKillLogger) Printf(format string, v ...any) {
	r.lines = append(r.lines, fmt.Sprintf(format, v...))
}

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

	if _, err := api.Kill(s, tmux, nil, api.KillParams{ClaudeInstanceID: "id-k-1"}); err != nil {
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

	if _, err := api.Kill(s, tmux, nil, api.KillParams{ClaudeInstanceID: "id-k-2"}); err != nil {
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
			if _, err := api.Kill(s, tmux, nil, api.KillParams{ClaudeInstanceID: tc.id}); err != nil {
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

	_, err := api.Kill(s, tmux, nil, api.KillParams{ClaudeInstanceID: "absent"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
	if len(tmux.calls) != 0 {
		t.Errorf("absent id triggered tmux call: %v", tmux.calls)
	}
}

// TestKillSwallowedTmuxFailureLogsAtWARN pins the b.5xd fix: when
// tmux.KillSession returns non-zero the verb still returns success
// (the row will reconcile on the next find-missing sweep), but it
// MUST emit a WARN log line carrying the spawn id and the underlying
// tmux error so an interactive operator can diagnose the failure in
// the moment.
func TestKillSwallowedTmuxFailureLogsAtWARN(t *testing.T) {
	s := openStoreWithRow(t, "id-k-warn", "cd-k-warn", store.StateWaiting, "off")
	tmuxErr := errors.New("ErrTmuxKillFailed: stale TMUX_TMPDIR")
	tmux := &recordingKillTmux{failErr: tmuxErr}
	lg := &recordingKillLogger{}

	if _, err := api.Kill(s, tmux, lg, api.KillParams{ClaudeInstanceID: "id-k-warn"}); err != nil {
		t.Fatalf("Kill must still swallow tmux errors at the verb surface; got %v", err)
	}

	if len(lg.lines) != 1 {
		t.Fatalf("Kill emitted %d log lines; want exactly 1", len(lg.lines))
	}
	line := lg.lines[0]
	if !strings.HasPrefix(line, "WARN:") {
		t.Errorf("log line missing WARN severity prefix: %q", line)
	}
	if !strings.Contains(line, "id-k-warn") {
		t.Errorf("log line missing spawn id %q: %q", "id-k-warn", line)
	}
	if !strings.Contains(line, tmuxErr.Error()) {
		t.Errorf("log line missing underlying tmux error %q: %q", tmuxErr.Error(), line)
	}
}

// TestKillSilentOnTmuxSuccess pins the converse: a successful
// tmux.KillSession does NOT emit any log line. We don't want
// operators sifting through WARN noise on the happy path.
func TestKillSilentOnTmuxSuccess(t *testing.T) {
	s := openStoreWithRow(t, "id-k-quiet", "cd-k-quiet", store.StateWaiting, "off")
	tmux := &recordingKillTmux{} // failErr nil → success
	lg := &recordingKillLogger{}

	if _, err := api.Kill(s, tmux, lg, api.KillParams{ClaudeInstanceID: "id-k-quiet"}); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if len(lg.lines) != 0 {
		t.Errorf("Kill emitted %d log lines on happy path; want 0: %v", len(lg.lines), lg.lines)
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
		if _, err := api.Kill(s, tmux, nil, api.KillParams{ClaudeInstanceID: "id-k-6"}); err != nil {
			t.Fatalf("Kill iter %d: %v", i, err)
		}
	}
	if got, want := len(tmux.calls), 3; got != want {
		t.Fatalf("tmux calls = %d; want %d (one per call)", got, want)
	}
}
