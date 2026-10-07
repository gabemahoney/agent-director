package api_test

// spawn_launch_test.go covers plain spawn's recorded launch (SR-3.3, SR-3.5,
// SR-3.6, SR-9.4, SR-22.2; AC-LKP-17, AC-LKP-19, AC-CLS-02) through the public
// Client: the one create invocation and its argv, the launch columns and the
// identity write with its guard, socket resolution end to end, and create
// failures. The spawnEnv fixture is in spawn_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// spawnTokenRE is a launch token's form: 16 lowercase hex characters (SR-3.5).
var spawnTokenRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// noIdentity is identityCols of a row with no server or pane identity.
var noIdentity = make([]any, 6)

// mustSpawn runs Spawn with p and returns the instance id, failing on an error.
func mustSpawn(t *testing.T, env spawnEnv, p api.SpawnParams) string {
	t.Helper()
	res, err := env.c.Spawn(p)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	return res.ClaudeInstanceID
}

// spawnRow reads id's row raw, and returns it with its launch token and this store's id.
func spawnRow(t *testing.T, env spawnEnv, id string) (row apitest.SpawnColumns, token, storeID string) {
	t.Helper()
	row, err := apitest.ReadSpawnColumns(env.dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	if storeID, err = apitest.ReadStoreID(env.dbPath); err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	token, _ = row.LaunchToken.(string)
	return row, token, storeID
}

// identityCols returns row's six server and pane identity columns, raw.
func identityCols(r apitest.SpawnColumns) []any {
	return []any{r.TmuxServerPID, r.TmuxServerStarted, r.TmuxServerStarttime, r.PaneID, r.PanePID, r.PaneStarttime}
}

// spawnCallKinds returns the kind of every recorded socket-taking call, in order.
func spawnCallKinds(rec *tmuxfix.Recorder) []tmux.Call {
	var out []tmux.Call
	for _, c := range rec.SocketCalls() {
		out = append(out, c.Call)
	}
	return out
}

// assertLaunchSentinel fails unless err matches want and no other catalogued sentinel (SR-1.5).
func assertLaunchSentinel(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v; want %v", err, want)
	}
	for _, e := range errnames.Catalog {
		if e.Err != want && errors.Is(err, e.Err) {
			t.Errorf("err %v also matches catalogued %s", err, e.Name)
		}
	}
}

// TestSpawnRecordsLaunchAndIdentity: one create on the resolved socket labels the
// session; the row holds the launch start, token, socket and identity at version 1.
func TestSpawnRecordsLaunchAndIdentity(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	const serverPID = 4242
	alive := procfix.Alive(procstarttimefix.DarwinProcStarttime)
	cases := []struct {
		name      string
		id        string
		pane      procfix.Process
		wantCalls []tmux.Call
		paneStart any
	}{
		{"minted id", "", alive, []tmux.Call{tmux.CallCreate}, procstarttimefix.DarwinProcStarttime},
		{"caller-supplied id", "launch-" + uuid.NewString()[:8], alive,
			[]tmux.Call{tmux.CallLookup, tmux.CallCreate}, procstarttimefix.DarwinProcStarttime},
		{"unreadable pane start time", "", procfix.Unreadable(), []tmux.Call{tmux.CallCreate}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			env.rec.StartServer(env.socket, tmuxfix.Server{PID: serverPID})
			env.pc.Set(serverPID, procfix.Alive(procstarttimefix.LinuxProcStarttime))
			env.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				env.pc.Set(env.rec.Sessions(env.socket)[0].Panes[0].PID, tc.pane)
			})
			wantStart := env.clock.Now().UnixMilli()
			id := mustSpawn(t, env, api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id})
			row, tok, storeID := spawnRow(t, env, id)

			calls := env.rec.SocketCalls()
			if got := spawnCallKinds(env.rec); !reflect.DeepEqual(got, tc.wantCalls) || len(env.rec.Calls()) != 0 {
				t.Fatalf("tmux calls = %v (+%d name-based); want %v only", got, len(env.rec.Calls()), tc.wantCalls)
			}
			for _, c := range calls {
				if c.Socket != env.socket {
					t.Errorf("%s on socket %q; want %q", c.Call, c.Socket, env.socket)
				}
			}
			if c := calls[len(calls)-1]; c.Token != tok || c.InstanceID != id || c.StoreID != storeID {
				t.Errorf("create label {%q %q %q}; want {%q %q %q}", c.Token, c.InstanceID, c.StoreID, tok, id, storeID)
			}
			sessions := env.rec.Sessions(env.socket)
			if len(sessions) != 1 || sessions[0].Label != tmuxfix.Valid(tok, id, storeID) || sessions[0].Panes[0].AdPane != tok {
				t.Fatalf("sessions = %+v; want one labelled ad1 %s <$N> %s %s with pane label %s", sessions, tok, id, storeID, tok)
			}
			srv, _ := env.rec.Server(env.socket)
			pane := sessions[0].Panes[0]
			want := []any{int64(serverPID), srv.Start, procstarttimefix.LinuxProcStarttime, pane.ID, int64(pane.PID), tc.paneStart}
			if got := identityCols(row); !reflect.DeepEqual(got, want) {
				t.Errorf("identity columns = %#v; want %#v", got, want)
			}
			if row.State != store.StatePending || row.RowVersion != int64(1) || row.LaunchStartedAt != wantStart ||
				row.TmuxSocket != env.socket || !spawnTokenRE.MatchString(tok) {
				t.Errorf("row {state %v, row_version %v, launch_started_at %v, socket %v, token %q}; want {pending, 1, %d, %s, 16 hex}",
					row.State, row.RowVersion, row.LaunchStartedAt, row.TmuxSocket, tok, wantStart, env.socket)
			}
		})
	}
}

