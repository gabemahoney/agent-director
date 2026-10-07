package store_test

// AC-REUSE-25 and AC-RES-20 (SR-8.7, SR-5.4, SR-22.6): get and resume over the
// schema-v4 history fixture, migrated through the real sentinel flow, behave as
// before apart from the current-session rule (holds-current loses its
// current-id entry), and a migrated row carries the no_pre_trust default, so
// its resume pre-trusts the row's directory.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// migratedClient is an api.Client over a freshly migrated v4 history fixture.
type migratedClient struct {
	f   store.MigratedV4History
	c   *api.Client
	rec *tmuxfix.Recorder
}

// newMigratedClient migrates a fresh fixture and opens a client on it with an
// explicit store path, an absent config file (defaults) and a tmux Recorder.
func newMigratedClient(t *testing.T) migratedClient {
	t.Helper()
	f := store.MigrateV4HistoryFixture(t)
	rec := tmuxfix.NewRecorder()
	c, err := api.New(api.Options{StorePath: f.Path, ConfigPath: filepath.Join(t.TempDir(), "absent", "config.toml"), TmuxClient: rec})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return migratedClient{f: f, c: c, rec: rec}
}

// fixtureRow picks one scenario row's id from the fixture.
type fixtureRow func(store.MigratedV4History) string

var (
	rotatedRow      fixtureRow = func(f store.MigratedV4History) string { return f.Rotated }
	neverWrittenRow fixtureRow = func(f store.MigratedV4History) string { return f.NeverWritten }
	holdsCurrentRow fixtureRow = func(f store.MigratedV4History) string { return f.HoldsCurrent }
	interleavedARow fixtureRow = func(f store.MigratedV4History) string { return f.InterleavedA }
	interleavedBRow fixtureRow = func(f store.MigratedV4History) string { return f.InterleavedB }
	pendingRow      fixtureRow = func(f store.MigratedV4History) string { return f.Pending }
)

// TestMigratedV4HistoryGet: prior_sessions is the row's pre-migration history
// minus only its current-id entry; transcript_status is as stated.
func TestMigratedV4HistoryGet(t *testing.T) {
	m := newMigratedClient(t)
	cases := []struct {
		name       string
		row        fixtureRow
		wantIDs    []string // prior_sessions ids, newest first
		dropped    int      // current-id entries in the pre-migration history
		wantStatus string
	}{
		{"rotated", rotatedRow, []string{"sess-rot-prior"}, 0, "present"},
		{"never_written", neverWrittenRow, []string{"sess-nw-prior"}, 0, "rotated"},
		{"holds_current", holdsCurrentRow, []string{}, 1, "present"},
		{"interleaved_a", interleavedARow, []string{"sess-a-2", "sess-a-1"}, 0, "present"},
		{"interleaved_b", interleavedBRow, []string{"sess-b-2", "sess-b-1"}, 0, "present"},
		{"pending", pendingRow, []string{}, 0, "no_session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.row(m.f)
			cur := m.f.CurrentSessionID[id]
			var want []store.V4HistoryEntry
			for _, e := range m.f.History[id] {
				if cur == "" || e.SessionID != cur {
					want = append(want, e)
				}
			}
			if dropped := len(m.f.History[id]) - len(want); dropped != tc.dropped {
				t.Fatalf("pre-migration history holds %d entries for current %q; want %d", dropped, cur, tc.dropped)
			}
			got, err := m.c.Get(id)
			if err != nil {
				t.Fatalf("Get(%s): %v", id, err)
			}
			if got.ClaudeSessionID != cur || got.TranscriptStatus != tc.wantStatus {
				t.Errorf("claude_session_id, transcript_status = %q, %q; want %q, %q", got.ClaudeSessionID, got.TranscriptStatus, cur, tc.wantStatus)
			}
			gotIDs := []string{}
			for _, p := range got.PriorSessions {
				gotIDs = append(gotIDs, p.ClaudeSessionID)
			}
			if strings.Join(gotIDs, ",") != strings.Join(tc.wantIDs, ",") || len(got.PriorSessions) != len(want) {
				t.Fatalf("prior_sessions = %+v; want ids %v from pre-migration %+v", got.PriorSessions, tc.wantIDs, want)
			}
			for i, p := range got.PriorSessions {
				w := want[i]
				if recorded, err := time.Parse("2006-01-02 15:04:05", w.RecordedAt); p.ClaudeSessionID != w.SessionID ||
					p.JSONLPath != w.JSONLPath || err != nil || !recordedAt(p.RecordedAt).Equal(recorded) {
					t.Errorf("prior_sessions[%d] = %+v; want pre-migration %+v", i, p, w)
				}
			}
		})
	}
}

