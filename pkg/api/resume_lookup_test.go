package api_test

// resume_lookup_test.go covers resume's one pre-launch lookup at the verb
// (SR-8.1 step 3, SR-8.2, SR-3.10; AC-RES-01, AC-RES-02, AC-LKP-14, AC-LKP-18,
// AC-LKP-20): its place after the guards and before pre-trust and the move,
// each name holder class, Leftover, prefix neighbours, $ and \ names, the
// agent process states and no adoption. Fixture: resume_lookup_fixture_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// rlkCase is one resume of a seeded resumable row: the row's agent process,
// whether it ended just now (else past the window and the bound), no launch
// token or a lost create reply, extra seed options, the session seed returns
// (the one the refusal names), and the refusal; want nil means the launch.
type rlkCase struct {
	name               string
	agent              agentState
	stopping           bool
	noToken, lostReply bool
	opts               []apitest.SpawnOption
	seed               func(*testing.T, *killEnv, *resumeRow) tmuxfix.SeedSession
	want               error
	desc               func(*killEnv, resumeRow, tmuxfix.SeedSession) apitest.DescCase
}

// rlkRun seeds tc's row and sessions, resumes it, and checks the launch or
// the refusal: its sentinel and description, one lookup on the row's socket,
// nothing written or touched, and no adoption.
func rlkRun(t *testing.T, tc rlkCase) {
	t.Helper()
	e := newKillEnv(t)
	age := rlkSettled(e)
	if tc.stopping {
		age = 0
	}
	spec := e.resumableSpec(age, tc.agent, tc.opts...)
	if tc.noToken {
		spec.Opts = append(spec.Opts, apitest.WithLaunchIdentity(store.LaunchIdentity{Socket: e.defaultSocket}))
	}
	spec.NoServerIdentity, spec.NoPane = tc.lostReply, tc.lostReply
	r := e.seedResumableRow(t, spec)
	var s tmuxfix.SeedSession
	if tc.seed != nil {
		s = tc.seed(t, e, &r)
	}
	before := e.snapshotResume(t, r)

	_, err := e.resume(r.ID)

	if tc.want == nil {
		e.rlkAssertLaunched(t, before, err)
		return
	}
	if !errors.Is(err, tc.want) {
		t.Fatalf("resume err = %v; want %v", err, tc.want)
	}
	other := s.Label.InstanceID
	if other == r.ID {
		other = ""
	}
	apitest.AssertDescription(t, err.Error(), tc.desc(e, r, s),
		other, r.Token, s.Label.Token, e.storeID, apitest.OtherStoreID(e.storeID))
	e.assertResumeWroteNothing(t, before)
	if calls := e.rec.SocketCalls()[before.calls:]; len(calls) != 1 || calls[0].Call != tmux.CallLookup || calls[0].Socket != r.Socket {
		t.Errorf("tmux calls = %+v; want one lookup on %s", calls, r.Socket)
	}
	if n := adoptedRecords(t, "resume", r.ID); n != 0 {
		t.Errorf("adopted records = %d; want 0", n)
	}
}

