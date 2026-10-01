package realtmux_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Plain spawn, and spawn with the reuse opt-in on a finished row, through the
// production pkg/api client on real tmux (SRD SR-20.4, SR-20.7, SR-3.3,
// SR-3.5, SR-3.6, SR-10.2, SR-10.3; PRD AC-LKP-17, AC-LKP-19, AC-CLS-02). Each
// test has a temp HOME and store, a stand-in `claude` that sleeps first on
// PATH, and the lookup fixture's private tmux world.

// spawnFix is the lookup fixture plus a production client on a temp store.
type spawnFix struct {
	*lookupFix
	API    *api.Client
	DBPath string
	CWD    string
}

// newSpawnFix isolates HOME, puts the stand-in claude first on PATH, writes
// generous [tmux] timeouts and opens the client on a fresh store.
func newSpawnFix(t *testing.T) *spawnFix {
	t.Helper()
	f := newLookupFix(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnv(t, "CLAUDE_CONFIG_DIR")
	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "claude"), []byte("#!/bin/sh\nexec sleep 3600\n"), 0o755); err != nil {
		t.Fatalf("write stand-in claude: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cfgPath := filepath.Join(home, ".agent-director", "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath,
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, clientTimeouts.Query.Milliseconds()),
		apitest.TmuxInt(config.TmuxActionTimeoutMs, clientTimeouts.Action.Milliseconds()),
		apitest.TmuxInt(config.TmuxCreateTimeoutMs, clientTimeouts.Create.Milliseconds()))
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	c, err := api.New(api.Options{StorePath: dbPath, ConfigPath: cfgPath, CreateIfMissing: true})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &spawnFix{lookupFix: f, API: c, DBPath: dbPath, CWD: t.TempDir()}
}

