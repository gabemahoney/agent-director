package api_test

// spawn_launch_test.go covers plain spawn's recorded and bounded launch
// (SR-3.3, SR-3.5, SR-3.6, SR-9.4, SR-13.2, SR-22.2; AC-LKP-17, AC-LKP-19,
// AC-SPN-05, AC-SPN-06, AC-CLS-02) through the public Client: the one create
// and the identity write with its guard, socket resolution, create failures,
// a lost or unanswered create keeping its label, and the longest path's
// virtual time. The create argv is spawn_reuse_launch_test.go's parity case.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/config"
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

// The default call timeouts, from the internal/config constants (SR-13.1),
// and SR-13.2's bound on a single-row verb's tmux time at the defaults.
var (
	boundQ = time.Duration(config.DefaultQueryTimeoutMs) * time.Millisecond
	boundA = time.Duration(config.DefaultActionTimeoutMs) * time.Millisecond
	boundC = time.Duration(config.DefaultCreateTimeoutMs) * time.Millisecond
)

const boundCap = 15 * time.Second

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

// TestSpawnRecordsLaunchAndIdentity (SR-3.5, SR-22.9): one create on the
// resolved socket labels the session; the row holds the launch start, token,
// socket and identity at version 1. The pending row records no pane until the
// identity write, so a hook between the create and it is ignored
// (no_pane_recorded); the hook handler's own wait is internal/hook's.
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
		hook      bool // the agent's SessionStart arrives before the identity write
	}{
		{"minted id", "", alive, []tmux.Call{tmux.CallCreate}, procstarttimefix.DarwinProcStarttime, false},
		{"caller-supplied id", "launch-" + uuid.NewString()[:8], alive,
			[]tmux.Call{tmux.CallLookup, tmux.CallCreate}, procstarttimefix.DarwinProcStarttime, false},
		{"unreadable pane start time", "", procfix.Unreadable(), []tmux.Call{tmux.CallCreate}, nil, false},
		{"a hook before the identity write", "", alive, []tmux.Call{tmux.CallCreate}, procstarttimefix.DarwinProcStarttime, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			env.rec.StartServer(env.socket, tmuxfix.Server{PID: serverPID})
			env.pc.Set(serverPID, procfix.Alive(procstarttimefix.LinuxProcStarttime))
			var hooked store.HookApplied
			env.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, _ error) {
				env.pc.Set(env.rec.Sessions(env.socket)[0].Panes[0].PID, tc.pane)
				if tc.hook {
					hooked = apitest.ApplyAgentHook(t, env.dbPath, c.InstanceID, "SessionStart", "sess-early")
				}
			})
			wantStart := env.clock.Now().UnixMilli()
			id := mustSpawn(t, env, api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id})
			row, tok, storeID := spawnRow(t, env, id)

			calls := env.rec.SocketCalls()
			if got := callKinds(env.rec); !reflect.DeepEqual(got, tc.wantCalls) || len(env.rec.Calls()) != 0 {
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
				row.TmuxSocket != env.socket || !spawnTokenRE.MatchString(tok) || row.ClaudeSessionID != nil {
				t.Errorf("row {state %v, row_version %v, launch_started_at %v, socket %v, token %q, session %v}; want {pending, 1, %d, %s, 16 hex, NULL}",
					row.State, row.RowVersion, row.LaunchStartedAt, row.TmuxSocket, tok, row.ClaudeSessionID, wantStart, env.socket)
			}
			if tc.hook && (hooked != (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}) || env.logs.Len() != 0) {
				t.Errorf("SessionStart before the identity write = %+v, client log %q; want ignored, %s, and no log",
					hooked, env.logs.String(), store.HookReasonNoPaneRecorded)
			}
		})
	}
}

// TestSpawnRecordsResolvedSocket (SR-3.3; AC-LKP-19): the create and the row use
// the socket TMUX's first field names; internal/tmux owns the resolution rules.
func TestSpawnRecordsResolvedSocket(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	env := newSpawnEnv(t)
	sock := filepath.Join(t.TempDir(), "other.sock")
	t.Setenv("TMUX", sock+",123,0")
	row, _, _ := spawnRow(t, env, mustSpawn(t, env, api.SpawnParams{CWD: t.TempDir()}))
	if creates := env.rec.SocketCallsOf(tmux.CallCreate); len(creates) != 1 || creates[0].Socket != sock || row.TmuxSocket != sock {
		t.Errorf("create calls %+v, row socket %v; want one create and the row on %q", creates, row.TmuxSocket, sock)
	}
}

