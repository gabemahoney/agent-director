// error_cases_spawn_tmux.go holds the spawn error rows whose error comes from
// tmux (SR-20.5): each runs against a private socket whose fake-tmux table
// the row writes, so both runners see the same tmux and no row shares a
// socket with another test.
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
	// shortCreateTimeoutMs is the create timeout the hung-create row
	// configures, so both runners give up quickly.
	shortCreateTimeoutMs = 300
	// hangBound caps the hung fake's own wait, so no fake outlives the row.
	hangBound = 10 * time.Second
	// hungID is the instance id the hung-create row spawns, explicit so its
	// description can be checked for it; its label scan's lookup answers
	// from the empty table, so the spawn reaches the create.
	hungID = "id-err-tun-1"
	// leftoverID is the instance id the leftover row spawns; its session
	// is labelled for it by this store under another launch's token.
	leftoverID = "id-err-tsc-1"
	// leftoverSessionID, leftoverName and leftoverCreated describe that
	// session in the fake's table.
	leftoverSessionID = "$4"
	leftoverName      = "err-tsc-leftover"
	leftoverCreated   = 1790549182
	// heldID is the instance id the held-name row spawns: no session is
	// labelled for it, so its label scan finds nothing and the spawn
	// reaches the create.
	heldID = "id-err-tsc-2"
	// heldSessionID, heldName and heldCreated describe the unlabelled
	// session that holds the held-name row's requested name.
	heldSessionID = "$5"
	heldName      = "err-tsc-held"
	heldCreated   = 1790549183
	// ctxStoreID is the ctx key under which the leftover row's seed passes
	// the store's id on to its desc (a value the description must not carry).
	ctxStoreID = "store_id"
)

// usePrivateFakeTmux points the subtest at a socket of its own under a
// fresh TMUX_TMPDIR, with its fake-tmux tables in a fresh directory, and
// returns the socket spawn resolves and the tables. runCLI forwards the
// variables (forwardedTmuxEnv); runClient reads them in-process.
func usePrivateFakeTmux(t *testing.T) (string, faketmuxfix.Tables) {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	tables := faketmuxfix.Tables{Dir: t.TempDir()}
	t.Setenv(faketmuxfix.EnvTables, tables.Dir)
	socket, err := tmux.ResolveSocket(true)
	if err != nil {
		t.Fatalf("usePrivateFakeTmux: resolve socket: %v", err)
	}
	return socket, tables
}

// spawnTmuxErrorCases are appended to errorCases.
var spawnTmuxErrorCases = []errorCase{

	// ── spawn / ErrTmuxUnresponsive ───────────────────────────────────────
	// The session-creating call hangs and the configured create timeout
	// ends it: the spawn returns ErrTmuxUnresponsive (SR-1.4).
	{
		verb:    "spawn",
		errName: "ErrTmuxUnresponsive",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			dbPath := apitest.SeedEmptyStore(t)
			srcDir := filepath.Dir(dbPath)
			apitest.WriteTmuxConfig(t, filepath.Join(srcDir, "config.toml"),
				apitest.TmuxInt(config.TmuxCreateTimeoutMs, shortCreateTimeoutMs))
			socket, tables := usePrivateFakeTmux(t)
			tables.Inject(t, socket, faketmuxfix.Hang(tmux.CallCreate).Bound(hangBound))
			return srcDir, nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"cwd": "/tmp", "claude_instance_id": hungID, "no_pre_trust": true}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"spawn", "--cwd", "/tmp", "--claude-instance-id", hungID, "--no-pre-trust"}
		},
		desc: func(_ map[string]any) (apitest.DescCase, []string) {
			return apitest.DescLaunchTimeout(apitest.LaunchTimeout{
				InstanceID:      hungID,
				Timeout:         shortCreateTimeoutMs * time.Millisecond,
				ExplicitSpawnID: true,
			}), nil
		},
	},

	// ── spawn / ErrTmuxSessionConflict ────────────────────────────────────
	// No row for the caller-supplied id, and a session labelled by this
	// store for that id under another token and name still runs: the label
	// scan refuses the spawn before anything is written (SR-9.3).
	{
		verb:    "spawn",
		errName: "ErrTmuxSessionConflict",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			dbPath := apitest.SeedEmptyStore(t)
			storeID, err := apitest.ReadStoreID(dbPath)
			if err != nil {
				t.Fatalf("read store id: %v", err)
			}
			socket, tables := usePrivateFakeTmux(t)
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: os.Getpid(), Start: leftoverCreated},
				Sessions: []faketmuxfix.Session{{
					ID: leftoverSessionID, Created: leftoverCreated, Name: leftoverName,
					Label: tmuxfix.LabelValue(tmuxfix.OtherToken, leftoverSessionID, leftoverID, storeID),
					Panes: []faketmuxfix.Pane{{ID: "%4", PID: os.Getpid()}},
				}},
			})
			return filepath.Dir(dbPath), map[string]any{ctxStoreID: storeID}
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"cwd": "/tmp", "claude_instance_id": leftoverID, "no_pre_trust": true}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"spawn", "--cwd", "/tmp", "--claude-instance-id", leftoverID, "--no-pre-trust"}
		},
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			storeID, _ := ctx[ctxStoreID].(string)
			c := apitest.DescScanLeftover(leftoverID,
				[]apitest.DescSession{{Name: leftoverName, ID: leftoverSessionID}})
			label := tmuxfix.LabelValue(tmuxfix.OtherToken, leftoverSessionID, leftoverID, storeID)
			return c, []string{storeID, tmuxfix.OtherToken, label}
		},
	},

	// ── spawn / ErrTmuxSessionConflict (held name) ────────────────────────
	// An unlabelled session holds the requested name: the create answers
	// "duplicate session", the spawn ends its new row and the re-lookup
	// finds a holder with no valid instance id (SR-9.4, SR-3.10).
	{
		verb:    "spawn",
		errName: "ErrTmuxSessionConflict",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			dbPath := apitest.SeedEmptyStore(t)
			socket, tables := usePrivateFakeTmux(t)
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: os.Getpid(), Start: heldCreated},
				Sessions: []faketmuxfix.Session{{
					ID: heldSessionID, Created: heldCreated, Name: heldName,
					Panes: []faketmuxfix.Pane{{ID: "%5", PID: os.Getpid()}},
				}},
			})
			return filepath.Dir(dbPath), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"cwd": "/tmp", "claude_instance_id": heldID,
				"tmux_session_name": heldName, "no_pre_trust": true}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"spawn", "--cwd", "/tmp", "--claude-instance-id", heldID,
				"--tmux-session-name", heldName, "--no-pre-trust"}
		},
		desc: func(_ map[string]any) (apitest.DescCase, []string) {
			return apitest.DescHeldNoValidID(apitest.HeldName{
				Name: heldName, SessionID: heldSessionID, Row: apitest.HeldRowEnded,
			}), nil
		},
	},
}
