package api_test

// readpane_pane_test.go: which pane the pane verbs act on (SR-3.3, SR-3.4,
// SR-3.6, SR-3.7, SR-7.2, SR-7.5), one table per behaviour run on read-pane,
// send-keys and pause through paneVerb (pane_verb_fixture_test.go): the
// agent's pane by its recorded pane id and pid wherever it now is; after a
// lost create reply, the one pane carrying the row's token; never a session
// holding the name, a neighbour's, another store's or another server's; the
// failed pane listing; the unusable-name refusal against the state guards;
// and read-pane's lone leftover.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// paneCase is a shared-table case: the row's spec and the world setup builds
// around it, returning the verb's answer.
type paneCase struct {
	name  string
	spec  killRowSpec
	setup func(t *testing.T, e *killEnv, v paneVerb, r *killRow) paneWant
}

// runPaneCases runs every case on each of verbs in parallel, each on its own fixture.
func runPaneCases(t *testing.T, verbs []paneVerb, cases []paneCase, after func(*testing.T, *killEnv, paneVerb, killRow, verbRun[string])) {
	t.Helper()
	for _, v := range verbs {
		for _, tc := range cases {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedRow(t, tc.spec)
				want := tc.setup(t, e, v, &r)
				run := v.check(t, e, r, want)
				if after != nil {
					after(t, e, v, r, run)
				}
			})
		}
	}
}

// agentPaneWant is the row's recorded pane, acted on.
func agentPaneWant(r *killRow) paneWant { return paneWant{pane: r.Spawn.Identity.PaneID} }

// TestPaneVerbsAgentPane: each verb acts once on the agent's pane id wherever
// it now is; a missing or respawned pane is the pane-not-found conflict.
func TestPaneVerbsAgentPane(t *testing.T) {
	t.Parallel()
	noSession := killRowSpec{NoSession: true}
	runPaneCases(t, paneVerbs(), []paneCase{
		{name: "in its own session", setup: func(_ *testing.T, _ *killEnv, _ paneVerb, r *killRow) paneWant {
			return agentPaneWant(r)
		}},
		{name: "moved to another window", spec: noSession, setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
			moved := r.pane()
			moved.Window, moved.Index = 3, 2
			e.seedOurs(t, r, e.otherPane(), moved)
			return agentPaneWant(r)
		}},
		{name: "moved to another session", spec: noSession, setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
			e.seedOurs(t, r, e.otherPane())
			e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "elsewhere", Panes: []tmuxfix.SeedPane{r.pane()}})
			return agentPaneWant(r)
		}},
		{name: "shown in two sessions (grouped session or linked window)",
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				e.seedViewer(t, *r)
				return agentPaneWant(r)
			}},
		{name: "recorded pane id missing from the listing", spec: noSession,
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				e.seedOurs(t, r, e.otherPane())
				return paneNotFound(*r, r.Name, false)
			}},
		{name: "recorded pane missing, another pane carries the row's token", spec: noSession,
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				p := e.otherPane()
				p.AdPane = r.Token
				e.seedOurs(t, r, p)
				return paneNotFound(*r, r.Name, false)
			}},
		{name: "respawned pane: same id, another pid", spec: noSession,
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				p := r.pane()
				p.PID = e.newPID()
				e.seedOurs(t, r, p)
				return paneNotFound(*r, r.Name, false)
			}},
	}, nil)
}

