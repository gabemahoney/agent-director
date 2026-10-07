package apitest

import (
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// seedV5Row seeds id in state with opts (creating the store if needed) and
// returns the row's raw columns through ReadSpawnColumns.
func seedV5Row(t *testing.T, dbPath, id, state string, opts ...SpawnOption) SpawnColumns {
	t.Helper()
	if _, err := SeedSpawn(dbPath, id, state, "/tmp", "off", "", true, opts...); err != nil {
		t.Fatalf("SeedSpawn(%q, %s): %v", id, state, err)
	}
	cols, err := ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%q): %v", id, err)
	}
	return cols
}

// getSpawnV5 reads id through the store's own GetSpawn.
func getSpawnV5(t *testing.T, dbPath, id string) store.Spawn {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck
	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%q): %v", id, err)
	}
	return sp
}

// mustReadStoreID returns ReadStoreID(dbPath) or fails the test.
func mustReadStoreID(t *testing.T, dbPath string) string {
	t.Helper()
	id, err := ReadStoreID(dbPath)
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}
	return id
}

// TestSeedRowSession_MatchesSeedSpawnPane seeds rows and their Recorder sessions, then asserts the
// lookup and pane listing on the test socket name each row's pane under a label valid for its id, token and
// the store's own id (a reseeded id included, so the label is read from the store, not a constant).
func TestSeedRowSession_MatchesSeedSpawnPane(t *testing.T) {
	t.Parallel()
	type row struct {
		id, state string
		opts      []SpawnOption
		paneID    string
		panePID   int
	}
	live := func(state string) []row {
		return []row{{id: "v5-match-" + state, state: state, paneID: TestPaneID, panePID: TestPanePID}}
	}
	// A second row on the same socket needs its own pane (SeedRowSession refuses a shared one).
	other := store.LaunchIdentity{Token: "0123456789abcdef", Socket: TestSocket, PaneID: "%7", PanePID: 4343}

	cases := []struct {
		name          string
		rows          []row
		reseedStoreID bool // replace the store's id before seeding the sessions
	}{
		{name: "pending", rows: live("pending")},
		{name: "waiting", rows: live("waiting")},
		{name: "working", rows: live("working")},
		{name: "ask_user", rows: live("ask_user")},
		{name: "check_permission", rows: live("check_permission")},
		{name: "two rows on one socket", rows: append(live("waiting"), row{id: "v5-match-other", state: "working",
			opts: []SpawnOption{WithLaunchIdentity(other)}, paneID: other.PaneID, panePID: other.PanePID})},
		{name: "reseeded store id", rows: live("waiting"), reseedStoreID: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(t.TempDir(), "state.db")
			rec := tmuxfix.NewRecorder()
			tokens := make([]string, len(tc.rows))
			seeded := make([]tmuxfix.SeedSession, len(tc.rows))
			for i, r := range tc.rows {
				tokens[i], _ = seedV5Row(t, dbPath, r.id, r.state, r.opts...).LaunchToken.(string)
			}
			if tc.reseedStoreID {
				if err := SeedStoreID(dbPath, OtherStoreID(mustReadStoreID(t, dbPath))); err != nil {
					t.Fatalf("SeedStoreID: %v", err)
				}
			}
			storeID := mustReadStoreID(t, dbPath)
			for i, r := range tc.rows {
				seeded[i] = rec.SeedRowSession(t, dbPath, r.id)
			}

			ans, err := rec.Lookup(TestSocket)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", TestSocket, err)
			}
			panes, err := rec.ListPanes(TestSocket)
			if err != nil {
				t.Fatalf("ListPanes(%q): %v", TestSocket, err)
			}
			if len(ans.Sessions) != len(tc.rows) || len(panes) != len(tc.rows) {
				t.Fatalf("lookup lists %d sessions, listing %d panes; want %d each", len(ans.Sessions), len(panes), len(tc.rows))
			}

			for i, r := range tc.rows {
				sid := seeded[i].ID
				wantLabel := tmux.Label{Kind: tmux.LabelValid, Token: tokens[i], InstanceID: r.id, StoreID: storeID}
				var found bool
				for _, s := range ans.Sessions {
					if s.ID == sid {
						found = true
						if s.Label != wantLabel {
							t.Errorf("row %s: session %s label = %+v; want %+v", r.id, sid, s.Label, wantLabel)
						}
					}
				}
				if !found {
					t.Errorf("row %s: lookup has no session %s", r.id, sid)
				}
				var got []tmux.Pane
				for _, p := range panes {
					if p.SessionID == sid {
						got = append(got, p)
					}
				}
				if len(got) != 1 || got[0].ID != r.paneID || got[0].PID != r.panePID {
					t.Errorf("row %s: session %s panes = %+v; want one pane %s pid %d", r.id, sid, got, r.paneID, r.panePID)
				}
			}
		})
	}
}