// storeID reads this store's id from the store file.
func (f *spawnFix) storeID(t testing.TB) string {
	t.Helper()
	id, err := apitest.ReadStoreID(f.DBPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return id
}

// spawn runs a spawn with instance id id ("" mints one), without pre-trust;
// reuse sets the reuse opt-in (ReuseFinished).
func (f *spawnFix) spawn(id string, reuse bool) (string, error) {
	res, err := f.API.Spawn(api.SpawnParams{CWD: f.CWD, ClaudeInstanceID: id, NoPreTrust: true, ReuseFinished: reuse})
	return res.ClaudeInstanceID, err
}

// readRow reads id's spawns row from the store file dbPath through the
// store-read helper.
func readRow(t testing.TB, dbPath, id string) apitest.SpawnColumns {
	t.Helper()
	row, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return row
}

// spawned is a successful spawn: its row, its session's $N and the world
// bound to the row's recorded socket.
type spawned struct {
	ID        string
	Row       apitest.SpawnColumns
	SessionID string
	rt        *realTmux
	// Before and After bracket the spawn call in Unix milliseconds.
	Before, After int64
}

// mustSpawn is a plain spawn of id through mustLaunch.
func (f *spawnFix) mustSpawn(t *testing.T, id string) spawned {
	t.Helper()
	return f.mustLaunch(t, id, false)
}

// mustLaunch spawns id (with the reuse opt-in when reuse is set), reads the
// row and finds its session through the recorded pane; the recorded socket
// must lie in the private TMUX_TMPDIR.
func (f *spawnFix) mustLaunch(t *testing.T, id string, reuse bool) spawned {
	t.Helper()
	before := time.Now().UnixMilli()
	got, err := f.spawn(id, reuse)
	after := time.Now().UnixMilli()
	if err != nil {
		t.Fatalf("spawn %q: %s", id, describe(err))
	}
	if id != "" && got != id {
		t.Fatalf("spawn returned id %q, want %q", got, id)
	}
	row := readRow(t, f.DBPath, got)
	socket, ok1 := row.TmuxSocket.(string)
	paneID, ok2 := row.PaneID.(string)
	if !ok1 || !ok2 {
		t.Fatalf("row %s: tmux_socket %T, pane_id %T; want both recorded", got, row.TmuxSocket, row.PaneID)
	}
	rt := f.at(t, socket)
	s := spawned{ID: got, Row: row, SessionID: rt.format(t, paneID, "#{session_id}"), rt: rt, Before: before, After: after}
	rt.trackServer(rt.formatInt(t, paneID, "#{pid}"))
	rt.trackPane(rt.formatInt(t, paneID, "#{pane_pid}"))
	return s
}

var tokenForm = regexp.MustCompile(`^[0-9a-f]{16}$`)

// assertSpawned checks the row (pending at wantVersion), the five-field label,
// @ad_pane and the recorded server and pane identity against real tmux and
// the start-time reader.
func (f *spawnFix) assertSpawned(t *testing.T, s spawned, wantSocket string, wantVersion int64) {
	t.Helper()
	row, rt := s.Row, s.rt
	token, _ := row.LaunchToken.(string)
	paneID := row.PaneID.(string)
	if !tokenForm.MatchString(token) {
		t.Errorf("launch_token is not 16 lowercase hex (set %v)", token != "")
	}
	if row.TmuxSocket != wantSocket {
		t.Errorf("tmux_socket = %v, want %s", row.TmuxSocket, wantSocket)
	}
	if row.State != "pending" || fmt.Sprint(row.RowVersion) != fmt.Sprint(wantVersion) {
		t.Errorf("state %v, row_version %v; want pending, %d", row.State, row.RowVersion, wantVersion)
	}
	if ms, ok := row.LaunchStartedAt.(int64); !ok || ms < s.Before || ms > s.After {
		t.Errorf("launch_started_at = %v, want within the spawn call [%d, %d]", row.LaunchStartedAt, s.Before, s.After)
	}
	if name := rt.format(t, s.SessionID, "#{session_name}"); row.TmuxSessionName != name {
		t.Errorf("tmux_session_name = %v, tmux says %s is %q", row.TmuxSessionName, s.SessionID, name)
	}

	assertLabel(t, s.SessionID, rt.label(t, s.SessionID), "ad1 "+token+" "+s.SessionID+" "+s.ID+" "+f.storeID(t))
	if got := strings.TrimSuffix(rt.must(t, "show-options", "-p", "-qv", "-t", paneID, paneLabelOption), "\n"); got != token+" "+paneID {
		t.Errorf("pane %s: @ad_pane is not <token> %s (set %v)", paneID, paneID, got != "")
	}

	serverPID := rt.formatInt(t, s.SessionID, "#{pid}")
	panePID := rt.formatInt(t, paneID, "#{pane_pid}")
	want := map[string][2]any{
		"tmux_server_pid":       {row.TmuxServerPID, serverPID},
		"tmux_server_started":   {row.TmuxServerStarted, rt.format(t, s.SessionID, "#{start_time}")},
		"tmux_server_starttime": {row.TmuxServerStarttime, f.startOf(t, serverPID)},
		"pane_pid":              {row.PanePID, panePID},
		"pane_starttime":        {row.PaneStarttime, f.startOf(t, panePID)},
	}
	for col, gw := range want {
		if gw[0] == nil || fmt.Sprint(gw[0]) != fmt.Sprint(gw[1]) {
			t.Errorf("%s = %v, real tmux / the start-time reader say %v", col, gw[0], gw[1])
		}
	}

	if argv := procCmdline(t, panePID); len(argv) == 0 || argv[0] != "sleep" {
		t.Errorf("pane %d runs %q, want the stand-in claude (sleep)", panePID, argv)
	}
	if v, _ := envValue(procEnviron(t, panePID), "AGENT_DIRECTOR_INSTANCE_ID"); v != s.ID {
		t.Errorf("pane AGENT_DIRECTOR_INSTANCE_ID = %q, want %q", v, s.ID)
	}
}

// assertLabel compares an @ad_owner value field by field and names only the
// fields that differ, so no token is printed (SR-2.3).
func assertLabel(t testing.TB, sessionID, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	g, w := strings.Split(got, " "), strings.Split(want, " ")
	var bad []string
	for i, name := range []string{"prefix", "token", "session id", "instance id", "store id"} {
		if i >= len(g) || i >= len(w) || g[i] != w[i] {
			bad = append(bad, name)
		}
	}
	t.Errorf("session %s: @ad_owner differs in %v (%d fields, want %d)", sessionID, bad, len(g), len(w))
}

// TestSpawnLabelsSessionWithStoreToken spawns on real tmux and checks the
// label ad1 <token> <$N> <id> <store id>, @ad_pane and the recorded identity.
func TestSpawnLabelsSessionWithStoreToken(t *testing.T) {
	cases := []struct {
		name   string
		id     string // "" mints an id
		inPane bool
	}{
		{name: "minted id"},
		{name: "id containing #", id: newInstanceID("rt#{pid}##x")},
		{name: "from inside another session's pane", inPane: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFix(t)
			wantSocket := f.Socket
			var other agent
			var otherLabel, otherPane string
			if tc.inPane {
				other = f.agentOn(t, f.fresh(t), createSpec{StoreID: f.storeID(t)})
				env := procEnviron(t, other.Reply.PanePID)
				tmuxVar, ok1 := envValue(env, "TMUX")
				tmuxPane, ok2 := envValue(env, "TMUX_PANE")
				if !ok1 || !ok2 {
					t.Fatalf("pane %s has TMUX set %v, TMUX_PANE set %v; want both", other.Reply.PaneID, ok1, ok2)
				}
				t.Setenv("TMUX", tmuxVar)
				t.Setenv("TMUX_PANE", tmuxPane)
				wantSocket, _, _ = strings.Cut(tmuxVar, ",")
				if wantSocket != other.Socket {
					t.Fatalf("TMUX's first field is %s, want the other session's socket %s", wantSocket, other.Socket)
				}
				otherLabel = f.at(t, other.Socket).label(t, other.Reply.SessionID)
				otherPane = f.at(t, other.Socket).ownPaneLabels(t)[other.Reply.PaneID]
			}

			s := f.mustSpawn(t, tc.id)
			f.assertSpawned(t, s, wantSocket, 1) // the insert's 0, plus the identity write

			if tc.inPane {
				ort := f.at(t, other.Socket)
				if s.SessionID == other.Reply.SessionID {
					t.Fatalf("spawn reused the other session %s", s.SessionID)
				}
				assertLabel(t, other.Reply.SessionID, ort.label(t, other.Reply.SessionID), otherLabel)
				assertLabel(t, other.Reply.SessionID, otherLabel,
					"ad1 "+other.Token+" "+other.Reply.SessionID+" "+other.InstanceID+" "+f.storeID(t))
				if ort.ownPaneLabels(t)[other.Reply.PaneID] != otherPane {
					t.Errorf("the other session's pane %s: @ad_pane changed", other.Reply.PaneID)
				}
			}
		})
	}
}

