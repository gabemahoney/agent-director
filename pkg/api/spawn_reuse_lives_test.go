package api_test

// spawn_reuse_lives_test.go: history by life across several lives (SR-5.9,
// SR-8.7, SR-10.4; AC-REUSE-23, 24, 25 last clause): rotations and two
// reuses, a re-archived session moving lives, a failed reuse keeping the
// pre-reuse life, and a row whose history holds its current id. Steps are
// the rlf helpers (spawn_reuse_history_test.go).

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rlvAssertLife fails unless id's row is in life.
func rlvAssertLife(t *testing.T, e *killEnv, id string, life int64) {
	t.Helper()
	if got := e.columns(t, id).LifeNumber; got != life {
		t.Fatalf("life_number = %#v; want %d", got, life)
	}
}

// TestSpawnReuseLivesRotationsAndTwoReuses (AC-REUSE-24): a rotation is a
// candidate and listed within its life; after a second reuse only the latest
// life's entries are, and a session re-archived there moves to that life.
func TestSpawnReuseLivesRotationsAndTwoReuses(t *testing.T) {
	e := newReuseEnv(t)
	r0 := e.seedReusable(t, agentGone, reuseRowSpec{})
	rlfEarlierOnDisk(t, r0)

	// First reuse: s1 messaged, rotated to s2, listed and resumed in that life.
	r, _ := e.reuseLaunch(t, r0, agentAlive, reuseRequest{})
	s1, s2 := rlfNewSession(), rlfNewSession()
	p1 := rlfReportIn(t, e, r.ID, s1, true)
	rlfReportIn(t, e, r.ID, s2, false)
	rlfAssertListed(t, rlfGet(t, e, r.ID), "rotated", getWantPrior{id: s1, path: p1})
	rlfEndLife(t, e, r.ID)
	if got := rlfResumed(t, e, r.ID); got != s1 {
		t.Fatalf("--resume %s; want the rotation's %s", got, s1)
	}
	rlfReportIn(t, e, r.ID, s1, true)
	rlfEndLife(t, e, r.ID)
	rlvAssertLife(t, e, r.ID, reuseLife+1)

	// Second reuse: a new life lists nothing until its own sessions archive.
	r, _ = e.reuseLaunch(t, r, agentAlive, reuseRequest{})
	rlvAssertLife(t, e, r.ID, reuseLife+2)
	s3, s4 := rlfNewSession(), rlfNewSession()
	rlfReportIn(t, e, r.ID, s3, false)
	rlfAssertListed(t, rlfGet(t, e, r.ID), "never_written")

	// s1 reported again, then rotated out: its entry moves to this life.
	rlfReportIn(t, e, r.ID, s1, true)
	rlfAssertListed(t, rlfGet(t, e, r.ID), "present", getWantPrior{id: s3})
	rlfReportIn(t, e, r.ID, s4, false)
	rlfAssertListed(t, rlfGet(t, e, r.ID), "rotated", getWantPrior{id: s1, path: p1}, getWantPrior{id: s3})
	rlfAssertStored(t, e, r.ID, s1, reuseLife+2)
	rlfAssertStored(t, e, r.ID, s2, reuseLife+1)

	rlfEndLife(t, e, r.ID)
	if got := rlfResumed(t, e, r.ID); got != s1 {
		t.Errorf("--resume %s; want %s, re-archived in this life", got, s1)
	}
	rlfAssertEarlierUnused(t, e, r0, "")
}

// rlvFailedReuse gives a row reused into a second life that was messaged
// (s1), rotated once to s2 (messaged) and ended, then a further reuse whose
// create fails and whose restore applies; it returns the row, get before the
// failed reuse, and s1, s2 and their transcripts.
func rlvFailedReuse(t *testing.T, e *killEnv) (r0, r reuseRow, before api.SpawnRow, s1, p1, s2, p2 string) {
	t.Helper()
	r0 = e.seedReusable(t, agentGone, reuseRowSpec{})
	rlfEarlierOnDisk(t, r0)
	r, _ = e.reuseLaunch(t, r0, agentAlive, reuseRequest{})
	s1, s2 = rlfNewSession(), rlfNewSession()
	p1 = rlfReportIn(t, e, r.ID, s1, true)
	p2 = rlfReportIn(t, e, r.ID, s2, true)
	rlfEndLife(t, e, r.ID)
	before = rlfGet(t, e, r.ID)

	e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
	_, logs, err := e.reuse(t, reuseParams(t, r, reuseRequest{}))
	assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
	if cols := e.columns(t, r.ID); cols.State != store.StateEnded {
		t.Fatalf("state after the failed reuse = %v (log %q); want ended, restored", cols.State, logs)
	}
	rlvAssertLife(t, e, r.ID, reuseLife+1)
	return r0, r, before, s1, p1, s2, p2
}