// recordedAt parses a prior session's recorded_at (store text or RFC 3339).
func recordedAt(s string) time.Time {
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// plantTranscript writes an empty transcript for sessionID at the path resume
// recomputes for cwd under the test HOME, removed at cleanup.
func plantTranscript(t *testing.T, cwd, sessionID string) {
	t.Helper()
	p, err := spawn.JsonlPath(cwd, sessionID)
	if err != nil {
		t.Fatalf("JsonlPath(%s, %s): %v", cwd, sessionID, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil || os.WriteFile(p, []byte("{}\n"), 0o644) != nil {
		t.Fatalf("plant %s: %v", p, err)
	}
	t.Cleanup(func() { _ = os.Remove(p) })
}

// seedHomeClaudeJSON writes body to $HOME/.claude.json, where pre-trust looks
// for a row whose extra env sets no CLAUDE_CONFIG_DIR, and puts back what was
// there at cleanup. HOME is TestMain's throwaway directory, never the real one.
func seedHomeClaudeJSON(t *testing.T, body string) string {
	t.Helper()
	home := os.Getenv("HOME")
	if !strings.HasPrefix(filepath.Base(home), "ad-store-home-") {
		t.Fatalf("HOME = %q; want TestMain's throwaway ad-store-home-* directory", home)
	}
	p := filepath.Join(home, ".claude.json")
	switch prev, err := os.ReadFile(p); {
	case err == nil:
		t.Cleanup(func() { _ = os.WriteFile(p, prev, 0o600) })
	case errors.Is(err, os.ErrNotExist):
		t.Cleanup(func() { _ = os.Remove(p) })
	default:
		t.Fatalf("read %s: %v", p, err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// trustAccepted reports projects[cwd].hasTrustDialogAccepted in the .claude.json at p.
func trustAccepted(t *testing.T, p, cwd string) bool {
	t.Helper()
	raw, err := os.ReadFile(p)
	var doc struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		t.Fatalf("read %s: %v\n%s", p, err, raw)
	}
	return doc.Projects[cwd].HasTrustDialogAccepted
}

// TestMigratedV4HistoryResume: each row, on its own migrated copy, relaunches
// the same candidate or returns the same error as before the migration; the
// relaunched row, carrying the no_pre_trust default, reports pre_trust ok,
// gets its directory's trust entry and moves to pending (AC-RES-20).
func TestMigratedV4HistoryResume(t *testing.T) {
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	t.Setenv("TMUX", "")
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	cases := []struct {
		name       string
		row        fixtureRow
		plantAll   bool     // plant the current and every history session's transcript
		plant      []string // session ids to plant otherwise
		wantErr    error    // nil: relaunched
		wantResume string   // --resume session id when relaunched
		wantInMsg  string   // substring of the error message
	}{
		{name: "rotated", row: rotatedRow, plantAll: true, wantErr: api.ErrSpawnNotResumable},
		{name: "never_written_missing", row: neverWrittenRow, wantErr: api.ErrJsonlMissing, wantInMsg: "history /t/nw-prior.jsonl"},
		{name: "never_written_prior_planted", row: neverWrittenRow, plant: []string{"sess-nw-prior"}, wantResume: "sess-nw-prior"},
		{name: "holds_current", row: holdsCurrentRow, plantAll: true, wantErr: api.ErrSpawnNotResumable},
		{name: "interleaved_a", row: interleavedARow, plantAll: true, wantErr: api.ErrSpawnNotResumable},
		{name: "interleaved_b", row: interleavedBRow, plantAll: true, wantErr: api.ErrSpawnNotResumable},
		{name: "pending", row: pendingRow, plantAll: true, wantErr: api.ErrSpawnNotResumable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMUX_TMPDIR", t.TempDir())
			m := newMigratedClient(t)
			id, cwd := tc.row(m.f), m.f.CWD[tc.row(m.f)]
			plant := tc.plant
			if tc.plantAll {
				if cur := m.f.CurrentSessionID[id]; cur != "" {
					plant = append(plant, cur)
				}
				for _, e := range m.f.History[id] {
					plant = append(plant, e.SessionID)
				}
			}
			for _, sid := range plant {
				plantTranscript(t, cwd, sid)
			}
			claudeJSON := seedHomeClaudeJSON(t, `{"numStartups": 3, "projects": {"/elsewhere": {"hasTrustDialogAccepted": false}}}`)

			res, err := m.c.Resume(api.ResumeParams{ClaudeInstanceID: id})
			launches := m.rec.SocketCallsOf(tmux.CallCreate)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || !strings.Contains(err.Error(), tc.wantInMsg) || len(launches) != 0 {
					t.Fatalf("Resume(%s) = %v with %d relaunches; want %v naming %q, no relaunch", id, err, len(launches), tc.wantErr, tc.wantInMsg)
				}
				return
			}
			if err != nil || len(launches) != 1 {
				t.Fatalf("Resume(%s) = %v with %d relaunches; want 1", id, err, len(launches))
			}
			if cmd := launches[0].Command; len(cmd) < 3 || cmd[1] != "--resume" || cmd[2] != tc.wantResume {
				t.Errorf("relaunch command = %v; want `claude --resume %s ...`", cmd, tc.wantResume)
			}
			if res.PreTrust != "ok" || !trustAccepted(t, claudeJSON, cwd) {
				t.Errorf("pre_trust = %q, trust entry for %s written %v; want ok, true", res.PreTrust, cwd, trustAccepted(t, claudeJSON, cwd))
			}
			s, err := store.Open(m.f.Path)
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			defer s.Close()
			if sp, err := s.GetSpawn(id); err != nil || sp.State != store.StatePending || sp.NoPreTrust {
				t.Errorf("after resume: state %q, no_pre_trust %v, %v; want pending, the default (false)", sp.State, sp.NoPreTrust, err)
			}
		})
	}
}
