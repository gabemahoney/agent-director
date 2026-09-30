package api_test

// spawn_scan_test.go covers plain spawn's label scan (SR-9.3, SR-20.6,
// AC-SPN-11): a caller-supplied id with no row makes one lookup; a leftover
// labelled by this store refuses with ErrTmuxSessionConflict and one
// ad.launch.name_held record, writing nothing; other labels and no server
// proceed; Can't tell refuses; minted ids and existing rows are not scanned.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// scanEnv is the spawn fixture with this store's id and an untouched trust
// file.
type scanEnv struct {
	spawnEnv
	storeID, claudeJSON string
}

// newScanEnv builds a scanEnv; it pins the trail singleton to TestMain's HOME
// first, since the fixture moves HOME per test.
func newScanEnv(t *testing.T) scanEnv {
	t.Helper()
	trail.Default()
	env := newSpawnEnv(t)
	storeID, err := apitest.ReadStoreID(env.dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	claudeJSON := filepath.Join(env.home, ".claude.json")
	if err := os.WriteFile(claudeJSON, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
	return scanEnv{spawnEnv: env, storeID: storeID, claudeJSON: claudeJSON}
}

// scanID returns a fresh caller-supplied instance id.
func scanID() string { return "scan-" + uuid.NewString()[:8] }

// leftover is a session of this store labelled for id with another token.
func (e scanEnv) leftover(name, sessionID, id string, created int64) tmuxfix.SeedSession {
	return tmuxfix.SeedSession{ID: sessionID, Name: name, Created: created,
		Label: tmuxfix.Valid(tmuxfix.OtherToken, id, e.storeID)}
}

// forbid lists the values no description may carry: tokens, store ids and
// the raw label values of the seeded sessions.
func (e scanEnv) forbid(id string, sessions []tmuxfix.SeedSession) []string {
	out := []string{tmuxfix.Token, tmuxfix.OtherToken, e.storeID, apitest.OtherStoreID(e.storeID)}
	for _, s := range sessions {
		out = append(out, tmuxfix.LabelValue(s.Label.Token, s.ID, id, s.Label.StoreID))
	}
	return out
}

// assertNothingWritten checks no row, an untouched trust file, no socket
// directory, and exactly one lookup as the only tmux call.
func (e scanEnv) assertNothingWritten(t *testing.T, id string) {
	t.Helper()
	if _, err := apitest.ReadSpawnColumns(e.dbPath, id); !errors.Is(err, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns err = %v; want ErrSpawnNotFound", err)
	}
	if b, err := os.ReadFile(e.claudeJSON); err != nil || string(b) != "{}" {
		t.Errorf(".claude.json = %q (err %v); want untouched {}", b, err)
	}
	if _, err := os.Stat(filepath.Dir(e.socket)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("socket directory stat err = %v; want not created", err)
	}
	calls := e.rec.SocketCalls()
	if len(calls) != 1 || calls[0].Call != tmux.CallLookup || calls[0].Socket != e.socket {
		t.Errorf("socket calls = %+v; want exactly one lookup on %s", calls, e.socket)
	}
	if n := len(e.rec.Calls()); n != 0 {
		t.Errorf("name-based calls = %d; want 0", n)
	}
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

// trailLen returns the trail's current line count, a checkpoint.
func trailLen(t *testing.T) int { return len(readAPITrailLines(t)) }

// trailSince returns the event records added after the first n lines.
func trailSince(t *testing.T, n int, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range readAPITrailLines(t)[n:] {
		if r["event"] == event {
			out = append(out, r)
		}
	}
	return out
}

// assertNameHeld checks the single ad.launch.name_held record's fields for
// first, the lowest-$N leftover of count.
func (e scanEnv) assertNameHeld(t *testing.T, recs []map[string]any, id string, first tmuxfix.SeedSession, count int) {
	t.Helper()
	if len(recs) != 1 {
		t.Fatalf("ad.launch.name_held records = %d; want 1", len(recs))
	}
	host, _ := os.Hostname()
	var username string
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	q := func(s string) string { return "'" + s + "'" }
	want := map[string]any{
		"source": "ad_spawn", "claude_instance_id": id, "launch": "spawn",
		"tmux_session_name": first.Name, "tmux_session_id": first.ID, "session_created": float64(first.Created),
		"tmux_socket": e.socket, "store_id": e.storeID, "carries_this_id": true, "current_launch": false,
		"lookup_outcome": "leftover", "outcome": "ErrTmuxSessionConflict", "row_result": "not_inserted",
		"leftover_count": float64(count), "store_error": nil,
		"attach_command": "tmux -u -S " + q(e.socket) + " attach-session -r -t " + q(first.ID),
		"end_command":    "tmux -u -S " + q(e.socket) + " kill-session -t " + q(first.ID),
		"caller_process": filepath.Base(os.Args[0]), "caller_pid": float64(os.Getpid()),
		"caller_hostname": host, "caller_user": username,
	}
	for k, v := range want {
		if got, ok := recs[0][k]; !ok || got != v {
			t.Errorf("name_held[%q] = %v (present %t); want %v", k, got, ok, v)
		}
	}
}

// TestScanRefusesLeftover: a session labelled by this store for the id, under
// any name, refuses the spawn, writes nothing but one name_held, and the spawn
// succeeds once the leftover is gone.
func TestScanRefusesLeftover(t *testing.T) {
	const base = int64(1790000000)
	cases := []struct {
		name      string
		requested string
		sessions  func(e scanEnv, id string) []tmuxfix.SeedSession
		named     []int // indexes into sessions, lowest $N first
	}{
		{"another name", "", func(e scanEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover("old-life", "$3", id, base)}
		}, []int{0}},
		{"the requested name", "scan-held", func(e scanEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover("scan-held", "$0", id, base)}
		}, []int{0}},
		{"two sessions", "", func(e scanEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover("a-old", "$7", id, base), e.leftover("b-old", "$4", id, base+1)}
		}, []int{1, 0}},
		{"four sessions, numeric order", "", func(e scanEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover("a-old", "$10", id, base), e.leftover("b-old", "$2", id, base+1),
				e.leftover("c-old", "$9", id, base+2), e.leftover("d-old", "$11", id, base+3)}
		}, []int{1, 2, 0, 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newScanEnv(t)
			id := scanID()
			seeded := tc.sessions(e, id)
			e.rec.SeedSessions(e.socket, seeded...)
			before := e.rec.Sessions(e.socket)
			mark := trailLen(t)
			p := api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id}
			if tc.requested != "" {
				p.TmuxSessionName, p.TmuxSessionNameSupplied = tc.requested, true
			}

			_, err := e.c.Spawn(p)

			assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
			var named []apitest.DescSession
			for _, i := range tc.named {
				named = append(named, apitest.DescSession{Name: seeded[i].Name, ID: seeded[i].ID})
			}
			if err != nil {
				apitest.AssertDescription(t, err.Error(), apitest.DescScanLeftover(id, named), e.forbid(id, seeded)...)
			}
			e.assertNothingWritten(t, id)
			if after := e.rec.Sessions(e.socket); !reflect.DeepEqual(before, after) {
				t.Errorf("sessions changed:\nbefore %+v\nafter  %+v", before, after)
			}
			e.assertNameHeld(t, trailSince(t, mark, "ad.launch.name_held"), id, seeded[tc.named[0]], len(seeded))
			if d := trailSince(t, mark, "ad.provenance.disagree"); len(d) != 0 {
				t.Errorf("ad.provenance.disagree records = %v; want none", d)
			}

			for _, s := range seeded {
				if kerr := e.rec.KillSessionID(e.socket, s.ID); kerr != nil {
					t.Fatalf("remove leftover %s: %v", s.ID, kerr)
				}
			}
			res, err := e.c.Spawn(p)
			if err != nil || res.ClaudeInstanceID != id {
				t.Fatalf("Spawn after removal = %+v, %v; want %s", res, err, id)
			}
		})
	}
}