// TestSpawnRefusesUnusableSocketDir (SR-20.6 RN-5): a 0755 or symlinked
// per-user directory, or a regular-file TMUX_TMPDIR, is ErrTmuxNotAvailable
// with tmux's reason, no row and no tmux call.
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
// is spawn_held_test.go's); the row stays pending with no identity, and no
// held-name end write, re-lookup or ad.launch.name_held follows.
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
		{"non-zero exit with no reply", tmux.FailUnrecognized, tmux.ErrTmuxSessionCreate, createFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			name := "fail-" + uuid.NewString()[:8]
			env.rec.Script(env.socket, tmuxfix.Script{Failure: tc.fail, ExitStatus: 1}, tmux.CallCreate)
			wantStart, mark := env.clock.Now().UnixMilli(), trailLen(t)
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
			if got := callKinds(env.rec); !reflect.DeepEqual(got, []tmux.Call{tmux.CallCreate}) {
				t.Errorf("tmux calls = %v; want the one create (no end write, no re-lookup)", got)
			}
			if recs := ptRecords(t, mark, "ad.launch.name_held", ids[0]); len(recs) != 0 || strings.Contains(env.logs.String(), "WARN") {
				t.Errorf("ad.launch.name_held records = %v, client log %q; want none, no WARN", recs, env.logs.String())
			}
		})
	}
}

// spawnReturned runs Spawn in its own goroutine, so an after-call hook may end
// it with runtime.Goexit; returned is false when it was ended that way.
func spawnReturned(c *api.Client, p api.SpawnParams) (res api.SpawnResult, err error, returned bool) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err = c.Spawn(p)
		returned = true
	}()
	<-done
	return res, err, returned
}

// TestSpawnLaunchBoundLostReplyKeepsLabel (SR-3.5, SR-9.4, SR-13.2; AC-LKP-17,
// AC-SPN-05): a create that times out (charged its default timeout), gives
// an unparseable reply, or whose launch stops before the identity write
// leaves its session labelled and the row pending with no identity.
func TestSpawnLaunchBoundLostReplyKeepsLabel(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		name         string
		minted       bool            // no caller-supplied id, so no scan lookup
		script       *tmuxfix.Script // the create's scripted result; nil answers from the table
		stop         bool            // an after-call hook on the create ends Spawn
		unresponsive bool            // ErrTmuxUnresponsive; else Spawn succeeds (or never returns)
		unrecognised bool            // the description's unrecognised-reply form
	}{
		{name: "create times out, minted id", minted: true, script: &tmuxfix.Script{Failure: tmux.FailTimeout, Applied: true},
			unresponsive: true},
		{name: "non-zero exit with unparseable reply", script: &tmuxfix.Script{Failure: tmux.FailUnrecognized,
			ExitStatus: 1, HadStdout: true, Applied: true}, unresponsive: true, unrecognised: true},
		{name: "exit 0 with unparseable reply", script: &tmuxfix.Script{Failure: tmux.FailUnrecognized,
			ExitStatus: 0, HadStdout: true, Applied: true}},
		{name: "stop before the identity write", stop: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id, calls, launchStart := heldID(), []tmux.Call{tmux.CallLookup, tmux.CallCreate}, e.start.Add(boundQ)
			if tc.minted {
				id, calls, launchStart = "", calls[1:], e.start
			}
			if tc.script != nil {
				e.rec.Script(tmuxfix.AnySocket, *tc.script, tmux.CallCreate)
			}
			if tc.stop {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { runtime.Goexit() })
			}
			mark := trailLen(t)

			res, err, returned := spawnReturned(e.c, api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})

			if got := callKinds(e.rec); !reflect.DeepEqual(got, calls) {
				t.Fatalf("tmux calls = %q; want %q (no label by id, no kill)", got, calls)
			}
			create := e.rec.SocketCallsOf(tmux.CallCreate)[0]
			id = create.InstanceID
			switch {
			case tc.stop:
				if returned {
					t.Fatalf("Spawn returned (%+v, %v); want it ended by the after-call hook", res, err)
				}
			case tc.unresponsive:
				assertOneName(t, err, "ErrTmuxUnresponsive")
			case err != nil || res.ClaudeInstanceID != id:
				t.Fatalf("Spawn = (%+v, %v); want success for %s", res, err, id)
			}
			cols, cerr := apitest.ReadSpawnColumns(e.dbPath, id)
			token, _ := cols.LaunchToken.(string)
			if cerr != nil || cols.State != store.StatePending || cols.RowVersion != int64(0) ||
				cols.LaunchStartedAt != launchStart.UnixMilli() || !spawnTokenRE.MatchString(token) || cols.TmuxSocket != e.socket ||
				!reflect.DeepEqual(identityCols(cols), noIdentity) {
				t.Errorf("row %+v (err %v); want pending at version 0, launch start %d, a token, socket %s, no identity",
					cols, cerr, launchStart.UnixMilli(), e.socket)
			}
			if create.Socket != e.socket || create.Token != token {
				t.Errorf("create socket, token = %q, %q; want %q, %q", create.Socket, create.Token, e.socket, token)
			}
			sessions := e.rec.Sessions(e.socket)
			if len(sessions) != 1 || sessions[0].Label != tmuxfix.Valid(token, id, e.storeID) || len(sessions[0].Panes) != 1 ||
				sessions[0].Panes[0].AdPane != token {
				t.Fatalf("sessions on %s = %+v; want one labelled ad1 %s <$N> %s <store id>, its pane labelled", e.socket, sessions, token, id)
			}
			if tc.minted {
				if got := e.clock.Now().Sub(e.start); got != boundC || strings.Contains(e.logs.String(), "WARN") {
					t.Errorf("virtual time charged = %v, client log %q; want the default create timeout %v, no WARN", got, e.logs.String(), boundC)
				}
			}
			if recs := ptRecords(t, mark, "ad.launch.name_held", id); len(recs) != 0 {
				t.Errorf("ad.launch.name_held records = %v; want none (no held-name path)", recs)
			}
			if tc.unresponsive {
				_, desc := errnames.Classify(err)
				apitest.AssertDescription(t, desc, apitest.DescLaunchTimeout(apitest.LaunchTimeout{InstanceID: id, Timeout: boundC,
					Unrecognised: tc.unrecognised, ExplicitSpawnID: !tc.minted}),
					token, e.storeID, tmuxfix.LabelValue(token, sessions[0].ID, id, e.storeID))
			}
		})
	}
}

