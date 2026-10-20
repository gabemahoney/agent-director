package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// A timeout requires inspection regardless of the agent or the pane's text.
func TestPauseTimeoutInspectionRequired(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"claude", "codex", "cursor"} {
		for _, pane := range []string{"Background work is running\n1. Exit and stop tasks / 2. Stay\n", "ordinary output\n", ""} {
			t.Run(agent+"/"+pane, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				e.rowAgent = agent
				r := e.seedRow(t, killRowSpec{})
				before := e.columns(t, r.ID)
				e.rec.SetCapture(r.Socket, r.Spawn.Identity.PaneID, pane)

				_, err := e.pauseWithin(context.Background(), 0, pauseParams(r))

				assertPauseInspectionDetails(t, err, store.StateWaiting)
				presses := 1
				if agent != "claude" {
					presses = codexClearPresses
				}
				e.assertExitDeliveredTimes(t, r.Socket, r.Spawn.Identity.PaneID, presses)
				e.assertRowUnchanged(t, r.ID, before)
			})
		}
	}
}

func TestPauseTimeoutDetailsLastObservedState(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	e.rec.AfterCall(tmux.CallSendEnter, func(_ tmuxfix.SocketCall, _ error) {
		if applied := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "UserPromptSubmit", r.Spawn.SessionID); !applied.Applied {
			t.Fatalf("UserPromptSubmit: %s", applied.Reason)
		}
	})

	_, err := e.pauseWithin(context.Background(), 0, pauseParams(r))

	assertPauseInspectionDetails(t, err, store.StateWorking)
	e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
	pauAssertState(t, e, r.ID, store.StateWorking)
}

func assertPauseInspectionDetails(t *testing.T, err error, state string) {
	t.Helper()
	if !errors.Is(err, api.ErrPauseTimeout) {
		t.Fatalf("Pause = %v; want ErrPauseTimeout", err)
	}
	assertOneName(t, err, "ErrPauseTimeout")
	data, marshalErr := json.Marshal(api.ErrDetails(err))
	var details map[string]any
	if marshalErr != nil || json.Unmarshal(data, &details) != nil {
		t.Fatalf("err_details = %s (%v); want an object", data, marshalErr)
	}
	want := map[string]any{"inspection_required": true, "exit_keys_sent": true, "exit_confirmed": false, "row_state": state}
	if !reflect.DeepEqual(details, want) {
		t.Errorf("err_details = %s; want %v", data, want)
	}
	for _, advice := range []string{"get", "read-pane", "caller", "do not retry pause automatically"} {
		if !strings.Contains(err.Error(), advice) {
			t.Errorf("error %q lacks advice %q", err, advice)
		}
	}
}
