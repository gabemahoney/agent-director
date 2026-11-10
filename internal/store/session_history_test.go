package store_test

// Session history (b.v2c, b.5jm, SR-5.9, SR-8.7): the life-keyed re-archive,
// the life-filtered read, and the provisional transcript and its heal. The
// rotation archive inside SessionStart is pinned in
// hook_gate_session_start_test.go. Rows are seeded through apitest.

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// historyAllLives returns id's history entries from every life.
func (f *v5Store) historyAllLives(id string) []apitest.HistoryEntry {
	f.t.Helper()
	h, err := apitest.ReadSessionHistoryAllLives(f.path, id)
	if err != nil {
		f.t.Fatalf("ReadSessionHistoryAllLives: %v", err)
	}
	return h
}

// historyIDs returns the session ids ListSessionHistory gives for id in life,
// failing on a nil result.
func (f *v5Store) historyIDs(t *testing.T, id string, life int64) []string {
	t.Helper()
	h, err := f.s.ListSessionHistory(id, life)
	if err != nil || h == nil {
		t.Fatalf("ListSessionHistory(%s, %d) = %v, %v; want a non-nil list", id, life, h, err)
	}
	ids := []string{}
	for _, e := range h {
		ids = append(ids, e.SessionID)
	}
	return ids
}

// sessionStart records the agent's SessionStart with sessionID and a
// transcript path present on disk or not; it must apply.
func (f *v5Store) sessionStart(id, sessionID, path string, present bool) {
	f.t.Helper()
	if got := storefix.ApplyAgentHook(f.t, f.s, id, "SessionStart", sessionID, storefix.HookTranscript(path, present)); !got.Applied {
		f.t.Fatalf("SessionStart(%s, %s) = %+v; want applied", id, sessionID, got)
	}
}

// rotate records the agent's SessionStart with a new session id, archiving
// the current one.
func (f *v5Store) rotate(id, newSessionID string) {
	f.t.Helper()
	f.sessionStart(id, newSessionID, "/x/"+newSessionID+".jsonl", true)
}

// TestSessionHistoryReArchiveAcrossLives: re-archiving X from a row in life 1
// keeps one entry per session id, moved to life 1 with recorded_at refreshed;
// its path is the row's, except that a NULL row path keeps a same-life entry's
// known one (COALESCE), and a NULL entry path is filled (b.5jm/4).
func TestSessionHistoryReArchiveAcrossLives(t *testing.T) {
	cases := []struct {
		name      string
		priorLife int64
		priorPath string // "" seeds the entry's path NULL
		rowPath   string // "" leaves the row's jsonl_path NULL
		wantPath  string // "" = NULL
	}{
		{"cross-life path known", 0, "/x/X-old.jsonl", "/x/X-life1.jsonl", "/x/X-life1.jsonl"},
		{"cross-life path NULL", 0, "/x/X-old.jsonl", "", ""},
		{"same-life path known", 1, "/x/X-old.jsonl", "/x/X-life1.jsonl", "/x/X-life1.jsonl"},
		{"same-life NULL path keeps the known one", 1, "/x/X-old.jsonl", "", "/x/X-old.jsonl"},
		{"same-life NULL entry path filled", 1, "", "/x/X-life1.jsonl", "/x/X-life1.jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newV5Store(t)
			opts := []apitest.SpawnOption{apitest.WithLifeNumber(1), apitest.WithSessionHistory(apitest.SessionHistorySeed{
				SessionID: "X", JSONLPath: tc.priorPath, Life: tc.priorLife, RecordedAt: time.Now().Add(-time.Hour)})}
			if tc.rowPath != "" {
				opts = append(opts, apitest.WithJsonlPath(tc.rowPath))
			}
			id := f.seed(store.StateWaiting, "X", opts...)
			before := f.historyAllLives(id)[0].RecordedAt

			f.rotate(id, "Y")

			got := f.historyAllLives(id)
			if len(got) != 1 {
				t.Fatalf("history = %+v; want one entry", got)
			}
			if e := got[0]; e.SessionID != "X" || e.LifeNumber != 1 || e.JSONLPath.String != tc.wantPath ||
				e.JSONLPath.Valid != (tc.wantPath != "") || e.RecordedAt <= before {
				t.Errorf("entry = %+v; want X in life 1 at %q, recorded after %q", e, tc.wantPath, before)
			}
			if ids0, ids1 := f.historyIDs(t, id, 0), f.historyIDs(t, id, 1); len(ids0) != 0 || !reflect.DeepEqual(ids1, []string{"X"}) {
				t.Errorf("life 0 read %v, life 1 read %v; want [], [X]", ids0, ids1)
			}
		})
	}
}

