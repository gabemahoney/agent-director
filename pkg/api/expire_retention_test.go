package api_test

// expire_retention_test.go pins b.sgw: no retention day count wraps expire's
// window into "every finished row". Exported Expire never wraps a count above
// config.MaxExpireRetentionDays; Client.Expire reads expire_retention_days
// with 0 giving 31 days, and api.New refuses a negative or over-limit value
// before anything runs. It also pins b.f4v: exported Expire reads its
// retentionDays 0 as 31 days too, refuses a negative retentionDays or
// olderThan, and selects every finished row only for an explicit zero olderThan.

import (
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The rows seedRetentionRows seeds: ended a minute, two days and just past
// the 31-day default before the fixture clock, and 300 years before it,
// beyond the largest window.
const (
	retRecent      = "ret-recent"
	retTwoDays     = "ret-two-days"
	retPastDefault = "ret-past-default"
	retAncient     = "ret-ancient"
)

// seedRetentionRows seeds the four ret* finishedSpec rows, agents gone, and returns them.
func (e *killEnv) seedRetentionRows(t *testing.T) []killRow {
	t.Helper()
	seed := func(id string, age time.Duration, opts ...apitest.SpawnOption) killRow {
		spec := e.finishedSpec(age, agentGone, opts...)
		spec.ID = id
		return e.seedRow(t, spec)
	}
	return []killRow{
		seed(retRecent, time.Minute),
		seed(retTwoDays, 48*time.Hour),
		seed(retPastDefault, expireRetention+time.Hour),
		// 300 years is beyond a Duration, so the option replaces finishedSpec's ended_at.
		seed(retAncient, 0, apitest.WithEndedAt(e.clock.Now().AddDate(-300, 0, 0))),
	}
}

// assertRetentionRun checks res deleted exactly deleted after one lookup, every
// other ret* row still stored.
func (e *killEnv) assertRetentionRun(t *testing.T, res api.ExpireResult, mark int, rows []killRow, deleted ...string) {
	t.Helper()
	want := map[string]string{}
	for _, id := range deleted {
		want[id] = ""
	}
	assertExpired(t, res, mark, want)
	e.assertLookupsOn(t, apitest.TestSocket)
	for _, r := range rows {
		if _, gone := want[r.ID]; !gone {
			e.assertStillStored(t, r)
		}
	}
}

// TestExpireRetentionDaysNeverWrap: exported Expire with a day count at or above
// the largest a Duration holds deletes only the row older than that window.
func TestExpireRetentionDaysNeverWrap(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, days := range []int{config.MaxExpireRetentionDays, config.MaxExpireRetentionDays + 1, 365000, math.MaxInt} {
		t.Run(strconv.Itoa(days), func(t *testing.T) {
			e := newKillEnv(t)
			rows := e.seedRetentionRows(t)
			mark := trailMark(t)
			res, lg, err := e.expireWithDays(e.st, days, nil)
			if err != nil {
				t.Fatalf("Expire: %v", err)
			}
			e.assertRetentionRun(t, res, mark, rows, retAncient)
			if len(lg.lines) != 0 {
				t.Errorf("log = %q; want none", lg.lines)
			}
		})
	}
}

// assertExpireRefused fails unless err is ErrInvalidFlags alone, res is empty
// with non-nil lists (it encodes `[]`, not null) and nothing was logged, with
// no tmux call, no trail record for rows since mark and every row still stored.
func (e *killEnv) assertExpireRefused(t *testing.T, res api.ExpireResult, logs string, err error, mark int, rows []killRow) {
	t.Helper()
	assertOneSentinel(t, err, api.ErrInvalidFlags)
	assertExpireJSON(t, res, `{"count":0,"ids":[],"kept":0,"kept_ids":[]}`)
	if logs != "" {
		t.Errorf("log = %q; want none", logs)
	}
	e.assertLookupsOn(t)
	for _, r := range rows {
		assertNoTrailSince(t, mark, r.ID)
	}
	e.assertStillStored(t, rows...)
}

// TestExpireOnlyExplicitZeroSelectsEvery (b.f4v): exported Expire takes
// retentionDays 0 as 31 days; only an explicit zero olderThan deletes every
// finished row; a negative retentionDays or olderThan is refused, unread.
func TestExpireOnlyExplicitZeroSelectsEvery(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name    string
		days    int
		over    *time.Duration
		refused bool
		deleted []string
	}{
		{name: "retentionDays 0 is 31 days", days: 0, deleted: []string{retPastDefault, retAncient}},
		{name: "explicit zero olderThan", days: 0, over: olderThan(0),
			deleted: []string{retRecent, retTwoDays, retPastDefault, retAncient}},
		{name: "retentionDays -1", days: -1, refused: true},
		{name: "retentionDays MinInt", days: math.MinInt, refused: true},
		{name: "retentionDays -1 with zero olderThan", days: -1, over: olderThan(0), refused: true},
		{name: "olderThan -1ns", days: config.DefaultExpireRetentionDays, over: olderThan(-time.Nanosecond), refused: true},
		{name: "olderThan MinInt64 with retentionDays 0", days: 0, over: olderThan(math.MinInt64), refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			rows := e.seedRetentionRows(t)
			w := e.expireStore()
			if tc.refused {
				w.failList(nil) // a candidate read would return errInjectedStore, not ErrInvalidFlags
			}
			mark := trailMark(t)
			res, lg, err := e.expireWithDays(w, tc.days, tc.over)
			if tc.refused {
				e.assertExpireRefused(t, res, strings.Join(lg.lines, "\n"), err, mark, rows)
				return
			}
			if err != nil {
				t.Fatalf("Expire: %v", err)
			}
			e.assertRetentionRun(t, res, mark, rows, tc.deleted...)
			if len(lg.lines) != 0 {
				t.Errorf("log = %q; want none", lg.lines)
			}
		})
	}
	t.Run("Client.Expire olderThan -1h", func(t *testing.T) {
		e := newKillEnv(t)
		rows := e.seedRetentionRows(t)
		mark := trailMark(t)
		res, logs, err := e.expireClient(t, olderThan(-time.Hour))
		e.assertExpireRefused(t, res, logs, err, mark, rows)
	})
}

