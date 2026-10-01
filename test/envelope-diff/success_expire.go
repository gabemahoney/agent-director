// success_expire.go holds expire's success cases (SR-12.2): the fixture's
// rows read Gone from each run's empty fake table and are deleted, and a
// finished row whose own labelled session runs is kept. Both cases pin kept
// and kept_ids on both envelopes, so neither may vanish from both sides.
package envelope_diff

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// expireKeptID is the kept case's finished row; expireKeptName,
	// expireKeptSessionID and expireKeptCreated describe its session.
	expireKeptID        = "id-expire-kept-1"
	expireKeptName      = "expire-kept-row"
	expireKeptSessionID = "$2"
	expireKeptCreated   = 1790549185
)

// expireParams and expireArgv are the expire call with --older-than window.
func expireParams(window string) map[string]any { return map[string]any{"older_than": window} }
func expireArgv(window string) []string         { return []string{"expire", "--older-than", window} }

// seedExpireKept seeds expireKeptID as an ended row launched on a private
// socket with token tmuxfix.Token and returns the store's directory and ctx.
func seedExpireKept(t *testing.T) (string, map[string]any) {
	t.Helper()
	socket, _ := usePrivateFakeTmux(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")
	if _, err := apitest.SeedSpawn(dbPath, expireKeptID, store.StateEnded, "", "", "", true,
		apitest.WithTmuxSessionName(expireKeptName),
		apitest.WithEndedAt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		apitest.WithLaunchIdentity(store.LaunchIdentity{Token: tmuxfix.Token, Socket: socket})); err != nil {
		t.Fatalf("seedExpireKept: %v", err)
	}
	storeID, err := apitest.ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("seedExpireKept: read store id: %v", err)
	}
	return filepath.Dir(dbPath), map[string]any{ctxSocket: socket, ctxStoreID: storeID}
}

// writeExpireKeptTable writes the kept row's own session, labelled for its
// id, launch token and store, onto its socket in tables.
func writeExpireKeptTable(t *testing.T, tables faketmuxfix.Tables, ctx map[string]any) {
	t.Helper()
	socket, _ := ctx[ctxSocket].(string)
	storeID, _ := ctx[ctxStoreID].(string)
	tables.Write(t, socket, faketmuxfix.Table{
		Server: &faketmuxfix.Server{PID: os.Getpid(), Start: expireKeptCreated},
		Sessions: []faketmuxfix.Session{{
			ID: expireKeptSessionID, Created: expireKeptCreated, Name: expireKeptName,
			Label: tmuxfix.LabelValue(tmuxfix.Token, expireKeptSessionID, expireKeptID, storeID),
		}},
	})
}

// expireSuccessCases are appended to successCases.
var expireSuccessCases = []successCase{

	// ── expire ────────────────────────────────────────────────────────────
	// SeedExpireFixture's rows record no socket, so each run looks them up on
	// its own private socket, whose empty table reads Gone: the two terminal
	// rows backdated past "--older-than 1h" are deleted and none is kept.
	{
		verb: "expire",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.SeedExpireFixture(t)
			return filepath.Dir(dbPath), nil
		},
		params:  func(_ map[string]any) map[string]any { return expireParams("1h") },
		cliArgv: func(_ map[string]any) []string { return expireArgv("1h") },
		want:    map[string]any{"kept": float64(0), "kept_ids": []any{}},
	},

	// ── expire / kept ─────────────────────────────────────────────────────
	// The finished row's own labelled session runs on its socket: it is
	// kept (ours) and nothing is deleted.
	{
		verb:      "expire",
		name:      "kept",
		seed:      seedExpireKept,
		params:    func(_ map[string]any) map[string]any { return expireParams("0d") },
		cliArgv:   func(_ map[string]any) []string { return expireArgv("0d") },
		tmuxTable: writeExpireKeptTable,
		want:      map[string]any{"count": float64(0), "ids": []any{}, "kept": float64(1), "kept_ids": []any{expireKeptID}},
	},
}