// fakeTmuxArgvs reads fake-tmux's argv log: one argv per invocation, program name dropped.
func fakeTmuxArgvs(t *testing.T, path string) [][]string {
	t.Helper()
	var out [][]string
	for _, argv := range faketmuxfix.ReadLog(t, path) {
		out = append(out, argv[1:])
	}
	return out
}

// containsRun reports whether seq occurs in argv as consecutive elements.
func containsRun(argv, seq []string) bool {
	for i := 0; i+len(seq) <= len(argv); i++ {
		if reflect.DeepEqual(argv[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

// isLookupOn reports whether argv is the lookup on -S socket: its server
// identity read first (b.47f), then the session listing.
func isLookupOn(argv []string, socket string) bool {
	return containsRun(argv, []string{"-S", socket, "display-message", "-p"}) && slices.Contains(argv, "list-sessions")
}

// TestSpawnCreateArgvCarriesChainedLabels: the one create invocation chains the
// @ad_owner and @ad_pane steps on =<name>: and launches an argv of 2+ elements.
func TestSpawnCreateArgvCarriesChainedLabels(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR, test/fake-tmux's log variable
	// with t.Setenv.
	env := buildSpawnEnv(t, faketmuxfix.Binary(t))
	logPath := filepath.Join(env.home, "fake-tmux.log")
	t.Setenv(faketmuxfix.EnvLog, logPath)
	name := "argv-" + uuid.NewString()[:8]
	id := mustSpawn(t, env, api.SpawnParams{CWD: t.TempDir(), TmuxSessionName: name, TmuxSessionNameSupplied: true})
	_, tok, storeID := spawnRow(t, env, id)

	argvs := fakeTmuxArgvs(t, logPath)
	if len(argvs) != 1 {
		t.Fatalf("tmux invocations = %d (%q); want exactly one create", len(argvs), argvs)
	}
	argv, target := argvs[0], "="+name+":"
	for _, seq := range [][]string{
		{"-u", "-S", env.socket, "new-session"},
		{";", "set-option", "-F", "-t", target, "@ad_owner", tmuxfix.ChainLabelValue(tok, id, storeID)},
		{";", "set-option", "-p", "-F", "-t", target, "@ad_pane", tmuxfix.ChainPaneLabelValue(tok)},
	} {
		if !containsRun(argv, seq) {
			t.Errorf("create argv lacks %q:\n%q", seq, argv)
		}
	}
	dash, semi := slices.Index(argv, "--"), slices.Index(argv, ";")
	if dash < 0 || semi-dash-1 < 2 || argv[dash+1] != "claude" {
		t.Errorf("create argv %q: want claude and at least one more element between -- and ; (SR-3.8)", argv)
	}
}

// TestSpawnRecordsResolvedSocket: the create and the row use the socket tmux
// would resolve from TMUX and TMUX_TMPDIR (SR-3.3; AC-LKP-19).
func TestSpawnRecordsResolvedSocket(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	realDir := func(t *testing.T) string {
		d, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, env spawnEnv) string
	}{
		{"TMUX first field given", func(t *testing.T, _ spawnEnv) string {
			sock := filepath.Join(t.TempDir(), "other.sock")
			t.Setenv("TMUX", sock+",123,0")
			return sock
		}},
		{"TMUX empty first field ignored", func(t *testing.T, env spawnEnv) string {
			t.Setenv("TMUX", ",123,0")
			return env.socket
		}},
		{"TMUX_TMPDIR through a symlink", func(t *testing.T, _ spawnEnv) string {
			target, link := realDir(t), filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMUX_TMPDIR", link)
			return filepath.Join(userSocketDir(target), "default")
		}},
		{"relative TMUX_TMPDIR", func(t *testing.T, _ spawnEnv) string {
			parent := realDir(t)
			if err := os.Mkdir(filepath.Join(parent, "rel"), 0o700); err != nil {
				t.Fatal(err)
			}
			wd, err := os.Getwd()
			if err != nil || os.Chdir(parent) != nil {
				t.Fatalf("chdir %s: %v", parent, err)
			}
			t.Cleanup(func() { _ = os.Chdir(wd) })
			t.Setenv("TMUX_TMPDIR", "rel")
			return filepath.Join(userSocketDir(filepath.Join(parent, "rel")), "default")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			want := tc.setup(t, env)
			id := mustSpawn(t, env, api.SpawnParams{CWD: t.TempDir()})
			row, _, _ := spawnRow(t, env, id)
			creates := env.rec.SocketCallsOf(tmux.CallCreate)
			if len(creates) != 1 || creates[0].Socket != want || row.TmuxSocket != want {
				t.Errorf("create calls %+v, row socket %v; want one create and the row on %q", creates, row.TmuxSocket, want)
			}
		})
	}
}

// TestSpawnRefusesUnusableSocketDir: an unsafe per-user directory or a regular-file
// TMUX_TMPDIR is ErrTmuxNotAvailable with tmux's reason, no row and no tmux call.
func TestSpawnRefusesUnusableSocketDir(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name  string
		setup func(t *testing.T, base string) tmux.SocketDirError
	}{
		{"per-user directory mode 0755", func(t *testing.T, base string) tmux.SocketDirError {
			dir := userSocketDir(base)
			if err := os.Mkdir(dir, 0o700); err != nil || os.Chmod(dir, 0o755) != nil {
				t.Fatalf("make %s 0755: %v", dir, err)
			}
			return tmux.SocketDirError{Dir: dir, Reason: tmux.SocketDirUnsafePermissions}
		}},
		{"per-user directory is a symlink", func(t *testing.T, base string) tmux.SocketDirError {
			dir := userSocketDir(base)
			if err := os.Symlink(t.TempDir(), dir); err != nil {
				t.Fatal(err)
			}
			return tmux.SocketDirError{Dir: dir, Reason: tmux.SocketDirSymlink}
		}},
		{"TMUX_TMPDIR names a regular file", func(t *testing.T, base string) tmux.SocketDirError {
			file := filepath.Join(base, "not-a-dir")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMUX_TMPDIR", file)
			return tmux.SocketDirError{Dir: userSocketDir(file), Reason: tmux.SocketDirCreateFailed, Err: syscall.ENOTDIR}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			want := tc.setup(t, os.Getenv("TMUX_TMPDIR"))
			want.Socket = filepath.Join(want.Dir, "default")
			_, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir()})
			assertLaunchSentinel(t, err, tmux.ErrTmuxNotAvailable)
			apitest.AssertDescription(t, err.Error(), apitest.DescSocketDir(want.Socket, want.Dir, want.Error()))
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); len(ids) != 0 {
				t.Errorf("List ids = %q; want none", ids)
			}
		})
	}
}