// TestClientExpireRetentionConfig: Client.Expire(nil) uses expire_retention_days
// (31 when 0); api.New refuses a negative or over-limit value, touching no row.
func TestClientExpireRetentionConfig(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name    string
		days    int64
		refused bool
		deleted []string
	}{
		{name: "zero", days: 0, deleted: []string{retPastDefault, retAncient}},
		{name: "one", days: 1, deleted: []string{retTwoDays, retPastDefault, retAncient}},
		{name: "largest", days: 106751, deleted: []string{retAncient}},
		{name: "negative", days: -1, refused: true},
		{name: "above_largest", days: 106752, refused: true},
		{name: "thousand_years", days: 365000, refused: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			rows := e.seedRetentionRows(t)
			mark := trailMark(t)
			if tc.refused {
				cfgPath := filepath.Join(t.TempDir(), "config.toml")
				apitest.WriteRetentionConfig(t, cfgPath, tc.days)
				_, _, err := e.clientFor(t, cfgPath)
				var ce *config.ConfigError
				if !errors.As(err, &ce) {
					t.Fatalf("api.New err = %v; want a *config.ConfigError", err)
				}
				apitest.AssertDescription(t, ce.Error(),
					apitest.DescConfigRefused(cfgPath, apitest.ConfigRefusal{Retention: true, Value: tc.days}))
				e.assertStillStored(t, rows...)
				e.assertLookupsOn(t)
				return
			}
			res, logs, err := e.expireClientDays(t, tc.days)
			if err != nil {
				t.Fatalf("Client.Expire: %v", err)
			}
			e.assertRetentionRun(t, res, mark, rows, tc.deleted...)
			if logs != "" {
				t.Errorf("Client log = %q; want none", logs)
			}
		})
	}
}
