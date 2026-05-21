package api_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/api"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// seedExpireFixture builds a 5-row DB with mixed states + ages.
// Returns the open store, the path of the DB so tests can re-open
// with a raw sql.DB to age-stamp rows (SQLite's CURRENT_TIMESTAMP is
// "now" — to test the older-than predicate we have to backdate the
// ended_at column directly).
func seedExpireFixture(t *testing.T) (*store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	insert := func(id, state, cwd string) {
		t.Helper()
		row := store.Spawn{
			ClaudeInstanceID: id,
			CWD:              cwd,
			TmuxSessionName:  "cd-" + id,
			RelayMode:        "off",
		}
		if err := s.InsertPending(row); err != nil {
			t.Fatalf("InsertPending %s: %v", id, err)
		}
		if state != store.StatePending {
			if err := s.ApplyHookTransition(id, state, false); err != nil {
				t.Fatalf("transition %s: %v", id, err)
			}
		}
	}

	insert("row-ended-old", store.StateEnded, "/tmp")
	insert("row-ended-fresh", store.StateEnded, "/tmp")
	insert("row-missing-old", store.StateMissing, "/tmp")
	insert("row-waiting-live", store.StateWaiting, "/tmp")
	insert("row-ended-null-at", store.StateEnded, "/tmp")

	// Backdate two terminal rows so they predate "now - 1h"; the
	// 3rd terminal row's ended_at stays at insert time (~now), the
	// live row has no ended_at, and the 5th has its ended_at NULLed
	// out to exercise the "ended_at IS NOT NULL" predicate.
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	backdate := time.Now().UTC().Add(-2 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := raw.Exec(
		`UPDATE spawns SET ended_at = ? WHERE claude_instance_id = ?`,
		backdate, "row-ended-old"); err != nil {
		t.Fatalf("backdate old: %v", err)
	}
	if _, err := raw.Exec(
		`UPDATE spawns SET ended_at = ? WHERE claude_instance_id = ?`,
		backdate, "row-missing-old"); err != nil {
		t.Fatalf("backdate missing: %v", err)
	}
	if _, err := raw.Exec(
		`UPDATE spawns SET ended_at = NULL WHERE claude_instance_id = ?`,
		"row-ended-null-at"); err != nil {
		t.Fatalf("null-at: %v", err)
	}

	return s, dbPath
}

// recordingExpireLogger captures Printf for assertion on the
// "per-row failure logged but doesn't abort" path. Reuses
// recordingLogger from find_missing_test via composition — but since
// that's package-private to find_missing_test.go, redefining here is
// the cleanest path.
type recordingExpireLogger struct{ lines int }

func (r *recordingExpireLogger) Printf(string, ...any) { r.lines++ }

func TestExpireOlderThan1HourRemovesOldTerminalRows(t *testing.T) {
	s, _ := seedExpireFixture(t)
	d := time.Hour
	res, err := api.Expire(s, config.Default(), &d, &recordingExpireLogger{})
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	want := []string{"row-ended-old", "row-missing-old"}
	if got := res.IDs; !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v (older-than 1h reaps only the 2-hour-old rows)", got, want)
	}
	if res.Count != 2 {
		t.Errorf("count = %d; want 2", res.Count)
	}
}

func TestExpireYoungTerminalRowsSurvive(t *testing.T) {
	s, _ := seedExpireFixture(t)
	// 24h window: nothing in the fixture is that old.
	d := 24 * time.Hour
	res, err := api.Expire(s, config.Default(), &d, &recordingExpireLogger{})
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("count = %d; want 0 (24h window — fixture rows are younger)", res.Count)
	}
}

func TestExpireZeroDurationReapsAllTerminalRows(t *testing.T) {
	// --older-than 0d is the "delete every terminal row" form.
	// Live row (row-waiting-live) and the NULL-ended_at row both
	// survive.
	s, _ := seedExpireFixture(t)
	d := time.Duration(0)
	res, err := api.Expire(s, config.Default(), &d, &recordingExpireLogger{})
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	want := []string{"row-ended-fresh", "row-ended-old", "row-missing-old"}
	if got := res.IDs; !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestExpireDoesNotTouchLiveRows(t *testing.T) {
	s, _ := seedExpireFixture(t)
	d := time.Duration(0)
	if _, err := api.Expire(s, config.Default(), &d, &recordingExpireLogger{}); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if _, err := s.GetSpawn("row-waiting-live"); err != nil {
		t.Errorf("live row was deleted: %v", err)
	}
}

func TestExpireDoesNotTouchNullEndedAt(t *testing.T) {
	// A terminal row without ended_at populated (e.g. legacy data
	// or a future-bug edge case) is preserved. The predicate
	// "ended_at IS NOT NULL AND ended_at < deadline" makes NULL a
	// no-touch, which is the conservative default.
	s, _ := seedExpireFixture(t)
	d := time.Duration(0)
	if _, err := api.Expire(s, config.Default(), &d, &recordingExpireLogger{}); err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if _, err := s.GetSpawn("row-ended-null-at"); err != nil {
		t.Errorf("NULL-ended_at row was deleted: %v", err)
	}
}

func TestExpireDefaultRetentionFromConfig(t *testing.T) {
	// Absent olderThan flag, the verb uses
	// cfg.Defaults.ExpireRetentionDays. The fixture rows are at most
	// 2h old; with the default 31-day window nothing should be reaped.
	s, _ := seedExpireFixture(t)
	res, err := api.Expire(s, config.Default(), nil, &recordingExpireLogger{})
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("count = %d; want 0 (31d default window)", res.Count)
	}
}

func TestExpirePropagatesStoreErrors(t *testing.T) {
	// A stub store that returns an error from DeleteTerminalOlderThan
	// must propagate. The verb-side logger is informed.
	s := &stubExpireStore{err: errors.New("boom")}
	lg := &recordingExpireLogger{}
	_, err := api.Expire(s, config.Default(), nil, lg)
	if err == nil {
		t.Fatalf("expected error; got nil")
	}
	if lg.lines == 0 {
		t.Errorf("expected the failure to be logged")
	}
}

type stubExpireStore struct {
	count int
	ids   []string
	err   error
}

func (s *stubExpireStore) DeleteTerminalOlderThan(_ time.Duration) (int, []string, error) {
	return s.count, s.ids, s.err
}
