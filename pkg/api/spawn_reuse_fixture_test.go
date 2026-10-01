package api_test

// spawn_reuse_fixture_test.go extends the kill fixture's resume helpers
// (resume_lookup_fixture_test.go, resume_held_fixture_test.go) for spawn with
// the reuse opt-in (SR-10, SR-20.2): the reusable finished row, the new
// request, the Client runners, the reuse-store seam (hookedReuseStore), a
// pending row made by a real reuse and reuse's "wrote nothing" snapshot. It
// holds no tests. The other arrangements are already one call on killEnv: the
// row's own session (seedSession with createdBefore or
// tmuxfix.WithRowSessionName), a leftover or a holder (seedHolder, under the
// requested name through killRow.withName), the agent process (the
// agentState argument), the bound and window (the runners' settings) and
// "duplicate session" (arrangeHeld). Later reuse tests use these helpers,
// never a copy of them.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// reuseLife is the life a reusable row is seeded in; its earlier history
// entry belongs to the life before.
const reuseLife = int64(3)

// The liveness columns and the raw request-field text a reusable row holds:
// well-formed JSON spaced as no encoder writes it, so a re-encoding shows.
const (
	reuseUnverifiedSince = "2026-09-30 11:05:00"
	reuseLivenessNote    = "probe: EACCES reading the process start time"
	reuseRawLabels       = `{"team": "reuse",  "tier":"a"}`
	reuseRawArgs         = `[ "--model",  "opus" ]`
)

// reuseEndedAt is what a reusable row holds in ended_at.
type reuseEndedAt int

const (
	endedAged        reuseEndedAt = iota // reuseRowSpec.Age before the rule's reading
	endedNull                            // NULL
	endedUnparseable                     // text that does not parse as a time (seeded raw)
)

// reuseRowSpec overrides seedReusable's defaults; Opts go last, so they win.
type reuseRowSpec struct {
	State       string        // ended (default) or missing
	Age         time.Duration // endedAged: how long before ruleInstant (heldInstant when Held) the row ended
	Held        bool          // Age is measured from heldInstant, the rule's reading after "duplicate session"
	EndedAt     reuseEndedAt  // default endedAged
	NoSessionID bool          // no session id or transcript; with agentNotRecorded, neither pid nor session id
	NoPreTrust  bool          // the old life's pre-trust opt-out (no_pre_trust 1)
	Malformed   bool          // malformed labels, claude_args and extra_env text (seeded raw)
	Bare        bool          // no parent, history or permission requests
	Child       bool          // also a finished child row whose parent_id is the row's id
	Opts        []apitest.SpawnOption
}

// reuseRow is a seeded reusable row: the resumable row (killRow, cwd,
// transcript, trust directory), its parent's and child's ids, its history
// entries as seeded and its open and decided permission requests' tokens.
type reuseRow struct {
	resumeRow
	ParentID, ChildID           string
	History                     []apitest.SessionHistorySeed
	OpenRequest, DecidedRequest string
}

// withName is r recorded under name, so seedHolder places a holder of a
// requested name.
func (r killRow) withName(name string) killRow {
	r.Name = name
	return r
}

// seedReusable seeds spec's finished row, its agent in state a, through
// seedOnServer: a full launch identity on e.defaultSocket with its server
// running, pid and start time (unless agentNotRecorded), a session id and
// transcript, life reuseLife, both liveness columns, raw request-field text,
// and (unless Bare) a parent, history of this life and the one before, and
// one open and one decided permission request.
func (e *killEnv) seedReusable(t *testing.T, a agentState, spec reuseRowSpec) reuseRow {
	t.Helper()
	ks := killRowSpec{ID: "reuse-" + uuid.NewString()[:8], State: spec.State, Agent: a, NoSession: true}
	if ks.State == "" {
		ks.State = store.StateEnded
	}
	if !spec.NoSessionID {
		ks.SessionID = "sess-" + uuid.NewString()[:8]
	}
	opts := []apitest.SpawnOption{apitest.WithLifeNumber(reuseLife),
		apitest.WithLivenessUnverifiedSince(reuseUnverifiedSince), apitest.WithLivenessNote(reuseLivenessNote),
		apitest.WithRawLabels(reuseRawLabels), apitest.WithRawClaudeArgs(reuseRawArgs)}
	switch spec.EndedAt {
	case endedNull:
		opts = append(opts, apitest.WithNoEndedAt())
	case endedUnparseable:
		opts = append(opts, apitest.WithEndedAt("not a time"))
	default:
		instant := e.ruleInstant
		if spec.Held {
			instant = e.heldInstant
		}
		opts = append(opts, apitest.WithEndedAt(instant().Add(-spec.Age)))
	}
	if spec.NoPreTrust {
		opts = append(opts, apitest.WithNoPreTrust())
	}
	var r reuseRow
	if !spec.Bare {
		r.History = []apitest.SessionHistorySeed{
			{SessionID: "hist-" + uuid.NewString()[:8], JSONLPath: filepath.Join(t.TempDir(), "this-life.jsonl"), Life: reuseLife},
			{SessionID: "hist-" + uuid.NewString()[:8], JSONLPath: filepath.Join(t.TempDir(), "earlier.jsonl"), Life: reuseLife - 1},
		}
		for _, h := range r.History {
			opts = append(opts, apitest.WithSessionHistory(h))
		}
	}
	seed := e.seedRow
	if spec.Malformed {
		opts = append(opts, apitest.WithRawLabels(`{"team":`), apitest.WithRawClaudeArgs(`["--model",`),
			apitest.WithRawExtraEnv(`not an object`))
	}
	if spec.Malformed || spec.EndedAt == endedUnparseable {
		seed = func(t *testing.T, s killRowSpec) killRow { return e.seedRawRow(t, s, nil) }
	}
	ks.Opts = append(opts, spec.Opts...)
	r.resumeRow = e.seedOnServer(t, ks, seed)
	if !spec.Bare {
		r.ParentID = e.seedRelative(t, r.ID, false)
		r.OpenRequest, r.DecidedRequest = e.seedRequest(t, r.ID), e.seedRequest(t, r.ID)
		if ok, err := e.st.DecidePermissionRequest(r.ID, r.DecidedRequest, "deny", store.DecisionReasonOperator,
			store.WriterProcessDecide); err != nil || !ok {
			t.Fatalf("DecidePermissionRequest(%s) = %v, %v; want decided", r.ID, ok, err)
		}
	}
	if spec.Child {
		r.ChildID = e.seedRelative(t, r.ID, true)
	}
	return r
}

