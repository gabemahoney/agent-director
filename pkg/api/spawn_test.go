package api_test

// spawn_test.go holds the shared spawn fixture (spawnEnv) and the checks every
// spawn test file uses, and covers Client.Spawn's explicit instance ids
// (SR-9.1, AC-SPN-02) and its collision pre-check (SR-9.3, SR-10.1,
// AC-SPN-03, AC-REUSE-13).

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// spawnEnv is an isolated Client wired to a tmuxfix.Recorder, its start-time
// reader, clock, captured log, paths and socket.
type spawnEnv struct {
	c      *api.Client
	rec    *tmuxfix.Recorder
	pc     *procfix.Checker
	clock  *tmuxfix.Clock
	logs   *bytes.Buffer
	dbPath string
	home   string
	socket string // the socket a launch resolves with TMUX unset
}

// newSpawnEnv builds a spawnEnv over a fresh store and empty config under a
// temp HOME and a per-test TMUX_TMPDIR, with a Recorder as its tmux client.
func newSpawnEnv(t *testing.T) spawnEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "") // no parent id from the host shell
	tmpdir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	t.Setenv("TMUX_TMPDIR", tmpdir)
	t.Setenv("TMUX", "")
	os.Unsetenv("TMUX")
	env := spawnEnv{dbPath: filepath.Join(home, "state.db"), home: home, logs: &bytes.Buffer{}, rec: tmuxfix.NewRecorder(),
		pc: procfix.New(), clock: tmuxfix.NewClock(time.Date(2026, 9, 29, 12, 0, 0, 123_000_000, time.UTC)),
		socket: filepath.Join(userSocketDir(tmpdir), "default")}
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if env.c, err = api.New(api.Options{StorePath: env.dbPath, ConfigPath: cfgPath, CreateIfMissing: true,
		Logger: log.New(env.logs, "", 0), TmuxClient: env.rec}); err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = env.c.Close() })
	api.SetClockForTest(env.c, env.clock.Now)
	api.SetProcCheckerForTest(env.c, env.pc)
	return env
}

// userSocketDir is tmux's per-user socket directory under base.
func userSocketDir(base string) string {
	return filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
}

// assertNoTmuxCalls fails when the Recorder saw any name-based or socket call.
func assertNoTmuxCalls(t *testing.T, rec *tmuxfix.Recorder) {
	t.Helper()
	if n, m := len(rec.Calls()), len(rec.SocketCalls()); n != 0 || m != 0 {
		t.Errorf("tmux calls: %d name-based, %d socket; want none", n, m)
	}
}

// callKinds returns the kinds of every recorded socket-taking call, in order.
func callKinds(rec *tmuxfix.Recorder) []tmux.Call {
	var out []tmux.Call
	for _, c := range rec.SocketCalls() {
		out = append(out, c.Call)
	}
	return out
}

// listIDs returns the instance ids of every row List reports.
func listIDs(t *testing.T, c *api.Client) []string {
	t.Helper()
	res, err := c.List(api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	ids := make([]string, 0, len(res.Spawns))
	for _, r := range res.Spawns {
		ids = append(ids, r.ClaudeInstanceID)
	}
	return ids
}

// assertOneSentinel checks err matches want and no other catalogued error.
func assertOneSentinel(t *testing.T, err, want error) {
	t.Helper()
	var matched []string
	for _, e := range errnames.Catalog {
		if errors.Is(err, e.Err) {
			matched = append(matched, e.Name)
		}
	}
	if !errors.Is(err, want) || len(matched) != 1 {
		t.Errorf("err %v matches %q; want exactly %v", err, matched, want)
	}
}

// assertLaunchSentinel is assertOneSentinel, stopping the test unless err is want (SR-1.5).
func assertLaunchSentinel(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v; want %v", err, want)
	}
	assertOneSentinel(t, err, want)
}

// assertOnlyCatalogued is assertOneName for a non-internal want.
func assertOnlyCatalogued(t *testing.T, err error, want string) {
	t.Helper()
	assertOneName(t, err, want)
}

// reuseRowState is everything about one row a spawn could change: its
// columns, its session history over every life and its permission requests.
type reuseRowState struct {
	cols    apitest.SpawnColumns
	history []apitest.HistoryEntry
	perms   []api.PermissionRow
}

// readReuseRowState reads id's reuseRowState from the store at dbPath.
func readReuseRowState(t *testing.T, dbPath, id string) reuseRowState {
	t.Helper()
	cols, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	history, err := apitest.ReadSessionHistoryAllLives(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSessionHistoryAllLives(%s): %v", id, err)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close() //nolint:errcheck
	perms, err := st.PermissionRequestsForSpawn(id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn(%s): %v", id, err)
	}
	return reuseRowState{cols: cols, history: history, perms: perms}
}

// seedFinishedRow seeds a row in state with a session id, one archived history
// entry, one permission request and opts, and returns its id and reuseRowState.
func seedFinishedRow(t *testing.T, dbPath, state string, opts ...apitest.SpawnOption) (string, reuseRowState) {
	t.Helper()
	id := "reuse-" + uuid.NewString()[:8]
	if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", uuid.NewString(), false, append([]apitest.SpawnOption{
		apitest.WithLifeNumber(1),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: uuid.NewString(), JSONLPath: "/tmp/old.jsonl", Life: 1})},
		opts...)...); err != nil {
		t.Fatalf("SeedSpawn(%s): %v", state, err)
	}
	if _, err := apitest.SeedPermissionRequest(dbPath, id, "Bash"); err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	return id, readReuseRowState(t, dbPath, id)
}

