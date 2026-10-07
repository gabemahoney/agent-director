package api_test

// spawn_scan_test.go covers plain spawn's label scan (SR-9.3, SR-20.6,
// AC-SPN-11): a caller-supplied id with no row makes one lookup before the
// insert; this store's leftover refuses with one ad.launch.name_held, writing
// nothing; other labels and no server proceed; Can't tell refuses.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// scanEnv is the spawn fixture with this store's id and an untouched trust
// file.
type scanEnv struct {
	spawnEnv
	storeID, claudeJSON string
}

// newScanEnv builds a scanEnv.
func newScanEnv(t *testing.T) scanEnv {
	t.Helper()
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

// TestScanRefusesLeftover: a session labelled by this store for the id, under
// any name, refuses the spawn (with the reuse opt-in too), writes nothing but
// one name_held with every SR-14 field, and the spawn succeeds once the
// leftover is gone.
func TestScanRefusesLeftover(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	const base = int64(1790000000)
	cases := []struct {
		name      string
		requested string
		reuse     bool
		sessions  func(e scanEnv, id string) []tmuxfix.SeedSession
		named     []int // indexes into sessions, lowest $N first
	}{
		{"another name, with the reuse opt-in", "", true, func(e scanEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover("old-life", "$3", id, base)}
		}, []int{0}},
		{"the requested name", "scan-held", false, func(e scanEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover("scan-held", "$0", id, base)}
		}, []int{0}},
		{"two sessions", "", false, func(e scanEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover("a-old", "$7", id, base), e.leftover("b-old", "$4", id, base+1)}
		}, []int{1, 0}},
		{"four sessions, numeric order", "", false, func(e scanEnv, id string) []tmuxfix.SeedSession {
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
			p := api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id, ReuseFinished: tc.reuse}
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
			recs := trailSince(t, mark, "ad.launch.name_held")
			if len(recs) != 1 {
				t.Fatalf("ad.launch.name_held records = %d; want 1", len(recs))
			}
			first := seeded[tc.named[0]]
			want := nameHeldFields("spawn", id, first.Name, e.socket, e.storeID, &first, "leftover", "ErrTmuxSessionConflict",
				"not_inserted", nil, true, false)
			want["leftover_count"] = float64(len(seeded))
			assertTrailRecord(t, recs[0], append(slices.Clone(ptKeys), "leftover_count"), want)
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

// TestScanProceeds: another store's label for the id (with the reuse opt-in
// too), a foreign label and no server let the spawn reach its create, the
// scan's lookup made before the row is inserted, and the new row's first life.
func TestScanProceeds(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	otherStore := func(e scanEnv, id string) {
		e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{Name: "elsewhere",
			Label: tmuxfix.Valid(tmuxfix.OtherToken, id, apitest.OtherStoreID(e.storeID))})
	}
	cases := []struct {
		name  string
		reuse bool
		setup func(e scanEnv, id string)
	}{
		{"another store's label for the id", false, otherStore},
		{"another store's label for the id, with the reuse opt-in", true, otherStore},
		{"foreign label", false, func(e scanEnv, _ string) {
			e.rec.SeedSessions(e.socket, e.leftover("foreign", "", scanID(), 0))
		}},
		{"no server", false, func(e scanEnv, _ string) { e.rec.SetNoServerFailure(e.socket, tmux.FailNoServer) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newScanEnv(t)
			id := scanID()
			tc.setup(e, id)
			rowAtLookup := true
			e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				_, err := apitest.ReadSpawnColumns(e.dbPath, id)
				rowAtLookup = !errors.Is(err, store.ErrSpawnNotFound)
			})
			mark := trailLen(t)

			if _, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id, ReuseFinished: tc.reuse}); err != nil {
				t.Fatalf("Spawn: %v", err)
			}

			if got := callKinds(e.rec); !slices.Equal(got, []tmux.Call{tmux.CallLookup, tmux.CallCreate}) || rowAtLookup {
				t.Errorf("tmux calls = %v, row present at the lookup %v; want a lookup with no row yet, then the create", got, rowAtLookup)
			}
			if cols, err := apitest.ReadSpawnColumns(e.dbPath, id); err != nil || cols.State != store.StatePending || cols.LifeNumber != int64(0) {
				t.Errorf("row %+v (err %v); want pending in life 0", cols, err)
			}
			for _, ev := range []string{"ad.launch.name_held", "ad.spawn.reused"} {
				if recs := ptRecords(t, mark, ev, id); len(recs) != 0 {
					t.Errorf("%s records = %v; want none", ev, recs)
				}
			}
		})
	}
}

// TestScanCantTellRefuses: an unreadable, conflicting or unavailable lookup
// refuses with its usual error and writes nothing.
func TestScanCantTellRefuses(t *testing.T) {
	// Serial: it sets AGENT_DIRECTOR_INSTANCE_ID, HOME, TMUX, TMUX_TMPDIR with t.Setenv.
	const timeout = 700 * time.Millisecond
	cases := []struct {
		name  string
		setup func(e scanEnv, id string)
		want  error
		desc  func(e scanEnv, id string) apitest.DescCase
	}{
		{"timeout", func(e scanEnv, _ string) {
			e.rec.WithVirtualTime(e.clock, tmux.Timeouts{Query: timeout})
			e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup)
		}, api.ErrTmuxUnresponsive, func(scanEnv, string) apitest.DescCase {
			return apitest.DescCallTimeout(tmux.CallLookup, timeout)
		}},
		{"scope value", func(e scanEnv, id string) {
			e.rec.SeedSessions(e.socket, e.leftover("old-life", "", id, 0))
			e.rec.SetScope(e.socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}, api.ErrTmuxSessionConflict, func(_ scanEnv, id string) apitest.DescCase {
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: id, Scope: true})
		}},
		{"socket permission", func(e scanEnv, _ string) {
			e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailSocketDenied}, tmux.CallLookup)
		}, tmux.ErrTmuxNotAvailable, func(e scanEnv, _ string) apitest.DescCase {
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
