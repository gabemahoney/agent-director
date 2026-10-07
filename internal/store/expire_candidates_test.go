package store_test

// expire's store surface (SR-12.1, SR-12.3, SR-5.3, SR-5.5; Appendix F.4): the
// candidate read and the conditional delete. Its stale-snapshot refusals are
// row_version_expire_test.go's.

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// expCutoff is the cutoff the selection cases read with; expOld is an
// ended_at well before it.
var (
	expCutoff = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	expOld    = expCutoff.Add(-24 * time.Hour)
)

// expCandidateIDs reads the candidates older than cutoff and returns their ids.
func expCandidateIDs(t *testing.T, f *v5Store, cutoff time.Time) []string {
	t.Helper()
	cands, err := f.s.ListExpireCandidates(cutoff)
	if err != nil {
		t.Fatalf("ListExpireCandidates(%v): %v", cutoff, err)
	}
	var ids []string
	for _, c := range cands {
		ids = append(ids, c.ClaudeInstanceID)
	}
	return ids
}

// TestExpireCandidatesSelection checks the read selects finished rows whose
// stored ended_at is before the cutoff (converted to UTC), and never live,
// pending or NULL-ended rows, nor a row resume moved on.
func TestExpireCandidatesSelection(t *testing.T) {
	rows := map[string]struct {
		state string
		opts  []apitest.SpawnOption
	}{
		"ended, a second before":   {store.StateEnded, []apitest.SpawnOption{apitest.WithEndedAt(expCutoff.Add(-time.Second))}},
		"missing, a day before":    {store.StateMissing, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}},
		"ended, at the cutoff":     {store.StateEnded, []apitest.SpawnOption{apitest.WithEndedAt(expCutoff)}},
		"missing, after":           {store.StateMissing, []apitest.SpawnOption{apitest.WithEndedAt(expCutoff.Add(time.Second))}},
		"missing, NULL ended_at":   {store.StateMissing, []apitest.SpawnOption{apitest.WithNoEndedAt()}},
		"waiting, old ended_at":    {store.StateWaiting, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}},
		"pending, old ended_at":    {store.StatePending, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}},
		"resumed, ended before it": {store.StateEnded, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}},
	}
	everyEnded := []string{"ended, a second before", "missing, a day before", "ended, at the cutoff", "missing, after"}
	f := newV5Store(t)
	ids := map[string]string{}
	for name, r := range rows {
		ids[name] = f.seed(r.state, "", r.opts...)
	}
	rvMove(t, f, ids["resumed, ended before it"], rvExamine(t, f, ids["resumed, ended before it"]), store.CondApplied)
	for _, c := range []struct {
		name   string
		cutoff time.Time
		want   []string
	}{
		{"cutoff in UTC", expCutoff, everyEnded[:2]},
		// An unconverted cutoff would read as 07:00 and select only the day-old row.
		{"cutoff off UTC", expCutoff.In(time.FixedZone("UTC+5", 5*3600)), everyEnded[:2]},
		{"cutoff after every ended_at", expCutoff.Add(48 * time.Hour), everyEnded},
	} {
		var want []string
		for _, name := range c.want {
			want = append(want, ids[name])
		}
		slices.Sort(want)
		if got := expCandidateIDs(t, f, c.cutoff); !slices.Equal(got, want) {
			t.Errorf("%s: candidates = %v; want (in id order) %v", c.name, got, want)
		}
	}
}

