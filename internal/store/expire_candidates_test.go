package store_test

// expire's store surface (SR-12.1, SR-12.3, SR-5.3, SR-5.5; Appendix F.4):
// the candidate read and the conditional delete. Rows are seeded through
// apitest and read raw through apitest.ReadSpawnColumns.

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

// expSharedName is the session name every delete-case row records, so a
// delete that matched on the name would reach the bystander.
const expSharedName = "exp-shared"

// expCandidateIDs reads the candidates older than cutoff and returns their ids.
func expCandidateIDs(t *testing.T, f *v5Store, cutoff time.Time) []string {
	t.Helper()
	cands, err := f.s.ListExpireCandidates(cutoff)
	if err != nil {
		t.Fatalf("ListExpireCandidates(%v): %v", cutoff, err)
	}
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.ClaudeInstanceID)
	}
	return ids
}

// expColString and expColInt read a nullable raw column as its zero value
// when NULL, as the candidate read does.
func expColString(v any) string {
	if v == nil {
		return ""
	}
	return v.(string)
}

func expColInt(v any) int64 {
	if v == nil {
		return 0
	}
	return v.(int64)
}

// expWantCandidate is the candidate the store-read helper's columns of id
// describe; token is the identity's token (only a well-formed one reads back).
func expWantCandidate(t *testing.T, f *v5Store, id, token string) store.ExpireCandidate {
	t.Helper()
	c := f.rawColumns(id)
	snap := store.RowSnapshot{
		RowVersion: expColInt(c.RowVersion), StartedAt: expColString(c.StartedAt),
		ClaudeSessionID: expColString(c.ClaudeSessionID), PID: int(expColInt(c.PID)),
		ProcStarttime: expColString(c.ProcStarttime), TmuxSessionName: expColString(c.TmuxSessionName),
	}
	return store.ExpireCandidate{
		ClaudeInstanceID: id, TmuxSessionName: snap.TmuxSessionName,
		PID: snap.PID, ProcStarttime: snap.ProcStarttime, Snapshot: snap,
		Identity: store.LaunchIdentity{
			Token: token, Socket: expColString(c.TmuxSocket),
			ServerPID: int(expColInt(c.TmuxServerPID)), ServerStart: expColInt(c.TmuxServerStarted),
			ServerStarttime: expColString(c.TmuxServerStarttime), PaneID: expColString(c.PaneID),
			PanePID: int(expColInt(c.PanePID)), PaneStarttime: expColString(c.PaneStarttime),
		},
	}
}

// TestExpireCandidatesSelection checks the read selects finished rows whose
// stored ended_at is before the cutoff, and never live, pending or NULL rows.
func TestExpireCandidatesSelection(t *testing.T) {
	type row struct {
		state string
		opts  []apitest.SpawnOption
		then  func(t *testing.T, f *v5Store, id string) // optional, after seeding
	}
	rows := map[string]row{
		"ended, a second before":   {store.StateEnded, []apitest.SpawnOption{apitest.WithEndedAt(expCutoff.Add(-time.Second))}, nil},
		"missing, a day before":    {store.StateMissing, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}, nil},
		"ended, at the cutoff":     {store.StateEnded, []apitest.SpawnOption{apitest.WithEndedAt(expCutoff)}, nil},
		"missing, after":           {store.StateMissing, []apitest.SpawnOption{apitest.WithEndedAt(expCutoff.Add(time.Second))}, nil},
		"missing, NULL ended_at":   {store.StateMissing, nil, nil},
		"waiting, old ended_at":    {store.StateWaiting, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}, nil},
		"working, old ended_at":    {store.StateWorking, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}, nil},
		"pending, old ended_at":    {store.StatePending, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}, nil},
		"resumed, ended before it": {store.StateEnded, []apitest.SpawnOption{apitest.WithEndedAt(expOld)}, (&rvResume{}).move},
	}
	everyEnded := []string{"ended, a second before", "missing, a day before", "ended, at the cutoff", "missing, after"}
	cases := []struct {
		name   string
		cutoff time.Time
		want   []string // row names selected
	}{
		{"cutoff in UTC", expCutoff, everyEnded[:2]},
		// An unconverted cutoff would read as 07:00 and select only the day-old row.
		{"cutoff off UTC", expCutoff.In(time.FixedZone("UTC+5", 5*3600)), everyEnded[:2]},
		{"cutoff after every ended_at (zero override)", expCutoff.Add(48 * time.Hour), everyEnded},
	}
	f := newV5Store(t)
	ids := map[string]string{}
	for name, r := range rows {
		ids[name] = f.seed(r.state, "", r.opts...)
		if r.then != nil {
			r.then(t, f, ids[name])
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var want []string
			for _, name := range c.want {
				want = append(want, ids[name])
			}
			slices.Sort(want)
			if got := expCandidateIDs(t, f, c.cutoff); !slices.Equal(got, want) {
				t.Errorf("candidates = %v\nwant (in id order) %v", got, want)
			}
		})
	}
}

