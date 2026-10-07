package mcp_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// expireMCPID is the one finished row every expire case seeds.
const expireMCPID = "mcp-expire-row"

// expireToolResult is toolResult, under the name the b.fji I6 test uses.
var expireToolResult = toolResult

// newExpireMCPServer seeds expireMCPID, finished past the default retention
// window, into a fresh mcpEnv (with its own labelled session running when
// ownSession) and returns the dispatcher, the Recorder and the store path.
func newExpireMCPServer(t *testing.T, ownSession bool) (mcp.Dispatcher, *tmuxfix.Recorder, string) {
	t.Helper()
	retention := time.Duration(config.Default().Defaults.ExpireRetentionDays) * 24 * time.Hour
	return newExpireMCPServerEnded(t, ownSession, time.Now().Add(-retention-24*time.Hour))
}

// newExpireMCPServerEnded is newExpireMCPServer with the row ended at ended.
func newExpireMCPServerEnded(t *testing.T, ownSession bool, ended time.Time) (mcp.Dispatcher, *tmuxfix.Recorder, string) {
	t.Helper()
	e := newEnv(t)
	e.seed(t, expireMCPID, store.StateEnded, apitest.WithEndedAt(ended))
	if ownSession {
		e.rec.SeedRowSession(t, e.storePath, expireMCPID)
	}
	return e.d, e.rec, e.storePath
}

// TestExpireMCP pins SR-12.2 on MCP: the expire tool deletes a row with no
// session, keeps one whose own session runs, and always carries kept and
// kept_ids. The selection and tmux matrix is pkg/api's.
func TestExpireMCP(t *testing.T) {
	for _, ownSession := range []bool{false, true} {
		t.Run("own session "+strconv.FormatBool(ownSession), func(t *testing.T) {
			d, _, storePath := newExpireMCPServer(t, ownSession)
			want := map[string]string{"ids": `["` + expireMCPID + `"]`, "kept": `0`, "kept_ids": `[]`}
			if ownSession {
				want = map[string]string{"ids": `[]`, "kept": `1`, "kept_ids": `["` + expireMCPID + `"]`}
			}

			obj := expireToolResult(t, callTool(t, d, "expire", `{}`))

			for key, w := range want {
				if got := string(obj[key]); got != w {
					t.Errorf("%s = %s; want %s", key, got, w)
				}
			}
			if _, err := apitest.ReadSpawnColumns(storePath, expireMCPID); errors.Is(err, store.ErrSpawnNotFound) == ownSession {
				t.Errorf("row read after expire: err = %v; want the row kept = %v", err, ownSession)
			}
		})
	}
}

// TestExpireMCPOlderThanSign pins b.hxn on a row that finished a minute ago: a
// negative older_than is ErrInvalidFlags with no tmux call and the row kept;
// "0d" and "0s" delete it, as before the fix.
func TestExpireMCPOlderThanSign(t *testing.T) {
	for _, olderThan := range []string{"-2h", "0d", "0s"} {
		t.Run(olderThan, func(t *testing.T) {
			d, rec, storePath := newExpireMCPServerEnded(t, false, time.Now().Add(-time.Minute))
			resp := callTool(t, d, "expire", paramJSON(t, map[string]any{"older_than": olderThan}))
			_, rowErr := apitest.ReadSpawnColumns(storePath, expireMCPID)
			if olderThan[0] != '-' {
				if got := string(expireToolResult(t, resp)["ids"]); got != `["`+expireMCPID+`"]` || !errors.Is(rowErr, store.ErrSpawnNotFound) {
					t.Errorf("ids = %s, row read err %v; want the row deleted", got, rowErr)
				}
				return
			}
			if data := toolErrorData(t, resp); data.ErrName != "ErrInvalidFlags" || data.ErrDescription != olderThanRefusal(olderThan) {
				t.Errorf("refusal = %s: %q; want ErrInvalidFlags: %q", data.ErrName, data.ErrDescription, olderThanRefusal(olderThan))
			}
			if n := len(rec.SocketCalls()) + len(rec.Calls()); rowErr != nil || n != 0 {
				t.Errorf("after the refusal: row read err %v, %d tmux calls; want the row kept, none", rowErr, n)
			}
		})
	}
}

// olderThanRefusal is MCP expire's description refusing older_than value v.
func olderThanRefusal(v string) string {
	return `ErrInvalidFlags: expire: parameter "older_than" value ` + strconv.Quote(v) +
		` must be a non-negative Go duration like "12h" or trailing-d days like "7d" up to "106751d"`
}