// TestPaneVerbsLostReplyPane: a row recording no pane is acted on in the one
// pane carrying its token, at any index, never a teammate's; none or two is
// the lost-reply conflict. read-pane writes nothing and, re-issued, makes the
// same calls (the keys verbs' adoption write is TestKeysVerbsAdoptionWrite's).
func TestPaneVerbsLostReplyPane(t *testing.T) {
	t.Parallel()
	token := func(t *testing.T, _ *killEnv, _ paneVerb, r *killRow) paneWant {
		return paneWant{pane: labelledPane(t, r.Session, r.Token)}
	}
	runPaneCases(t, paneVerbs(), []paneCase{
		{name: "pane and server identity lost", spec: killRowSpec{NoPane: true, NoServerIdentity: true}, setup: token},
		{name: "only the pane lost", spec: killRowSpec{NoPane: true}, setup: token},
		{name: "a teammate split from the agent's pane", spec: killRowSpec{NoPane: true, NoServerIdentity: true, Teammates: 1},
			setup: token},
		{name: "a teammate at 0.0, the token pane at base-index 1", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, v paneVerb, r *killRow) paneWant {
				e.seedOurs(t, r, e.otherPane(), tmuxfix.SeedPane{Window: 1, Index: 1, AdPane: r.Token})
				return token(t, e, v, r)
			}},
		{name: "no pane carries the token", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				e.seedOurs(t, r, e.otherPane())
				return paneNotFound(*r, r.Name, true)
			}},
		{name: "two panes carry the token", spec: killRowSpec{NoPane: true, NoSession: true},
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				e.seedOurs(t, r, tmuxfix.SeedPane{AdPane: r.Token}, tmuxfix.SeedPane{Index: 1, AdPane: r.Token})
				return paneNotFound(*r, r.Name, true)
			}},
	}, func(t *testing.T, e *killEnv, v paneVerb, r killRow, first verbRun[string]) {
		if !v.keys {
			assertSameRun(t, runVerb(e, func() (string, error) { return v.run(t, e, r) }), first)
		}
	})
}

// TestPaneVerbsOtherStore (SR-3.4, AC-LKP-20): another store's session naming
// the row's id, with the row's token or another, is never acted on nor
// counted as a leftover: alone it is Gone; beside this store's Ours or lone
// leftover, those decide (read-pane reads the leftover, the keys verbs refuse it).
func TestPaneVerbsOtherStore(t *testing.T) {
	t.Parallel()
	var cases []paneCase
	for _, tk := range []struct {
		name  string
		token func(r killRow) string
	}{{"this row's token", func(r killRow) string { return r.Token }}, {"another token", func(killRow) string { return newToken() }}} {
		holding := func(t *testing.T, e *killEnv, r *killRow) {
			e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.otherStore(tk.token(*r)), true))
		}
		cases = append(cases,
			paneCase{name: tk.name + "/alone, holding the recorded name", spec: killRowSpec{NoSession: true},
				setup: func(t *testing.T, e *killEnv, v paneVerb, r *killRow) paneWant {
					holding(t, e, r)
					return paneGoneWant(v, *r)
				}},
			paneCase{name: tk.name + "/beside this store's Ours",
				setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
					token := tk.token(*r)
					e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "other-store-" + uuid.NewString()[:8],
						Label: r.otherStore(token), Panes: []tmuxfix.SeedPane{{AdPane: token}}})
					return agentPaneWant(r)
				}},
			paneCase{name: tk.name + "/beside this store's lone leftover", spec: killRowSpec{NoSession: true},
				setup: func(t *testing.T, e *killEnv, v paneVerb, r *killRow) paneWant {
					holding(t, e, r)
					lo := e.seedLeftover(t, *r, tmuxfix.OtherToken)
					if !v.keys {
						return paneWant{pane: lo.Panes[0].ID}
					}
					return paneWant{errName: "ErrTmuxSessionConflict", calls: paneLookupCalls,
						desc: func(pv apitest.PaneVerb) apitest.DescCase {
							return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: pv, InstanceID: r.ID,
								Sessions: []apitest.DescSession{{Name: lo.Name, ID: lo.ID}}})
						}}
				}})
	}
	runPaneCases(t, paneVerbs(), cases, nil)
}

// TestPaneVerbsRenamedSession: the renamed session's agent pane is acted on
// by id; a session now holding the recorded name never is.
func TestPaneVerbsRenamedSession(t *testing.T) {
	t.Parallel()
	renamed := func(holder func(r killRow) *tmuxfix.SeedSession) func(*testing.T, *killEnv, paneVerb, *killRow) paneWant {
		return func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
			e.seedSession(t, r, tmuxfix.WithRowSessionName("renamed-"+r.ID))
			if h := holder(*r); h != nil {
				e.seedOther(t, r.Socket, *h)
			}
			return agentPaneWant(r)
		}
	}
	noSession := killRowSpec{NoSession: true}
	runPaneCases(t, paneVerbs(), []paneCase{
		{name: "renamed, unlabelled session holds the name", spec: noSession,
			setup: renamed(func(r killRow) *tmuxfix.SeedSession { return &tmuxfix.SeedSession{Name: r.Name} })},
		{name: "renamed, another store's session holds the name", spec: noSession,
			setup: renamed(func(r killRow) *tmuxfix.SeedSession {
				return &tmuxfix.SeedSession{Name: r.Name, Label: r.otherStore(r.Token), Panes: []tmuxfix.SeedPane{{AdPane: r.Token}}}
			})},
	}, nil)
}