// TestSessionHistoryReadFiltersByLife: a life's read returns that life's
// entries of that id only, newest first; an empty life or absent id reads empty.
func TestSessionHistoryReadFiltersByLife(t *testing.T) {
	f := newV5Store(t)
	base := time.Now().Add(-10 * time.Hour)
	entry := func(sid string, life int64, hours int) apitest.SpawnOption {
		return apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: sid, JSONLPath: "/x/" + sid + ".jsonl",
			Life: life, RecordedAt: base.Add(time.Duration(hours) * time.Hour)})
	}
	id := f.seed(store.StateWaiting, "cur", apitest.WithLifeNumber(1),
		entry("A0", 0, 1), entry("B1", 1, 3), entry("C0", 0, 5), entry("D1", 1, 7))
	other := f.seed(store.StateWaiting, "cur", apitest.WithLifeNumber(1), entry("O0", 0, 2), entry("O1", 1, 6))
	for _, tc := range []struct {
		id   string
		life int64
		want []string
	}{
		{id, 0, []string{"C0", "A0"}},
		{id, 1, []string{"D1", "B1"}},
		{other, 1, []string{"O1"}},
		{id, 2, []string{}},
		{"no-such-id", 0, []string{}},
	} {
		if got := f.historyIDs(t, tc.id, tc.life); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("ListSessionHistory(%s, %d) = %v; want %v", tc.id, tc.life, got, tc.want)
		}
	}
}