// rlkAssertLaunched fails unless the resume launched: one lookup on the
// row's socket first, then one create there, the trust entry written, the
// row pending, and every session seeded before untouched.
func (e *killEnv) rlkAssertLaunched(t *testing.T, before resumeSnapshot, err error) {
	t.Helper()
	r := before.r
	if err != nil {
		t.Fatalf("resume err = %v; want the launch", err)
	}
	calls := e.rec.SocketCalls()[before.calls:]
	if len(calls) < 2 || calls[0].Call != tmux.CallLookup || calls[1].Call != tmux.CallCreate {
		t.Errorf("tmux calls = %+v; want the lookup, then the create", calls)
	}
	creates := 0
	for i, c := range calls {
		if c.Socket != r.Socket || (i > 0 && c.Call == tmux.CallLookup) {
			t.Errorf("tmux call %d = %v on %s; want no second lookup and every call on %s", i, c.Call, c.Socket, r.Socket)
		}
		if c.Call == tmux.CallCreate {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("creates = %d; want 1", creates)
	}
	if st := e.columns(t, r.ID).State; st != store.StatePending {
		t.Errorf("state = %v; want pending", st)
	}
	r.Trust.check(t, r.CWD, true, "after the launch")
	for socket, want := range before.sessions {
		got := e.rec.Sessions(socket)
		for _, w := range want {
			if !slices.ContainsFunc(got, func(g tmuxfix.SeedSession) bool { return reflect.DeepEqual(g, w) }) {
				t.Errorf("session %s %q on %s changed or gone; sessions now %+v", w.ID, w.Name, socket, got)
			}
		}
	}
}

// rlkSettled is an age past both the stopping window and the
// starting-session bound of e's [tmux] values.
func rlkSettled(e *killEnv) time.Duration {
	return 2 * (e.cfg.EffectiveStoppingWindow() + e.cfg.EffectiveStartingSession())
}

// rlkStarting is the starting-session case parameters for r at e's defaults.
func rlkStarting(e *killEnv, r resumeRow, noSession bool) apitest.StartingSession {
	return apitest.StartingSession{InstanceID: r.ID, Name: r.Name, Window: e.cfg.EffectiveStoppingWindow(),
		Bound: e.cfg.EffectiveStartingSession(), NoSession: noSession, WindowChecked: true, SessionID: true}
}

// rlkHolder seeds seedHolder's kind k as the case's session.
func rlkHolder(k holderKind) func(*testing.T, *killEnv, *resumeRow) tmuxfix.SeedSession {
	return func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession { return e.seedHolder(t, r.killRow, k) }
}

// rlkNamed seeds an unlabelled session named name(r) on r's socket.
func rlkNamed(name func(resumeRow) string) func(*testing.T, *killEnv, *resumeRow) tmuxfix.SeedSession {
	return func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
		return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: name(*r)})
	}
}

// rlkOwn seeds r's own session with opts.
func rlkOwn(opts ...func(*killEnv) tmuxfix.RowSessionOption) func(*testing.T, *killEnv, *resumeRow) tmuxfix.SeedSession {
	return func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
		var o []tmuxfix.RowSessionOption
		for _, f := range opts {
			o = append(o, f(e))
		}
		e.seedSession(t, &r.killRow, o...)
		return r.Session
	}
}

// rlkLabel is the own-session option giving label (set) instead of the current one.
func rlkLabel(label func(resumeRow) tmux.Label, r *resumeRow) func(*killEnv) tmuxfix.RowSessionOption {
	return func(*killEnv) tmuxfix.RowSessionOption { return tmuxfix.WithRowSessionLabel(label(*r), true) }
}

// rlkHeld is the pre-launch holder parameters naming s.
func rlkHeld(r resumeRow, s tmuxfix.SeedSession) apitest.HeldName {
	return apitest.HeldName{Name: r.Name, SessionID: s.ID, BeforeLaunch: true}
}

// The description builders the tables share.
var (
	rlkNoValidID = func(_ *killEnv, r resumeRow, s tmuxfix.SeedSession) apitest.DescCase {
		return apitest.DescHeldNoValidID(rlkHeld(r, s))
	}
	rlkDifferentID = func(_ *killEnv, r resumeRow, s tmuxfix.SeedSession) apitest.DescCase {
		return apitest.DescHeldDifferentID(rlkHeld(r, s))
	}
	rlkOtherStore = func(e *killEnv, r resumeRow, s tmuxfix.SeedSession) apitest.DescCase {
		return apitest.DescHeldOtherStore(rlkHeld(r, s), e.storeID)
	}
	rlkLeftover = func(_ *killEnv, r resumeRow, s tmuxfix.SeedSession) apitest.DescCase {
		return apitest.DescPreLaunchLeftover(r.ID, []apitest.DescSession{{Name: s.Name, ID: s.ID}})
	}
	rlkOwnOld = func(noSession bool) func(*killEnv, resumeRow, tmuxfix.SeedSession) apitest.DescCase {
		return func(e *killEnv, r resumeRow, _ tmuxfix.SeedSession) apitest.DescCase {
			return apitest.DescOwnOldSession(rlkStarting(e, r, noSession))
		}
	}
)

