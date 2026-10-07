package store_test

// Session history (b.v2c, b.5jm, SR-5.9, SR-8.7): the life-keyed re-archive,
// the life-filtered read, and the provisional transcript and its heal. The
// rotation archive inside SessionStart is pinned in
// hook_gate_session_start_test.go. Rows are seeded through apitest.

import (
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
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
		ids = append(ids, e.ClaudeSessionID)
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
			if e := got[0]; e.ClaudeSessionID != "X" || e.LifeNumber != 1 || e.JSONLPath.String != tc.wantPath ||
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
func TestProvisionalTranscriptHeal(t *testing.T) {
	f := newV5Store(t)
	prov := f.seed(store.StateWaiting, "", apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": "/cfg"}))
	present := f.seed(store.StateWaiting, "")
	f.sessionStart(present, "session-present", "/x/here.jsonl", true)
	f.sessionStart(prov, "session-1", "/x/present.jsonl", true)
	f.sessionStart(prov, "session-1", "/x/gone.jsonl", false)
	if got := f.rawColumns(prov).JSONLPath; got != nil {
		t.Errorf("jsonl_path after a reported-but-absent transcript = %v; want NULL", got)
	}
	listed, err := f.s.ListProvisionalTranscripts()
	if err != nil || len(listed) != 1 || listed[0].ClaudeInstanceID != prov || listed[0].ClaudeSessionID != "session-1" ||
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