// TestPaneVerbsNeighbours (AC-LKP-01/02/03): a row with no session gets the
// gone error after the lookup alone and never touches a prefix-, name- or
// 8-character-id-sharing neighbour, whose id and token no description names.
func TestPaneVerbsNeighbours(t *testing.T) {
	t.Parallel()
	for _, v := range paneVerbs() {
		for _, tc := range []struct{ name, xID, yID, xName, yName string }{
			{"name prefix", "", "", "proj-abc", "proj-abc123"},
			{"same name, held by the other row's session", "", "", "proj-same", "proj-same"},
			{"ids share first 8 characters, same-named folders", "abcd1234-x-row", "abcd1234-y-row", "work", "work"},
		} {
			t.Run(v.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				x := e.seedRow(t, killRowSpec{ID: tc.xID, NoSession: true,
					Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.xName)}})
				y := e.seedRow(t, killRowSpec{ID: tc.yID, Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(tc.yName)}})
				want := paneGoneWant(v, x)
				want.forbid = []string{y.ID, y.Token}
				v.check(t, e, x, want)
			})
		}
	}
}

// TestPaneVerbsStoredNames (AC-LKP-09): a row whose recorded name holds $ or
// \ is found by its label under tmux's stored form and acted on by pane id.
func TestPaneVerbsStoredNames(t *testing.T) {
	t.Parallel()
	for _, v := range paneVerbs() {
		for _, n := range tmuxfix.StoredNames() {
			if !n.LabelByID || strings.ContainsAny(n.Raw, ".:") { // '.' and ':' names are unusable (Epic 19)
				continue
			}
			t.Run(v.name+"/"+n.Raw, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r := e.seedRow(t, killRowSpec{Opts: []apitest.SpawnOption{apitest.WithTmuxSessionName(n.Raw)}})
				if r.Session.Name != n.Stored {
					t.Fatalf("seeded session name %q; want the stored form %q", r.Session.Name, n.Stored)
				}
				v.check(t, e, r, agentPaneWant(&r))
			})
		}
	}
}

// TestPaneVerbsRecordedSocket (AC-LKP-19): with TMUX and TMUX_TMPDIR naming
// another server that holds the row's label, every call names the row's
// recorded socket.
func TestPaneVerbsRecordedSocket(t *testing.T) {
	// Serial: it sets TMUX, TMUX_TMPDIR with t.Setenv.
	for _, v := range paneVerbs() {
		t.Run(v.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			elsewhere := filepath.Join(t.TempDir(), "elsewhere") // no commas: TMUX's socket field ends at one
			t.Setenv("TMUX", elsewhere+",4242,0")
			t.Setenv("TMUX_TMPDIR", t.TempDir())
			e.seedOther(t, elsewhere, tmuxfix.SeedSession{Name: r.Name, Label: r.current(),
				Panes: []tmuxfix.SeedPane{{AdPane: r.Token}}})
			v.check(t, e, r, agentPaneWant(&r))
			for _, c := range e.rec.SocketCalls() {
				if c.Socket != r.Socket {
					t.Errorf("%v on socket %q; want the recorded %q", c.Call, c.Socket, r.Socket)
				}
			}
		})
	}
}

