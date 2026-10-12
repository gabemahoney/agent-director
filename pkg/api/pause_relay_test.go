package api_test

// pause_relay_test.go — b.zdz: pause types C-u, /exit and Enter as a plain
// send-keys without a pane hash would, so on a waiting relay-on row it is held
// as one (send-keys' relay guard: ErrSendKeysWhileRelayed, then
// ErrRelayFallenBack, then ErrDialogMaybeOpen, with send-keys' err_details),
// typing nothing and starting no wait; a row with no request, or every request
// proven gone, pauses as before. Fixture: pane_answer_fixture_test.go.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestPauseHeldWhileSubagentRequestUnproven (b.zdz regression): a background
// subagent's request acked by its relay hook reads delivered but is not proven
// gone by the main agent's Stop, which moves the row to waiting. A plain
// send-keys is ErrDialogMaybeOpen, and so is pause, with the same description
// and err_details: no tmux call (no C-u), no wait, no row or trail write.
func TestPauseHeldWhileSubagentRequestUnproven(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	for _, ag := range paAgents {
		for _, en := range ptrEntries {
			t.Run(ag.label(en.name), func(t *testing.T) {
				t.Parallel()
				p := ag.subagent(t)
				r7Acked(t, p)
				p.mainAgentStop(t)
				if pr := p.request(t, tok); pr.AgentID != paSubagentID || pr.AwaitsAnswer() || pr.ProvenGone() {
					t.Fatalf("request A = %+v; want the subagent's, acked, not proven gone", pr)
				}
				sk := p.sendKeysRun(p.plain("hi"))
				want := assertDialogMaybeOpen(t, sk.err, tok, store.StateWaiting)
				if want.Delivery != api.DeliveryDelivered || want.PaneAnswer != "none" {
					t.Fatalf("send-keys err_details = %s; want A delivered, pane_answer none", jsonOf(t, want))
				}
				before, mark := p.columns(t, p.r.ID), trailMark(t)

				run := p.pauseRun(t, en.client)

				got := assertDialogMaybeOpen(t, run.err, tok, store.StateWaiting)
				if errText(run.err) != errText(sk.err) {
					t.Errorf("pause = %q; want send-keys' %q", errText(run.err), errText(sk.err))
				}
				if jsonOf(t, got) != jsonOf(t, want) {
					t.Errorf("pause err_details = %s; want send-keys' %s", jsonOf(t, got), jsonOf(t, want))
				}
				p.assertNoTmuxCall(t)
				// Client.Pause polls through its own store, which p.store does not count.
				if !en.client && p.store.stateReads != 0 {
					t.Errorf("state reads = %d; want no wait after the refusal", p.store.stateReads)
				}
				p.assertRowUnchanged(t, p.r.ID, before)
				ptrAssertOnlyDisagrees(t, mark, p.r.ID, true)
			})
		}
	}
}