// TestScanProceeds: other stores', foreign and invalid labels, an empty
// server, no server and no socket let the spawn reach its create.
func TestScanProceeds(t *testing.T) {
	other := func(e scanEnv, id, name string) tmuxfix.SeedSession {
		return tmuxfix.SeedSession{Name: name, Label: tmuxfix.Valid(tmuxfix.OtherToken, id, apitest.OtherStoreID(e.storeID))}
	}
	cases := []struct {
		name      string
		requested string
		setup     func(e scanEnv, id string)
		wantErr   error
	}{
		{"another store's label for the id", "", func(e scanEnv, id string) {
			e.rec.SeedSessions(e.socket, other(e, id, "elsewhere"))
		}, nil},
		{"foreign label", "", func(e scanEnv, _ string) {
			e.rec.SeedSessions(e.socket, e.leftover("foreign", "", scanID(), 0))
		}, nil},
		{"no valid label", "", func(e scanEnv, _ string) {
			e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{Name: "malformed", LabelSet: true})
		}, nil},
		{"empty server", "", func(e scanEnv, _ string) { e.rec.StartServer(e.socket, tmuxfix.Server{}) }, nil},
		{"no server", "", func(e scanEnv, _ string) { e.rec.SetNoServerFailure(e.socket, tmux.FailNoServer) }, nil},
		{"missing socket", "", func(scanEnv, string) {}, nil},
		{"requested name held by another store", "scan-held", func(e scanEnv, id string) {
			e.rec.SeedSessions(e.socket, other(e, id, "scan-held"))
		}, api.ErrTmuxSessionCreate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newScanEnv(t)
			id := scanID()
			tc.setup(e, id)
			mark := trailLen(t)
			p := api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id}
			if tc.requested != "" {
				p.TmuxSessionName, p.TmuxSessionNameSupplied = tc.requested, true
			}

			_, err := e.c.Spawn(p)

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			if tc.wantErr != nil {
				assertOneSentinel(t, err, tc.wantErr)
			}
			calls := e.rec.SocketCalls()
			if len(calls) < 2 || calls[0].Call != tmux.CallLookup || calls[1].Call != tmux.CallCreate {
				t.Errorf("socket calls = %+v; want a lookup then the create", calls)
			}
			if n := len(trailSince(t, mark, "ad.launch.name_held")); n != 0 {
				t.Errorf("ad.launch.name_held records = %d; want 0", n)
			}
		})
	}
}

