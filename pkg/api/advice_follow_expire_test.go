package api_test

// advice_follow_expire_test.go (b.fji F4, F5; b.f4v): exported Expire's
// refusals of a negative retentionDays and a negative olderThan, each
// alternative the advice offers followed literally.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// adviceExpireFollow is one alternative of an expire refusal's advice: the
// retentionDays and olderThan re-issued and the ret* rows they delete.
type adviceExpireFollow struct {
	name    string
	days    int
	over    *time.Duration
	deleted []string
}

// adviceFollowExpire checks, per follow on fresh ret* rows, that Expire(days,
// over) is refused ErrInvalidFlags carrying advice and the re-issue deletes follow.deleted.
func adviceFollowExpire(t *testing.T, days int, over *time.Duration, advice string, follows []adviceExpireFollow) {
	t.Helper()
	for _, f := range follows {
		t.Run(f.name, func(t *testing.T) {
			e := newKillEnv(t)
			rows := e.seedRetentionRows(t)
			_, _, err := e.expireWithDays(e.st, days, over)
			adviceAssertAdvice(t, err, api.ErrInvalidFlags, advice)

			mark := trailMark(t)
			res, _, err := e.expireWithDays(e.st, f.days, f.over)
			if err != nil {
				t.Fatalf("Expire after %q: %v", f.name, err)
			}
			e.assertRetentionRun(t, res, mark, rows, f.deleted...)
		})
	}
}

// TestAdviceFollow_F4_ExpireRetentionDaysNegative: F4 "pass 0 for the default
// of 31 days or a positive number of days (only an explicit zero olderThan
// selects every finished row)"; Expire re-issued each way runs.
func TestAdviceFollow_F4_ExpireRetentionDaysNegative(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceFollowExpire(t, -1, nil,
		"pass 0 for the default of 31 days or a positive number of days (only an explicit zero olderThan selects every finished row)",
		[]adviceExpireFollow{
			{"pass 0", 0, nil, []string{retPastDefault, retAncient}},
			{"a positive number of days", 1, nil, []string{retTwoDays, retPastDefault, retAncient}},
			{"pass 0 and an explicit zero olderThan", 0, olderThan(0), []string{retRecent, retTwoDays, retPastDefault, retAncient}},
		})
}

// TestAdviceFollow_F5_ExpireOlderThanNegative: F5 "pass a positive duration,
// nil for the retention window (retentionDays, or the configured
// expire_retention_days through Client.Expire), or an explicit zero to select
// every finished row"; Expire re-issued each way runs.
func TestAdviceFollow_F5_ExpireOlderThanNegative(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	days := config.DefaultExpireRetentionDays
	adviceFollowExpire(t, days, olderThan(-time.Hour),
		"pass a positive duration, nil for the retention window (retentionDays, or the configured expire_retention_days through Client.Expire), or an explicit zero to select every finished row",
		[]adviceExpireFollow{
			{"a positive duration", days, olderThan(24 * time.Hour), []string{retTwoDays, retPastDefault, retAncient}},
			{"nil", days, nil, []string{retPastDefault, retAncient}},
			{"an explicit zero", days, olderThan(0), []string{retRecent, retTwoDays, retPastDefault, retAncient}},
		})
}