// seedRelative seeds a finished row with no session, its agent gone, as id's
// parent (or, when child, as its child) and returns its id.
func (e *killEnv) seedRelative(t *testing.T, id string, child bool) string {
	t.Helper()
	other := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
	parent, kid := other, id
	if child {
		parent, kid = id, other
	}
	if err := apitest.SeedParentChild(e.dbPath, parent, kid); err != nil {
		t.Fatalf("SeedParentChild(%s, %s): %v", parent, kid, err)
	}
	return other
}

// seedRequest seeds an open permission request for id and returns its token.
func (e *killEnv) seedRequest(t *testing.T, id string) string {
	t.Helper()
	req, err := apitest.SeedPermissionRequest(e.dbPath, id, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest(%s): %v", id, err)
	}
	return req.RequestToken
}

// reuseRequest is a reuse call's request beyond the row's id and the opt-in.
type reuseRequest struct {
	Name       string            // the requested session name; "" = the row's recorded name
	CWD        string            // "" = the row's cwd
	Args       []string          // Claude args
	Env        map[string]string // extra env added to CLAUDE_CONFIG_DIR, the row's trust directory
	Labels     map[string]string // agent-director labels
	NoPreTrust bool              // the call's pre-trust opt-out
	Parent     string            // the caller's AGENT_DIRECTOR_INSTANCE_ID ("" = none)
}

// reuseParams is q's SpawnParams for r with the opt-in; it sets the test's
// AGENT_DIRECTOR_INSTANCE_ID to q.Parent, the parent id the reuse records.
func reuseParams(t *testing.T, r reuseRow, q reuseRequest) api.SpawnParams {
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", q.Parent)
	p := api.SpawnParams{ClaudeInstanceID: r.ID, ReuseFinished: true, TmuxSessionName: q.Name, CWD: q.CWD,
		ClaudeArgs: q.Args, AgentDirectorLabels: q.Labels, NoPreTrust: q.NoPreTrust, ExtraEnv: r.Trust.extraEnv()}
	if p.TmuxSessionName == "" {
		p.TmuxSessionName = r.Name
	}
	if p.CWD == "" {
		p.CWD = r.CWD
	}
	for k, v := range q.Env {
		p.ExtraEnv[k] = v
	}
	return p
}

// reuse runs Client.Spawn with p on a new e.client with settings (the bound
// and window as configured); logs is the Client's captured log.
func (e *killEnv) reuse(t *testing.T, p api.SpawnParams, settings ...apitest.TmuxSetting) (res api.SpawnResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = c.Spawn(p)
	return res, buf.String(), err
}

// reuseWith is reuse through api.SpawnWithReuseStore, with rs as the reuse
// path's store.
func (e *killEnv) reuseWith(t *testing.T, rs api.ReuseStore, p api.SpawnParams, settings ...apitest.TmuxSetting) (res api.SpawnResult, logs string, err error) {
	t.Helper()
	c, buf := e.client(t, settings...)
	res, err = api.SpawnWithReuseStore(c, rs, p)
	return res, buf.String(), err
}

// snapshotReuse takes r's writesSnapshot for a reuse (verb spawn) whose
// request pre-trusts in r's trust directory (reuseParams); take it just
// before the call.
func (e *killEnv) snapshotReuse(t *testing.T, r reuseRow) writesSnapshot {
	t.Helper()
	return e.snapshotWrites(t, "spawn", r.ID, r.Trust, r.Socket)
}

