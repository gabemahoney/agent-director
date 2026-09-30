package store_test

// AC-REUSE-25 (SR-8.7, SR-5.4): get and resume over the schema-v4 history
// fixture, migrated through the real sentinel flow, behave as before apart
// from the current-session rule (holds-current loses its current-id entry).

import (
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
	c, err := api.New(api.Options{
		StorePath:  f.Path,
		ConfigPath: filepath.Join(t.TempDir(), "absent", "config.toml"),
		TmuxClient: rec,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return migratedClient{f: f, c: c, rec: rec}
}

// sameInstant reports whether two store timestamps (SQLite text or RFC3339) are equal.
func sameInstant(a, b string) bool {
	parse := func(s string) (time.Time, bool) {
		for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts, true
			}
		}
		return time.Time{}, false
	}
	ta, okA := parse(a)
	tb, okB := parse(b)
	return okA && okB && ta.Equal(tb)
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
			pre := m.f.History[id]
			var want []store.V4HistoryEntry
			for _, e := range pre {
				if cur == "" || e.SessionID != cur {
					want = append(want, e)
				}
			}
			if dropped := len(pre) - len(want); dropped != tc.dropped {
				t.Fatalf("pre-migration history %+v holds %d entries for current %q; want %d", pre, dropped, cur, tc.dropped)
			}

			got, err := m.c.Get(id)
			if err != nil {
				t.Fatalf("Get(%s): %v", id, err)
			}
			if got.ClaudeSessionID != cur {
				t.Errorf("claude_session_id = %q; want pre-migration %q", got.ClaudeSessionID, cur)
			}
			if got.TranscriptStatus != tc.wantStatus {
				t.Errorf("transcript_status = %q; want %q", got.TranscriptStatus, tc.wantStatus)
			}
			gotIDs := make([]string, 0, len(got.PriorSessions))
			for _, p := range got.PriorSessions {
				gotIDs = append(gotIDs, p.ClaudeSessionID)
				if cur != "" && p.ClaudeSessionID == cur {
					t.Errorf("prior_sessions holds the current session id %q", cur)
				}
			}
			if strings.Join(gotIDs, ",") != strings.Join(tc.wantIDs, ",") {
				t.Errorf("prior_sessions ids = %v; want %v", gotIDs, tc.wantIDs)
			}
			if len(got.PriorSessions) != len(want) {
				t.Fatalf("prior_sessions = %+v; want pre-migration %+v", got.PriorSessions, want)
			}
			for i, p := range got.PriorSessions {
				w := want[i]
				if p.ClaudeSessionID != w.SessionID || p.JSONLPath != w.JSONLPath || !sameInstant(p.RecordedAt, w.RecordedAt) {
					t.Errorf("prior_sessions[%d] = %+v; want pre-migration %+v", i, p, w)
				}
			}
		})
	}
}

// plantTranscript writes an empty transcript for sessionID at the path resume
// recomputes for the row's cwd under the test HOME, removed at cleanup.
func plantTranscript(t *testing.T, cwd, sessionID string) {
	t.Helper()
	p, err := spawn.JsonlPath(cwd, sessionID)
	if err != nil {
		t.Fatalf("JsonlPath(%s, %s): %v", cwd, sessionID, err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(p) })
}

// TestMigratedV4HistoryResume: each row, on its own migrated copy, relaunches
// the same candidate or returns the same error as before the migration.
func TestMigratedV4HistoryResume(t *testing.T) {
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	t.Setenv("TMUX", "")
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
			id := tc.row(m.f)
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
				plantTranscript(t, m.f.CWD[id], sid)
			}

			_, err := m.c.Resume(api.ResumeParams{ClaudeInstanceID: id})
			launches := m.rec.SocketCallsOf(tmux.CallCreate)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Resume(%s) err = %v; want %v", id, err, tc.wantErr)
				}
				if tc.wantInMsg != "" && !strings.Contains(err.Error(), tc.wantInMsg) {
					t.Errorf("Resume(%s) err = %q; want it to name %q", id, err, tc.wantInMsg)
				}
				if len(launches) != 0 {
					t.Errorf("Resume(%s) relaunched %v; want no relaunch", id, launches)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resume(%s): %v", id, err)
			}
			if len(launches) != 1 {
				t.Fatalf("Resume(%s) made %d relaunches; want 1", id, len(launches))
			}
			if cmd := launches[0].Command; len(cmd) < 3 || cmd[1] != "--resume" || cmd[2] != tc.wantResume {
				t.Errorf("relaunch command = %v; want `claude --resume %s ...`", cmd, tc.wantResume)
			}
		})
	}
}