// TestResumeLookupAfterGuards: every guard, the control-character id
// included, refuses before the lookup, with no tmux call, although a session
// holds the recorded name.
func TestResumeLookupAfterGuards(t *testing.T) {
	held := func(t *testing.T, e *killEnv, spec killRowSpec) resumeRow {
		r := e.seedResumableRow(t, spec)
		e.seedHolder(t, r.killRow, holderNone)
		return r
	}
	live := func(state string) func(*testing.T, *killEnv) string {
		return func(t *testing.T, e *killEnv) string {
			return held(t, e, killRowSpec{State: state, Agent: agentGone, NoSession: true}).ID
		}
	}
	cases := []struct {
		name string
		seed func(*testing.T, *killEnv) string
		want error // nil: ErrInternal, which matches no sentinel
	}{
		{"unknown id", func(*testing.T, *killEnv) string { return "unknown-" + uuid.NewString()[:8] }, api.ErrSpawnNotFound},
		{"live row", live(store.StateWaiting), api.ErrSpawnNotResumable},
		{"pending row", live(store.StatePending), api.ErrSpawnNotResumable},
		{"no session id", func(t *testing.T, e *killEnv) string {
			r := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true})
			e.seedHolder(t, r, holderNone)
			return r.ID
		}, api.ErrNoSessionId},
		{"transcript missing", func(t *testing.T, e *killEnv) string {
			r := held(t, e, e.resumableSpec(rlkSettled(e), agentGone))
			if err := os.Remove(r.JSONLPath); err != nil {
				t.Fatalf("remove transcript: %v", err)
			}
			return r.ID
		}, api.ErrJsonlMissing},
		{"control character in the id", func(t *testing.T, e *killEnv) string {
			spec := e.resumableSpec(rlkSettled(e), agentGone)
			spec.ID = "legacy\x1b" + uuid.NewString()[:8]
			return held(t, e, spec).ID
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			id := tc.seed(t, e)

			_, err := e.resume(id)

			if name, _ := errnames.Classify(err); (tc.want == nil && (err == nil || name != "ErrInternal")) ||
				(tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("resume err = %v (%s); want %v", err, name, tc.want)
			}
			assertNoTmuxCalls(t, e.rec)
		})
	}
}

// TestResumeLookupOnRecordedSocketBeforePreTrustAndMove: the one lookup goes
// to the row's recorded socket while the row is still ended and untrusted;
// a holder of the name on another socket is not consulted.
func TestResumeLookupOnRecordedSocketBeforePreTrustAndMove(t *testing.T) {
	e := newKillEnv(t)
	recorded := filepath.Join(filepath.Dir(e.defaultSocket), "recorded-"+uuid.NewString()[:8])
	r := e.seedResumableRow(t, e.resumableSpec(rlkSettled(e), agentGone, apitest.WithTmuxSocket(recorded)))
	e.seedHolder(t, killRow{Name: r.Name, Socket: e.defaultSocket}, holderNone)
	before := e.snapshotResume(t, r)
	e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
		if st := e.columns(t, r.ID).State; st != store.StateEnded {
			t.Errorf("state when the lookup returned = %v; want ended (not yet moved)", st)
		}
		r.Trust.check(t, r.CWD, false, "when the lookup returned")
	})

	_, err := e.resume(r.ID)

	e.rlkAssertLaunched(t, before, err)
}