// reusePending seeds spec's row, its old agent gone, and reuses it with q
// through Client.Spawn, failing unless that succeeds; it returns the row as
// the reuse left it (pending, its new name, token, identity and session),
// the new pane's process put in the fake in state a before the identity
// write reads it.
func (e *killEnv) reusePending(t *testing.T, a agentState, spec reuseRowSpec, q reuseRequest) reuseRow {
	t.Helper()
	r := e.seedReusable(t, agentGone, spec)
	var created bool
	e.rec.AfterCall(tmux.CallCreate, func(c tmuxfix.SocketCall, err error) {
		if created || err != nil || c.InstanceID != r.ID {
			return
		}
		created = true
		for _, s := range e.rec.Sessions(c.Socket) {
			if s.Label.Token == c.Token && len(s.Panes) > 0 {
				e.pc.Set(s.Panes[0].PID, a.process(apitest.LinuxProcStarttime))
			}
		}
	})
	if _, logs, err := e.reuse(t, reuseParams(t, r, q)); err != nil {
		t.Fatalf("reuse of %s: %v (log %q)", r.ID, err, logs)
	}
	row, err := e.st.GetSpawn(r.ID)
	if err != nil || row.State != store.StatePending {
		t.Fatalf("GetSpawn(%s) after the reuse = %s, %v; want pending", r.ID, row.State, err)
	}
	r.Spawn, r.Name, r.Token, r.Agent = row, row.TmuxSessionName, row.Identity.Token, a
	for _, s := range e.rec.Sessions(r.Socket) {
		if s.Label.Token == r.Token {
			r.Session = s
		}
	}
	e.setAgent(&r.killRow, row.Identity.PanePID, apitest.LinuxProcStarttime)
	return r
}

// hookedReuseStore is api.ReuseStore over a real store, delegating every
// call. failRead makes ReadForReuse fail (nothing read); afterRead,
// beforeReset and afterReset run a function once (e.g. e.st.SetParentID,
// DeleteSpawn or a competing call); reads counts ReadForReuse calls. Each
// hook is cleared before it runs. Safe for concurrent use.
type hookedReuseStore struct {
	st *store.Store

	hookLock
	readErr                                  error
	afterReadFn, beforeResetFn, afterResetFn func()
	reads                                    int
}

// The wrapper satisfies the seam's store surface.
var _ api.ReuseStore = (*hookedReuseStore)(nil)

// failRead makes every later ReadForReuse return err (errInjectedStore when
// nil) without calling the store.
func (w *hookedReuseStore) failRead(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.readErr = orInjected(err)
}

// afterRead runs fn once, when the next ReadForReuse returns.
func (w *hookedReuseStore) afterRead(fn func()) { w.setHook(&w.afterReadFn, fn) }

// beforeReset runs fn once, just before the next ResetForReuse.
func (w *hookedReuseStore) beforeReset(fn func()) { w.setHook(&w.beforeResetFn, fn) }

// afterReset runs fn once, when the next ResetForReuse returns (before the create).
func (w *hookedReuseStore) afterReset(fn func()) { w.setHook(&w.afterResetFn, fn) }

// setHook sets *slot to fn under the lock.
func (w *hookedReuseStore) setHook(slot *func(), fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*slot = fn
}

// readCount is the number of ReadForReuse calls so far.
func (w *hookedReuseStore) readCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reads
}

// ReadForReuse counts the call and delegates unless failRead is set, then
// runs the afterRead hook.
func (w *hookedReuseStore) ReadForReuse(instanceID string) (store.ReuseRow, bool, error) {
	w.mu.Lock()
	w.reads++
	w.mu.Unlock()
	var (
		row   store.ReuseRow
		found bool
		err   = w.injected(&w.readErr)
	)
	if err == nil {
		row, found, err = w.st.ReadForReuse(instanceID)
	}
	if fn := w.take(&w.afterReadFn); fn != nil {
		fn()
	}
	return row, found, err
}

// ResetForReuse runs the beforeReset hook, delegates, then runs the
// afterReset hook.
func (w *hookedReuseStore) ResetForReuse(instanceID string, examined api.RowSnapshot, fresh api.Spawn) (api.CondResult, string, int64, error) {
	if fn := w.take(&w.beforeResetFn); fn != nil {
		fn()
	}
	res, archived, version, err := w.st.ResetForReuse(instanceID, examined, fresh)
	if fn := w.take(&w.afterResetFn); fn != nil {
		fn()
	}
	return res, archived, version, err
}

// RestoreAfterFailedReuse delegates.
func (w *hookedReuseStore) RestoreAfterFailedReuse(instanceID string, resetVersion int64, prior store.RawLife, failedAt time.Time) (api.CondResult, error) {
	return w.st.RestoreAfterFailedReuse(instanceID, resetVersion, prior, failedAt)
}

// RecordLaunchIdentity delegates.
func (w *hookedReuseStore) RecordLaunchIdentity(instanceID string, launchVersion int64, token string, id api.LaunchIdentity) (api.CondResult, error) {
	return w.st.RecordLaunchIdentity(instanceID, launchVersion, token, id)
}
