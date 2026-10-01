package api_test

// expire_fixture_test.go extends the kill fixture (killEnv, killRow) for
// expire (SR-20.2, SR-20.6): finished rows on the fixture clock, the
// expireStore wrapper, the run helpers and the result, trail and call
// readers. It holds no tests. Seed with finishedSpec / seedFinished, never
// apitest.SeedExpireFixture (wall-clock ended_at), and give the agent a
// state that is not alive for a row the lookup must decide.

import (
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// expireKeptEvent is the trail event of a row expire kept (SR-12.5).
const expireKeptEvent = "ad.expire.kept"

// expireStore is api.ExpireStore over the real store; failList, failDelete,
// refuseDelete and beforeDelete inject a failure or a write, else it delegates.
type expireStore struct {
	st        *store.Store
	listErr   error
	deleteErr map[string]error
	deleteRes map[string]api.CondResult
	before    map[string]func()
}

var _ api.ExpireStore = (*expireStore)(nil)

// expireStore returns a new expireStore over e.st with nothing injected.
func (e *killEnv) expireStore() *expireStore {
	return &expireStore{st: e.st, deleteErr: map[string]error{}, deleteRes: map[string]api.CondResult{},
		before: map[string]func(){}}
}

// failList makes every candidate read return err (errInjectedStore when nil).
func (w *expireStore) failList(err error) { w.listErr = orInjected(err) }

// failDelete makes id's delete return err (errInjectedStore when nil), deleting nothing.
func (w *expireStore) failDelete(id string, err error) { w.deleteErr[id] = orInjected(err) }

// refuseDelete makes id's delete return res (CondChanged or CondAbsent), deleting nothing.
func (w *expireStore) refuseDelete(id string, res api.CondResult) { w.deleteRes[id] = res }

// beforeDelete runs fn once, just before id's delete reaches the store.
func (w *expireStore) beforeDelete(id string, fn func()) { w.before[id] = fn }

// ListExpireCandidates returns failList's error, else delegates.
func (w *expireStore) ListExpireCandidates(cutoff time.Time) ([]api.ExpireCandidate, error) {
	if w.listErr != nil {
		return nil, w.listErr
	}
	return w.st.ListExpireCandidates(cutoff)
}

// DeleteFinishedIfSameLife returns id's injected answer, else runs its
// beforeDelete hook and delegates.
func (w *expireStore) DeleteFinishedIfSameLife(id string, examined api.RowSnapshot) (api.CondResult, error) {
	if err := w.deleteErr[id]; err != nil {
		return 0, err
	}
	if res, ok := w.deleteRes[id]; ok {
		return res, nil
	}
	if fn := w.before[id]; fn != nil {
		delete(w.before, id)
		fn()
	}
	return w.st.DeleteFinishedIfSameLife(id, examined)
}

// StoreID delegates.
func (w *expireStore) StoreID() string { return w.st.StoreID() }

// finishedSpec is a row that ended age before e.clock's now, its agent in
// state a and no session seeded; opts go last.
func (e *killEnv) finishedSpec(age time.Duration, a agentState, opts ...apitest.SpawnOption) killRowSpec {
	return killRowSpec{State: store.StateEnded, Agent: a, NoSession: true,
		Opts: append([]apitest.SpawnOption{apitest.WithEndedAt(e.clock.Now().Add(-age))}, opts...)}
}

// seedFinished seeds finishedSpec's row; add its session, a leftover or a
// name holder with seedSession / seedLeftover.
func (e *killEnv) seedFinished(t *testing.T, age time.Duration, a agentState, opts ...apitest.SpawnOption) killRow {
	t.Helper()
	return e.seedRow(t, e.finishedSpec(age, a, opts...))
}

// olderThan is d as expire's override (0 selects every finished row).
func olderThan(d time.Duration) *time.Duration { return &d }

// expire runs expireWith on e.st.
func (e *killEnv) expire(over *time.Duration) (api.ExpireResult, *recordingLogger, error) {
	return e.expireWith(e.st, over)
}

// expireWith runs the exported api.Expire with s, e.rec, e.pc, config's
// default retention, over, e.cfg's effective sweep budget, e.clock.Now and a
// new recordingLogger (returned).
func (e *killEnv) expireWith(s api.ExpireStore, over *time.Duration) (api.ExpireResult, *recordingLogger, error) {
	lg := &recordingLogger{}
	res, err := api.Expire(s, e.rec, e.pc, config.Default().Defaults.ExpireRetentionDays, over,
		e.cfg.EffectiveSweepBudget(), e.clock.Now, lg)
	return res, lg, err
}

// expireClient runs Client.Expire on a new e.client with settings; logs is
// the Client's captured log.
func (e *killEnv) expireClient(t *testing.T, over *time.Duration, settings ...apitest.TmuxSetting) (res api.ExpireResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.Expire(over)
	return res, buf.String(), err
}

// expireKeptSince returns the ad.expire.kept records written after mark (trailMark).
func expireKeptSince(t *testing.T, mark int) []map[string]any {
	t.Helper()
	return trailSince(t, mark, expireKeptEvent)
}

// expireDisagreesSince returns id's ad.provenance.disagree records written
// by expire after mark.
func expireDisagreesSince(t *testing.T, mark int, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range trailSince(t, mark, "ad.provenance.disagree") {
		if l["verb"] == "expire" && l["claude_instance_id"] == id {
			out = append(out, l)
		}
	}
	return out
}

// assertExpired fails unless res deleted the ids want maps to "" and kept
// the others, each kept id with exactly one ad.expire.kept record since mark
// giving its reason; an id in neither list is left out of want.
func assertExpired(t *testing.T, res api.ExpireResult, mark int, want map[string]string) {
	t.Helper()
	deleted, kept := []string{}, []string{}
	for id, reason := range want {
		if reason == "" {
			deleted = append(deleted, id)
		} else {
			kept = append(kept, id)
		}
	}
	sort.Strings(deleted)
	sort.Strings(kept)
	if res.IDs == nil || res.KeptIDs == nil {
		t.Errorf("ids %#v, kept_ids %#v; want both non-nil", res.IDs, res.KeptIDs)
	}
	if !slices.Equal(res.IDs, deleted) || res.Count != len(res.IDs) {
		t.Errorf("ids = %v (count %d); want %v", res.IDs, res.Count, deleted)
	}
	if !slices.Equal(res.KeptIDs, kept) || res.Kept != len(res.KeptIDs) {
		t.Errorf("kept_ids = %v (kept %d); want %v", res.KeptIDs, res.Kept, kept)
	}
	got := map[string]string{}
	for _, l := range expireKeptSince(t, mark) {
		id, _ := l["claude_instance_id"].(string)
		if prev, dup := got[id]; dup {
			t.Errorf("row %s: ad.expire.kept %v and %v; want one record", id, prev, l["reason"])
		}
		got[id], _ = l["reason"].(string)
	}
	for _, id := range kept {
		if got[id] != want[id] {
			t.Errorf("row %s kept reason = %q; want %q", id, got[id], want[id])
		}
		delete(got, id)
	}
	for id, reason := range got {
		t.Errorf("row %s: ad.expire.kept %q; want none", id, reason)
	}
}

// assertLookupsOn fails unless the tmux calls were exactly one lookup on each
// of sockets (any order; none when empty) and nothing name-based.
func (e *killEnv) assertLookupsOn(t *testing.T, sockets ...string) {
	t.Helper()
	want := make([]tmux.Call, len(sockets))
	for i := range want {
		want[i] = tmux.CallLookup
	}
	e.assertKillCalls(t, want...)
	var got []string
	for _, c := range e.rec.SocketCalls() {
		got = append(got, c.Socket)
	}
	sort.Strings(got)
	wantSockets := slices.Clone(sockets)
	sort.Strings(wantSockets)
	if !slices.Equal(got, wantSockets) {
		t.Errorf("lookup sockets = %v; want %v", got, wantSockets)
	}
}
