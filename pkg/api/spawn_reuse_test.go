package api_test

// spawn_reuse_test.go covers spawn with the reuse opt-in up to its change
// (SR-10.1, SR-10.3, SR-10.5, SR-10.7; AC-REUSE-02, 03, 04, 09, 15, 27): the
// applied reset as get, the store and the trail show it, a fresh spawn's
// result; a store failure as ErrInternal, and a row inserted, removed or
// changed after the pre-check read as ErrInstanceIdCollision, writing nothing.

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestSpawnReuseInsertRace: a row inserted after the pre-check read found
// none makes the insert collide: ErrInstanceIdCollision, no create, and the
// competing row is left as it was.
func TestSpawnReuseInsertRace(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	id := "reuse-" + uuid.NewString()[:8]
	w := &hookedReuseStore{st: e.st}
	var seeded apitest.SpawnColumns
	w.afterRead(func() {
		if _, err := apitest.SeedSpawn(e.dbPath, id, store.StateWaiting, t.TempDir(), "off", "", false); err != nil {
			t.Fatalf("SeedSpawn(%s): %v", id, err)
		}
		seeded = e.columns(t, id)
	})

	_, _, err := e.reuseWith(t, w, api.SpawnParams{ClaudeInstanceID: id, ReuseFinished: true, CWD: t.TempDir(),
		ExtraEnv: seedTrustConfig(t, t.TempDir(), trustLacksEntry).extraEnv()})

	assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
	e.assertRowUnchanged(t, id, seeded)
	if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
		t.Errorf("create calls = %d; want 0", n)
	}
}

// TestSpawnReusePreCheckReadFails: a failed pre-check read is ErrInternal with
// the pre-check wording, no tmux call and nothing written, trust file included.
func TestSpawnReusePreCheckReadFails(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{})
	w := &hookedReuseStore{st: e.st}
	w.failRead(nil)
	before := e.snapshotReuse(t, r)

	_, _, err := e.reuseWith(t, w, reuseParams(t, r, reuseRequest{}))

	rtabAssertInternal(t, err, apitest.DescPreCheckRead())
	if calls := e.rec.SocketCalls()[before.calls:]; len(calls) != 0 {
		t.Errorf("tmux calls = %+v; want none", calls)
	}
	e.assertWroteNothing(t, before)
}

// rchgClockStep is how far each reading of the Client clock moves e.clock: over
// a second with a millisecond fraction, so no two readings agree in the
// store's whole-second layout or in milliseconds.
const rchgClockStep = 1100 * time.Millisecond

// rchgRun is one reuse: its result, error and log, every reading of the
// Client clock, the snapshot taken just before it, and the ad.spawn.reused
// records and row state when the create returned.
type rchgRun struct {
	res            api.SpawnResult
	err            error
	logs           string
	readings       []time.Time
	before         writesSnapshot
	reusedAtCreate int
	stateAtCreate  any
}

// rchgReuse reuses r with p through Client.Spawn on a Client whose clock
// advances e.clock by rchgClockStep on every reading and records it.
func (e *killEnv) rchgReuse(t *testing.T, r reuseRow, p api.SpawnParams) *rchgRun {
	t.Helper()
	c, buf := e.client(t)
	run := &rchgRun{before: e.snapshotReuse(t, r), reusedAtCreate: -1}
	api.SetClockForTest(c, func() time.Time {
		e.clock.Advance(rchgClockStep)
		run.readings = append(run.readings, e.clock.Now())
		return e.clock.Now()
	})
	e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
		run.reusedAtCreate, run.stateAtCreate = len(run.before.since(t, rutReused)), e.columns(t, r.ID).State
	})
	run.res, run.err = c.Spawn(p)
	run.logs = buf.String()
	return run
}

// rchgRequest is a reuse request differing from the seeded row in every
// request field: a free name, a new cwd, args, an added env var and labels.
func rchgRequest(t *testing.T) reuseRequest {
	return reuseRequest{Name: "newname-" + uuid.NewString()[:8], CWD: t.TempDir(), Args: []string{"--model", "sonnet"},
		Env: map[string]string{"REUSE_NEW_ENV": "1"}, Labels: map[string]string{"team": "new"}}
}

