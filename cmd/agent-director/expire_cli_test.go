package main_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The expire CLI over test/fake-tmux tables (SR-12.2, SR-12.5): one lookup on
// the rows' socket decides which finished rows go and which are kept.

// The finished rows a case seeds; ids sort in this order.
const (
	expireGoneID = "id-exp-a-gone" // no session on the socket
	expireKeptID = "id-exp-b-kept" // its own labelled session runs, or the lookup hangs
)

// expireCLICase is one run: the rows seeded, the socket's fake answer and
// the result and ad.expire.kept reasons expected.
type expireCLICase struct {
	name        string
	rows        []string
	ownSession  bool // write expireKeptID's own labelled session into the table
	hang        bool // the lookup hangs past a lowered query timeout
	wantIDs     string
	wantKept    string
	wantKeptIDs string
	wantReasons map[string]string // kept id -> ad.expire.kept reason
}

var expireCLICases = []expireCLICase{
	{name: "no session", rows: []string{expireGoneID},
		wantIDs: `["` + expireGoneID + `"]`, wantKept: `0`, wantKeptIDs: `[]`},
	{name: "own session kept", rows: []string{expireGoneID, expireKeptID}, ownSession: true,
		wantIDs: `["` + expireGoneID + `"]`, wantKept: `1`, wantKeptIDs: `["` + expireKeptID + `"]`,
		wantReasons: map[string]string{expireKeptID: "ours"}},
	{name: "hung lookup", rows: []string{expireKeptID}, hang: true,
		wantIDs: `[]`, wantKept: `1`, wantKeptIDs: `["` + expireKeptID + `"]`,
		wantReasons: map[string]string{expireKeptID: "cant_tell"}},
}

// seedExpireRows seeds each id as an ended row on the per-test socket the CLI
// child resolves (SR-20.3) and returns the HOME and the socket.
func seedExpireRows(t *testing.T, ids []string) (home, socket string) {
	t.Helper()
	home = t.TempDir()
	socket = spawnSocket(t, home)
	ended := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range ids {
		if _, err := apitest.SeedSpawn(stateDB(home), id, store.StateEnded, "", "", "", true,
			apitest.WithTmuxSocket(socket), apitest.WithEndedAt(ended)); err != nil {
			t.Fatalf("SeedSpawn %s: %v", id, err)
		}
	}
	return home, socket
}

// writeOwnSession writes id's own session, labelled with its launch token and
// the store's id, onto socket's fake table.
func writeOwnSession(t *testing.T, home, id, socket string) {
	t.Helper()
	token, _, storeID := launchIdentity(t, home, id)
	name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
	const sessionID = "$4"
	faketmuxfix.Tables{}.Write(t, socket, killTable(faketmuxfix.Session{
		ID: sessionID, Created: time.Now().Unix(), Name: name, Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
	}))
}

// TestExpireCLIKeptRows: expire --older-than 0d deletes a row with no session,
// keeps one whose own session runs or whose lookup hangs, and exits 0.
func TestExpireCLIKeptRows(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, tc := range expireCLICases {
		t.Run(tc.name, func(t *testing.T) {
			home, socket := seedExpireRows(t, tc.rows)
			if tc.ownSession {
				writeOwnSession(t, home, expireKeptID, socket)
			}
			if tc.hang {
				faketmuxfix.Tables{}.Inject(t, socket, faketmuxfix.Hang(tmux.CallLookup).Bound(fakeHangBound))
				apitest.WriteTmuxConfig(t, filepath.Join(directorDir(home), "config.toml"),
					apitest.TmuxInt(config.TmuxQueryTimeoutMs, 300))
			}

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "expire", "--older-than", "0d")
			if code != 0 {
				t.Fatalf("expire exit = %d; want 0\nstderr=%s", code, stderr)
			}
			var res map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stdout), &res); err != nil {
				t.Fatalf("parse stdout %q: %v", stdout, err)
			}
			for key, want := range map[string]string{"ids": tc.wantIDs, "kept": tc.wantKept, "kept_ids": tc.wantKeptIDs} {
				if got, ok := res[key]; !ok || string(got) != want {
					t.Errorf("%s = %s (present %v); want %s", key, got, ok, want)
				}
			}
			assertInvocationKinds(t, home, "list-sessions") // one lookup, never a kill

			for _, id := range tc.rows {
				_, kept := tc.wantReasons[id]
				_, err := apitest.ReadSpawnColumns(stateDB(home), id)
				if present := !errors.Is(err, store.ErrSpawnNotFound); present != kept {
					t.Errorf("row %s present = %v (err %v); want %v", id, present, err, kept)
				}
			}
			assertExpireKeptRecords(t, home, tc.wantReasons)
		})
	}
}

// assertExpireKeptRecords checks the trail holds exactly one ad.expire.kept
// record per kept row, carrying its reason, recorded name and source ad_expire.
func assertExpireKeptRecords(t *testing.T, home string, want map[string]string) {
	t.Helper()
	var recs []map[string]any
	for _, l := range trailLinesOrNil(t, trailDir(home)) {
		if l["event"] == "ad.expire.kept" {
			recs = append(recs, l)
		}
	}
	if len(recs) != len(want) {
		t.Fatalf("ad.expire.kept records = %v; want one per kept row %v", recs, want)
	}
	for _, r := range recs {
		id, _ := r["claude_instance_id"].(string)
		reason, ok := want[id]
		if !ok || r["reason"] != reason || r["source"] != "ad_expire" || r["tmux_session_name"] != rowColumns(t, home, id).TmuxSessionName {
			t.Errorf("ad.expire.kept record %v; want reason %q, source ad_expire and the row's recorded name", r, reason)
		}
	}
}