// TestResumeLookupRefusals: each name holder class with the agent dead,
// Leftover under any name, the process states, a row with no launch token,
// Ours under another name while another session holds the recorded name,
// and an Ours that resume does not adopt; each refusal writes nothing.
func TestResumeLookupRefusals(t *testing.T) {
	conflict, unresponsive := api.ErrTmuxSessionConflict, api.ErrTmuxUnresponsive
	fourFields := func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
		for _, sh := range tmuxfix.LabelShapes() {
			if sh.Name == "four-fields" {
				return e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: r.Name, Label: sh.Want, LabelSet: true})
			}
		}
		t.Fatal("no four-fields label shape in the catalogue")
		return tmuxfix.SeedSession{}
	}
	old := func(r resumeRow) tmux.Label { return r.old() }
	earlier := func(r resumeRow) tmux.Label { return tmuxfix.Valid(newToken(), r.ID, r.StoreID) }
	elsewhere := func(*killEnv) tmuxfix.RowSessionOption {
		return tmuxfix.WithRowSessionName("earlier-" + uuid.NewString()[:8])
	}
	renamed := func(age func(*killEnv) time.Duration) func(*testing.T, *killEnv, *resumeRow) tmuxfix.SeedSession {
		return func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
			e.seedHolder(t, r.killRow, holderForeign)
			return rlkOwn(func(*killEnv) tmuxfix.RowSessionOption {
				return tmuxfix.WithRowSessionName("renamed-" + uuid.NewString()[:8])
			}, func(e *killEnv) tmuxfix.RowSessionOption { return e.createdBefore(age(e)) })(t, e, r)
		}
	}
	young := func(*killEnv) time.Duration { return 0 }
	oldSession := func(e *killEnv) tmuxfix.RowSessionOption { return e.createdBefore(rlkSettled(e)) }
	for _, tc := range []rlkCase{
		{name: "held by another row's session", agent: agentGone, seed: rlkHolder(holderForeign), want: conflict, desc: rlkDifferentID},
		{name: "held by another store's session", agent: agentGone, seed: rlkHolder(holderOtherStore), want: conflict, desc: rlkOtherStore},
		{name: "held by another store's session with this row's id and token", agent: agentGone,
			seed: rlkHolder(holderOtherStoreOwn), want: conflict, desc: rlkOtherStore},
		{name: "held by an unlabelled session", agent: agentGone, seed: rlkHolder(holderNone), want: conflict, desc: rlkNoValidID},
		{name: "held by a malformed label", agent: agentGone, seed: rlkHolder(holderMalformed), want: conflict, desc: rlkNoValidID},
		{name: "held by a four-field label", agent: agentGone, seed: fourFields, want: conflict, desc: rlkNoValidID},
		{name: "leftover at the recorded name", agent: agentGone, want: conflict, desc: rlkLeftover,
			seed: func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
				return rlkOwn(rlkLabel(old, r))(t, e, r)
			}},
		{name: "leftover under another name", agent: agentAlive, want: conflict, desc: rlkLeftover,
			seed: func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
				return rlkOwn(rlkLabel(old, r), elsewhere)(t, e, r)
			}},
		{name: "agent uncheckable, held by another row's session", agent: agentUnreadable,
			seed: rlkHolder(holderForeign), want: conflict, desc: rlkDifferentID},
		{name: "no agent recorded, held by an unlabelled session", agent: agentNotRecorded,
			seed: rlkHolder(holderNone), want: conflict, desc: rlkNoValidID},
		{name: "agent running, inside the window, name held: the rule", agent: agentAlive, stopping: true,
			seed: rlkHolder(holderForeign), want: unresponsive,
			desc: func(e *killEnv, r resumeRow, _ tmuxfix.SeedSession) apitest.DescCase {
				return apitest.DescStillStopping(rlkStarting(e, r, true))
			}},
		{name: "agent running, past the window, name held: the rule", agent: agentAlive,
			seed: rlkHolder(holderForeign), want: conflict, desc: rlkOwnOld(true)},
		{name: "no launch token, this id's label: leftover", agent: agentGone, noToken: true, want: conflict, desc: rlkLeftover,
			seed: func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
				return rlkOwn(rlkLabel(earlier, r))(t, e, r)
			}},
		{name: "no launch token, unlabelled session at the name", agent: agentGone, noToken: true,
			seed: rlkHolder(holderNone), want: conflict, desc: rlkNoValidID},
		{name: "ours renamed and young, name held: still starting", agent: agentGone, seed: renamed(young), want: unresponsive,
			desc: func(e *killEnv, r resumeRow, _ tmuxfix.SeedSession) apitest.DescCase {
				return apitest.DescStillStarting(rlkStarting(e, r, false))
			}},
		{name: "ours renamed and old, name held: own old session", agent: agentGone, seed: renamed(rlkSettled),
			want: conflict, desc: rlkOwnOld(false)},
		{name: "ours with no recorded identity is not adopted", agent: agentGone, lostReply: true,
			seed: rlkOwn(oldSession), want: conflict, desc: rlkOwnOld(false)},
	} {
		t.Run(tc.name, func(t *testing.T) { rlkRun(t, tc) })
	}
}

