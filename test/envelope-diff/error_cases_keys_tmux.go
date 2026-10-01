// error_cases_keys_tmux.go holds the keys verbs' (send-keys, pause) error
// rows whose error comes from tmux (SR-7.2, SR-20.5): each verb gets the same
// two rows, each seeding a live waiting row recording a private socket whose
// fake-tmux table the row writes (or leaves empty), so both runners see the
// same tmux, nothing is sent and pause never reaches its wait.
package envelope_diff

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// keysLeftoverSessionID is the tmux id of the session holding a Leftover
// row's recorded name.
const keysLeftoverSessionID = "$9"

// keysVerb is one keys verb's two rows: Gone (goneID, whose socket has no
// server) and Leftover (leftoverID, whose recorded name is held by a session
// labelled by this store for its id under another launch's token); args are
// the verb's own flags and params beyond the instance id, and secret what no
// description may carry beyond the ids, tokens and label.
type keysVerb struct {
	pane               apitest.PaneVerb
	goneID, leftoverID string
	args               map[string]string
	secret             []string
}

// keysVerbs are the keys verbs whose rows keysTmuxErrorCases holds.
var keysVerbs = []keysVerb{
	{pane: apitest.PaneSendKeys, goneID: "id-err-skf-1", leftoverID: "id-err-sksc-1",
		args: map[string]string{"text": sendKeysText}, secret: []string{sendKeysText}},
	{pane: apitest.PanePause, goneID: "id-err-psf-1", leftoverID: "id-err-pssc-1"},
}

// params and argv are v's call on id for each runner.
func (v keysVerb) params(id string) map[string]any {
	p := map[string]any{"claude_instance_id": id}
	for k, val := range v.args {
		p[k] = val
	}
	return p
}
func (v keysVerb) argv(id string) []string {
	a := []string{string(v.pane), "--claude-instance-id", id}
	for k, val := range v.args {
		a = append(a, "--"+k, val)
	}
	return a
}

// seedKeysLeftover seeds seedPaneRow's row id with its recorded name held by
// keysLeftoverSessionID, labelled for id under tmuxfix.OtherToken.
func seedKeysLeftover(t *testing.T, id string) (string, map[string]any) {
	t.Helper()
	socket, tables := usePrivateFakeTmux(t)
	dir, storeID := seedKillRow(t, id, socket, apitest.TestPanePID, "")
	tables.Write(t, socket, faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: os.Getpid(), Start: paneLeftoversCreated},
		Sessions: []faketmuxfix.Session{leftoverSession(keysLeftoverSessionID, killRowName,
			apitest.TestPaneID, id, storeID)},
	})
	return dir, map[string]any{ctxStoreID: storeID}
}

// rows returns v's Gone and Leftover rows.
func (v keysVerb) rows() []errorCase {
	verb := string(v.pane)
	return []errorCase{
		// Gone: the row's socket has no server, so the row's session is not
		// there and nothing is sent (SR-7.2).
		{
			verb:    verb,
			errName: "ErrTmuxSendKeys",
			seed: func(t *testing.T) (string, map[string]any) {
				t.Helper()
				return seedPaneRow(t, v.goneID)
			},
			params:  func(_ map[string]any) map[string]any { return v.params(v.goneID) },
			cliArgv: func(_ map[string]any) []string { return v.argv(v.goneID) },
			desc: func(ctx map[string]any) (apitest.DescCase, []string) {
				storeID, _ := ctx[ctxStoreID].(string)
				return apitest.DescPaneGone(apitest.PaneGone{
					Verb: v.pane, InstanceID: v.goneID, Name: killRowName,
				}), append([]string{storeID, tmuxfix.Token}, v.secret...)
			},
		},
		// Leftover: the row's recorded name is held by a session carrying an
		// old label of the row, so the verb refuses before any listing or
		// send (SR-7.2, SR-3.4).
		{
			verb:    verb,
			errName: "ErrTmuxSessionConflict",
			seed: func(t *testing.T) (string, map[string]any) {
				t.Helper()
				return seedKeysLeftover(t, v.leftoverID)
			},
			params:  func(_ map[string]any) map[string]any { return v.params(v.leftoverID) },
			cliArgv: func(_ map[string]any) []string { return v.argv(v.leftoverID) },
			desc: func(ctx map[string]any) (apitest.DescCase, []string) {
				storeID, _ := ctx[ctxStoreID].(string)
				c := apitest.DescPaneLeftover(apitest.PaneLeftover{
					Verb: v.pane, InstanceID: v.leftoverID,
					Sessions: []apitest.DescSession{{Name: killRowName, ID: keysLeftoverSessionID}},
				})
				return c, append([]string{storeID, tmuxfix.Token, tmuxfix.OtherToken,
					tmuxfix.LabelValue(tmuxfix.OtherToken, keysLeftoverSessionID, v.leftoverID, storeID)},
					v.secret...)
			},
		},
	}
}

// keysTmuxErrorCases are appended to errorCases.
var keysTmuxErrorCases = func() []errorCase {
	var cs []errorCase
	for _, v := range keysVerbs {
		cs = append(cs, v.rows()...)
	}
	return cs
}()
