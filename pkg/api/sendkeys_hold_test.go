package api_test

// sendkeys_hold_test.go — b.146 step 2c at send-keys: a plain send-keys is
// held (ErrDialogMaybeOpen) while any permission request of the Spawn is not
// proven gone by Claude Code's hooks, whatever agent-director's own records
// say of it; a matching pane hash passes the hold and no other refusal; a
// Spawn with no request is never held. Proofs are recorded through the store
// as the agent's hooks record them (proveGone). Fixture:
// pane_answer_fixture_test.go.

import (
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// holdClosure is one way agent-director's records close request A: what its
// delivery facts then read and since when they read closed.
type holdClosure struct {
	name     string
	close    func(t *testing.T, p *paEnv)
	delivery string
	pane     string
	since    func(p *paEnv, pr store.PermissionRow) time.Time
}

// holdClosures are the three records that are no proof (b.146 step 2c rule 1):
// the relay hook's ack, a pane answer sent through send-keys, and
// record-pane-answer.
var holdClosures = []holdClosure{
	{"acked by its relay hook (delivered)", r7Acked, api.DeliveryDelivered, "none",
		func(p *paEnv, _ store.PermissionRow) time.Time { return p.now }},
	{"answered at the pane (pane_answer sent)", func(t *testing.T, p *paEnv) {
		if err := p.sendKeys(p.answer(storefix.TestRequestTokenA, "allow", "1")); err != nil {
			t.Fatalf("pane answer: %v", err)
		}
	}, api.DeliveryFallenBack, "sent", func(_ *paEnv, pr store.PermissionRow) time.Time { return pr.DecidedAt }},
	{"recorded with record-pane-answer (pane_answer outside)", func(t *testing.T, p *paEnv) {
		p.paGoneLongAgo(t, storefix.TestRequestTokenA)
		if _, err := p.recordPaneAnswer(p.outside(storefix.TestRequestTokenA, "allow")); err != nil {
			t.Fatalf("record-pane-answer: %v", err)
		}
	}, api.DeliveryFallenBack, "outside", func(_ *paEnv, pr store.PermissionRow) time.Time { return pr.DecidedAt }},
}

// TestPlainSendKeysHeldUntilProven (b.146 step 2c rules 1, 2): once request A
// reads closed in agent-director's records (acked, answered at the pane, or
// recorded with record-pane-answer) it is still not proven gone, so a plain
// send-keys is ErrDialogMaybeOpen naming it with its delivery facts and since
// when it reads closed, nothing sent; a PostToolUse with another tool_use_id
// proves nothing; its own tool's PostToolUse, or the main agent's Stop, proves
// it gone and the same send-keys then types.
func TestPlainSendKeysHeldUntilProven(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	proofs := []struct {
		name  string
		proof store.RequestProof
	}{
		{"its tool's PostToolUse", toolRan(paToolUseID)},
		{"the main agent's Stop", turnEnd},
	}
	for _, c := range holdClosures {
		for _, pf := range proofs {
			t.Run(c.name+"/"+pf.name, func(t *testing.T) {
				t.Parallel()
				p := newPAEnv(t)
				c.close(t, p)
				since := c.since(p, p.request(t, tok))
				p.now = p.now.Add(time.Minute)
				if n := p.prove(t, toolRan("toolu_01OTHER")); n != 0 {
					t.Fatalf("another tool's PostToolUse proved %d; want none", n)
				}
				row, err := p.st.GetSpawn(p.r.ID)
				if err != nil {
					t.Fatalf("GetSpawn: %v", err)
				}

				held := p.sendKeysRun(p.plain("hi"))

				d := assertDialogMaybeOpen(t, held.err, tok, row.State)
				if len(held.calls) != 0 {
					t.Errorf("calls = %v; want none, nothing sent", paKinds(held.calls))
				}
				if d.Delivery != c.delivery || d.PaneAnswer != c.pane || d.UnprovenSince == nil || !d.UnprovenSince.Equal(since) {
					t.Errorf("err_details = %s; want delivery %s, pane_answer %s, unproven_since %v", jsonOf(t, d), c.delivery, c.pane, since)
				}
				if pr := p.request(t, tok); pr.AwaitsAnswer() || pr.ProvenGone() {
					t.Fatalf("request A = %+v; want closed in agent-director's records, not proven gone", pr)
				}

				if n := p.prove(t, pf.proof); n != 1 {
					t.Fatalf("%s proved %d; want request A", pf.name, n)
				}
				run := p.sendKeysRun(p.plain("hi"))
				if run.err != nil || !slices.Equal(paKinds(run.calls), paneSendCalls) {
					t.Fatalf("plain send-keys once proven = %v, calls %v; want it typed", run.err, paKinds(run.calls))
				}
				if pr := p.request(t, tok); pr.ProvenGoneHow != pf.proof.How {
					t.Errorf("request A proven_gone_how = %q; want %q", pr.ProvenGoneHow, pf.proof.How)
				}
			})
		}
	}
}

// TestDialogMaybeOpenNamesTheOldest (b.146 step 2c rule 2): with A and B
// acked and C acked and proven, ErrDialogMaybeOpen names A, the oldest, with
// B, and not C, in unproven_requests; once A is proven it names B.
func TestDialogMaybeOpenNamesTheOldest(t *testing.T) {
	t.Parallel()
	tokA, tokB, tokC := storefix.TestRequestTokenA, storefix.TestRequestTokenB, storefix.TestRequestTokenC
	p := newPAEnv(t)
	p.seedRequest(t, tokB, "toolu_01B", relayEnvHook)
	p.seedRequest(t, tokC, "toolu_01C", relayEnvHook)
	for _, tok := range []string{tokA, tokB, tokC} {
		if ok, err := p.st.DecideRelayRequest(p.r.ID, tok, "allow", "", store.WriterProcessDecide, time.Time{}, store.DefaultLockWait); err != nil || !ok {
			t.Fatalf("decide %s = %v, %v", tok, ok, err)
		}
		if _, _, ok, err := p.st.AckRelayDecision(p.r.ID, tok, p.now, store.DefaultLockWait, nil); err != nil || !ok {
			t.Fatalf("ack %s = %v, %v", tok, ok, err)
		}
	}
	p.prove(t, toolRan("toolu_01C"))

	assertDialogMaybeOpen(t, p.sendKeys(p.plain("hi")), tokA, store.StateCheckPermission, tokB)
	adviceAssertPhrase(t, p.sendKeys(p.plain("hi")), "1 other request(s) of the spawn are not proven gone")

	p.prove(t, toolRan(paToolUseID))
	assertDialogMaybeOpen(t, p.sendKeys(p.plain("hi")), tokB, store.StateCheckPermission)
}

// TestPlainSendKeysHashPassesTheHold (b.146 step 2c rule 3): a plain send-keys
// whose expect_pane_sha256 matches the pane captured now is not held by an
// unproven request (it types; the request stays unproven); a stale hash is
// ErrPaneChanged with nothing sent; the hash passes no other refusal: a relay
// hook that may still answer is ErrSendKeysWhileRelayed and a fallen-back
// request ErrRelayFallenBack, before any tmux call.
func TestPlainSendKeysHashPassesTheHold(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		steps []r7Step
		hash  string // "pane": the pane's hash; "stale": an earlier pane's; "": none
		want  error
		calls []tmux.Call
	}{
		{"A acked, unproven, no hash", []r7Step{r7Acked}, "", api.ErrDialogMaybeOpen, nil},
		{"A acked, unproven, the pane's hash", []r7Step{r7Acked}, "pane", nil,
			[]tmux.Call{tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture, tmux.CallSendText, tmux.CallSendEnter}},
		{"A acked, unproven, a stale hash", []r7Step{r7Acked}, "stale", api.ErrPaneChanged, paneReadCalls},
		{"A's relay hook alive, the pane's hash", []r7Step{r7HookAlive}, "pane", api.ErrSendKeysWhileRelayed, nil},
		{"A fallen back, the pane's hash", nil, "pane", api.ErrRelayFallenBack, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			for _, step := range tc.steps {
				step(t, p)
			}
			params := p.plain("hi")
			switch tc.hash {
			case "pane":
				params.ExpectPaneSHA256 = p.hash()
			case "stale":
				params.ExpectPaneSHA256 = p.hash()
				p.setPane("the pane changed\n")
			}

			run := p.sendKeysRun(params)

			if tc.want == nil {
				if run.err != nil {
					t.Fatalf("send-keys = %v; want it typed", run.err)
				}
			} else {
				assertOneSentinel(t, run.err, tc.want)
			}
			if !slices.Equal(paKinds(run.calls), tc.calls) {
				t.Errorf("calls = %v; want %v", paKinds(run.calls), tc.calls)
			}
			if pr := p.request(t, storefix.TestRequestTokenA); pr.ProvenGone() {
				t.Errorf("request A proven gone at %v; want the hash to prove nothing", pr.ProvenGoneAt)
			}
		})
	}
}

// TestNoPermissionRequestNeverHeld (b.146 step 2c rule 5, NeverHeldNoRequest):
// a relay-on row with no permission request is never held, in any live state;
// nor is one whose every request is proven gone.
func TestNoPermissionRequestNeverHeld(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StateWaiting, store.StateWorking, store.StateAskUser, store.StateCheckPermission} {
		t.Run("no request, "+state, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: state, RelayOn: true})

			run := runVerb(e, func() (api.SendKeysResult, error) {
				return e.sendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "go"})
			})

			if run.err != nil || !slices.Equal(paKinds(run.calls), paneSendCalls) {
				t.Errorf("send-keys = %v, calls %v; want it typed", run.err, paKinds(run.calls))
			}
		})
	}
	t.Run("every request proven gone", func(t *testing.T) {
		t.Parallel()
		p := newPAEnv(t)
		r7Acked(t, p)
		p.prove(t, turnEnd)
		if run := p.sendKeysRun(p.plain("go")); run.err != nil || !slices.Equal(paKinds(run.calls), paneSendCalls) {
			t.Errorf("send-keys = %v, calls %v; want it typed", run.err, paKinds(run.calls))
		}
	})
}