// TestSpawnReuseLivesFailedReuseKeepsLife (AC-REUSE-23): after a failed,
// restored reuse, get and resume see the pre-reuse life exactly as before,
// and nothing of the earlier life or the failed attempt's.
func TestSpawnReuseLivesFailedReuseKeepsLife(t *testing.T) {
	cases := []struct {
		name          string
		removeCurrent bool // the current session's transcript is gone, so resume falls back to history
		want          func(s1, s2 string) string
	}{
		{"current session's transcript present", false, func(_, s2 string) string { return s2 }},
		{"current session's transcript gone", true, func(s1, _ string) string { return s1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReuseEnv(t)
			r0, r, before, s1, p1, s2, p2 := rlvFailedReuse(t, e)
			after := rlfGet(t, e, r.ID)
			rlfAssertListed(t, after, "present", getWantPrior{id: s1, path: p1})
			if after.ClaudeSessionID != s2 || after.TranscriptStatus != before.TranscriptStatus ||
				!reflect.DeepEqual(after.PriorSessions, before.PriorSessions) {
				t.Errorf("get after the failed reuse = %+v; want as before %+v", after, before)
			}
			all, err := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID)
			if err != nil {
				t.Fatalf("ReadSessionHistoryAllLives: %v", err)
			}
			for _, h := range all {
				if h.LifeNumber > reuseLife+1 {
					t.Errorf("history entry %+v in life %d; the failed reuse's life must hold none", h, h.LifeNumber)
				}
			}
			if tc.removeCurrent {
				if err := os.Remove(p2); err != nil {
					t.Fatalf("remove %s: %v", p2, err)
				}
			}
			if got, want := rlfResumed(t, e, r.ID), tc.want(s1, s2); got != want {
				t.Errorf("--resume %s; want %s", got, want)
			}
			rlfAssertEarlierUnused(t, e, r0, "")
		})
	}
}

// TestSpawnReuseLivesHistoryHoldsCurrentID (AC-REUSE-25 last clause): a row
// seeded as migrated, its life-0 history holding its current session id, is
// reused and the new life sees none of that history.
func TestSpawnReuseLivesHistoryHoldsCurrentID(t *testing.T) {
	e := newReuseEnv(t)
	sid, older := rlfNewSession(), rlfNewSession()
	cur, old := apitest.SessionHistorySeed{SessionID: sid, JSONLPath: filepath.Join(t.TempDir(), "cur.jsonl")},
		apitest.SessionHistorySeed{SessionID: older, JSONLPath: filepath.Join(t.TempDir(), "older.jsonl")}
	spec := e.resumableSpec(time.Hour, agentGone, apitest.WithSessionHistory(cur), apitest.WithSessionHistory(old))
	spec.SessionID = sid
	r0 := reuseRow{resumeRow: e.seedResumableRow(t, spec), History: []apitest.SessionHistorySeed{cur, old}}
	rlfEarlierOnDisk(t, r0)
	rlfAssertListed(t, rlfGet(t, e, r0.ID), "present", getWantPrior{id: older, path: old.JSONLPath})

	r, _ := e.reuseLaunch(t, r0, agentAlive, reuseRequest{})
	rlvAssertLife(t, e, r.ID, 1)
	rlfReportIn(t, e, r.ID, rlfNewSession(), false)
	rlfEndLife(t, e, r.ID)
	rlfAssertListed(t, rlfGet(t, e, r.ID), "never_written")
	msg := rlfResumeRefused(t, e, r.ID, api.ErrJsonlNeverWritten)
	rlfAssertEarlierUnused(t, e, r0, msg)
	rlfAssertStored(t, e, r.ID, sid, 0)
}