// TestSpawnHookBeforeIdentityWriteIsIgnored (SR-22.9): the pending row records no
// pane until the identity write, so a hook between the create and it is ignored
// (no_pane_recorded); the identity write applies and spawn succeeds. The hook
// here is the store's gated write, driven directly; the hook handler's
// SessionStart first waits for the identity write, bounded by the pending
// grace (SR-13.4), and that wait is tested in internal/hook.
func TestSpawnHookBeforeIdentityWriteIsIgnored(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env := newSpawnEnv(t)
	var got store.HookApplied
	env.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
		got = apitest.ApplyAgentHook(t, env.dbPath, c.InstanceID, "SessionStart", "sess-early")
	})
	id := mustSpawn(t, env, api.SpawnParams{CWD: t.TempDir()})
	if want := (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}); got != want {
		t.Errorf("SessionStart before the identity write = %+v; want %+v", got, want)
	}
	row, _, _ := spawnRow(t, env, id)
	pane := env.rec.Sessions(env.socket)[0].Panes[0]
	if row.State != store.StatePending || row.RowVersion != int64(1) || row.ClaudeSessionID != nil ||
		row.PaneID != pane.ID || row.PanePID != int64(pane.PID) {
		t.Errorf("row {state %v, row_version %v, session %#v, pane %#v pid %#v}; want pending, 1, NULL, the create's pane %s pid %d",
			row.State, row.RowVersion, row.ClaudeSessionID, row.PaneID, row.PanePID, pane.ID, pane.PID)
	}
	if env.logs.Len() != 0 {
		t.Errorf("client log = %q; want nothing", env.logs.String())
	}
}