// rchgParams is reuseParams for r with q and relay mode on (the row's is off).
func rchgParams(t *testing.T, r reuseRow, q reuseRequest) api.SpawnParams {
	p := reuseParams(t, r, q)
	p.RelayMode = "on"
	return p
}

// rchgHistory is history as "session|path|life" keys, sorted (recorded_at
// left out).
func rchgHistory(history []apitest.HistoryEntry) []string {
	keys := []string{}
	for _, h := range history {
		keys = append(keys, fmt.Sprintf("%s|%v|%d", h.ClaudeSessionID, h.JSONLPath, h.LifeNumber))
	}
	slices.Sort(keys)
	return keys
}

// rchgArchived is before's history after a reuse archived the row's session
// id as before's columns hold it: its entry, in the ending life with the
// current path, replaces any entry of that id; no session id archives nothing.
func rchgArchived(before writesSnapshot) []string {
	sid, _ := before.cols.ClaudeSessionID.(string)
	if sid == "" {
		return rchgHistory(before.history)
	}
	path, _ := before.cols.JSONLPath.(string)
	life, _ := before.cols.LifeNumber.(int64)
	kept := slices.DeleteFunc(slices.Clone(before.history), func(h apitest.HistoryEntry) bool { return h.ClaudeSessionID == sid })
	entry := apitest.HistoryEntry{ClaudeSessionID: sid, LifeNumber: life}
	entry.JSONLPath.String, entry.JSONLPath.Valid = path, path != ""
	return rchgHistory(append(kept, entry))
}