// TestPauseHeldByPreV7RequestOnIdleRow (b.zdz, the ticket's second starting
// state; docs/migration-guide.md, "What the upgrade changes for an existing
// request"): request A, recorded before schema v7 (no relay hook identity, no
// settle instant, no tool_use_id, no agent_id) and allowed with decide, on a
// row its agent's Stop moved to waiting before the upgrade, so with no proof;
// an agent idle at its prompt sends no later Stop or idle-prompt
// Notification. Judged by time: inside A's relay window its relay hook may
// still be delivering the verdict, ErrSendKeysWhileRelayed ("retry later");
// from its confirm_by on, A reads delivered and is not proven gone,
// ErrDialogMaybeOpen, unproven since its decided_at. Either is plain
// send-keys' refusal, with its description and err_details: no tmux call (no
// C-u), no wait, no row or trail write.
func TestPauseHeldByPreV7RequestOnIdleRow(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	cases := []struct {
		name    string
		settled bool // pause at A's confirm_by, else at once
		want    error
	}{
		{"inside its relay window", false, api.ErrSendKeysWhileRelayed},
		{"past its relay window", true, api.ErrDialogMaybeOpen},
	}
	for _, en := range ptrEntries {
		for _, tc := range cases {
			t.Run(en.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				p := newPreV7IdlePAEnv(t, tok)
				pr := p.request(t, tok)
				if !pr.PreV7() || pr.AgentID != "" || pr.Decision != "allow" || pr.ProvenGone() {
					t.Fatalf("request A = %+v; want recorded before v7, the main agent's, allowed, not proven gone", pr)
				}
				res, err := api.GetPermission(p.st, p.view(), api.GetPermissionParams{RequestToken: tok})
				if err != nil || res.Delivery != api.DeliveryNotConfirmed {
					t.Fatalf("get-permission = %+v, %v; want not_confirmed inside its relay window", res, err)
				}
				if tc.settled {
					p.now = res.ConfirmBy
				}
				sk := p.sendKeysRun(p.plain("hi"))
				assertOneSentinel(t, sk.err, tc.want)
				before, mark := p.columns(t, p.r.ID), trailMark(t)

				run := p.pauseRun(t, en.client)

				assertOneSentinel(t, run.err, tc.want)
				want := errText(sk.err)
				if tc.settled {
					d := assertDialogMaybeOpen(t, run.err, tok, store.StateWaiting)
					if d.Delivery != api.DeliveryDelivered || d.UnprovenSince == nil || !d.UnprovenSince.Equal(pr.DecidedAt) {
						t.Errorf("err_details = %s; want A delivered, unproven since its decided_at %v", jsonOf(t, d), pr.DecidedAt)
					}
				} else {
					adviceAssertPhrase(t, sk.err, "request "+tok+
						" is recorded and its relay hook may still be delivering it; retry send-keys later")
					want = strings.TrimSuffix(want, "retry send-keys later") + "retry later"
				}
				if errText(run.err) != want {
					t.Errorf("pause = %q; want %q", errText(run.err), want)
				}
				if got, want := jsonOf(t, api.ErrDetails(run.err)), jsonOf(t, api.ErrDetails(sk.err)); got != want {
					t.Errorf("pause err_details = %s; want send-keys' %s", got, want)
				}
				p.assertNoTmuxCall(t)
				// Client.Pause polls through its own store, which p.store does not count.
				if !en.client && p.store.stateReads != 0 {
					t.Errorf("state reads = %d; want no wait after the refusal", p.store.stateReads)
				}
				p.assertRowUnchanged(t, p.r.ID, before)
				ptrAssertOnlyDisagrees(t, mark, p.r.ID, true)
			})
		}
	}
}

// newPreV7IdlePAEnv is a paEnv on a waiting relay-on row whose only request,
// token, was recorded before schema v7 (seedRelayRow's), allowed with decide,
// then the agent's Stop moved the row to waiting with no proof, as a hook from
// before v7 did: no request is proven gone. Its clock starts at the wall
// clock, which stamped the request's created_at.
func newPreV7IdlePAEnv(t *testing.T, token string) *paEnv {
	t.Helper()
	e := newKillEnv(t)
	r := seedRelayRow(t, e, token)
	if ok, err := e.st.DecidePermissionRequest(r.ID, token, "allow", "", store.WriterProcessDecide); err != nil || !ok {
		t.Fatalf("decide = %v, %v", ok, err)
	}
	if a := apitest.ApplyAgentHook(t, e.dbPath, r.ID, "Stop", ""); !a.Applied {
		t.Fatalf("the agent's Stop not applied: %s", a.Reason)
	}
	pauAssertState(t, e, r.ID, store.StateWaiting)
	return &paEnv{killEnv: e, r: r, now: time.Now().UTC().Truncate(time.Millisecond), self: paSender, sks: e.store}
}