// TestProvisionalTranscriptHeal (b.v2c AC1, AC3): a SessionStart whose
// reported transcript is not on disk records no path, clearing a recorded one,
// so the live row is listed as provisional with its CLAUDE_CONFIG_DIR;
// HealJsonlPath records a path only while it is NULL and the session matches.
// A live Codex row with a session and no transcript path is never listed: the
// heal composes a Claude transcript path.
func TestProvisionalTranscriptHeal(t *testing.T) {
	f := newV5Store(t)
	prov := f.seed(store.StateWaiting, "", apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": "/cfg"}))
	present := f.seed(store.StateWaiting, "")
	f.sessionStart(present, "session-present", "/x/here.jsonl", true)
	codex := f.seed(store.StateWaiting, "session-codex", apitest.WithAgent(store.AgentCodex))
	if c := f.rawColumns(codex); c.JSONLPath != nil || c.SessionID != "session-codex" {
		t.Fatalf("Codex row jsonl_path, session_id = %v, %v; want NULL and a session, or the exclusion check is vacuous", c.JSONLPath, c.SessionID)
	}
	f.sessionStart(prov, "session-1", "/x/present.jsonl", true)
	f.sessionStart(prov, "session-1", "/x/gone.jsonl", false)
	if got := f.rawColumns(prov).JSONLPath; got != nil {
		t.Errorf("jsonl_path after a reported-but-absent transcript = %v; want NULL", got)
	}
	listed, err := f.s.ListProvisionalTranscripts()
	if err != nil || len(listed) != 1 || listed[0].InstanceID != prov || listed[0].SessionID != "session-1" ||
		listed[0].ConfigDir != "/cfg" {
		t.Fatalf("ListProvisionalTranscripts = %+v, %v; want only %s/session-1 with /cfg", listed, err, prov)
	}

	heals := []struct {
		session string
		want    bool
	}{{"session-other", false}, {"session-1", true}, {"session-1", false}}
	for _, h := range heals {
		if wrote, err := f.s.HealJsonlPath(prov, h.session, "/x/appeared-"+h.session+".jsonl"); err != nil || wrote != h.want {
			t.Errorf("HealJsonlPath(%s) = %v, %v; want %v", h.session, wrote, err, h.want)
		}
	}
	if got := f.rawColumns(prov).JSONLPath; got != "/x/appeared-session-1.jsonl" {
		t.Errorf("jsonl_path = %v; want the first matching heal's path", got)
	}
	if listed, err := f.s.ListProvisionalTranscripts(); err != nil || len(listed) != 0 {
		t.Errorf("ListProvisionalTranscripts after the heal = %+v, %v; want none", listed, err)
	}
}

// b.x9o: filesystem access may finish after a hook or a new life repeats the
// same session/path text. Retain the old sample across the real intervening
// writes; a fresh sample is the positive control for every interleaving.
func TestProvisionalTranscriptSampleRejectsNewerEvidence(t *testing.T) {
	for _, change := range []string{"none", "same-session hook", "reused life"} {
		t.Run(change, func(t *testing.T) {
			f := newV5Store(t)
			parent := f.seed(store.StateWaiting, "")
			id := f.seed(store.StateWaiting, "same-session", apitest.WithLifeNumber(1),
				apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "older", JSONLPath: "/x/older.jsonl", Life: 1, RecordedAt: time.Unix(100, 0)}))
			if err := apitest.SeedParentChild(f.path, parent, id); err != nil {
				t.Fatal(err)
			}
			const path = "/x/same-session.jsonl"
			f.sessionStart(id, "same-session", path, false)
			sample := func() store.ProvisionalTranscript {
				t.Helper()
				rows, err := f.s.ProvisionalTranscriptsFor(id)
				if err != nil || len(rows) != 1 {
					t.Fatalf("provisional sample: %+v %v", rows, err)
				}
				return rows[0]
			}
			old := sample()
			if change == "same-session hook" {
				f.sessionStart(id, old.SessionID, path, false)
			}
			if change == "reused life" {
				for _, agent := range []string{store.AgentCodex, store.AgentClaude} {
					hookTo(store.StateEnded)(t, f, id)
					fresh := reuseFresh(parent, false)
					fresh.Agent, fresh.Identity, fresh.CWD = agent, old.Identity, old.CWD
					r := rrDoReset(t, f, id, fresh)
					if result, err := f.s.RecordLaunchIdentity(id, r.version, fresh.Identity.Token, old.Identity); err != nil || result != store.CondApplied {
						t.Fatalf("new life pane: %v %v", result, err)
					}
					f.sessionStart(id, old.SessionID, path, false)
				}
				if now := sample(); now.LifeNumber != old.LifeNumber+2 || now.SessionID != old.SessionID || now.Identity.Token != old.Identity.Token {
					t.Fatalf("same-text new-life precondition: old=%+v new=%+v", old, now)
				}
			}
			family := func() [3]any {
				raw, base, at := familyHistory(t, f, parent)
				return [3]any{raw, base, at}
			}
			before, history, parentFamily := f.rawColumns(id), f.historyAllLives(id), family()
			wrote, err := f.s.HealProvisionalTranscript(old, path)
			if err != nil || wrote != (change == "none") {
				t.Fatalf("old sample commit: wrote=%v err=%v change=%s", wrote, err, change)
			}
			if change != "none" {
				if after := f.rawColumns(id); !reflect.DeepEqual(after, before) {
					t.Fatalf("stale heal changed winning row: before=%+v after=%+v", before, after)
				}
				if wrote, err := f.s.HealProvisionalTranscript(sample(), path); err != nil || !wrote {
					t.Fatalf("fresh sample failed: %v %v", wrote, err)
				}
			}
			before.JSONLPath, before.RowVersion = path, before.RowVersion.(int64)+1
			if after := f.rawColumns(id); !reflect.DeepEqual(after, before) || !reflect.DeepEqual(f.historyAllLives(id), history) || family() != parentFamily {
				t.Fatalf("heal changed more than path/version or family/history: got=%+v want=%+v", after, before)
			}
		})
	}
}

// historySelectionStore interleaves real lifecycle writes after the fixed row
// selection and optionally leases the real pool before its final refresh.
// All history and lifecycle capabilities remain the underlying Store's.
type historySelectionStore struct {
	*store.Store
	afterSelection func()
	beforeRefresh  func()
	selections     int
}

func (s *historySelectionStore) ListSpawns(filters store.ListFilters) ([]store.Spawn, error) {
	s.selections++
	if s.selections > 1 && s.beforeRefresh != nil {
		take := s.beforeRefresh
		s.beforeRefresh = nil
		take()
	}
	rows, err := s.Store.ListSpawns(filters)
	if s.selections == 1 && err == nil && s.afterSelection != nil {
		s.afterSelection()
	}
	return rows, err
}