// rchgAssertReset checks r's applied reset by run with p as get, the store and
// the trail show it: the request's fields, nothing of the old life, times from
// one clock reading, life + 1, a new token, every call on socket, the call's
// pre-trust choice, the old session archived and exactly one ad.spawn.reused,
// written after the create on the reset (pending) row.
func (e *killEnv) rchgAssertReset(t *testing.T, r reuseRow, p api.SpawnParams, run *rchgRun, socket string) {
	t.Helper()
	if run.err != nil {
		t.Fatalf("reuse of %s: %v (log %q)", r.ID, run.err, run.logs)
	}
	c, _ := e.client(t)
	g, err := c.Get(r.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", r.ID, err)
	}
	cwd, _ := filepath.EvalSymlinks(p.CWD)
	if g.State != store.StatePending || g.CWD != cwd || g.TmuxSessionName != p.TmuxSessionName || g.RelayMode != p.RelayMode ||
		!slices.Equal(g.ClaudeArgs, p.ClaudeArgs) || !reflect.DeepEqual(g.Labels, p.AgentDirectorLabels) {
		t.Errorf("Get = {%s %q %q %q %q %v}; want pending with the request's cwd %q, name %q, relay %q, args %q, labels %v",
			g.State, g.CWD, g.TmuxSessionName, g.RelayMode, g.ClaudeArgs, g.Labels, cwd, p.TmuxSessionName, p.RelayMode, p.ClaudeArgs, p.AgentDirectorLabels)
	}
	if g.ClaudeSessionID != "" || g.JSONLPath != "" || g.LivenessUnverifiedSince != nil || g.LivenessNote != nil || g.EndedAt != nil {
		t.Errorf("Get session %q, transcript %q, liveness %v %v, ended_at %v; want none of the old life",
			g.ClaudeSessionID, g.JSONLPath, g.LivenessUnverifiedSince, g.LivenessNote, g.EndedAt)
	}
	cols := e.columns(t, r.ID)
	var env map[string]string
	if s, _ := cols.ExtraEnv.(string); json.Unmarshal([]byte(s), &env) != nil || !reflect.DeepEqual(env, p.ExtraEnv) {
		t.Errorf("extra_env = %v; want the request's %v", cols.ExtraEnv, p.ExtraEnv)
	}
	if cols.PID != nil || cols.ProcStarttime != nil {
		t.Errorf("pid %v, proc_starttime %v; want NULL", cols.PID, cols.ProcStarttime)
	}
	at := slices.IndexFunc(run.readings, func(rd time.Time) bool { return cols.LaunchStartedAt == rd.UnixMilli() })
	if at < 0 {
		t.Errorf("launch_started_at = %v; want a clock reading's milliseconds (readings %v)", cols.LaunchStartedAt, run.readings)
	} else if want := run.readings[at].UTC().Format(heldStoreLayout); cols.StartedAt != want || cols.LastSeenAt != want {
		t.Errorf("started_at %v, last_seen_at %v; want %q, the launch start's reading", cols.StartedAt, cols.LastSeenAt, want)
	}
	creates := e.rec.SocketCallsOf(tmux.CallCreate)
	tok, _ := cols.LaunchToken.(string)
	if len(creates) != 1 || !spawnTokenRE.MatchString(tok) || tok == r.Token || creates[0].Token != tok {
		t.Fatalf("launch_token %q (old %q), creates %+v; want a new token the one create labels with", tok, r.Token, creates)
	}
	if creates[0].Target != p.TmuxSessionName || creates[0].Cwd != cwd {
		t.Errorf("create name %q in %q; want the request's %q in %q", creates[0].Target, creates[0].Cwd, p.TmuxSessionName, cwd)
	}
	if cols.TmuxSocket != socket || creates[0].Socket != socket || cols.LifeNumber != reuseLife+1 {
		t.Errorf("tmux_socket %v, create's socket %q, life %v; want %q and life %d", cols.TmuxSocket, creates[0].Socket, cols.LifeNumber, socket, reuseLife+1)
	}
	for _, c := range e.rec.SocketCalls() {
		if c.Socket != socket {
			t.Errorf("tmux %v on %s; want every call on the row's socket %s", c.Call, c.Socket, socket)
		}
	}
	if want := map[bool]int64{false: 0, true: 1}[p.NoPreTrust]; cols.NoPreTrust != want {
		t.Errorf("no_pre_trust = %#v; want the call's %d, not the old life's", cols.NoPreTrust, want)
	}
	r.Trust.check(t, cwd, !p.NoPreTrust, "after the reuse")
	if got, err := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID); err != nil || !slices.Equal(rchgHistory(got), rchgArchived(run.before)) {
		t.Errorf("history = %q (%v); want %q", rchgHistory(got), err, rchgArchived(run.before))
	}
	if run.reusedAtCreate != 0 || run.stateAtCreate != store.StatePending {
		t.Errorf("at the create: %s records = %d, state %v; want none on the reset (pending) row", rutReused, run.reusedAtCreate,
			run.stateAtCreate)
	}
	recs := run.before.since(t, rutReused)
	if len(recs) != 1 || len(run.before.since(t, rutRestored)) != 0 || len(run.before.since(t, rutNameHeld)) != 0 {
		t.Fatalf("%s records = %v; want 1, and no %s or %s", rutReused, recs, rutRestored, rutNameHeld)
	}
	var archived any
	if sid, _ := run.before.cols.ClaudeSessionID.(string); sid != "" {
		archived = sid
	}
	assertTrailRecord(t, recs[0], rutReusedKeys, map[string]any{"claude_instance_id": r.ID, "prior_state": run.before.cols.State,
		"archived_session_id": archived, "lookup_outcome": "gone", "source": "ad_spawn"})
}

// rchgJSONKeys is v's JSON object keys, sorted.
func rchgJSONKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	var m map[string]any
	if err != nil || json.Unmarshal(b, &m) != nil {
		t.Fatalf("JSON of %+v: %v", v, err)
	}
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// rchgAssertLikeFresh spawns p's request fresh under another id and name and
// checks the reuse's result and get row show the same fields (SR-10.7): the
// result equals {id, pre_trust}.
func (e *killEnv) rchgAssertLikeFresh(t *testing.T, id string, p api.SpawnParams, run *rchgRun) {
	t.Helper()
	c, _ := e.client(t)
	fp := p
	fp.ClaudeInstanceID, fp.TmuxSessionName, fp.ReuseFinished = "fresh-"+uuid.NewString()[:8], "fresh-"+uuid.NewString()[:8], false
	fresh, err := c.Spawn(fp)
	if err != nil {
		t.Fatalf("fresh Spawn: %v", err)
	}
	if want := (api.SpawnResult{ClaudeInstanceID: id, PreTrust: fresh.PreTrust}); run.res != want ||
		!slices.Equal(rchgJSONKeys(t, run.res), rchgJSONKeys(t, fresh)) {
		t.Errorf("reuse result %+v; want %+v, a fresh spawn's fields", run.res, want)
	}
	reused, rerr := c.Get(id)
	row, ferr := c.Get(fp.ClaudeInstanceID)
	if rerr != nil || ferr != nil || !slices.Equal(rchgJSONKeys(t, reused), rchgJSONKeys(t, row)) {
		t.Errorf("get fields %q (%v); want a fresh spawn's %q (%v)", rchgJSONKeys(t, reused), rerr, rchgJSONKeys(t, row), ferr)
	}
}