// TestPaneVerbsListingFails (AC-PANE-03): a pane listing that times out or is
// unrecognised is ErrTmuxUnresponsive naming the listing, one tmux cannot run
// or reach ErrTmuxNotAvailable, and one finding the server exited the gone
// error; nothing is acted on.
func TestPaneVerbsListingFails(t *testing.T) {
	t.Parallel()
	const firstLine = "list-panes: unexpected reply"
	listing := func(s tmuxfix.Script, errName string,
		desc func(e *killEnv, r killRow) apitest.DescCase) func(*testing.T, *killEnv, paneVerb, *killRow) paneWant {
		return func(_ *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
			e.rec.Script(r.Socket, s, tmux.CallListPanes)
			return paneWant{errName: errName, calls: paneListedCalls,
				desc: func(apitest.PaneVerb) apitest.DescCase { return desc(e, *r) }}
		}
	}
	runPaneCases(t, paneVerbs(), []paneCase{
		{name: "timeout", setup: listing(tmuxfix.Script{Failure: tmux.FailTimeout}, "ErrTmuxUnresponsive",
			func(e *killEnv, _ killRow) apitest.DescCase {
				return apitest.DescCallTimeout(tmux.CallListPanes, e.cfg.EffectiveQueryTimeout())
			})},
		{name: "unrecognised reply", setup: listing(tmuxfix.Script{Failure: tmux.FailUnrecognized, FirstLine: firstLine,
			ExitStatus: 1, HadStdout: true}, "ErrTmuxUnresponsive",
			func(*killEnv, killRow) apitest.DescCase {
				return apitest.DescUnrecognisedReply(tmux.CallListPanes, firstLine)
			})},
		{name: "tmux not run", setup: listing(tmuxfix.Script{Failure: tmux.FailUnavailable}, "ErrTmuxNotAvailable",
			func(*killEnv, killRow) apitest.DescCase { return apitest.DescTmuxNotRun() })},
		{name: "socket permission", setup: listing(tmuxfix.Script{Failure: tmux.FailSocketDenied}, "ErrTmuxNotAvailable",
			func(_ *killEnv, r killRow) apitest.DescCase { return apitest.DescSocketPermission(r.Socket) })},
		{name: "no server: it exited after the lookup", setup: func(_ *testing.T, e *killEnv, v paneVerb, r *killRow) paneWant {
			e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				e.rec.StopServer(r.Socket).SetNoServerFailure(r.Socket, tmux.FailNoServer)
				e.syncServers()
			})
			want := paneGoneWant(v, *r)
			want.calls = paneListedCalls
			return want
		}},
	}, nil)
}

// TestReadPaneLeftover: a lone leftover's pane carrying its label's token is
// read on a pending, live or finished row, wherever it is; none such, or two
// leftovers, refuse.
func TestReadPaneLeftover(t *testing.T) {
	t.Parallel()
	lone := func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
		e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
		return paneWant{pane: labelledPane(t, r.Session, tmuxfix.OtherToken)}
	}
	finished := killRowSpec{State: store.StateEnded, NoSession: true, Opts: []apitest.SpawnOption{
		apitest.WithEndedAt(killClockStart.Add(-(defWindow + time.Second)))}}
	noSession := killRowSpec{NoSession: true}
	runPaneCases(t, []paneVerb{readPaneVerb()}, []paneCase{
		{name: "pending row, launching process stopped before its create", spec: killPendSpec(store.StatePending), setup: lone},
		{name: "live row", spec: noSession, setup: lone},
		{name: "finished row", spec: finished, setup: lone},
		{name: "its pane moved to window 2, pane 1", spec: noSession,
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				e.ensureServer(r)
				s := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "leftover-moved", Label: r.old(),
					Panes: []tmuxfix.SeedPane{e.otherPane(), {Window: 2, Index: 1, AdPane: tmuxfix.OtherToken}}})
				return paneWant{pane: labelledPane(t, s, tmuxfix.OtherToken)}
			}},
		{name: "no pane carries its token", spec: noSession,
			setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
				e.ensureServer(r)
				s := e.seedOther(t, r.Socket, tmuxfix.SeedSession{Name: "leftover-bare", Label: r.old(),
					Panes: []tmuxfix.SeedPane{e.otherPane()}})
				return paneNotFound(*r, s.Name, false)
			}},
		{name: "two leftovers", spec: noSession, setup: func(t *testing.T, e *killEnv, _ paneVerb, r *killRow) paneWant {
			e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
			second := e.seedLeftover(t, *r, newToken())
			return paneWant{errName: "ErrTmuxSessionConflict", calls: paneLookupCalls, desc: func(v apitest.PaneVerb) apitest.DescCase {
				return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: v, InstanceID: r.ID,
					Sessions: []apitest.DescSession{{Name: r.Session.Name, ID: r.Session.ID}, {Name: second.Name, ID: second.ID}}})
			}}
		}},
	}, nil)
}