// TestPauseRelayRefusalsAsPlainSendKeys (b.zdz; b.146 rule 7 and step 2c): on
// a waiting relay-on row whose requests are a background subagent's, pause is
// refused in send-keys' order, with send-keys' description (but "retry later"
// for its "retry send-keys later") and err_details, nothing typed and no wait:
// a relay hook that may still answer first (even beside a fallen-back or an
// unproven request), then a fallen-back request. The last, a request not
// proven gone, is TestPauseHeldWhileSubagentRequestUnproven's.
func TestPauseRelayRefusalsAsPlainSendKeys(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		steps   []r7Step
		want    error
		decided bool // A's verdict recorded: send-keys ends "retry send-keys later", pause "retry later"
	}{
		{"A's relay hook alive", []r7Step{r7HookAlive}, api.ErrSendKeysWhileRelayed, false},
		{"A acked, its relay hook still running", []r7Step{r7Acked, r7HookAlive}, api.ErrSendKeysWhileRelayed, true},
		{"A fallen back, B's relay hook alive", []r7Step{r7OtherAlive}, api.ErrSendKeysWhileRelayed, false},
		{"A fallen back", nil, api.ErrRelayFallenBack, false},
	}
	for _, ag := range paAgents {
		for _, en := range ptrEntries {
			for _, tc := range cases {
				t.Run(ag.label(en.name+"/"+tc.name), func(t *testing.T) {
					t.Parallel()
					p := ag.subagent(t)
					for _, step := range tc.steps {
						step(t, p)
					}
					p.mainAgentStop(t)
					before := p.columns(t, p.r.ID)

					run := p.pauseRun(t, en.client)
					sk := p.sendKeysRun(p.plain("hi"))

					assertOneSentinel(t, run.err, tc.want)
					want := errText(sk.err)
					if tc.decided {
						adviceAssertPhrase(t, sk.err, "; retry send-keys later")
						want = strings.TrimSuffix(want, "retry send-keys later") + "retry later"
					}
					if errText(run.err) != want {
						t.Errorf("pause = %v; want %q", run.err, want)
					}
					if strings.Contains(errText(run.err), "retry send-keys") {
						t.Errorf("pause = %v; want no send-keys named as the call to retry", run.err)
					}
					if got, want := jsonOf(t, api.ErrDetails(run.err)), jsonOf(t, api.ErrDetails(sk.err)); got != want {
						t.Errorf("pause err_details = %s; want send-keys' %s", got, want)
					}
					p.assertNoTmuxCall(t)
					// Client.Pause polls through its own store, which p.store does not count.
					if !en.client && p.store.stateReads != 0 {
						t.Errorf("state reads = %d; want no wait after the refusal", p.store.stateReads)
					}
					p.assertRowUnchanged(t, p.r.ID, before)
				})
			}
		}
	}
}

// TestPauseRelayFallenBackRecordsHookGone (b.zdz; b.146 rule 15): pause on a
// waiting row whose request A fell back records hook_gone_at on A, which had
// none, before it refuses, so ErrRelayFallenBack's err_details carry it.
func TestPauseRelayFallenBackRecordsHookGone(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newSubagentPAEnv(t)
	p.mainAgentStop(t)
	if pr := p.request(t, tok); !pr.HookGoneAt.IsZero() {
		t.Fatalf("A's hook_gone_at = %v before pause; want none", pr.HookGoneAt)
	}
	p.now = p.now.Add(time.Minute)

	run := p.pauseRun(t, false)

	adviceAssertAdvice(t, run.err, api.ErrRelayFallenBack, advRelayFallenBack)
	adviceAssertPhrase(t, run.err, "nothing was sent")
	m := assertFallenBackDetails(t, run.err, tok, store.StateWaiting)
	if m["hook_gone_at"] != jsonTime(t, p.now) {
		t.Errorf("err_details hook_gone_at = %v; want %s", m["hook_gone_at"], jsonTime(t, p.now))
	}
	if got := p.request(t, tok).HookGoneAt; !got.Equal(p.now) {
		t.Errorf("A's hook_gone_at = %v; want %v, recorded by the refusal", got, p.now)
	}
	p.assertNoTmuxCall(t)
}