// TestExpireCandidatesCarryTheRow checks each candidate carries its row as
// GetSpawn would (names byte for byte, process, snapshot and launch identity,
// a malformed token as none), beside rows a hand edit made malformed (SR-5.5).
func TestExpireCandidatesCarryTheRow(t *testing.T) {
	badToken := fullIdentity()
	badToken.Token = "NOT-A-HEX-TOKEN!"
	tokenless := badToken
	tokenless.Token = ""
	cases := []struct {
		name, session string
		opts          []apitest.SpawnOption
		want          store.ExpireCandidate // the name, process and identity it carries
	}{
		{"$-named, every identity field", "$3", []apitest.SpawnOption{apitest.WithLaunchIdentity(fullIdentity()),
			apitest.WithPID(5151), apitest.WithProcStarttime(apitest.LinuxProcStarttime)},
			store.ExpireCandidate{PID: 5151, ProcStarttime: apitest.LinuxProcStarttime, Identity: fullIdentity()}},
		{"non-ASCII name, no process or identity", "agent-é-名-\x7f", []apitest.SpawnOption{apitest.WithNoLaunchToken()},
			store.ExpireCandidate{}},
		{"malformed labels, args, env and launch start", "malformed", []apitest.SpawnOption{apitest.WithLaunchIdentity(fullIdentity()),
			apitest.WithRawLabels(`{"team":`), apitest.WithRawClaudeArgs(`["--x"`), apitest.WithRawExtraEnv(`not an object`),
			apitest.WithRawLaunchStartedAt("soon")}, store.ExpireCandidate{Identity: fullIdentity()}},
		{"malformed launch token", "bad-token", []apitest.SpawnOption{apitest.WithLaunchIdentity(badToken)},
			store.ExpireCandidate{Identity: tokenless}},
	}
	f := newV5Store(t)
	want := map[string]store.ExpireCandidate{}
	for _, c := range cases {
		id := f.seed(store.StateEnded, "sess-exp-carry", append(c.opts, apitest.WithTmuxSessionName(c.session), apitest.WithEndedAt(expOld))...)
		w := c.want
		w.ClaudeInstanceID, w.TmuxSessionName = id, c.session
		raw := f.rawColumns(id)
		w.Snapshot = store.RowSnapshot{RowVersion: raw.RowVersion.(int64), StartedAt: raw.StartedAt.(string),
			ClaudeSessionID: "sess-exp-carry", PID: w.PID, ProcStarttime: w.ProcStarttime, TmuxSessionName: c.session}
		want[id] = w
	}
	cands, err := f.s.ListExpireCandidates(expCutoff)
	if err != nil || len(cands) != len(cases) {
		t.Fatalf("ListExpireCandidates = %d candidates, %v; want %d", len(cands), err, len(cases))
	}
	for _, c := range cands {
		if w := want[c.ClaudeInstanceID]; !reflect.DeepEqual(c, w) {
			t.Errorf("candidate:\n got %+v\nwant %+v", c, w)
		}
	}
}

// TestExpireDeleteFinishedIfSameLife checks the delete removes a finished row
// still holding the examined snapshot, its requests and history with it and
// its children unlinked, refuses one resume moved on or another caller
// deleted, and never touches a row sharing its session name.
func TestExpireDeleteFinishedIfSameLife(t *testing.T) {
	cases := []struct {
		name  string
		state string
		after func(t *testing.T, f *v5Store, id string) // after examination
		want  store.CondResult
	}{
		{"unchanged ended row", store.StateEnded, nil, store.CondApplied},
		{"unchanged missing row", store.StateMissing, nil, store.CondApplied},
		{"resume's move to pending after examination", store.StateEnded, (&rvResume{}).move, store.CondChanged},
		{"deleted by another caller", store.StateEnded, func(t *testing.T, f *v5Store, id string) {
			if err := f.s.DeleteSpawn(id); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}, store.CondAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			shared := []apitest.SpawnOption{apitest.WithLaunchIdentity(fullIdentity()), apitest.WithTmuxSessionName("exp-shared"),
				apitest.WithEndedAt(expOld)}
			bystander := f.seed(store.StateEnded, "sess-exp", shared...)
			id := f.seed(c.state, "sess-exp", append(shared, apitest.WithSessionHistory(apitest.SessionHistorySeed{
				SessionID: "sess-exp-old", JSONLPath: "/tmp/ad-expire-test/old.jsonl"}))...)
			seedRequest(t, f, id)
			child := f.seed(store.StateWaiting, "")
			if err := apitest.SeedParentChild(f.path, id, child); err != nil {
				t.Fatalf("SeedParentChild: %v", err)
			}
			examined := rvExamine(t, f, id).Snapshot
			if c.after != nil {
				c.after(t, f, id)
			}
			bystanderBefore := f.rawColumns(bystander)
			before, readErr := apitest.ReadSpawnColumns(f.path, id)
			if got, err := f.s.DeleteFinishedIfSameLife(id, examined); err != nil || got != c.want {
				t.Fatalf("DeleteFinishedIfSameLife = %v, %v; want %v", got, err, c.want)
			}
			after, afterErr := apitest.ReadSpawnColumns(f.path, id)
			if c.want == store.CondChanged && (readErr != nil || afterErr != nil || !reflect.DeepEqual(after, before)) {
				t.Errorf("row changed (%v, %v):\nbefore %+v\nafter  %+v", readErr, afterErr, before, after)
			}
			if c.want == store.CondApplied {
				reqs, err := f.s.PermissionRequestsForSpawn(id)
				if !errors.Is(afterErr, store.ErrSpawnNotFound) || err != nil || len(reqs) != 0 ||
					len(f.historyAllLives(id)) != 0 || f.rawColumns(child).ParentID != nil {
					t.Errorf("after the delete: row %v, requests %d (%v), history %d, child parent %#v; want all gone, parent NULL",
						afterErr, len(reqs), err, len(f.historyAllLives(id)), f.rawColumns(child).ParentID)
				}
			}
			if after := f.rawColumns(bystander); !reflect.DeepEqual(after, bystanderBefore) {
				t.Errorf("bystander with the same session name changed:\nbefore %+v\nafter  %+v", bystanderBefore, after)
			}
		})
	}
}
