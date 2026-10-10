package api_test

// record_pane_answer_test.go — record-pane-answer (b.146 rule 13, decision 6
// C): a fallen-back request recorded answered outside agent-director, with
// the caller's claim, after a capture and hash check of the pane and nothing
// typed; its refusals before any tmux call; and a request recorded before
// schema v7 closed by it. Its timing (ErrClaimTooSoon) and hash advice are
// advice_follow_pane_answer_test.go's. Fixture: pane_answer_fixture_test.go.

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// paGoneLongAgo records request token's hook_gone_at a minute before p.now.
func (p *paEnv) paGoneLongAgo(t *testing.T, token string) {
	t.Helper()
	if _, err := p.st.RecordHookGone(p.now.Add(-time.Minute), store.DefaultLockWait, p.request(t, token).RequestID); err != nil {
		t.Fatalf("RecordHookGone: %v", err)
	}
}

// TestRecordPaneAnswerClaims (b.146 rule 13): with request A fallen back and
// its hook gone long enough, record-pane-answer captures the pane (n_lines
// lines, ANSI stripped), types nothing, and records pane_answer outside,
// pane_as and decision the claim (null for unknown), decision_reason
// pane_outside; plain send-keys then types, and a second record is
// ErrAlreadyDecided.
func TestRecordPaneAnswerClaims(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	for _, tc := range []struct {
		as       string
		decision *string
		nLines   int
	}{{"allow", ptr("allow"), 0}, {"deny", ptr("deny"), 7}, {"unknown", nil, 0}} {
		t.Run(tc.as, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			p.paGoneLongAgo(t, tok)
			params := p.outside(tok, tc.as)
			params.NLines = tc.nLines

			run := runVerb(p.killEnv, func() (api.RecordPaneAnswerResult, error) { return p.recordPaneAnswer(params) })

			if run.err != nil {
				t.Fatalf("record-pane-answer: %v", run.err)
			}
			want := api.RecordPaneAnswerResult{RequestToken: tok, PaneAnswer: "outside", PaneAs: tc.as, Decision: tc.decision,
				DecisionReason: "pane_outside"}
			if jsonOf(t, run.res) != jsonOf(t, want) {
				t.Errorf("result = %s; want %s", jsonOf(t, run.res), jsonOf(t, want))
			}
			wantLines := tc.nLines
			if wantLines == 0 {
				wantLines = api.DefaultReadPaneLines
			}
			if !slices.Equal(paKinds(run.calls), paneReadCalls) || run.calls[2].NLines != wantLines || run.calls[2].ANSI {
				t.Errorf("calls = %+v; want the lookup, the listing and one capture of %d lines, no escapes, nothing typed", run.calls, wantLines)
			}
			pr := p.request(t, tok)
			if pr.PaneAnswer != store.PaneAnswerOutside || pr.PaneAs != tc.as || pr.DecisionReason != store.DecisionReasonPaneOutside ||
				(tc.decision == nil) != (pr.Decision == "") || pr.AwaitsAnswer() {
				t.Errorf("request A = %+v; want outside, pane_as %s, pane_outside, closed", pr, tc.as)
			}
			if err := p.sendKeys(p.plain("next")); err != nil {
				t.Errorf("plain send-keys after the record: %v; want it typed", err)
			}
			_, err := p.recordPaneAnswer(p.outside(tok, "deny"))
			assertOneSentinel(t, err, store.ErrAlreadyDecided)
		})
	}
}

// r7HookUnjudgeable: request A's relay hook cannot be checked (its /proc
// entry unreadable).
var r7HookUnjudgeable r7Step = func(_ *testing.T, p *paEnv) { p.pc.Set(relayEnvHook.PID, procfix.Unreadable()) }

// rpaUnjudgeable names TestRecordPaneAnswerRefusals' case whose relay hook
// cannot be checked.
const rpaUnjudgeable = "its relay hook cannot be checked"