// TestExpireCandidatesCarryTheRow checks each candidate holds the row as the
// store-read helper shows it, names byte for byte included.
func TestExpireCandidatesCarryTheRow(t *testing.T) {
	cases := []struct {
		name, session string
		pid           int
		identity      store.LaunchIdentity
	}{
		{"$-named, every identity field", "$3", 5151, fullIdentity()},
		{"non-ASCII name, no process or identity", "agent-é-名-\x7f", 0, store.LaunchIdentity{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			opts := []apitest.SpawnOption{apitest.WithTmuxSessionName(c.session),
				apitest.WithLaunchIdentity(c.identity), apitest.WithEndedAt(expOld)}
			if c.pid != 0 {
				opts = append(opts, apitest.WithPID(c.pid), apitest.WithProcStarttime(apitest.LinuxProcStarttime))
			}
			id := f.seed(store.StateEnded, "sess-exp-carry", opts...)
			cands, err := f.s.ListExpireCandidates(expCutoff)
			if err != nil || len(cands) != 1 {
				t.Fatalf("ListExpireCandidates = %d candidates, %v; want 1, nil", len(cands), err)
			}
			got := cands[0]
			if want := expWantCandidate(t, f, id, c.identity.Token); !reflect.DeepEqual(got, want) {
				t.Errorf("candidate:\n got %+v\nwant %+v", got, want)
			}
			if got.TmuxSessionName != c.session || got.PID != c.pid || got.Identity != c.identity ||
				got.Snapshot.ClaudeSessionID != "sess-exp-carry" {
				t.Errorf("candidate name %q, pid %d, identity %+v, session %q; want the seeded %q, %d, %+v, sess-exp-carry",
					got.TmuxSessionName, got.PID, got.Identity, got.Snapshot.ClaudeSessionID, c.session, c.pid, c.identity)
			}
			sp := rvExamine(t, f, id)
			if got.Snapshot != sp.Snapshot || got.Identity != sp.Identity {
				t.Errorf("candidate snapshot, identity %+v, %+v; GetSpawn's %+v, %+v", got.Snapshot, got.Identity, sp.Snapshot, sp.Identity)
			}
		})
	}
}

