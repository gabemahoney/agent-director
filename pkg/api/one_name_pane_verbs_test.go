package api_test

// one_name_pane_verbs_test.go holds the pane verbs' SR-1.5 one-name rows
// (read-pane, send-keys and pause) for oneNameRows (one_name_per_error_test.go).
// Every error these verbs return is checked by assertOneName where its tests
// trigger it, on the same inputs, so no row is repeated here:
//   - each lookup outcome, the first action failing and each unusable name:
//     the call table (lookup_calltable_*_test.go); a failed capture's follow-up
//     outcomes: TestCallTableReadPaneFollowUp;
//   - the pane not found, a lost reply's, read-pane's leftovers and the failed
//     listing: readpane_pane_test.go;
//   - the keys calls' timeouts and follow-up outcomes:
//     TestKeysVerbsActionTimeout and TestKeysVerbsActionFailureFollowUp;
//   - send-keys' pending refusals: sendkeys_pending_test.go; pause's state
//     refusals and its wait's timeout: TestPauseGuards and TestPauseTimeout.
// It also holds rebindServer and unusableNameSpec, which other verbs' one-name rows use.

import (
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rebindServer binds a new server on r's socket (a different server, SR-3.3)
// holding only a bystander session.
func rebindServer(t *testing.T, e *killEnv, r *killRow) {
	e.rec.RebindServer(r.Socket, tmuxfix.Server{})
	e.syncServers()
	e.seedBystander(t, r.Socket)
}

// unusableNameSpec is a waiting row, no session seeded, recording the pre-b.gqe default name ('.').
// Later one-name tables use it for their unusable-name case, never their own spec.
func unusableNameSpec() killRowSpec {
	return killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(preGqeDefaultName)}}
}

// oneNameReadPaneRows are read-pane's rows: none beyond the checks above.
func oneNameReadPaneRows() []oneNameRow { return nil }

// oneNameSendKeysRows are send-keys' rows: none beyond the checks above.
func oneNameSendKeysRows() []oneNameRow { return nil }

// oneNamePauseRows are pause's rows: none beyond the checks above.
func oneNamePauseRows() []oneNameRow { return nil }