// b.x9o review06 R2: selection of ended session A precedes a real same-agent
// reuse and B-to-C rotation. History acquired later must never be labelled as
// A's snapshot when the final projection is unavailable.
func TestGetHistoryAcquisitionRejectsReusedLaunch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		reuse, held bool
	}{
		{"replacement refresh succeeds", true, false},
		{"replacement refresh connection held", true, true},
		{"validated old pair refresh connection held", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			held := tc.held
			f := newV5Store(t)
			opts := []apitest.SpawnOption{apitest.WithLifeNumber(3), apitest.WithJsonlPath("/x/A.jsonl")}
			if !tc.reuse {
				opts = append(opts, apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "older", Life: 3, RecordedAt: time.Unix(1, 0)}))
			}
			id := f.seed(store.StateEnded, "A", opts...)
			initial, err := f.s.GetSpawn(id)
			if err != nil {
				t.Fatal(err)
			}
			empty, absentErr := api.Get(f.s, api.RelayView{}, id+"-absent")
			if !errors.Is(absentErr, store.ErrSpawnNotFound) {
				t.Fatalf("empty failure control: %v", absentErr)
			}
			var replacement apitest.SpawnColumns
			s := &historySelectionStore{Store: f.s}
			s.afterSelection = func() {
				if !tc.reuse {
					replacement = f.rawColumns(id)
					return
				}
				fresh := reuseFresh("", false)
				r := rrDoReset(t, f, id, fresh)
				if r.archived != "A" {
					t.Fatalf("reuse archived %q", r.archived)
				}
				if applied, err := f.s.RecordLaunchIdentity(id, r.version, fresh.Identity.Token, fresh.Identity); err != nil || applied != store.CondApplied {
					t.Fatalf("replacement launch identity: %v %v", applied, err)
				}
				f.rotate(id, "B")
				f.rotate(id, "C")
				replacement = f.rawColumns(id)
				if replacement.LifeNumber != int64(3) || replacement.SessionID != "C" || replacement.LaunchToken == initial.Identity.Token {
					t.Fatalf("same-life replacement not established: %+v", replacement)
				}
				if got := f.historyIDs(t, id, 3); !slices.Contains(got, "A") || !slices.Contains(got, "B") || len(got) != 2 {
					t.Fatalf("replacement history=%v; want A and B", got)
				}
			}
			releases := make(chan func(), 1)
			if held {
				s.beforeRefresh = func() { releases <- store.HoldConnectionForTest(t, f.s) }
			}
			// A genuinely blocked refresh must still return before the held pool
			// is released; the guard releases on failure so the suite cannot hang.
			var got api.SpawnRow
			done := make(chan struct{})
			go func() { defer close(done); got, err = api.Get(s, api.RelayView{}, id) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Get waited for the unavailable final projection")
				select {
				case release := <-releases:
					release()
				default:
					t.Fatal("Get did not reach its final refresh")
				}
				<-done
			}
			select {
			case release := <-releases:
				release()
			default:
			}
			if s.selections < 2 {
				t.Fatalf("final refresh not attempted: %d selections", s.selections)
			}
			if held && tc.reuse {
				if !(errors.Is(err, store.ErrStoreBusy) || store.RecoverySampleChanged(err)) || !reflect.DeepEqual(got, empty) {
					t.Fatalf("unvalidated acquisition returned mixed row/history: %+v %v; want empty stale/busy failure", got, err)
				}
			} else {
				var sessions []string
				for _, row := range got.PriorSessions {
					sessions = append(sessions, row.SessionID)
				}
				slices.Sort(sessions)
				wantSession, wantPath, wantHistory := "C", "/x/C.jsonl", []string{"A", "B"}
				if !tc.reuse {
					wantSession, wantPath, wantHistory = "A", "/x/A.jsonl", []string{"older"}
				}
				if err != nil || got.SessionID != wantSession || got.JSONLPath != wantPath || !slices.Equal(sessions, wantHistory) {
					t.Fatalf("row/history: %+v %v; want %s with %v", got, err, wantSession, wantHistory)
				}
			}
			if after := f.rawColumns(id); !reflect.DeepEqual(after, replacement) {
				t.Fatalf("history read changed replacement: before=%+v after=%+v", replacement, after)
			}
		})
	}
}