// TestExpireCandidatesMalformedRows checks rows a hand edit made malformed
// read without error beside a well-formed one (SR-5.5).
func TestExpireCandidatesMalformedRows(t *testing.T) {
	badToken := fullIdentity()
	badToken.Token = "NOT-A-HEX-TOKEN!"
	rows := []struct {
		name     string
		opts     []apitest.SpawnOption
		badToken bool // the stored token is not well formed, so the candidate reads none
	}{
		{"well formed", []apitest.SpawnOption{apitest.WithLaunchIdentity(fullIdentity())}, false},
		{"malformed labels", []apitest.SpawnOption{apitest.WithRawLabels(`{"team":`)}, false},
		{"malformed claude_args", []apitest.SpawnOption{apitest.WithRawClaudeArgs(`["--x"`)}, false},
		{"malformed extra_env", []apitest.SpawnOption{apitest.WithRawExtraEnv(`not an object`)}, false},
		{"malformed launch token", []apitest.SpawnOption{apitest.WithLaunchIdentity(badToken)}, true},
		{"non-integer launch start", []apitest.SpawnOption{apitest.WithRawLaunchStartedAt("soon")}, false},
		{"from before the release, no token", []apitest.SpawnOption{apitest.WithNoLaunchToken()}, true},
	}
	f := newV5Store(t)
	want := map[string]store.ExpireCandidate{}
	for _, r := range rows {
		id := f.seed(store.StateEnded, "", append(r.opts, apitest.WithEndedAt(expOld))...)
		token := ""
		if !r.badToken {
			token = expColString(f.rawColumns(id).LaunchToken)
		}
		want[id] = expWantCandidate(t, f, id, token)
	}
	cands, err := f.s.ListExpireCandidates(expCutoff)
	if err != nil {
		t.Fatalf("ListExpireCandidates: %v", err)
	}
	if len(cands) != len(rows) {
		t.Errorf("read %d candidates, want %d", len(cands), len(rows))
	}
	for _, c := range cands {
		if w := want[c.ClaudeInstanceID]; !reflect.DeepEqual(c, w) {
			t.Errorf("candidate:\n got %+v\nwant %+v", c, w)
		}
	}
}

// expSeed seeds a row in state recording its agent's pane and the shared
// session name, with an old ended_at when finished.
func expSeed(f *v5Store, state string, opts ...apitest.SpawnOption) string {
	f.t.Helper()
	all := []apitest.SpawnOption{apitest.WithLaunchIdentity(fullIdentity()), apitest.WithTmuxSessionName(expSharedName)}
	if state == store.StateEnded || state == store.StateMissing {
		all = append(all, apitest.WithEndedAt(expOld))
	}
	return f.seed(state, "sess-exp", append(all, opts...)...)
}

// expFinished seeds a finished row and examines it.
func expFinished(state string) func(*testing.T, *v5Store) (string, store.RowSnapshot) {
	return func(t *testing.T, f *v5Store) (string, store.RowSnapshot) {
		id := expSeed(f, state)
		return id, rvExamine(t, f, id).Snapshot
	}
}

// expThen seeds an ended row, examines it, then runs write.
func expThen(write func(t *testing.T, f *v5Store, id string)) func(*testing.T, *v5Store) (string, store.RowSnapshot) {
	return func(t *testing.T, f *v5Store) (string, store.RowSnapshot) {
		id, examined := expFinished(store.StateEnded)(t, f)
		write(t, f, id)
		return id, examined
	}
}

// expAgentHook is event from id's own agent; the gate must apply it.
func expAgentHook(event, wantState string) func(*testing.T, *v5Store, string) {
	return func(t *testing.T, f *v5Store, id string) {
		if got := apitest.ApplyAgentHook(t, f.path, id, event, "sess-exp-relaunch"); !got.Applied {
			t.Fatalf("own agent's %s = %+v; want applied", event, got)
		}
		if got := f.rawColumns(id).State; got != wantState {
			t.Fatalf("state after %s = %#v, want %q", event, got, wantState)
		}
	}
}

