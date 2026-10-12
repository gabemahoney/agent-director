package api_test

// pane_codex_exited_test.go: read-pane and send-keys on a Codex row whose agent exited outside pause (SR-7.2,
// SR-7.3). Neither has Codex-specific code: the agent sent its SessionEnd (the row is still waiting, with an
// end report) and its tmux session went with it, so each answers its gone error and does nothing.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestKeysVerbsCodexExitedGone: on a waiting Codex row holding an end report whose agent exited and whose
// session is gone, send-keys is ErrTmuxSendKeys and read-pane ErrTmuxCaptureFailed, each with the pane-gone
// description after the lookup alone: nothing typed or read, the row unchanged (find-missing or pause ends it).
func TestKeysVerbsCodexExitedGone(t *testing.T) {
	t.Parallel()
	for _, v := range []paneVerb{readPaneVerb(), sendKeysVerb()} {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			spec := codexSpec(store.StateWaiting, agentGone, true)
			spec.NoSession = true
			r := e.seedRow(t, spec)

			v.check(t, e, r, paneGoneWant(v, r))

			pauAssertState(t, e, r.ID, store.StateWaiting)
			if e.store.endedTries != 0 {
				t.Errorf("moves to ended attempted = %d; want none", e.store.endedTries)
			}
		})
	}
}