// TestSpawnReuseAppliedRow (AC-REUSE-27 too): an ended or missing row, its new
// name free, reuses: the reset's values, the socket rule, the parent from the
// environment, pre-trust by the call's own opt-out (the old life's the
// opposite), no permission requests, children kept, and a fresh spawn's result.
func TestSpawnReuseAppliedRow(t *testing.T) {
	// Serial: its parent cases set AGENT_DIRECTOR_INSTANCE_ID with t.Setenv; its other cases run in
	// parallel.
	cases := []struct {
		name, state, socket string // socket: "own" (the caller's too), "other" or "none" recorded
		parent, noPreTrust  bool
	}{
		{"ended, parent from the environment", store.StateEnded, "own", true, false},
		{"missing, no parent, opting out of pre-trust", store.StateMissing, "own", false, true},
		{"ended, its socket not the caller's", store.StateEnded, "other", false, false},
		{"missing, no socket recorded", store.StateMissing, "none", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.parent {
				t.Parallel()
			}
			e := newKillEnv(t)
			spec, socket := reuseRowSpec{State: tc.state, Child: true, NoPreTrust: !tc.noPreTrust}, e.defaultSocket
			switch tc.socket {
			case "other":
				socket = filepath.Join(filepath.Dir(e.defaultSocket), "reuse-other")
				spec.Opts = []apitest.SpawnOption{apitest.WithTmuxSocket(socket)}
			case "none":
				spec.Opts = []apitest.SpawnOption{apitest.WithTmuxSocket("")}
			}
			r := e.seedReusable(t, agentGone, spec)
			q := rchgRequest(t)
			q.NoPreTrust = tc.noPreTrust
			if tc.parent {
				q.Parent = e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
			}
			p := rchgParams(t, r, q)
			if !tc.parent {
				unsetenvIfSet(t, "AGENT_DIRECTOR_INSTANCE_ID")
			}

			run := e.rchgReuse(t, r, p)

			e.rchgAssertReset(t, r, p, run, socket)
			if got, want := e.columns(t, r.ID).ParentID, any(nil); tc.parent && got != q.Parent || !tc.parent && got != want {
				t.Errorf("parent_id = %v; want %q", got, q.Parent)
			}
			if perms, err := e.st.PermissionRequestsForSpawn(r.ID); err != nil || len(perms) != 0 {
				t.Errorf("permission requests = %+v (%v); want none", perms, err)
			}
			if got := e.childIDs(t, r.ID); !slices.Equal(got, []string{r.ChildID}) {
				t.Errorf("children = %q; want [%s]", got, r.ChildID)
			}
			e.rchgAssertLikeFresh(t, r.ID, p, run)
		})
	}
}

// rchgSeedArchived seeds a reusable row whose session id is already in its
// history in this life, under its transcript path or (otherPath) another.
func (e *killEnv) rchgSeedArchived(t *testing.T, otherPath bool) reuseRow {
	t.Helper()
	sid := "sess-" + uuid.NewString()[:8]
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	archived := path
	if otherPath {
		archived = filepath.Join(t.TempDir(), "earlier.jsonl")
	}
	spec := e.resumableSpec(0, agentGone, apitest.WithLifeNumber(reuseLife), apitest.WithJsonlPath(path),
		apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: sid, JSONLPath: archived, Life: reuseLife}))
	spec.ID, spec.SessionID = "reuse-"+uuid.NewString()[:8], sid
	return reuseRow{resumeRow: e.seedResumableRow(t, spec)}
}