// TestResumeLookupProceeds: no holder, another store's session with this
// row's id and token under another name, prefix neighbours, and an
// uncheckable or unrecorded agent with the name free launch, touching nothing.
func TestResumeLookupProceeds(t *testing.T) {
	for _, tc := range []rlkCase{
		{name: "name free", agent: agentGone},
		{name: "another store's session with this row's id and token elsewhere", agent: agentGone,
			seed: rlkHolder(holderOtherStoreElsewhere)},
		{name: "prefix neighbour: a longer name", agent: agentGone, seed: rlkHolder(holderPrefixNeighbour)},
		{name: "prefix neighbour: a prefix of the name", agent: agentGone,
			seed: rlkNamed(func(r resumeRow) string { return r.Name[:len(r.Name)-1] })},
		{name: "agent uncheckable, name free", agent: agentUnreadable},
		{name: "no agent recorded, name free", agent: agentNotRecorded},
	} {
		t.Run(tc.name, func(t *testing.T) { rlkRun(t, tc) })
	}
}

// TestResumeLookupDollarAndBackslashNames (AC-LKP-14): for each catalogued
// usable $ or \ name, a holder in either stored form blocks, both forms listed is
// ambiguous, a form matching neither is not held, and the own session is found by label.
func TestResumeLookupDollarAndBackslashNames(t *testing.T) {
	for _, n := range tmuxfix.StoredNames() {
		if !n.LabelByID || strings.ContainsAny(n.Raw, ".:") { // '.' and ':' names are unusable (Epic 19)
			continue
		}
		named := func(name string) func(*testing.T, *killEnv, *resumeRow) tmuxfix.SeedSession {
			return rlkNamed(func(resumeRow) string { return name })
		}
		cases := []rlkCase{
			{name: "held in the stored form", seed: named(n.Stored), want: api.ErrTmuxSessionConflict, desc: rlkNoValidID},
			{name: "no stored form matches", seed: named(`\` + n.Stored)},
			{name: "own session found by its label", want: api.ErrTmuxSessionConflict, desc: rlkOwnOld(false),
				seed: rlkOwn(func(e *killEnv) tmuxfix.RowSessionOption { return e.createdBefore(rlkSettled(e)) })},
		}
		if n.Raw != n.Stored {
			cases = append(cases,
				rlkCase{name: "held in the raw form", seed: named(n.Raw), want: api.ErrTmuxSessionConflict, desc: rlkNoValidID},
				rlkCase{name: "listed in both forms", want: api.ErrTmuxUnresponsive,
					seed: func(t *testing.T, e *killEnv, r *resumeRow) tmuxfix.SeedSession {
						named(n.Raw)(t, e, r)
						return named(n.Stored)(t, e, r)
					},
					desc: func(e *killEnv, r resumeRow, _ tmuxfix.SeedSession) apitest.DescCase {
						c := apitest.DescHeldAmbiguous(apitest.HeldName{Name: r.Name, BeforeLaunch: true})
						for _, s := range e.rec.Sessions(r.Socket) {
							if s.Name == n.Raw || s.Name == n.Stored {
								c.Forbid = append(c.Forbid, s.ID)
							}
						}
						return c
					}})
		}
		for _, tc := range cases {
			tc.agent, tc.opts = agentGone, []apitest.SpawnOption{apitest.WithTmuxSessionName(n.Raw)}
			t.Run(strings.ReplaceAll(n.Raw, "/", "_")+"/"+tc.name, func(t *testing.T) { rlkRun(t, tc) })
		}
	}
}