// TestScanCantTellRefuses: an unreadable, conflicting or unavailable lookup
// refuses with its usual error and writes nothing.
func TestScanCantTellRefuses(t *testing.T) {
	const timeout = 700 * time.Millisecond
	const firstLine = "scan: unexpected reply"
	script := func(s tmuxfix.Script) func(e scanEnv, id string) {
		return func(e scanEnv, _ string) { e.rec.Script(e.socket, s, tmux.CallLookup) }
	}
	cases := []struct {
		name  string
		setup func(e scanEnv, id string)
		want  error
		desc  func(e scanEnv, id string) apitest.DescCase
	}{
		{"timeout", func(e scanEnv, id string) {
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: timeout})
			script(tmuxfix.Script{Failure: tmux.FailTimeout})(e, id)
		}, api.ErrTmuxUnresponsive, func(scanEnv, string) apitest.DescCase {
			return apitest.DescCallTimeout(tmux.CallLookup, timeout)
		}},
		{"unrecognised reply", script(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: firstLine, ExitStatus: 1, HadStdout: true}),
			api.ErrTmuxUnresponsive, func(scanEnv, string) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallLookup, firstLine)
			}},
		{"scope value", func(e scanEnv, id string) {
			e.rec.SeedSessions(e.socket, e.leftover("old-life", "", id, 0))
			e.rec.SetScope(e.socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}, api.ErrTmuxSessionConflict, func(_ scanEnv, id string) apitest.DescCase {
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: id, Scope: true})
		}},
		{"binary unavailable", script(tmuxfix.Script{Failure: tmux.FailUnavailable}),
			tmux.ErrTmuxNotAvailable, func(scanEnv, string) apitest.DescCase {
				return apitest.DescTmuxNotRun()
			}},
		{"socket permission", script(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			tmux.ErrTmuxNotAvailable, func(e scanEnv, _ string) apitest.DescCase {
				return apitest.DescSocketPermission(e.socket)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newScanEnv(t)
			id := scanID()
			tc.setup(e, id)
			mark := trailLen(t)

			_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})

			assertOneSentinel(t, err, tc.want)
			if err != nil {
				apitest.AssertDescription(t, err.Error(), tc.desc(e, id), e.forbid(id, e.rec.Sessions(e.socket))...)
			}
			e.assertNothingWritten(t, id)
			if n := len(trailSince(t, mark, "ad.launch.name_held")); n != 0 {
				t.Errorf("ad.launch.name_held records = %d; want 0", n)
			}
		})
	}
}