// TestPauseNeverHeldNoRequest (b.zdz; step 2c's NeverHeldNoRequest for
// pause): a waiting relay-on row with no permission request, or whose every
// request is proven gone, pauses as before: C-u, /exit and Enter, then the
// wait sees the row end.
func TestPauseNeverHeldNoRequest(t *testing.T) {
	t.Parallel()
	t.Run("no request", func(t *testing.T) {
		t.Parallel()
		e := newKillEnv(t)
		r := e.seedRow(t, killRowSpec{RelayOn: true})
		e.endAfterEnter(t, r)

		if _, err := e.pause(pauseParams(r)); err != nil {
			t.Fatalf("Pause: %v; want /exit delivered and the row ended", err)
		}

		e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
		pauAssertState(t, e, r.ID, store.StateEnded)
	})
	toolRanA := func(t *testing.T, p *paEnv) { p.prove(t, toolRan(paToolUseID)) }
	for _, tc := range []struct {
		name  string
		env   func(*testing.T) *paEnv
		steps []r7Step
	}{
		{"a subagent's acked request, proven by its tool's PostToolUse", newSubagentPAEnv, []r7Step{r7Acked, toolRanA}},
		{"the main agent's acked request, proven by its Stop", newPAEnv, []r7Step{r7Acked}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := tc.env(t)
			for _, step := range tc.steps {
				step(t, p)
			}
			p.mainAgentStop(t)
			if pr := p.request(t, storefix.TestRequestTokenA); !pr.ProvenGone() {
				t.Fatalf("request A = %+v; want it proven gone", pr)
			}
			p.endAfterEnter(t, p.r)

			if run := p.pauseRun(t, false); run.err != nil {
				t.Fatalf("Pause: %v; want /exit delivered and the row ended", run.err)
			}

			p.assertExitDelivered(t, p.r.Socket, p.r.Spawn.Identity.PaneID)
			if p.store.stateReads == 0 {
				t.Error("state reads = 0; want the wait after /exit")
			}
			pauAssertState(t, p.killEnv, p.r.ID, store.StateEnded)
		})
	}
}

// TestPauseRelayGuardScope (b.zdz): with every read of the permission requests
// failing, pause reads them only on a waiting relay-on row (the store's error,
// nothing typed, no wait). A relay-off waiting row with an open request still
// pauses; on a relay-on row the state rules come first: an ended row is the
// no-op success, a working one ErrSpawnNotPausable (each state's rule is
// TestPauseGuards').
func TestPauseRelayGuardScope(t *testing.T) {
	t.Parallel()
	relayOn := func(state string) killRowSpec { return killRowSpec{State: state, RelayOn: true} }
	cases := []struct {
		name     string
		spec     killRowSpec
		want     error // nil: success
		delivers bool  // C-u, /exit and Enter typed, then the wait sees the row end
	}{
		{"relay off, waiting, an open request", killRowSpec{}, nil, true},
		{"relay on, ended", relayOn(store.StateEnded), nil, false},
		{"relay on, working", relayOn(store.StateWorking), api.ErrSpawnNotPausable, false},
		{"relay on, waiting", relayOn(store.StateWaiting), errInjectedStore, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, tc.spec)
			if tc.delivers {
				storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
				e.endAfterEnter(t, r)
			}
			e.store.failPermissionRequests(nil)

			_, err := e.pause(pauseParams(r))

			if !errors.Is(err, tc.want) {
				t.Fatalf("Pause = %v; want %v", err, tc.want)
			}
			if tc.delivers {
				e.assertExitDelivered(t, r.Socket, r.Spawn.Identity.PaneID)
				pauAssertState(t, e, r.ID, store.StateEnded)
				return
			}
			e.assertNoTmuxCall(t)
			if e.store.stateReads != 0 {
				t.Errorf("state reads = %d; want no wait", e.store.stateReads)
			}
		})
	}
}
