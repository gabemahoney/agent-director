package api_test

// security_expire_test.go is expire's part of SR-15's per-verb table
// (security_test.go; Epic 15): a finished target row whose agent process is
// gone meets the planted SECRET=xyz sessions on expire's Gone paths (the row
// is deleted, no ad.expire.kept), on its Leftover, conflicting-labels and
// Ours paths (kept, one ad.expire.kept), and on a Gone row whose delete fails
// in the store (logged, kept store_error).

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securityExpireCall runs expire through the Client with a zero window, so
// every finished row (the target only) is selected.
func securityExpireCall(_ *testing.T, c *api.Client, _ *securityScene) (any, error) {
	return c.Expire(olderThan(0))
}

// securityExpireKeptRecord checks that the ad.expire.kept record names the target's recorded session.
func securityExpireKeptRecord(t *testing.T, s *securityScene, rec map[string]any, _ map[string]string) {
	t.Helper()
	if rec["tmux_session_name"] != s.target.Name {
		t.Errorf("tmux_session_name = %v; want %q", rec["tmux_session_name"], s.target.Name)
	}
}

// exSecTarget is a target that ended a day before the fixture clock, its
// agent process gone so its lookup decides; noSession seeds no session of its own.
func exSecTarget(noSession bool) killRowSpec {
	return killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: noSession,
		Opts: []apitest.SpawnOption{apitest.WithEndedAt(killClockStart.Add(-24 * time.Hour))}}
}

// exSecResult checks that the result deleted the target (deleted) or kept it,
// and that the row is gone or still there to match.
func exSecResult(deleted bool) func(*testing.T, *securityScene, any) {
	return func(t *testing.T, s *securityScene, res any) {
		t.Helper()
		got := res.(api.ExpireResult)
		ids, kept := []string{}, []string{s.target.ID}
		if deleted {
			ids, kept = kept, ids
		}
		if !slices.Equal(got.IDs, ids) || !slices.Equal(got.KeptIDs, kept) {
			t.Errorf("ids %v, kept_ids %v; want %v, %v", got.IDs, got.KeptIDs, ids, kept)
		}
		_, err := apitest.ReadSpawnColumns(s.e.dbPath, s.target.ID)
		if gone := errors.Is(err, store.ErrSpawnNotFound); gone != deleted {
			t.Errorf("target row read: %v; want deleted %t", err, deleted)
		}
	}
}

// securityExpireGoneCases meet the planted sessions on expire's Gone paths: a
// holder of each kind, and another store's session with this launch's name,
// token and id (WD 2026-09-29 STORE). Each deletes the row and writes no record for it.
var securityExpireGoneCases = append(secHeldBy(securityCase{name: "gone, ", target: exSecTarget(true), check: exSecResult(true)}, nil, nil),
	securityCase{
		name: "gone, another store's session with this launch's name, token and id", target: exSecTarget(true),
		arrange: rpSecOtherStore(func(s *securityScene) string { return s.target.Name },
			func(s *securityScene) string { return s.target.Token }),
		check: exSecResult(true),
	})

// securityExpireKeptCases meet the planted sessions on expire's Leftover,
// conflicting-labels and Ours paths (SR-12.2): each keeps the row, with one ad.expire.kept.
var securityExpireKeptCases = []securityCase{
	{
		name: "leftover", target: exSecTarget(true),
		arrange: func(t *testing.T, s *securityScene) {
			s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionLabel(s.target.old(), true))
		},
		fields: map[string]any{"reason": "leftover_running", "source": "ad_expire"}, check: exSecResult(false),
	},
	{
		name: "conflicting labels", target: exSecTarget(false), arrange: secDuplicate, disagree: true,
		fields: map[string]any{"reason": "provenance_conflict", "source": "ad_expire"}, check: exSecResult(false),
	},
	{
		name: "ours", target: exSecTarget(false),
		fields: map[string]any{"reason": "ours", "source": "ad_expire"}, check: exSecResult(false),
	},
}

// TestSecurityExpireStoreErrorLog checks SR-15 when each Gone case's delete
// fails in the store: the logged error, the result and the store_error record carry no forbidden value.
func TestSecurityExpireStoreErrorLog(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	for _, c := range securityExpireGoneCases {
		t.Run(c.name, func(t *testing.T) {
			s := newSecurityScene(t, securityVerb{verb: "expire"}, c)
			w := s.e.expireStore()
			w.failDelete(s.target.ID, nil)
			s.e.rec.Reset()
			mark := trailMark(t)

			res, lg, err := s.e.expireWith(w, olderThan(0))

			if err != nil {
				t.Fatalf("Expire: %v; want success", err)
			}
			assertExpired(t, res, mark, map[string]string{s.target.ID: "store_error"})
			out, jerr := json.Marshal(res)
			if jerr != nil {
				t.Fatalf("marshal result: %v", jerr)
			}
			securityAbsent(t, "result", string(out), s)
			if len(lg.lines) != 1 || !strings.Contains(lg.lines[0], s.target.ID) {
				t.Errorf("expire log = %q; want one line naming %s", lg.lines, s.target.ID)
			}
			securityAbsent(t, "expire log", strings.Join(lg.lines, "\n"), s)
			securityCheckTrail(t, expireKeptEvent, s, readAPITrailLines(t)[mark:], c)
			securityCheckReads(t, s, false)
		})
	}
}
