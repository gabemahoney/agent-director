package api_test

// spawn_reuse_change_test.go covers the applied reuse as the caller sees it
// (SR-10.3, SR-10.7; AC-REUSE-02, AC-REUSE-03, AC-REUSE-09): the reset row's
// fresh values from one clock reading, the socket rule, the parent from the
// environment, the archive, the deleted permission requests and the result.
// Store failures and a changed or removed row are in
// spawn_reuse_change_fail_test.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rchgClockStep is how far each reading of the Client clock moves e.clock: over
// a second with a millisecond fraction, so no two readings agree in the
// store's whole-second layout or in milliseconds.
const rchgClockStep = 1100 * time.Millisecond

// rchgRun is one reuse: its result, error and log, every reading of the
// Client clock, and the snapshot taken just before it.
type rchgRun struct {
	res      api.SpawnResult
	err      error
	logs     string
	readings []time.Time
	before   writesSnapshot
}

// rchgReuse reuses r with p through Client.Spawn on a Client whose clock
// advances e.clock by rchgClockStep on every reading and records it.
func (e *killEnv) rchgReuse(t *testing.T, r reuseRow, p api.SpawnParams) *rchgRun {
	t.Helper()
	c, buf := e.client(t)
	run := &rchgRun{before: e.snapshotReuse(t, r)}
	api.SetClockForTest(c, func() time.Time {
		e.clock.Advance(rchgClockStep)
		run.readings = append(run.readings, e.clock.Now())
		return e.clock.Now()
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

// rchgAssertReset checks the applied reset of r by run with p: get shows the
// new request and nothing of the old life; the store holds the new extra env,
// no pid, started_at, last_seen_at and launch_started_at from one clock
// reading, life + 1, a new token the one create labels with and socket; the
// history is the archive of the old session; ad.spawn.reused records it.
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
	if got, err := apitest.ReadSessionHistoryAllLives(e.dbPath, r.ID); err != nil || !slices.Equal(rchgHistory(got), rchgArchived(run.before)) {
		t.Errorf("history = %q (%v); want %q", rchgHistory(got), err, rchgArchived(run.before))
	}
	recs := run.before.since(t, "ad.spawn.reused")
	if len(recs) != 1 {
		t.Fatalf("ad.spawn.reused records = %v; want 1", recs)
	}
	var archived any
	if sid, _ := run.before.cols.ClaudeSessionID.(string); sid != "" {
		archived = sid
	}
	if v, ok := recs[0]["archived_session_id"]; !ok || v != archived {
		t.Errorf("archived_session_id = %v (present %v); want %v", v, ok, archived)
	}
	assertAPITrailStr(t, recs[0], "prior_state", fmt.Sprint(run.before.cols.State))
	assertAPITrailStr(t, recs[0], "lookup_outcome", "gone")
	assertAPITrailStr(t, recs[0], "source", "ad_spawn")
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

// TestSpawnReuseAppliedRow: an ended or missing row, its new name free,
// reuses: the reset's values, the socket rule, the parent from the
// environment, no permission requests, children kept, and a fresh spawn's result.
func TestSpawnReuseAppliedRow(t *testing.T) {
	cases := []struct {
		name, state, socket string // socket: "own" (the caller's too), "other" or "none" recorded
		parent              bool
	}{
		{"ended, parent from the environment", store.StateEnded, "own", true},
		{"missing, no parent", store.StateMissing, "own", false},
		{"ended, its socket not the caller's", store.StateEnded, "other", false},
		{"missing, no socket recorded", store.StateMissing, "none", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			spec, socket := reuseRowSpec{State: tc.state, Child: true}, e.defaultSocket
			switch tc.socket {
			case "other":
				socket = filepath.Join(filepath.Dir(e.defaultSocket), "reuse-other")
				spec.Opts = []apitest.SpawnOption{apitest.WithTmuxSocket(socket)}
			case "none":
				spec.Opts = []apitest.SpawnOption{apitest.WithTmuxSocket("")}
			}
			r := e.seedReusable(t, agentGone, spec)
			q := rchgRequest(t)
			if tc.parent {
				q.Parent = e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
			}
			p := rchgParams(t, r, q)
			if !tc.parent {
				os.Unsetenv("AGENT_DIRECTOR_INSTANCE_ID") //nolint:errcheck // reuseParams' t.Setenv restores it
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
	cases := []struct {
		name string
		seed func(t *testing.T, e *killEnv) reuseRow
	}{
		{"no session id", func(t *testing.T, e *killEnv) reuseRow {
			return e.seedReusable(t, agentGone, reuseRowSpec{NoSessionID: true})
		}},
		{"neither pid nor session id", func(t *testing.T, e *killEnv) reuseRow {
			return e.seedReusable(t, agentNotRecorded, reuseRowSpec{NoSessionID: true})
		}},
		{"session id archived, same path", func(t *testing.T, e *killEnv) reuseRow { return e.rchgSeedArchived(t, false) }},
		{"session id archived, other path", func(t *testing.T, e *killEnv) reuseRow { return e.rchgSeedArchived(t, true) }},
		{"malformed labels, args and env", func(t *testing.T, e *killEnv) reuseRow {
			return e.seedReusable(t, agentGone, reuseRowSpec{Malformed: true})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := tc.seed(t, e)
			p := rchgParams(t, r, rchgRequest(t))

			run := e.rchgReuse(t, r, p)

			e.rchgAssertReset(t, r, p, run, e.defaultSocket)
		})
	}
}
