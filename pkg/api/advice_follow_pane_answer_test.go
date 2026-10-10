package api_test

// advice_follow_pane_answer_test.go (b.146 step 2b, E10-E12): each refusal of
// a pane answer or of record-pane-answer that names a next step is followed
// literally: ErrClaimTooSoon's not_before, ErrPaneChanged's "read-pane again",
// and a pane answer whose sent write failed. ErrRelayFallenBack's advice (E7)
// is advice_follow_pane_test.go's. Fixture: pane_answer_fixture_test.go.

import (
	"errors"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestAdviceFollow_E10_ClaimTooSoonRetryAtNotBefore: E10 Client.RecordPaneAnswer's
// ErrClaimTooSoon "(retry at not_before; null only while the hook is seen
// running)" (b.146 rule 13, decision 5). The first record is refused with a
// not_before: on a request whose relay hook is gone it writes hook_gone_at and
// names 2 s after it; while a hook that cannot be checked may still answer (a
// request from v7 on before its settle instant, or one from before v7 inside
// its relay window), it writes none and names confirm_by plus 2 s. Retried
// just before, the same refusal comes back unchanged; retried at not_before,
// it is accepted.
func TestAdviceFollow_E10_ClaimTooSoonRetryAtNotBefore(t *testing.T) {
	t.Parallel()
	adviceAssertGoDoc(t, "record_pane_answer.go", "RecordPaneAnswer", "[ClaimTooSoonDetails] (retry at not_before; null only while the hook is seen running)")
	tok := storefix.TestRequestTokenA
	cases := []struct {
		name  string
		env   func(t *testing.T) *paEnv
		alive string // the refusal's hook_alive
		gone  bool   // the refusal writes hook_gone_at; not_before is 2 s after it, else confirm_by plus 2 s
	}{
		{"its relay hook gone", newPAEnv, "false", true},
		{"its relay hook cannot be checked", func(t *testing.T) *paEnv {
			p := newPAEnv(t)
			r7HookUnjudgeable(t, p)
			return p
		}, "null", false},
		{"recorded before v7, inside its relay window", func(t *testing.T) *paEnv {
			e := newKillEnv(t)
			p := &paEnv{killEnv: e, r: seedRelayRow(t, e, tok), now: time.Now().UTC().Truncate(time.Millisecond), self: paSender, sks: e.store}
			p.setPane("pane of an old request\n")
			return p
		}, "null", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := tc.env(t)
			want := p.now.Add(2 * time.Second)
			if !tc.gone {
				res, err := api.GetPermission(p.st, p.view(), api.GetPermissionParams{RequestToken: tok})
				if err != nil || res.Delivery != api.DeliveryNotConfirmed {
					t.Fatalf("get-permission = %+v, %v; want not_confirmed", res, err)
				}
				want = res.ConfirmBy.Add(2 * time.Second)
			}

			_, first := p.recordPaneAnswer(p.outside(tok, "deny"))

			assertOneSentinel(t, first, api.ErrClaimTooSoon)
			d, ok := api.ErrDetails(first).(api.ClaimTooSoonDetails)
			if !ok || d.NotBefore == nil || !d.NotBefore.Equal(want) || aliveIs(d.HookAlive) != tc.alive || (d.HookGoneAt != nil) != tc.gone {
				t.Fatalf("err_details = %s; want hook_alive %s, not_before %v, hook_gone_at set %v", jsonOf(t, api.ErrDetails(first)),
					tc.alive, want, tc.gone)
			}
			if pr := p.request(t, tok); pr.HookGoneAt.IsZero() == tc.gone || pr.PaneAnswer != store.PaneAnswerNone {
				t.Fatalf("request A = %+v; want hook_gone_at written %v, nothing recorded", pr, tc.gone)
			}
			p.assertNoTmuxCall(t)

			p.now = d.NotBefore.Add(-time.Millisecond)
			if _, again := p.recordPaneAnswer(p.outside(tok, "deny")); errText(again) != errText(first) {
				t.Errorf("retried before not_before: %v; want the same refusal %v", again, first)
			}

			p.now = *d.NotBefore
			if res, err := p.recordPaneAnswer(p.outside(tok, "deny")); err != nil || res.Decision == nil || *res.Decision != "deny" {
				t.Fatalf("retried at not_before = %+v, %v; want deny recorded", res, err)
			}
		})
	}
}

// TestAdviceFollow_E11_PaneChangedReadPaneAgain: E11 "read-pane again before
// any retry" (b.146 rules 7, 8, 13). A pane answer and record-pane-answer over
// a pane that changed since it was read are ErrPaneChanged, nothing sent or
// recorded; read-pane again, and the retry with its pane_sha256 is accepted.
func TestAdviceFollow_E11_PaneChangedReadPaneAgain(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	for _, verb := range []string{"send-keys", "record-pane-answer"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			p.paGoneLongAgo(t, tok)
			call := func(hash string) error {
				if verb == "send-keys" {
					params := p.answer(tok, "allow", "1")
					params.ExpectPaneSHA256 = hash
					return p.sendKeys(params)
				}
				params := p.outside(tok, "allow")
				params.ExpectPaneSHA256 = hash
				_, err := p.recordPaneAnswer(params)
				return err
			}
			read := p.hash()
			p.setPane("the pane changed\n")

			err := call(read)

			adviceAssertAdvice(t, err, api.ErrPaneChanged, "read-pane again before any retry")
			if pr := p.request(t, tok); pr.PaneAnswer != store.PaneAnswerNone {
				t.Fatalf("request A = %+v; want nothing recorded", pr)
			}
			p.assertNothingSent(t)

			res, err := p.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: p.r.ID})
			if err != nil {
				t.Fatalf("read-pane again: %v", err)
			}
			if err := call(res.PaneSHA256); err != nil {
				t.Fatalf("%s with the fresh read's pane_sha256: %v; want it accepted", verb, err)
			}
			if pr := p.request(t, tok); pr.AwaitsAnswer() {
				t.Errorf("request A = %+v; want it closed", pr)
			}
		})
	}
}

// TestAdviceFollow_E12_PaneAnswerNotRecordedRecordPaneAnswer: E12 "read-pane,
// then close it with record-pane-answer if the pane shows it answered"
// (b.146 rule 8). A pane answer whose key was sent but whose sent write failed
// leaves pane_answer intent, released; read-pane, then record-pane-answer
// with its pane_sha256 closes the request, and plain send-keys types again.
func TestAdviceFollow_E12_PaneAnswerNotRecordedRecordPaneAnswer(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	p := newPAEnv(t)
	p.sks = &paDyingStore{killStore: p.store, sentErr: errors.New("disk I/O error")}

	err := p.sendKeys(p.answer(tok, "allow", "1"))

	assertOneName(t, err, "")
	adviceAssertPhrase(t, err, "read-pane, then close it with record-pane-answer if the pane shows it answered")
	p.sks = p.store
	p.setPane("the dialog answered\n")
	p.now = p.now.Add(2 * time.Second) // the intent recorded hook_gone_at
	res, err := p.readPaneClient(t, api.ReadPaneParams{ClaudeInstanceID: p.r.ID})
	if err != nil {
		t.Fatalf("read-pane: %v", err)
	}
	params := p.outside(tok, "allow")
	params.ExpectPaneSHA256 = res.PaneSHA256
	if _, err := p.recordPaneAnswer(params); err != nil {
		t.Fatalf("record-pane-answer: %v; want the request closed", err)
	}
	if err := p.sendKeys(p.plain("next")); err != nil {
		t.Errorf("plain send-keys afterwards: %v; want it typed", err)
	}
}
