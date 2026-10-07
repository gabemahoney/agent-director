package api_test

// kill_optin_reported_test.go: SR-6.7's reported-in rule at each boundary on
// an ended and a missing row past the stopping window and the starting-session
// bound (SR-20.6): a pid recorded, and a session created in an earlier whole
// second than ended_at. A refusal makes only the lookup; a kill makes Epic
// 10's calls and no more. The runner is kill_optin_table_test.go's.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestKillIncludeFinishedReportedIn: each reported-in condition at its
// boundary, after the window and the bound; only a recorded pid with a session created before ended_at is killed.
func TestKillIncludeFinishedReportedIn(t *testing.T) {
	t.Parallel()
	longAgo := defWindow + defBound
	cases := []struct {
		name string
		row  startingRow
		want kftWant
	}{
		{"pid and session id, created 1 s before ended_at",
			startingRow{endedAgo: longAgo, age: longAgo + time.Second}, kftKilled},
		{"pid without a session id, created 1 s before ended_at",
			startingRow{endedAgo: longAgo, age: longAgo + time.Second, noSessionID: true}, kftKilled},
		{"session id but no pid", startingRow{endedAgo: longAgo, age: longAgo + time.Second, noPID: true}, kftNeverReportedIn},
		{"session id but no pid, ended window-1 s ago: the window first",
			startingRow{endedAgo: defWindow - time.Second, age: longAgo, noPID: true}, kftStopping},
		{"no pid, session bound-1 s old: the bound first",
			startingRow{endedAgo: defWindow, age: defBound - time.Second, noPID: true}, kftStarting},
		{"neither pid nor session id",
			startingRow{endedAgo: longAgo, age: longAgo + time.Second, noPID: true, noSessionID: true}, kftNeverReportedIn},
		{"neither pid nor session id, ended window-1 s ago",
			startingRow{endedAgo: defWindow - time.Second, age: longAgo, noPID: true, noSessionID: true}, kftNeverReportedIn},
		{"created in the same second as ended_at", startingRow{endedAgo: longAgo, age: longAgo}, kftNeverReportedIn},
		{"created 1 s after ended_at", startingRow{endedAgo: longAgo, age: longAgo - time.Second}, kftNeverReportedIn},
		{"NULL ended_at with a pid", startingRow{noEndedAt: true, age: longAgo}, kftNeverReportedIn},
	}
	for _, state := range kftStates {
		for _, tc := range cases {
			t.Run(state+", "+tc.name, func(t *testing.T) {
				row := tc.row
				row.state = state
				kftOurs(t, row, tc.want, defBound, defWindow, func(e *killEnv, r resumeRow) (api.KillResult, error) {
					cols := e.columns(t, r.ID)
					sid, _ := cols.ClaudeSessionID.(string)
					if row.noEndedAt != (cols.EndedAt == nil) || row.noPID != (cols.PID == nil) || row.noSessionID != (sid == "") {
						t.Fatalf("precondition: ended_at %v, pid %v, claude_session_id %v; want %+v",
							cols.EndedAt, cols.PID, cols.ClaudeSessionID, row)
					}
					return e.killOptIn(r.ID)
				})
			})
		}
	}
}