// TestSpawnLaunchBoundCeiling (SR-13.2; AC-SPN-06): the longest plain-spawn path
// (chained label, relabel and kill all failing) charges C + 2A, plus Q for a
// caller-supplied id's scan, and leaves the row pending.
func TestSpawnLaunchBoundCeiling(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	const name = "bound-ceiling"
	cases := []struct {
		name     string
		id       string
		wantCall []tmux.Call
		want     time.Duration
	}{
		{"minted id", "", []tmux.Call{tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession},
			boundC + 2*boundA},
		{"caller-supplied id", "bound-ceiling-" + uuid.NewString()[:8],
			[]tmux.Call{tmux.CallLookup, tmux.CallCreate, tmux.CallSetLabel, tmux.CallKillSession},
			boundQ + boundC + 2*boundA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailLabel}, tmux.CallCreate).
				Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallSetLabel, tmux.CallKillSession)

			_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id,
				TmuxSessionName: name, TmuxSessionNameSupplied: true})
			if !errors.Is(err, api.ErrTmuxSessionCreate) {
				t.Fatalf("Spawn err = %v; want ErrTmuxSessionCreate", err)
			}
			if got := callKinds(e.rec); !reflect.DeepEqual(got, tc.wantCall) {
				t.Fatalf("tmux calls = %q; want %q", got, tc.wantCall)
			}
			if got := e.clock.Now().Sub(e.start); got != tc.want || got > boundCap {
				t.Errorf("virtual time charged = %v; want %v, at most %v (SR-13.2)", got, tc.want, boundCap)
			}
			label := e.rec.SocketCallsOf(tmux.CallSetLabel)[0]
			_, desc := errnames.Classify(err)
			apitest.AssertDescription(t, desc, apitest.DescUnlabelledSession(apitest.UnlabelledSession{
				Name: name, SessionID: label.Target, PlainSpawn: true,
			}), label.Token, e.storeID)
			if cols, err := apitest.ReadSpawnColumns(e.dbPath, label.InstanceID); err != nil || cols.State != store.StatePending {
				t.Errorf("row %s = %v (err %v); want pending", label.InstanceID, cols.State, err)
			}
		})
	}
}