// assertRowStateUnchanged fails unless id's reuseRowState still equals before.
func assertRowStateUnchanged(t *testing.T, dbPath, id string, before reuseRowState) {
	t.Helper()
	if after := readReuseRowState(t, dbPath, id); !reflect.DeepEqual(after, before) {
		t.Errorf("row %s changed:\nbefore %+v\nafter  %+v", id, before, after)
	}
}

// TestSpawnRejectsControlCharacterInstanceID (SR-9.1): a caller-supplied id
// holding any byte 0x00-0x1f or 0x7f is ErrInvalidFlags before every other
// check (cwd, template, session name, collision, the reuse opt-in), with no
// row and no tmux call; a row whose id already holds one stays listed.
func TestSpawnRejectsControlCharacterInstanceID(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	const marker = "idmark"
	cases := []struct {
		name  string
		id    string
		setup func(t *testing.T, env spawnEnv, p *api.SpawnParams)
	}{
		{"newline middle", marker + "\n-a", nil},
		{"tab leading", "\t" + marker, nil},
		{"NUL trailing", marker + "\x00", nil},
		{"unit separator 0x1f", marker + "\x1f-b", nil},
		{"DEL, with the reuse opt-in", marker + "\x7f", func(_ *testing.T, _ spawnEnv, p *api.SpawnParams) { p.ReuseFinished = true }},
		{"before a missing cwd", marker + "\tx", func(_ *testing.T, _ spawnEnv, p *api.SpawnParams) { p.CWD = "" }},
		{"before a nonexistent cwd", marker + "\tx", func(t *testing.T, _ spawnEnv, p *api.SpawnParams) {
			p.CWD = filepath.Join(t.TempDir(), "absent")
		}},
		{"before a nonexistent template", marker + "\tx", func(_ *testing.T, _ spawnEnv, p *api.SpawnParams) {
			p.Template = "no-such-template"
		}},
		{"before a malformed template", marker + "\tx", func(t *testing.T, env spawnEnv, p *api.SpawnParams) {
			if _, err := apitest.SeedTemplate(filepath.Join(env.home, ".agent-director", "templates"), "broken",
				"not = [valid toml"); err != nil {
				t.Fatalf("SeedTemplate: %v", err)
			}
			p.Template = "broken"
		}},
		{"before an invalid session name", marker + "\tx", func(_ *testing.T, _ spawnEnv, p *api.SpawnParams) {
			p.TmuxSessionName, p.TmuxSessionNameSupplied = "bad:name", true
		}},
		{"before a live row with the same id", marker + "\tx", func(t *testing.T, env spawnEnv, p *api.SpawnParams) {
			if _, err := apitest.SeedSpawn(env.dbPath, p.ClaudeInstanceID, store.StateWaiting, "", "", "", false); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			if ids := listIDs(t, env.c); len(ids) != 1 || ids[0] != p.ClaudeInstanceID {
				t.Errorf("List ids = %q; want the control-character row listed", ids)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			p := api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id}
			if tc.setup != nil {
				tc.setup(t, env, &p)
			}
			rows := listIDs(t, env.c)

			_, err := env.c.Spawn(p)

			if !errors.Is(err, api.ErrInvalidFlags) {
				t.Fatalf("Spawn err = %v; want ErrInvalidFlags", err)
			}
			apitest.AssertDescription(t, err.Error(), apitest.DescInstanceIDControlChar(tc.id), marker)
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); !reflect.DeepEqual(ids, rows) {
				t.Errorf("List ids = %q; want unchanged %q", ids, rows)
			}
		})
	}
}

// TestSpawnAcceptsEmptyAndPrintableInstanceID: an empty id mints a fresh UUID
// (with the reuse opt-in too, a finished row of another id left as it was)
// and printable ids (space, '~', non-ASCII) spawn; none is over-rejected.
func TestSpawnAcceptsEmptyAndPrintableInstanceID(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	suffix := uuid.NewString()[:8]
	cases := []struct {
		name  string
		id    string
		reuse bool // with the reuse opt-in, beside a finished row
	}{
		{"empty mints fresh id", "", false},
		{"empty with the reuse opt-in mints fresh id", "", true},
		{"plain printable", "agent-" + suffix, false},
		{"space tilde and UTF-8", "id é ~" + suffix, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSpawnEnv(t)
			var other string
			var before reuseRowState
			if tc.reuse {
				other, before = seedFinishedRow(t, env.dbPath, store.StateEnded)
			}
			res, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: tc.id, ReuseFinished: tc.reuse})
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			got := res.ClaudeInstanceID
			if tc.id == "" {
				if _, perr := uuid.Parse(got); perr != nil {
					t.Errorf("minted id %q is not a UUID: %v", got, perr)
				}
			} else if got != tc.id {
				t.Errorf("ClaudeInstanceID = %q; want %q", got, tc.id)
			}
			if n := len(env.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("create calls = %d; want 1", n)
			}
			if cols, err := apitest.ReadSpawnColumns(env.dbPath, got); err != nil || cols.State != store.StatePending {
				t.Errorf("new row state = %v (err %v); want pending", cols.State, err)
			}
			want := 1
			if tc.reuse {
				want = 2
				assertRowStateUnchanged(t, env.dbPath, other, before)
			}
			if ids := listIDs(t, env.c); len(ids) != want {
				t.Errorf("List ids = %q; want %d rows (the new one, and any finished one)", ids, want)
			}
		})
	}
}

