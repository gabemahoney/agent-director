// error_cases_send_keys_tmux.go holds the send-keys error rows whose error
// comes from tmux (SR-7.2, SR-20.5): each seeds a live row recording a
// private socket whose fake-tmux table the row writes (or leaves empty), so
// both runners see the same tmux and nothing is sent.
package envelope_diff

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// sendKeysGoneID is the row whose socket has no server (Gone).
	sendKeysGoneID = "id-err-skf-1"
	// sendKeysLeftoverID is the row whose recorded name is held by a session
	// labelled by this store for its id under another launch's token;
	// sendKeysLeftoverSessionID describes that session.
	sendKeysLeftoverID        = "id-err-sksc-1"
	sendKeysLeftoverSessionID = "$9"
)

// sendKeysParams and sendKeysArgv are the send-keys call on id for each runner.
func sendKeysParams(id string) map[string]any {
	return map[string]any{"claude_instance_id": id, "text": sendKeysText}
}
func sendKeysArgv(id string) []string {
	return []string{"send-keys", "--claude-instance-id", id, "--text", sendKeysText}
}

// sendKeysTmuxErrorCases are appended to errorCases.
var sendKeysTmuxErrorCases = []errorCase{

	// ── send-keys / ErrTmuxSendKeys ───────────────────────────────────────
	// Gone: the live row's socket has no server, so the row's session is
	// not there and nothing is sent (SR-7.2).
	{
		verb:    "send-keys",
		errName: "ErrTmuxSendKeys",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			return seedPaneRow(t, sendKeysGoneID)
		},
		params:  func(_ map[string]any) map[string]any { return sendKeysParams(sendKeysGoneID) },
		cliArgv: func(_ map[string]any) []string { return sendKeysArgv(sendKeysGoneID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			storeID, _ := ctx[ctxStoreID].(string)
			return apitest.DescPaneGone(apitest.PaneGone{
				Verb: apitest.PaneSendKeys, InstanceID: sendKeysGoneID, Name: killRowName,
			}), []string{storeID, tmuxfix.Token}
		},
	},

	// ── send-keys / ErrTmuxSessionConflict ────────────────────────────────
	// Leftover on a live row: the row's recorded name is held by a session
	// carrying an old label of the row, so send-keys refuses before any
	// listing or send (SR-7.2, SR-3.4).
	{
		verb:    "send-keys",
		errName: "ErrTmuxSessionConflict",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			socket, tables := usePrivateFakeTmux(t)
			dir, storeID := seedKillRow(t, sendKeysLeftoverID, socket, apitest.TestPanePID, "")
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: os.Getpid(), Start: paneLeftoversCreated},
				Sessions: []faketmuxfix.Session{leftoverSession(sendKeysLeftoverSessionID, killRowName,
					apitest.TestPaneID, sendKeysLeftoverID, storeID)},
			})
			return dir, map[string]any{ctxStoreID: storeID}
		},
		params:  func(_ map[string]any) map[string]any { return sendKeysParams(sendKeysLeftoverID) },
		cliArgv: func(_ map[string]any) []string { return sendKeysArgv(sendKeysLeftoverID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			storeID, _ := ctx[ctxStoreID].(string)
			c := apitest.DescPaneLeftover(apitest.PaneLeftover{
				Verb: apitest.PaneSendKeys, InstanceID: sendKeysLeftoverID,
				Sessions: []apitest.DescSession{{Name: killRowName, ID: sendKeysLeftoverSessionID}},
			})
			return c, []string{storeID, tmuxfix.Token, tmuxfix.OtherToken, sendKeysText,
				tmuxfix.LabelValue(tmuxfix.OtherToken, sendKeysLeftoverSessionID, sendKeysLeftoverID, storeID)}
		},
	},
}