// TestRecordPaneAnswerRefusals (b.146 rule 13): each refusal records nothing
// and makes no tmux call: the params' shape (ErrInvalidFlags), an unknown
// token, a relay hook that may still answer (ErrClaimTooSoon, no hook_gone_at
// written: not_before null while the hook runs, its confirm_by plus 2 s when
// it cannot be checked), a request acked or answered outside, and a request of
// a row that ended or went missing.
func TestRecordPaneAnswerRefusals(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	bad := func(edit func(*api.RecordPaneAnswerParams)) func(p *paEnv) api.RecordPaneAnswerParams {
		return func(p *paEnv) api.RecordPaneAnswerParams {
			params := p.outside(tok, "allow")
			edit(&params)
			return params
		}
	}
	cases := []struct {
		name   string
		steps  []r7Step
		params func(p *paEnv) api.RecordPaneAnswerParams // nil: p.outside(A, "allow")
		want   error
		phrase string
	}{
		{"as maybe", nil, bad(func(q *api.RecordPaneAnswerParams) { q.As = "maybe" }), api.ErrInvalidFlags, "as must be allow, deny or unknown"},
		{"no as", nil, bad(func(q *api.RecordPaneAnswerParams) { q.As = "" }), api.ErrInvalidFlags, "as must be allow, deny or unknown"},
		{"no request_token", nil, bad(func(q *api.RecordPaneAnswerParams) { q.RequestToken = "" }), api.ErrInvalidFlags, "request_token is required"},
		{"no expect_pane_sha256", nil, bad(func(q *api.RecordPaneAnswerParams) { q.ExpectPaneSHA256 = "" }), api.ErrInvalidFlags, "expect_pane_sha256"},
		{"a malformed expect_pane_sha256", nil, bad(func(q *api.RecordPaneAnswerParams) { q.ExpectPaneSHA256 = strings.Repeat("g", 64) }),
			api.ErrInvalidFlags, "expect_pane_sha256"},
		{"a negative n_lines", nil, bad(func(q *api.RecordPaneAnswerParams) { q.NLines = -1 }), api.ErrInvalidFlags, "n_lines"},
		{"an unknown token", nil, bad(func(q *api.RecordPaneAnswerParams) { q.RequestToken = "dddddddd-dddd-4ddd-addd-dddddddddddd" }),
			store.ErrPermissionRequestNotFound, ""},
		{"its relay hook alive", []r7Step{r7HookAlive}, nil, api.ErrClaimTooSoon, "its relay hook runs and may still answer it"},
		{rpaUnjudgeable, []r7Step{r7HookUnjudgeable}, nil, api.ErrClaimTooSoon,
			"its relay hook cannot be checked and may still answer it until its confirm_by"},
		{"acked", []r7Step{r7Acked}, nil, store.ErrAlreadyDecided, "already decided"},
		{"answered outside already", []r7Step{r7Outside}, nil, store.ErrAlreadyDecided, `pane_answer "outside"`},
		{"the row ended", []r7Step{r7End}, nil, store.ErrNoOpenPermissionRequest, "the spawn is ended"},
		{"the row ended, then resumed", []r7Step{r7End, r7Resume}, nil, store.ErrNoOpenPermissionRequest,
			"its spawn ended, or find-missing marked it missing, before the request's relay hook delivered a verdict"},
		{"the row missing", []r7Step{r7Mark}, nil, store.ErrNoOpenPermissionRequest, "the spawn is missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			for _, step := range tc.steps {
				step(t, p)
			}
			before := p.request(t, tok)
			params := p.outside(tok, "allow")
			if tc.params != nil {
				params = tc.params(p)
			}

			_, err := p.recordPaneAnswer(params)

			assertOneSentinel(t, err, tc.want)
			if tc.phrase != "" {
				adviceAssertPhrase(t, err, tc.phrase)
			}
			if tc.want != store.ErrPermissionRequestNotFound {
				adviceAssertPhrase(t, err, "nothing was recorded")
			}
			p.assertNoTmuxCall(t)
			after := p.request(t, tok)
			if after.PaneAnswer != before.PaneAnswer || after.Decision != before.Decision || after.PaneAs != before.PaneAs {
				t.Errorf("request A = %+v; want it as it was, %+v", after, before)
			}
			if tc.want == api.ErrClaimTooSoon {
				d, ok := api.ErrDetails(err).(api.ClaimTooSoonDetails)
				alive, notBefore := "true", (*time.Time)(nil) // the hook runs: no time can be named
				if tc.name == rpaUnjudgeable {
					alive, notBefore = "null", ptr(after.SettledAt.Add(2*time.Second)) // its confirm_by plus 2 s
				}
				if !ok || d.RequestToken != tok || aliveIs(d.HookAlive) != alive || (d.NotBefore == nil) != (notBefore == nil) ||
					notBefore != nil && !d.NotBefore.Equal(*notBefore) || d.HookGoneAt != nil || !after.HookGoneAt.IsZero() {
					t.Errorf("err_details = %s, hook_gone_at %v; want hook_alive %s, not_before %v, hook_gone_at null, none written",
						jsonOf(t, api.ErrDetails(err)), after.HookGoneAt, alive, notBefore)
				}
			}
		})
	}
}

// TestRecordPaneAnswerClosesAPreV7Request (b.146 rule 13; the upgrade note): a
// request recorded before schema v7 (no relay hook identity, no tool_use_id)
// that fell back by its relay window refuses plain send-keys, which records
// its hook_gone_at; 2 s later record-pane-answer closes it, and plain
// send-keys types again.
func TestRecordPaneAnswerClosesAPreV7Request(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	e := newKillEnv(t)
	r := seedRelayRow(t, e, tok)
	storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, tok, 2*time.Hour)
	p := &paEnv{killEnv: e, r: r, now: time.Now().UTC().Truncate(time.Millisecond), self: paSender, sks: e.store}
	p.setPane("pane of an old request\n")

	err := p.sendKeys(p.plain("hi"))

	d := assertFallenBackDetails(t, err, tok, store.StateCheckPermission)
	if d["tool_use_id"] != nil || d["hook_alive"] != nil {
		t.Errorf("err_details = %v; want no tool_use_id and hook_alive null (recorded before v7)", d)
	}
	pr := p.request(t, tok)
	if !pr.PreV7() || !pr.HookGoneAt.Equal(p.now) {
		t.Fatalf("request A = %+v; want recorded before v7, hook_gone_at %v recorded by the refusal", pr, p.now)
	}
	p.now = p.now.Add(2 * time.Second)
	res, err := p.recordPaneAnswer(p.outside(tok, "unknown"))
	if err != nil || res.Decision != nil || res.PaneAnswer != "outside" {
		t.Fatalf("record-pane-answer = %+v, %v; want outside, decision null", res, err)
	}
	p.rec.Reset()
	if err := p.sendKeys(p.plain("hi")); err != nil {
		t.Fatalf("plain send-keys after the record: %v; want it typed", err)
	}
	p.assertDelivered(t, r.Socket, r.Spawn.Identity.PaneID, "hi")
}
