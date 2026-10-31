package api_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

// find-missing's provisional-transcript healing (b.v2c AC3, SR-11.7); fakes and runFindMissing live in
// find_missing_test.go.

// TestFindMissingHealsProvisionalTranscript: a provisional row (NULL jsonl_path) whose transcript has since
// appeared is healed with the path recomposed under a usable CLAUDE_CONFIG_DIR, else ~/.claude (b.v2c AC3/AC4,
// b.nje), also while the row is pending inside its grace period (not judged); one whose transcript is still absent
// stays provisional.
func TestFindMissingHealsProvisionalTranscript(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	cases := []struct {
		name      string
		configDir string
		absolute  bool // configDir is a per-test absolute directory
		absent    bool // the transcript has not appeared
		pending   bool // the row is pending inside its grace period
	}{
		{name: "no CLAUDE_CONFIG_DIR"},
		{name: "absolute CLAUDE_CONFIG_DIR", absolute: true},
		{name: "relative CLAUDE_CONFIG_DIR falls back to ~/.claude", configDir: "rel"},
		{name: "pending inside grace", pending: true},
		{name: "transcript still absent", absent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const (
				id      = "prov-heal-1"
				session = "session-heal-uuid"
				cwd     = "/tmp/proj"
			)
			cfg := tc.configDir
			var want []healCall
			switch {
			case tc.absent:
			case tc.absolute:
				cfg = t.TempDir()
				want = []healCall{{id, session, apitest.SeedJsonlUnder(t, cfg, cwd, session)}}
			default:
				want = []healCall{{id, session, apitest.SeedJsonl(t, cwd, session)}}
			}
			pc := procfix.New()
			pc.Set(11, procfix.Alive(fmStart))
			r := liveRow(id, withSessionStart(11, fmStart))
			if tc.pending {
				r = liveRow(id, withLaunch(store.StatePending, fmNow.Add(-fmGrace+time.Second).UnixMilli()), withPane(10, fmStart))
			}
			st := &fakeFindMissingStore{
				rows:        []store.LiveSpawnIdentity{r},
				provisional: []store.ProvisionalTranscript{{InstanceID: id, SessionID: session, CWD: cwd, ConfigDir: cfg}},
			}

			res := mustFindMissing(t, st, pc)
			assertLists(t, res, nil, nil)
			if !slices.Equal(st.healed, want) || len(st.calls) != 0 {
				t.Errorf("heal calls = %+v, writes %+v; want %+v and no write", st.healed, st.calls, want)
			}
		})
	}
}

// b.x9o: the production provisional sample/commit capability must be wired
// into the first selected read and the first sweep, on fresh real stores.
func TestProvisionalTranscriptSharedFirstCall(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"list", "find-missing"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			cfg := t.TempDir()
			e := newPaneEnv(t, paneSpec{agent: store.AgentClaude, state: store.StateWaiting,
				opts: []apitest.SpawnOption{apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": cfg})}})
			session := uuid.NewString()
			if got := storefix.ApplyAgentHook(t, e.s, e.id, "SessionStart", session, storefix.HookTranscript("", false)); !got.Applied {
				t.Fatalf("SessionStart: %+v", got)
			}
			path := apitest.SeedJsonlUnder(t, cfg, e.row(t).CWD, session)
			paths := map[string]string{e.id: path}
			for _, state := range []string{store.StateWaiting, store.StateEnded} {
				sid := uuid.NewString()
				id, err := apitest.SeedSpawn(e.dbPath, "", state, "/tmp", "off", sid, false,
					apitest.WithNoPane(), apitest.WithPID(apitest.TestPanePID), apitest.WithProcStarttime(apitest.LinuxProcStarttime),
					apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": cfg}))
				if err != nil {
					t.Fatal(err)
				}
				paths[id] = apitest.SeedJsonlUnder(t, cfg, "/tmp", sid)
			}
			before := map[string]store.Spawn{}
			for id := range paths {
				row, err := e.s.GetSpawn(id)
				if err != nil || row.JSONLPath != "" {
					t.Fatalf("provisional precondition: %+v %v", row, err)
				}
				before[id] = row
			}
			c := e.client(t, apitest.ConfigKeys{})
			if verb == "list" {
				got, err := c.List(api.ListParams{TmuxSessionName: before[e.id].TmuxSessionName})
				if err != nil || len(got.Spawns) != 1 || got.Spawns[0].InstanceID != e.id {
					t.Fatalf("selected read: %+v %v", got, err)
				}
			} else {
				got, err := c.FindMissing(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				assertLists(t, got, nil, nil)
			}
			for id, want := range before {
				if want.State != store.StateEnded && (verb == "find-missing" || id == e.id) {
					want.JSONLPath = paths[id]
					want.RowVersion++
					want.Snapshot.RowVersion++
				}
				got, err := e.s.GetSpawn(id)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("first-call stored result %s: got=%+v want=%+v err=%v", id, got, want, err)
				}
			}
			if calls := e.rec.SocketCalls(); len(calls) != 0 {
				t.Errorf("transcript recovery dispatched pane work: %+v", calls)
			}
		})
	}
}