// TestScanSkippedForMintedID: a spawn with no id makes no lookup; its first
// tmux call is the create.
func TestScanSkippedForMintedID(t *testing.T) {
	e := newScanEnv(t)
	if _, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir()}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	calls := e.rec.SocketCalls()
	if len(calls) == 0 || calls[0].Call != tmux.CallCreate || len(e.rec.SocketCallsOf(tmux.CallLookup)) != 0 {
		t.Errorf("socket calls = %+v; want the create first and no lookup", calls)
	}
}

// TestScanSkippedForExistingRow: a finished or live row for the id is not
// scanned, even with a leftover running, and still collides.
func TestScanSkippedForExistingRow(t *testing.T) {
	for _, state := range []string{store.StateEnded, store.StateMissing, store.StateWorking} {
		t.Run(state, func(t *testing.T) {
			e := newScanEnv(t)
			id := scanID()
			e.rec.SeedSessions(e.socket, e.leftover("old-life", "", id, 0))
			if _, err := apitest.SeedSpawn(e.dbPath, id, state, "", "", "", false); err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}
			mark := trailLen(t)

			_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})

			assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
			assertNoTmuxCalls(t, e.rec)
			if n := len(trailSince(t, mark, "ad.launch.name_held")); n != 0 {
				t.Errorf("ad.launch.name_held records = %d; want 0", n)
			}
		})
	}
}

// scanTrailChildEnv marks the child process of TestScanNameHeldFailOpen.
const scanTrailChildEnv = "AD_SCAN_TRAIL_FAIL_CHILD"

// TestScanNameHeldFailOpen: when the trail cannot be written the refusal is
// unchanged; run in a child process whose trail singleton cannot open.
func TestScanNameHeldFailOpen(t *testing.T) {
	if os.Getenv(scanTrailChildEnv) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestScanNameHeldFailOpen$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
		cmd.Env = append(os.Environ(), scanTrailChildEnv+"=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestScanNameHeldFailOpen") {
			t.Fatalf("child: %v\n%s", err, out)
		}
		return
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".agent-director"), nil, 0o600); err != nil {
		t.Fatalf("block trail directory: %v", err)
	}
	t.Setenv("HOME", home)
	if err := trail.Emit(context.Background(), "ad.test.scan_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}
	e := newScanEnv(t)
	id := scanID()
	left := e.leftover("old-life", "$5", id, 0)
	e.rec.SeedSessions(e.socket, left)

	_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})

	assertOneSentinel(t, err, api.ErrTmuxSessionConflict)
	if err != nil {
		apitest.AssertDescription(t, err.Error(),
			apitest.DescScanLeftover(id, []apitest.DescSession{{Name: left.Name, ID: left.ID}}),
			e.forbid(id, []tmuxfix.SeedSession{left})...)
	}
	e.assertNothingWritten(t, id)
}