// TestSpawnSocketDeniedIsTmuxNotAvailable denies an existing server's socket
// (mode 000): a minted id leaves a pending row, a supplied id is refused by the scan with no row.
func TestSpawnSocketDeniedIsTmuxNotAvailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root's access ignores the socket's mode 000, so tmux would still connect")
	}
	cases := []struct {
		name    string
		id      string // "" mints an id
		wantRow bool
	}{
		{name: "minted id, at the create", wantRow: true},
		{name: "caller-supplied id, at the label scan", id: newInstanceID("denied")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSpawnFix(t)
			f.startSession(t, "")
			chmodSocket(t, f.Socket, 0o000)

			_, err := f.spawn(tc.id, false)
			if !errors.Is(err, tmux.ErrTmuxNotAvailable) {
				t.Fatalf("spawn error = %s, want ErrTmuxNotAvailable", describe(err))
			}
			for _, other := range []error{tmux.ErrTmuxSessionCreate, tmux.ErrTmuxUnresponsive, tmux.ErrTmuxSessionConflict} {
				if errors.Is(err, other) {
					t.Errorf("spawn error also matches %v", other)
				}
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescSocketPermission(f.Socket))

			rows, lerr := f.API.List(api.ListParams{})
			if lerr != nil {
				t.Fatalf("list: %v", lerr)
			}
			switch {
			case tc.wantRow && len(rows.Spawns) == 1:
				row := readRow(t, f.DBPath, rows.Spawns[0].ClaudeInstanceID)
				if row.State != "pending" || row.TmuxSocket != f.Socket || row.TmuxServerPID != nil || row.PaneID != nil {
					t.Errorf("row: state %v, tmux_socket %v, server pid %v, pane %v; want pending on %s with no identity",
						row.State, row.TmuxSocket, row.TmuxServerPID, row.PaneID, f.Socket)
				}
			case !tc.wantRow && len(rows.Spawns) == 0:
				if tc.id != "" {
					if _, err := apitest.ReadSpawnColumns(f.DBPath, tc.id); !errors.Is(err, store.ErrSpawnNotFound) {
						t.Errorf("read row %s: %v, want no row", tc.id, err)
					}
				}
			default:
				t.Errorf("store holds %d rows, want row %v", len(rows.Spawns), tc.wantRow)
			}

			chmodSocket(t, f.Socket, 0o600)
			if n := len(strings.Fields(f.must(t, "list-sessions", "-F", "#{session_id}"))); n != 1 {
				t.Errorf("server holds %d sessions after the denied spawn, want 1", n)
			}
		})
	}
}

