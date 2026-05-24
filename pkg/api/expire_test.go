package api_test

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/internal/config"
)

// recordingExpireLogger captures Printf for assertion on the
// "per-row failure logged but doesn't abort" path. Reuses
// recordingLogger from find_missing_test via composition — but since
// that's package-private to find_missing_test.go, redefining here is
// the cleanest path.
type recordingExpireLogger struct{ lines int }

func (r *recordingExpireLogger) Printf(string, ...any) { r.lines++ }

func TestExpireOlderThan1HourRemovesOldTerminalRows(t *testing.T) {
	s, _ := apitest.SeedExpireFixture(t)
	d := time.Hour
	res, err := api.Expire(s, config.Default().Defaults.ExpireRetentionDays, &d, &recordingExpireLogger{})
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
	s, _ := apitest.SeedExpireFixture(t)
	// 24h window: nothing in the fixture is that old.
	d := 24 * time.Hour
	res, err := api.Expire(s, config.Default().Defaults.ExpireRetentionDays, &d, &recordingExpireLogger{})
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
	s, _ := apitest.SeedExpireFixture(t)
	d := time.Duration(0)
	res, err := api.Expire(s, config.Default().Defaults.ExpireRetentionDays, &d, &recordingExpireLogger{})
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	want := []string{"row-ended-fresh", "row-ended-old", "row-missing-old"}
	if got := res.IDs; !equalStrings(got, want) {
		t.Errorf("ids = %v; want %v", got, want)
	}
}

func TestExpireDoesNotTouchLiveRows(t *testing.T) {
	s, _ := apitest.SeedExpireFixture(t)
	d := time.Duration(0)
	if _, err := api.Expire(s, config.Default().Defaults.ExpireRetentionDays, &d, &recordingExpireLogger{}); err != nil {
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
	s, _ := apitest.SeedExpireFixture(t)
	d := time.Duration(0)
	if _, err := api.Expire(s, config.Default().Defaults.ExpireRetentionDays, &d, &recordingExpireLogger{}); err != nil {
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
	s, _ := apitest.SeedExpireFixture(t)
	res, err := api.Expire(s, config.Default().Defaults.ExpireRetentionDays, nil, &recordingExpireLogger{})
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
	_, err := api.Expire(s, config.Default().Defaults.ExpireRetentionDays, nil, lg)
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
