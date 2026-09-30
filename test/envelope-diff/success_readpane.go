// success_readpane.go holds the read-pane success case's seed and fake-tmux
// table (SR-7.2, SR-3.7): a live row on a socket of its own whose table holds
// the row's own labelled session and pane, so both runners reach the capture
// through the lookup and pane listing and read the same fixed text.
package envelope_diff

import (
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// readPaneID is the row read-pane reads; readPaneSessionID and
	// readPaneCreated describe its session in the fake's table.
	readPaneID        = "id-rp-1"
	readPaneSessionID = "$1"
	readPaneCreated   = 1790549184
	// readPaneText is the row's pane content, with no escape sequences, so
	// the default ANSI stripping leaves it unchanged.
	readPaneText = "read-pane line one\nread-pane line two\n"
	// ctxSocket is the ctx key under which the seed passes the row's socket
	// on to the table writer.
	ctxSocket = "socket"
)

// seedReadPane seeds a waiting row launched on a private socket whose pane
// is apitest.TestPaneID with pid apitest.TestPanePID (seedKillRow).
func seedReadPane(t *testing.T) (string, map[string]any) {
	t.Helper()
	socket, _ := usePrivateFakeTmux(t)
	dir, storeID := seedKillRow(t, readPaneID, socket, apitest.TestPanePID, "")
	return dir, map[string]any{"id": readPaneID, ctxSocket: socket, ctxStoreID: storeID}
}

// writeReadPaneTable writes the row's own session, labelled for the row's id,
// launch token and store, with its pane tagged @ad_pane and capturing
// readPaneText, onto the row's socket in tables.
func writeReadPaneTable(t *testing.T, tables faketmuxfix.Tables, ctx map[string]any) {
	t.Helper()
	socket, _ := ctx[ctxSocket].(string)
	storeID, _ := ctx[ctxStoreID].(string)
	tables.Write(t, socket, faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: os.Getpid(), Start: readPaneCreated},
		Sessions: []faketmuxfix.Session{{
			ID: readPaneSessionID, Created: readPaneCreated, Name: killRowName,
			Label: tmuxfix.LabelValue(tmuxfix.Token, readPaneSessionID, readPaneID, storeID),
			Panes: []faketmuxfix.Pane{{
				ID: apitest.TestPaneID, PID: apitest.TestPanePID, Capture: readPaneText,
				AdPane: tmuxfix.PaneLabelValue(tmuxfix.Token, apitest.TestPaneID),
			}},
		}},
	})
}