// reuseRows is the kill fixture over the spawn fixture's store, for seeding
// finished rows (seedRow) and comparing them (assertRowUnchanged).
func (f *spawnFix) reuseRows(t testing.TB) *killFix {
	t.Helper()
	return &killFix{lookupFix: f.lookupFix, DBPath: f.DBPath, StoreID: f.storeID(t)}
}

// TestSpawnReuseLabelsSessionWithNewToken reuses an ended row whose session is
// gone: a new life on the recorded socket's server, labelled with a new token.
func TestSpawnReuseLabelsSessionWithNewToken(t *testing.T) {
	f := newSpawnFix(t)
	k := f.reuseRows(t)
	recorded := f.fresh(t) // not the socket a plain spawn would resolve
	keep := recorded.startSession(t, "")
	old := f.agentOn(t, recorded, createSpec{StoreID: k.StoreID})
	before := k.seedRow(t, old.InstanceID, old.Name, store.StateEnded, store.LaunchIdentity{
		Token: old.Token, Socket: old.Socket, ServerPID: old.Server.PID, ServerStart: old.Server.Start,
		ServerStarttime: old.Server.Starttime, PaneID: old.Reply.PaneID, PanePID: old.Reply.PanePID, PaneStarttime: old.PaneStart})
	recorded.must(t, "kill-session", "-t", old.Reply.SessionID)

	s := f.mustLaunch(t, old.InstanceID, true)
	f.assertSpawned(t, s, recorded.Socket, before.RowVersion.(int64)+2) // the reset, then the identity write
	if s.Row.LaunchToken == before.LaunchToken {
		t.Errorf("launch_token is the old life's; want a new token")
	}
	if s.Row.EndedAt != nil || fmt.Sprint(s.Row.LifeNumber) != fmt.Sprint(before.LifeNumber.(int64)+1) {
		t.Errorf("ended_at %v, life_number %v; want NULL, %v + 1", s.Row.EndedAt, s.Row.LifeNumber, before.LifeNumber)
	}
	if srv := s.rt.formatInt(t, s.SessionID, "#{pid}"); srv != keep.ServerPID || s.SessionID == old.Reply.SessionID {
		t.Errorf("new session %s on server pid %d; want a new session on the recorded server %d", s.SessionID, srv, keep.ServerPID)
	}
	if res := f.run(t, "list-sessions"); res.Exit == 0 {
		t.Errorf("a server runs on the resolved socket %s; the reuse should create only on the recorded one", f.Socket)
	}
}

// TestSpawnReuseSocketDeniedIsTmuxNotAvailable denies an ended row's recorded
// socket (mode 000): reuse is ErrTmuxNotAvailable naming it; nothing changes.
func TestSpawnReuseSocketDeniedIsTmuxNotAvailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: root's access ignores the socket's mode 000, so tmux would still connect")
	}
	f := newSpawnFix(t)
	k := f.reuseRows(t)
	raw := f.startSession(t, "")
	srv := f.serverOf(t, f.realTmux, raw.ID)
	id := newInstanceID("reuse-denied")
	token := newToken(t)
	before := k.seedRow(t, id, uniqueName(), store.StateEnded, store.LaunchIdentity{
		Token: token, Socket: f.Socket, ServerPID: srv.PID, ServerStart: srv.Start, ServerStarttime: srv.Starttime})
	chmodSocket(t, f.Socket, 0o000)

	_, err := f.spawn(id, true)
	assertVerbError(t, "spawn", err, "ErrTmuxNotAvailable", apitest.DescSocketPermission(f.Socket), token, k.StoreID)
	chmodSocket(t, f.Socket, 0o600)
	k.assertRowUnchanged(t, id, before)
	if got := strings.Fields(f.must(t, "list-sessions", "-F", "#{session_id}")); len(got) != 1 || got[0] != raw.ID {
		t.Errorf("server holds sessions %v after the refused reuse, want only %s", got, raw.ID)
	}
}
