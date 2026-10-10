// success_pane.go holds the pane verbs' success-case seed and fake-tmux table
// (SR-7.2, SR-3.7): a live row on a socket of its own whose table holds the
// row's own labelled session and pane, so both runners reach the pane through
// the lookup and pane listing and act on it by its pane id. read-pane reads
// the same fixed text; send-keys sends its text and Enter into the pane (the
// fake fails a send to a pane id its table does not hold).
package envelope_diff

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// readPaneID and sendKeysID are the rows read-pane and send-keys act on;
	// paneSessionID and paneCreated describe the row's session in the fake's
	// table.
	readPaneID    = "id-rp-1"
	sendKeysID    = "id-sk-1"
	paneSessionID = "$1"
	paneCreated   = 1790549184
	// readPaneText is the row's pane content, with no escape sequences, so
	// the default ANSI stripping leaves it unchanged.
	readPaneText = "read-pane line one\nread-pane line two\n"
	// sendKeysText is the text send-keys sends.
	sendKeysText = "hello"
	// ctxSocket is the ctx key under which the seed passes the row's socket
	// on to the table writer; ctxCapture, when set, the pane's capture text.
	ctxSocket  = "socket"
	ctxCapture = "capture"
)

// seedPaneRow seeds a waiting row id launched on a private socket whose pane
// is apitest.TestPaneID with pid apitest.TestPanePID (seedKillRow).
func seedPaneRow(t *testing.T, id string) (string, map[string]any) {
	t.Helper()
	socket, _ := usePrivateFakeTmux(t)
	dir, storeID := seedKillRow(t, id, socket, apitest.TestPanePID, "")
	return dir, map[string]any{"id": id, ctxSocket: socket, ctxStoreID: storeID}
}

// seedReadPane seeds read-pane's row, whose pane captures readPaneText.
func seedReadPane(t *testing.T) (string, map[string]any) {
	t.Helper()
	dir, ctx := seedPaneRow(t, readPaneID)
	ctx[ctxCapture] = readPaneText
	return dir, ctx
}

// seedSendKeys seeds send-keys' row and the text it sends.
func seedSendKeys(t *testing.T) (string, map[string]any) {
	t.Helper()
	dir, ctx := seedPaneRow(t, sendKeysID)
	ctx["text"] = sendKeysText
	return dir, ctx
}

// recordPaneAnswerID is the row record-pane-answer acts on.
const recordPaneAnswerID = "id-rpa-1"

// seedRecordPaneAnswer seeds record-pane-answer's row (seedPaneRow), its pane
// capturing readPaneText, with request A recorded by a relay hook that
// recorded no identity and settled an hour ago (fallen back) and found gone a
// minute ago (b.146 rule 13); ctx carries the pane's SHA-256 as "hash".
func seedRecordPaneAnswer(t *testing.T) (string, map[string]any) {
	t.Helper()
	dir, ctx := seedPaneRow(t, recordPaneAnswerID)
	ctx[ctxCapture] = readPaneText
	dbPath := filepath.Join(dir, "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("seedRecordPaneAnswer: open: %v", err)
	}
	storefix.RegisterStorePath(t, s, dbPath)
	storefix.SeedRelayRequest(t, s, recordPaneAnswerID, store.RelayRequest{RequestToken: storefix.TestRequestTokenA,
		ToolName: "Bash", ToolInput: `{"command":"ls"}`, SettledAt: time.Now().Add(-time.Hour)})
	pr, err := s.GetPermissionRequest(recordPaneAnswerID, storefix.TestRequestTokenA)
	if err == nil {
		_, err = s.RecordHookGone(time.Now().Add(-time.Minute), store.DefaultLockWait, pr.RequestID)
	}
	if cerr := s.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("seedRecordPaneAnswer: %v", err)
	}
	sum := sha256.Sum256([]byte(readPaneText))
	ctx["hash"] = hex.EncodeToString(sum[:])
	return dir, ctx
}

// writePaneRowTable writes the row's own session, labelled for the row's id,
// launch token and store, with its pane tagged @ad_pane (capturing the ctx's
// capture text, if any), onto the row's socket in tables.
func writePaneRowTable(t *testing.T, tables faketmuxfix.Tables, ctx map[string]any) {
	t.Helper()
	id, _ := ctx["id"].(string)
	socket, _ := ctx[ctxSocket].(string)
	storeID, _ := ctx[ctxStoreID].(string)
	capture, _ := ctx[ctxCapture].(string)
	tables.Write(t, socket, faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: os.Getpid(), Start: paneCreated},
		Sessions: []faketmuxfix.Session{{
			ID: paneSessionID, Created: paneCreated, Name: killRowName,
			Label: tmuxfix.LabelValue(tmuxfix.Token, paneSessionID, id, storeID),
			Panes: []faketmuxfix.Pane{{
				ID: apitest.TestPaneID, PID: apitest.TestPanePID, Capture: capture,
				AdPane: tmuxfix.PaneLabelValue(tmuxfix.Token, apitest.TestPaneID),
			}},
		}},
	})
}