// TestExpireDeleteFinishedIfSameLife checks the delete applies only to a row
// still finished with the examined snapshot, and never touches another row.
func TestExpireDeleteFinishedIfSameLife(t *testing.T) {
	cases := []struct {
		name string
		seed func(t *testing.T, f *v5Store) (id string, examined store.RowSnapshot)
		want store.CondResult
	}{
		{"unchanged ended row", expFinished(store.StateEnded), store.CondApplied},
		{"unchanged missing row", expFinished(store.StateMissing), store.CondApplied},
		{"relaunch's SessionStart after examination", expThen(expAgentHook("SessionStart", store.StateWaiting)), store.CondChanged},
		{"own agent's hook after examination, still ended", expThen(expAgentHook("Notification", store.StateEnded)), store.CondChanged},
		{"resume's move to pending after examination", expThen((&rvResume{}).move), store.CondChanged},
		{"resume's restore after examination", expThen(func(t *testing.T, f *v5Store, id string) {
			r := &rvResume{}
			r.move(t, f, id)
			r.restore(store.CondApplied)(t, f, id)
		}), store.CondChanged},
		{"live row", func(t *testing.T, f *v5Store) (string, store.RowSnapshot) {
			id := expSeed(f, store.StateWaiting)
			return id, rvExamine(t, f, id).Snapshot
		}, store.CondChanged},
		{"pending row", func(t *testing.T, f *v5Store) (string, store.RowSnapshot) {
			id := expSeed(f, store.StatePending)
			return id, rvExamine(t, f, id).Snapshot
		}, store.CondChanged},
		{"deleted by another caller", expThen(func(t *testing.T, f *v5Store, id string) {
			if err := f.s.DeleteSpawn(id); err != nil {
				t.Fatalf("DeleteSpawn: %v", err)
			}
		}), store.CondAbsent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newV5Store(t)
			bystander := expSeed(f, store.StateEnded)
			id, examined := c.seed(t, f)
			bystanderBefore := f.rawColumns(bystander)
			before, readErr := apitest.ReadSpawnColumns(f.path, id)
			got, err := f.s.DeleteFinishedIfSameLife(id, examined)
			if err != nil || got != c.want {
				t.Fatalf("DeleteFinishedIfSameLife = %v, %v; want %v, nil", got, err, c.want)
			}
			after, afterErr := apitest.ReadSpawnColumns(f.path, id)
			switch c.want {
			case store.CondChanged:
				if readErr != nil || afterErr != nil || !reflect.DeepEqual(after, before) {
					t.Errorf("row changed (%v, %v):\nbefore %+v\nafter  %+v", readErr, afterErr, before, after)
				}
			default:
				if !errors.Is(afterErr, store.ErrSpawnNotFound) {
					t.Errorf("ReadSpawnColumns after the delete: %v; want ErrSpawnNotFound", afterErr)
				}
			}
			if after := f.rawColumns(bystander); !reflect.DeepEqual(after, bystanderBefore) {
				t.Errorf("bystander with the same session name changed:\nbefore %+v\nafter  %+v", bystanderBefore, after)
			}
		})
	}
}

// TestExpireDeleteRemovesDependentRows checks an applied delete takes the
// row's permission requests and history with it and unlinks its children.
func TestExpireDeleteRemovesDependentRows(t *testing.T) {
	f := newV5Store(t)
	id := expSeed(f, store.StateEnded, apitest.WithSessionHistory(apitest.SessionHistorySeed{
		SessionID: "sess-exp-old", JSONLPath: "/tmp/ad-expire-test/old.jsonl", Life: 0}))
	if _, err := apitest.SeedPermissionRequest(f.path, id, "Bash"); err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	child := f.seed(store.StateWaiting, "")
	if err := apitest.SeedParentChild(f.path, id, child); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}
	reqs, err := f.s.PermissionRequestsForSpawn(id)
	if err != nil || len(reqs) != 1 || historyLen(t, f, id) != 1 || f.rawColumns(child).ParentID != id {
		t.Fatalf("seeded %d requests (%v), %d history entries, child parent %#v; want 1, 1, %q",
			len(reqs), err, historyLen(t, f, id), f.rawColumns(child).ParentID, id)
	}

	if got, err := f.s.DeleteFinishedIfSameLife(id, rvExamine(t, f, id).Snapshot); err != nil || got != store.CondApplied {
		t.Fatalf("DeleteFinishedIfSameLife = %v, %v; want CondApplied, nil", got, err)
	}
	if reqs, err := f.s.PermissionRequestsForSpawn(id); err != nil || len(reqs) != 0 {
		t.Errorf("permission requests after the delete = %d, %v; want none", len(reqs), err)
	}
	if n := historyLen(t, f, id); n != 0 {
		t.Errorf("history entries after the delete = %d, want 0", n)
	}
	if got := f.rawColumns(child).ParentID; got != nil {
		t.Errorf("child parent_id = %#v, want NULL", got)
	}
}