// TestSpawnReuseArchiveCases: a row with no session id archives nothing; an
// already archived session id, same or other path, gets no duplicate and no
// ErrInternal; malformed labels, args and env reuse (AC-REUSE-02, 03, 09).
func TestSpawnReuseArchiveCases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		seed func(t *testing.T, e *killEnv) reuseRow
	}{
		{"no session id", func(t *testing.T, e *killEnv) reuseRow {
			return e.seedReusable(t, agentGone, reuseRowSpec{NoSessionID: true})
		}},
		{"missing with neither pid nor session id", func(t *testing.T, e *killEnv) reuseRow {
			return e.seedReusable(t, agentNotRecorded, reuseRowSpec{State: store.StateMissing, NoSessionID: true})
		}},
		{"session id archived, same path", func(t *testing.T, e *killEnv) reuseRow { return e.rchgSeedArchived(t, false) }},
		{"session id archived, other path", func(t *testing.T, e *killEnv) reuseRow { return e.rchgSeedArchived(t, true) }},
		{"malformed labels, args and env", func(t *testing.T, e *killEnv) reuseRow {
			return e.seedReusable(t, agentGone, reuseRowSpec{Malformed: true})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := tc.seed(t, e)
			p := rchgParams(t, r, rchgRequest(t))

			run := e.rchgReuse(t, r, p)

			e.rchgAssertReset(t, r, p, run, e.defaultSocket)
		})
	}
}

// TestSpawnReuseStoreFailureIsInternal: an archive failure, a reset failure
// and a permission-request deletion failure are ErrInternal, the archive's
// with its own wording, with no create, no ad.spawn.reused and nothing written.
func TestSpawnReuseStoreFailureIsInternal(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name string
		kind storefix.WriteFailureKind
		want func(instanceID string) apitest.DescCase
	}{
		{"archive", storefix.WriteFailReuseArchive, apitest.DescReuseArchiveFailure},
		{"reset", storefix.WriteFailReuseReset, apitest.DescReuseChangeFailure},
		{"permission-request deletion", storefix.WriteFailReusePermissionDelete, apitest.DescReuseChangeFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{})
			storefix.InjectWriteFailure(t, e.dbPath, tc.kind, r.ID)
			before := e.snapshotReuse(t, r)

			_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{Name: "newname-" + uuid.NewString()[:8]}))

			rtabAssertInternal(t, err, tc.want(r.ID))
			e.assertWroteNothing(t, before, exceptTrust)
		})
	}
}

// TestSpawnReuseRowRemovedAtReset (SR-10.5; AC-REUSE-15, as expire's delete):
// a row deleted after the lookup is ErrInstanceIdCollision with no create, no
// row, history or request written back, and no ad.spawn.reused.
func TestSpawnReuseRowRemovedAtReset(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedReusable(t, agentGone, reuseRowSpec{})
	sessions := e.rec.Sessions(r.Socket)
	mark := trailMark(t)
	removed := false
	e.rec.AfterCall(tmux.CallLookup, func(c tmuxfix.SocketCall, _ error) {
		if !removed && c.Socket == r.Socket {
			removed = true
			if err := e.st.DeleteSpawn(r.ID); err != nil {
				t.Errorf("DeleteSpawn(%s): %v", r.ID, err)
			}
		}
	})

	_, _, err := e.reuse(t, reuseParams(t, r, reuseRequest{Name: "newname-" + uuid.NewString()[:8]}))

	assertOneSentinel(t, err, spawn.ErrInstanceIdCollision)
	apitest.AssertDescription(t, err.Error(), apitest.DescReuseLostRace(r.ID))
	if _, rerr := apitest.ReadSpawnColumns(e.dbPath, r.ID); !errors.Is(rerr, store.ErrSpawnNotFound) {
		t.Errorf("ReadSpawnColumns(%s) err = %v; want no row", r.ID, rerr)
	}
	if h, herr := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID); herr != nil || len(h) != 0 {
		t.Errorf("history = %+v (%v); want none", h, herr)
	}
	if perms, perr := e.st.PermissionRequestsForSpawn(r.ID); perr != nil || len(perms) != 0 {
		t.Errorf("permission requests = %+v (%v); want none", perms, perr)
	}
	if got := callKinds(e.rec); len(got) != 1 || got[0] != tmux.CallLookup || len(e.rec.Sessions(r.Socket)) != len(sessions) {
		t.Errorf("tmux calls %v, sessions %d; want one lookup and the %d seeded sessions", got, len(e.rec.Sessions(r.Socket)), len(sessions))
	}
	if recs := ptRecords(t, mark, rutReused, r.ID); len(recs) != 0 {
		t.Errorf("%s records = %v; want none", rutReused, recs)
	}
}