// TestSpawnIdentityWriteStoreErrorWarnsOnce: a failed identity write gives one
// WARN line and leaves the result and the pending row unchanged.
func TestSpawnIdentityWriteStoreErrorWarnsOnce(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env := newSpawnEnv(t)
	id := "identfail-" + uuid.NewString()[:8]
	storefix.InjectWriteFailure(t, env.dbPath, storefix.WriteFailLaunchIdentity, id)
	res, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})
	if err != nil || res.ClaudeInstanceID != id {
		t.Fatalf("Spawn = %+v, %v; want %s, nil", res, err, id)
	}
	row, tok, storeID := spawnRow(t, env, id)
	if row.State != store.StatePending || row.RowVersion != int64(0) || !reflect.DeepEqual(identityCols(row), noIdentity) {
		t.Errorf("row {state %v, row_version %v, identity %#v}; want pending, 0, none", row.State, row.RowVersion, identityCols(row))
	}
	lines := strings.Split(strings.TrimSpace(env.logs.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("client log lines = %q; want exactly one", lines)
	}
	apitest.AssertDescription(t, lines[0], apitest.DescIdentityWriteWarn(id), tok, storeID)
}

// TestSpawnCreateFailureLeavesRowPending: tmux unavailable at the create is
// ErrTmuxNotAvailable, other failures ErrTmuxSessionCreate ("duplicate session"
// is spawn_held_test.go's).
func TestSpawnCreateFailureLeavesRowPending(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	createFailed := func(_ spawnEnv, name string) apitest.DescCase {
		return apitest.DescSessionCreateFailed(apitest.SessionCreateFailed{Name: name})
	}
	cases := []struct {
		name string
		fail tmux.Failure
		want error
		desc func(env spawnEnv, name string) apitest.DescCase
	}{
		{"tmux binary not run", tmux.FailUnavailable, tmux.ErrTmuxNotAvailable,
			func(spawnEnv, string) apitest.DescCase { return apitest.DescTmuxNotRun() }},
		{"socket permission denied", tmux.FailSocketDenied, tmux.ErrTmuxNotAvailable,
			func(env spawnEnv, _ string) apitest.DescCase { return apitest.DescSocketPermission(env.socket) }},
		{"no server", tmux.FailNoServer, tmux.ErrTmuxSessionCreate, createFailed},
		{"no socket", tmux.FailNoSocket, tmux.ErrTmuxSessionCreate, createFailed},
		{"non-zero exit with no reply", tmux.FailUnrecognized, tmux.ErrTmuxSessionCreate, createFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			name := "fail-" + uuid.NewString()[:8]
			env.rec.Script(env.socket, tmuxfix.Script{Failure: tc.fail, ExitStatus: 1}, tmux.CallCreate)
			wantStart := env.clock.Now().UnixMilli()
			_, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), TmuxSessionName: name, TmuxSessionNameSupplied: true})
			assertLaunchSentinel(t, err, tc.want)
			ids := listIDs(t, env.c)
			if len(ids) != 1 {
				t.Fatalf("List ids = %q; want the one pending row", ids)
			}
			row, tok, storeID := spawnRow(t, env, ids[0])
			apitest.AssertDescription(t, err.Error(), tc.desc(env, name), tok, storeID)
			if row.State != store.StatePending || row.LaunchStartedAt != wantStart || !spawnTokenRE.MatchString(tok) ||
				!reflect.DeepEqual(identityCols(row), noIdentity) {
				t.Errorf("row {state %v, launch_started_at %v, token %q, identity %#v}; want pending, %d, a token, none",
					row.State, row.LaunchStartedAt, tok, identityCols(row), wantStart)
			}
			if got := spawnCallKinds(env.rec); !reflect.DeepEqual(got, []tmux.Call{tmux.CallCreate}) {
				t.Errorf("tmux calls = %v; want the one create", got)
			}
		})
	}
}