// TestPaneVerbsUnusableName (SR-3.2, AC-LKP-10, FR1 C7(k)): an unusable
// recorded name is ErrInternal quoting it in any state for read-pane (no
// state guard), and after send-keys' state and relay guards, whose answers
// win; no tmux call, the row unchanged. Each verb's waiting row with every
// unusable name, send-keys' pending row and pause's guarded rows are the call
// table's (lookup_calltable_unusable_test.go).
func TestPaneVerbsUnusableName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		verb    string
		state   string
		allow   bool
		relay   bool // relay_mode on, one request still in its window
		fixture string
		want    string
	}{
		{"read-pane", store.StatePending, false, false, "pre-b.gqe default name", "ErrInternal"},
		{"read-pane", store.StateWorking, false, false, "newline", "ErrInternal"},
		{"read-pane", store.StateEnded, false, false, "colon", "ErrInternal"},
		{"read-pane", store.StateMissing, false, false, "invalid UTF-8", "ErrInternal"},
		{"send-keys", store.StateWorking, true, false, "newline", "ErrInternal"},
		{"send-keys", store.StatePending, false, false, "pre-b.gqe default name", "ErrSpawnNotInteractive"},
		{"send-keys", store.StateEnded, false, false, "dot and newline", "ErrSpawnNotInteractive"},
		{"send-keys", store.StateMissing, true, false, "colon", "ErrSpawnNotInteractive"},
		{"send-keys", store.StateCheckPermission, false, true, "pre-b.gqe default name", "ErrSendKeysWhileRelayed"},
	}
	for _, tc := range cases {
		t.Run(tc.verb+"/"+tc.state+"/"+tc.fixture, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			f := unusableFixture(t, tc.fixture)
			spec := killRowSpec{State: tc.state, RelayOn: tc.relay}
			if tc.state == store.StatePending {
				spec = e.pendingSpec(pendingOurs)
			}
			r := e.seedUnusableRow(t, spec, f)
			if tc.relay {
				storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
			}
			before := e.columns(t, r.ID)

			var err error
			if tc.verb == "read-pane" {
				var res api.ReadPaneResult
				if res, err = e.readPane(api.ReadPaneParams{ClaudeInstanceID: r.ID}); res.Pane != "" {
					t.Errorf("Pane = %q; want none", res.Pane)
				}
			} else { // the requests are stored at wall-clock time, so the guard is judged against it
				_, err = e.sendKeysAt(sendKeysWindow(), time.Now(), api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "1",
					AllowPending: tc.allow})
			}

			assertOneName(t, err, tc.want)
			if tc.want == "ErrInternal" && err != nil {
				apitest.AssertDescription(t, err.Error(), f.desc, r.Token)
			}
			e.assertNoTmuxCall(t)
			e.assertRowUnchanged(t, r.ID, before)
		})
	}
}

// TestPaneVerbsUnknownIDAndClosedClient: an unknown id (beside a row whose
// recorded name cannot be used) is ErrSpawnNotFound for each pane verb; a
// closed Client is ErrClientClosed for kill and the pane verbs and writes no
// trail record. Neither makes a tmux call. Kill's unknown id is
// TestKillNonLookupRows'.
func TestPaneVerbsUnknownIDAndClosedClient(t *testing.T) {
	t.Parallel()
	for _, v := range paneVerbs() {
		t.Run(v.name+"/unknown id", func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			e.seedUnusableRow(t, killRowSpec{}, unusableFixture(t, "pre-b.gqe default name"))
			_, err := v.run(t, e, killRow{ID: "absent-" + uuid.NewString()[:8]})
			assertOneName(t, err, "ErrSpawnNotFound")
			e.assertNoTmuxCall(t)
		})
	}
	for _, verb := range []string{"kill", "read-pane", "send-keys", "pause"} {
		t.Run(verb+"/closed Client", func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{})
			c, _ := e.client(t)
			if err := c.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			mark := trailMark(t)
			var err error
			switch verb {
			case "kill":
				_, err = c.Kill(api.KillParams{ClaudeInstanceID: r.ID})
			case "read-pane":
				_, err = c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: r.ID})
			case "send-keys":
				_, err = c.SendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hi"})
			default:
				_, err = c.Pause(context.Background(), pauseParams(r))
			}
			if !errors.Is(err, api.ErrClientClosed) {
				t.Errorf("%s on a closed Client: err = %v; want ErrClientClosed", verb, err)
			}
			assertNoTrailSince(t, mark, r.ID)
			e.assertNoTmuxCall(t)
		})
	}
}
