package api_test

// one_name_per_error_test.go holds the SR-1.5 one-name-per-error check: the
// shared assertOneName helper, the catalogue-wide case, and oneNameRows, one
// row per tmux-caused error (and reachable ErrInternal case) a verb returns
// that no other test checks by catalogue match: the call-site table
// (lookup_calltable_*_test.go) checks every lookup outcome's error, and the
// spawn, reuse and advice-follow tests their own (kill's past the lookup are
// TestAdviceFollow_C1 to C6's). The pane verbs' rows are in
// one_name_pane_verbs_test.go and kill's finished-row opt-in's in
// one_name_kill_optin_test.go.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// assertOneName checks SR-1.5 on an error a verb returned: exactly one
// errnames.Catalog entry matches err under errors.Is, it is want, and
// errnames.Classify names it too. A want of "" or "ErrInternal" asserts that
// no entry matches and Classify gives ErrInternal.
func assertOneName(t testing.TB, err error, want string) {
	t.Helper()
	if want == "" {
		want = "ErrInternal"
	}
	if err == nil {
		t.Fatalf("err = nil; want %s", want)
	}
	var matched []string
	for _, e := range errnames.Catalog {
		if errors.Is(err, e.Err) {
			matched = append(matched, e.Name)
		}
	}
	if internal := want == "ErrInternal"; internal && len(matched) != 0 || !internal && !slices.Equal(matched, []string{want}) {
		t.Errorf("err %q matches catalogued %q; want exactly [%s] (none for ErrInternal)", err, matched, want)
	}
	if name, _ := errnames.Classify(err); name != want {
		t.Errorf("Classify(%q) = %s; want %s", err, name, want)
	}
}

// TestOneNameCatalogue: each catalogued sentinel, wrapped, and each typed
// ErrTmuxNotAvailable carrier matches its own entry and no other, so no class
// sentinel (ErrTmuxUnresponsive, ErrTmuxSessionConflict, ErrTmuxKillFailed)
// wraps the send, capture, create or unavailable one.
func TestOneNameCatalogue(t *testing.T) {
	t.Parallel()
	for _, e := range errnames.Catalog {
		t.Run(e.Name, func(t *testing.T) { assertOneName(t, fmt.Errorf("verb: %w", e.Err), e.Name) })
	}
	for name, err := range map[string]error{
		"SocketDeniedError": &spawn.SocketDeniedError{Socket: "/tmp/s", Consequence: "nothing was written"},
		"SocketDirError": fmt.Errorf("verb: %w", &tmux.SocketDirError{Socket: "/tmp/d/s", Dir: "/tmp/d",
			Reason: tmux.SocketDirNotCreatable}),
	} {
		t.Run(name, func(t *testing.T) { assertOneName(t, err, "ErrTmuxNotAvailable") })
	}
}

// oneNameRow is one returned error: run drives a verb and returns its error;
// want is the catalogued name, or "" for ErrInternal.
type oneNameRow struct {
	name string
	want string
	run  func(t *testing.T) error
}

// oneNameRows is every returned-error row.
func oneNameRows() []oneNameRow {
	return slices.Concat(oneNameSpawnRows(), oneNameResumeRows(), oneNameReadPaneRows(), oneNameSendKeysRows(),
		oneNamePauseRows())
}

// TestOneNameReturnedErrors: every tmux-caused error the verbs return matches
// exactly one catalogued sentinel, and every ErrInternal case none.
func TestOneNameReturnedErrors(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	for _, row := range oneNameRows() {
		t.Run(row.name, func(t *testing.T) { assertOneName(t, row.run(t), row.want) })
	}
}

// oneNameSpawnRows is plain spawn's label-scan socket refused (SR-9.3): the
// one test of a spawn with a caller-supplied id, so the scan runs, under a
// TMUX_TMPDIR that is a regular file. The scan's Can't tell errors are
// cantTellError's, checked by the call-site table and TestScanCantTellRefuses.
func oneNameSpawnRows() []oneNameRow {
	return []oneNameRow{{name: "spawn/TMUX_TMPDIR is a regular file", want: "ErrTmuxNotAvailable", run: func(t *testing.T) error {
		e := newSpawnEnv(t)
		f := filepath.Join(t.TempDir(), "not-a-dir")
		for path, data := range map[string][]byte{filepath.Join(e.home, ".claude.json"): []byte("{}"), f: nil} {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
		t.Setenv("TMUX_TMPDIR", f)
		_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: "one-" + uuid.NewString()[:8]})
		return err
	}}}
}

// oneNameResumeRows are resume's own ErrInternal cases: a control character
// in the id, and a failed move to pending. Its socket, create and "duplicate
// session" errors come from the code it shares with reuse
// (spawn.ResolveRowLaunchSocket, finishedLaunch), checked through reuse by
// TestSpawnReuseSocketRefused, TestSpawnReuseRestoreAfterEachLaunchFailure
// and TestSpawnReuseTrailNameHeldPerOutcome, and through resume by the
// call-site table and the advice-follow tests.
func oneNameResumeRows() []oneNameRow {
	return []oneNameRow{
		{name: "resume/control character in the id", run: func(t *testing.T) error {
			e := newResumeEnv(t)
			suffix := uuid.NewString()[:8]
			r := e.seedRow(t, resumableSpec{ID: "legacy\x1b" + suffix, SessionID: "sess-ctl-" + suffix})
			_, err := e.c.Resume(api.ResumeParams{ClaudeInstanceID: r.ID})
			return err
		}},
		{name: "resume/move to pending fails", run: func(t *testing.T) error {
			e := newResumeEnv(t)
			r := e.seedResumable(t, store.StateEnded)
			e.store.failMove(fmt.Errorf("store: move: %w", tmux.ErrTmuxSessionCreate))
			_, err := e.resume(r.ID)
			return err
		}},
		// Reuse of a row already live after a reuse (SR-10.3); no other test reaches it.
		{name: "spawn Reuse/live row: pending after a reuse", want: "ErrInstanceIdCollision", run: func(t *testing.T) error {
			e := newKillEnv(t)
			r := e.reusePending(t, agentAlive, reuseRowSpec{Age: time.Hour}, reuseRequest{})
			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
			return err
		}},
	}
}

// scriptKill scripts ss in order on every call of kind call on r's socket.
func scriptKill(call tmux.Call, ss ...tmuxfix.Script) func(*testing.T, *killEnv, *killRow) {
	return func(_ *testing.T, e *killEnv, r *killRow) {
		for _, s := range ss {
			e.rec.Script(r.Socket, s, call)
		}
	}
}
