// error_cases_pane_tmux.go holds the read-pane error rows whose error comes
// from tmux (SR-7.2, SR-7.3, SR-20.5): each seeds a live row recording a
// private socket whose fake-tmux table the row writes (or leaves empty), so
// both runners see the same tmux and nothing is captured.
package envelope_diff

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// paneGoneID is the row whose socket has no server (Gone).
	paneGoneID = "id-err-rcf-1"
	// paneLeftoversID is the row whose socket holds two sessions labelled
	// by this store for its id under another launch's token.
	paneLeftoversID = "id-err-rsc-1"
	// paneLeftoverA and paneLeftoverB describe those two sessions.
	paneLeftoverASessionID = "$6"
	paneLeftoverAName      = "err-rsc-leftover-a"
	paneLeftoverBSessionID = "$7"
	paneLeftoverBName      = "err-rsc-leftover-b"
	paneLeftoversCreated   = 1790549185
	// paneHungID is the row whose own session and pane are on its socket
	// and whose capture hangs until the configured action timeout ends it.
	paneHungID        = "id-err-run-1"
	paneHungSessionID = "$8"
	paneHungCreated   = 1790549186
	// shortActionTimeoutMs is the action timeout the hung-capture row
	// configures, so both runners give up quickly.
	shortActionTimeoutMs = 300
)

// readPaneParams and readPaneArgv are the read-pane call on id for each runner.
func readPaneParams(id string) map[string]any { return map[string]any{"claude_instance_id": id} }
func readPaneArgv(id string) []string         { return []string{"read-pane", "--claude-instance-id", id} }

// leftoverSession is a session labelled by storeID for instanceID under
// tmuxfix.OtherToken, with one pane.
func leftoverSession(sessionID, name, paneID, instanceID, storeID string) faketmuxfix.Session {
	return faketmuxfix.Session{
		ID: sessionID, Created: paneLeftoversCreated, Name: name,
		Label: tmuxfix.LabelValue(tmuxfix.OtherToken, sessionID, instanceID, storeID),
		Panes: []faketmuxfix.Pane{{ID: paneID, PID: os.Getpid()}},
	}
}

// paneTmuxErrorCases are appended to errorCases.
var paneTmuxErrorCases = []errorCase{

	// ── read-pane / ErrTmuxCaptureFailed ──────────────────────────────────
	// Gone: the row's socket has no server, so the row's session is not
	// there and nothing is read (SR-7.2).
	{
		verb:    "read-pane",
		errName: "ErrTmuxCaptureFailed",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			socket, _ := usePrivateFakeTmux(t)
			dir, storeID := seedKillRow(t, paneGoneID, socket, apitest.TestPanePID, "")
			return dir, map[string]any{ctxStoreID: storeID}
		},
		params:  func(_ map[string]any) map[string]any { return readPaneParams(paneGoneID) },
		cliArgv: func(_ map[string]any) []string { return readPaneArgv(paneGoneID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			storeID, _ := ctx[ctxStoreID].(string)
			return apitest.DescPaneGone(apitest.PaneGone{
				Verb: apitest.PaneReadPane, InstanceID: paneGoneID, Name: killRowName,
			}), []string{storeID, tmuxfix.Token}
		},
	},

	// ── read-pane / ErrTmuxSessionConflict ────────────────────────────────
	// Leftover with more than one leftover: two sessions carry old labels
	// of the row, so read-pane refuses before any listing or capture.
	{
		verb:    "read-pane",
		errName: "ErrTmuxSessionConflict",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			socket, tables := usePrivateFakeTmux(t)
			dir, storeID := seedKillRow(t, paneLeftoversID, socket, apitest.TestPanePID, "")
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: os.Getpid(), Start: paneLeftoversCreated},
				Sessions: []faketmuxfix.Session{
					leftoverSession(paneLeftoverASessionID, paneLeftoverAName, "%6", paneLeftoversID, storeID),
					leftoverSession(paneLeftoverBSessionID, paneLeftoverBName, "%7", paneLeftoversID, storeID),
				},
			})
			return dir, map[string]any{ctxStoreID: storeID}
		},
		params:  func(_ map[string]any) map[string]any { return readPaneParams(paneLeftoversID) },
		cliArgv: func(_ map[string]any) []string { return readPaneArgv(paneLeftoversID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			storeID, _ := ctx[ctxStoreID].(string)
			c := apitest.DescPaneLeftover(apitest.PaneLeftover{
				Verb: apitest.PaneReadPane, InstanceID: paneLeftoversID,
				Sessions: []apitest.DescSession{
					{Name: paneLeftoverAName, ID: paneLeftoverASessionID},
					{Name: paneLeftoverBName, ID: paneLeftoverBSessionID},
				},
			})
			return c, []string{storeID, tmuxfix.Token, tmuxfix.OtherToken,
				tmuxfix.LabelValue(tmuxfix.OtherToken, paneLeftoverASessionID, paneLeftoversID, storeID),
				tmuxfix.LabelValue(tmuxfix.OtherToken, paneLeftoverBSessionID, paneLeftoversID, storeID)}
		},
	},

	// ── read-pane / ErrTmuxUnresponsive ───────────────────────────────────
	// Ours with the agent's pane found, but its capture hangs and the
	// configured action timeout ends it: no follow-up lookup, and the
	// description says "nothing was done" as kill's do (SR-7.3, SR-1.4).
	{
		verb:    "read-pane",
		errName: "ErrTmuxUnresponsive",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			socket, tables := usePrivateFakeTmux(t)
			dir, storeID := seedKillRow(t, paneHungID, socket, apitest.TestPanePID, "")
			apitest.WriteTmuxConfig(t, filepath.Join(dir, "config.toml"),
				apitest.TmuxInt(config.TmuxActionTimeoutMs, shortActionTimeoutMs))
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: os.Getpid(), Start: paneHungCreated},
				Sessions: []faketmuxfix.Session{{
					ID: paneHungSessionID, Created: paneHungCreated, Name: killRowName,
					Label: tmuxfix.LabelValue(tmuxfix.Token, paneHungSessionID, paneHungID, storeID),
					Panes: []faketmuxfix.Pane{{
						ID: apitest.TestPaneID, PID: apitest.TestPanePID,
						AdPane: tmuxfix.PaneLabelValue(tmuxfix.Token, apitest.TestPaneID),
					}},
				}},
			})
			tables.Inject(t, socket, faketmuxfix.Hang(tmux.CallCapture).Bound(hangBound))
			return dir, map[string]any{ctxStoreID: storeID}
		},
		params:  func(_ map[string]any) map[string]any { return readPaneParams(paneHungID) },
		cliArgv: func(_ map[string]any) []string { return readPaneArgv(paneHungID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			storeID, _ := ctx[ctxStoreID].(string)
			c := apitest.DescCallTimeout(tmux.CallCapture, shortActionTimeoutMs*time.Millisecond)
			return c, []string{storeID, tmuxfix.Token}
		},
	},
}