// failingCollisionReader is a spawn.CollisionChecker whose store read fails.
type failingCollisionReader struct{ err error }

func (f failingCollisionReader) SpawnState(string) (string, bool, error) { return "", false, f.err }

// TestSpawnPreCheckReadFailureIsErrInternal: a failed pre-check read is
// ErrInternal (even when the store error wraps a sentinel) and creates nothing.
func TestSpawnPreCheckReadFailureIsErrInternal(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	for _, readErr := range []error{
		errors.New("store: live spawn lookup: disk I/O error"),
		fmt.Errorf("store: live spawn lookup: %w", spawn.ErrInstanceIdCollision),
		fmt.Errorf("store: live spawn lookup: %w", store.ErrSpawnNotFound),
	} {
		t.Run(readErr.Error(), func(t *testing.T) {
			env := newSpawnEnv(t)
			claudeJSON := filepath.Join(env.home, ".claude.json")
			if err := os.WriteFile(claudeJSON, []byte("{}"), 0o600); err != nil {
				t.Fatalf("write .claude.json: %v", err)
			}
			_, err := api.SpawnWithCollisionReader(env.c, failingCollisionReader{err: readErr},
				api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: "precheck-" + uuid.NewString()[:8]})
			assertOneName(t, err, "ErrInternal")
			_, desc := errnames.Classify(err)
			apitest.AssertDescription(t, desc, apitest.DescPreCheckRead())
			assertNoTmuxCalls(t, env.rec)
			if ids := listIDs(t, env.c); len(ids) != 0 {
				t.Errorf("List ids = %q; want none", ids)
			}
			if b, err := os.ReadFile(claudeJSON); err != nil || string(b) != "{}" {
				t.Errorf(".claude.json = %q (err %v); want untouched {}", b, err)
			}
		})
	}
}

// TestSpawnExistingRowCollides (SR-9.3, SR-10.2; b.hjs): a row with the id
// collides at the pre-check when it is finished and the call lacks the reuse
// opt-in, or live with or without it: ErrInstanceIdCollision, even with a
// leftover of the id running and an unusable recorded name; no tmux call, no
// socket directory, no trail record, the row and the trust file unchanged.
func TestSpawnExistingRowCollides(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	cases := []struct {
		state string
		reuse bool
	}{
		{store.StateEnded, false}, {store.StateMissing, false}, {store.StatePending, false}, {store.StateWorking, false},
		{store.StatePending, true}, {store.StateWaiting, true}, {store.StateWorking, true}, {store.StateAskUser, true},
		{store.StateCheckPermission, true},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s, reuse opt-in %t", tc.state, tc.reuse), func(t *testing.T) {
			e := newScanEnv(t)
			id, before := seedFinishedRow(t, e.dbPath, tc.state, apitest.WithTmuxSessionName(preGqeDefaultName))
			e.rec.SeedSessions(e.socket, e.leftover("old-life", "", id, 0))
			trust, cwd, mark := seedTrustConfig(t, t.TempDir(), trustLacksEntry), t.TempDir(), trailLen(t)

			_, err := e.c.Spawn(api.SpawnParams{CWD: cwd, ClaudeInstanceID: id, ReuseFinished: tc.reuse,
				ExtraEnv: trust.extraEnv()})

			assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
			want := "ErrInstanceIdCollision: " + id
			if !slices.Contains(finishedStates, tc.state) {
				want += " already live"
			}
			if err != nil && err.Error() != want {
				t.Errorf("err = %q; want %q", err, want)
			}
			assertRowStateUnchanged(t, e.dbPath, id, before)
			assertNoTmuxCalls(t, e.rec)
			trust.check(t, cwd, false, "after the refused spawn")
			if _, serr := os.Lstat(filepath.Dir(e.socket)); !errors.Is(serr, os.ErrNotExist) {
				t.Errorf("socket directory %s: Lstat err = %v; want it not created", filepath.Dir(e.socket), serr)
			}
			for _, l := range readAPITrailLines(t)[mark:] {
				if l["claude_instance_id"] == id {
					t.Errorf("trail record %v; want none for %s", l, id)
				}
			}
		})
	}
}
