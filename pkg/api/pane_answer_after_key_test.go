package api_test

// pane_answer_after_key_test.go — b.146 step 2b at send-keys (rule 8, problem
// 2): what follows a pane answer's one key. A close of the request meanwhile
// wins over its sent write; when the key or the sent write fails the intent is
// released, a failed release tried a bounded number of times; a failed sent
// write is ErrInternal with err_details key_sent. The key itself is
// pane_answer_test.go's. Fixture: pane_answer_fixture_test.go.

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// TestPaneAnswerReleasesItsIntentOnFailure (b.146 rule 8, problem 2): when the
// key call fails, or the sent write does after it, the pane answer releases
// its intent (pane_answer stays intent, no sender, no pane_intent_at), so a
// retry at once by the same live sender is accepted.
func TestPaneAnswerReleasesItsIntentOnFailure(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	cases := []struct {
		name    string
		arrange func(p *paEnv)
		want    string // the error's one name
		phrase  string
	}{
		{"the key call times out", func(p *paEnv) {
			p.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}, tmux.CallSendText)
		}, "ErrTmuxUnresponsive", "may have been delivered"},
		{"the sent write fails", func(p *paEnv) {
			p.sks = &paDyingStore{killStore: p.store, sentErr: errors.New("disk I/O error")}
		}, "", "the key was sent (err_details.key_sent), but recording it failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			tc.arrange(p)

			err := p.sendKeys(p.answer(tok, "allow", "1"))

			assertOneName(t, err, tc.want)
			adviceAssertPhrase(t, err, tc.phrase)
			pr := p.request(t, tok)
			if pr.PaneAnswer != store.PaneAnswerIntent || pr.PaneAs != "allow" || pr.PaneSender != (api.ProcessIdentity{}) ||
				!pr.PaneIntentAt.IsZero() || pr.Decision != "" {
				t.Fatalf("request A = %+v; want intent allow, released (no sender, no pane_intent_at)", pr)
			}
			p.sks = p.store
			if err := p.sendKeys(p.answer(tok, "allow", "1")); err != nil {
				t.Fatalf("retry at once by the same live sender: %v; want it accepted", err)
			}
			if pr := p.request(t, tok); pr.PaneAnswer != store.PaneAnswerSent {
				t.Errorf("request A after the retry = %+v; want sent", pr)
			}
		})
	}
}

// TestPaneAnswerClosedWhileItsKeyGoesOut (b.146 rules 8, 12, 13): a close of
// the request while the pane answer's key goes out wins: the call succeeds
// with exactly its one key and no Enter, its sent write changes nothing
// (no ad.row_mutation.committed by send_keys), and the request keeps the
// close's verdict: its tool's PostToolUse (tool_ran, allow), or a close of the
// row's requests by its SessionEnd or find-missing's mark (deny, closed).
func TestPaneAnswerClosedWhileItsKeyGoesOut(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	cases := []struct {
		name                         string
		close                        r7Step
		paneAnswer, decision, reason string
		closed                       bool
	}{
		{"its tool ran", r7ToolRan, store.PaneAnswerToolRan, "allow", store.DecisionReasonToolRan, false},
		{"its row ended", r7End, store.PaneAnswerIntent, "deny", store.DecisionReasonEnded, true},
		{"find-missing marked its row missing", r7Mark, store.PaneAnswerIntent, "deny", store.DecisionReasonFindMissing, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			p.rec.AfterCall(paKeyCall("1"), func(tmuxfix.SocketCall, error) { tc.close(t, p) })

			run := p.sendKeysRun(p.answer(tok, "allow", "1"))

			if run.err != nil {
				t.Fatalf("pane answer: %v; want success, the key was sent", run.err)
			}
			assertOneKey(t, run.calls, p.r.Spawn.Identity.PaneID, "1", tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
			if pr := p.request(t, tok); pr.PaneAnswer != tc.paneAnswer || pr.Decision != tc.decision || pr.DecisionReason != tc.reason ||
				pr.Closed() != tc.closed {
				t.Errorf("request A = %+v; want pane_answer %s, %s (%s), closed %v: the close's, not sent's", pr, tc.paneAnswer,
					tc.decision, tc.reason, tc.closed)
			}
			for _, l := range readAPITrailLines(t) {
				if l["event"] == "ad.row_mutation.committed" && l["claude_instance_id"] == p.r.ID && l["writer_process"] == store.WriterProcessSendKeys {
					t.Errorf("ad.row_mutation.committed by send_keys: %v; want none (sent wrote nothing)", l)
				}
			}
		})
	}
}

// paReleaseStore is a SendKeysStore whose pane answer's sent write fails
// (nothing written) and whose intent release fails its first fails attempts,
// nothing written, each failed one taking tick on p's clock; waits records
// each release attempt's wait for the store.
type paReleaseStore struct {
	*killStore
	p     *paEnv
	fails int
	tick  time.Duration
	waits []time.Duration
}

// RecordPaneSent fails, writing nothing.
func (s *paReleaseStore) RecordPaneSent(string, string, api.PaneIntent, time.Duration) (bool, error) {
	return false, errors.New("disk I/O error")
}

// ReleasePaneIntent fails while fails attempts remain, else delegates.
func (s *paReleaseStore) ReleasePaneIntent(id, token string, intent api.PaneIntent, maxWait time.Duration) (bool, error) {
	s.waits = append(s.waits, maxWait)
	if len(s.waits) <= s.fails {
		s.p.now = s.p.now.Add(s.tick)
		return false, fmt.Errorf("%w: the write lock was not free", store.ErrStoreBusy)
	}
	return s.killStore.ReleasePaneIntent(id, token, intent, maxWait)
}

// TestPaneAnswerReleaseIsBounded (b.146 rule 8, problem 2): a pane answer
// whose sent write failed after its key is ErrInternal (no catalogued name)
// with err_details key_sent true and its request token, and tries to release
// its intent at most 3 times, sharing 10 s of waits for the store on the
// call's clock. Released, the description says so and the intent is clear;
// not released, it says the request reads in progress until this process
// exits, and a second pane answer is ErrPaneAnswerInProgress.
func TestPaneAnswerReleaseIsBounded(t *testing.T) {
	t.Parallel()
	tok := storefix.TestRequestTokenA
	s := time.Second
	cases := []struct {
		name     string
		fails    int
		tick     time.Duration
		waits    []time.Duration // each attempt's wait for the store
		released bool
	}{
		{"released at the second attempt", 1, s, []time.Duration{10 * s, 9 * s}, true},
		{"never released: three attempts", 5, s, []time.Duration{10 * s, 9 * s, 8 * s}, false},
		{"never released: the 10 s used up", 5, 6 * s, []time.Duration{10 * s, 4 * s}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := newPAEnv(t)
			rs := &paReleaseStore{killStore: p.store, p: p, fails: tc.fails, tick: tc.tick}
			p.sks = rs

			run := p.sendKeysRun(p.answer(tok, "allow", "1"))

			assertOneName(t, run.err, "")
			assertOneKey(t, run.calls, p.r.Spawn.Identity.PaneID, "1", tmux.CallLookup, tmux.CallListPanes, tmux.CallCapture)
			adviceAssertPhrase(t, run.err, "the key was sent (err_details.key_sent), but recording it failed")
			if d := api.ErrDetails(run.err); d != (api.PaneKeySentDetails{KeySent: true, RequestToken: tok}) ||
				jsonOf(t, d) != `{"key_sent":true,"request_token":"`+tok+`"}` {
				t.Errorf("err_details = %s; want key_sent true and request A", jsonOf(t, d))
			}
			if !slices.Equal(rs.waits, tc.waits) {
				t.Errorf("release attempts waited %v; want %v", rs.waits, tc.waits)
			}
			pr := p.request(t, tok)
			p.sks = p.store
			if tc.released {
				adviceAssertPhrase(t, run.err, "its intent is released")
				if pr.PaneSender != (api.ProcessIdentity{}) || !pr.PaneIntentAt.IsZero() {
					t.Errorf("request A = %+v; want its intent released", pr)
				}
				return
			}
			adviceAssertPhrase(t, run.err, "its intent could not be released, so it reads in progress (ErrPaneAnswerInProgress) until this process exits")
			if pr.PaneAnswer != store.PaneAnswerIntent || pr.PaneSender != paSender {
				t.Errorf("request A = %+v; want its intent kept", pr)
			}
			assertOneSentinel(t, p.sendKeys(p.answer(tok, "allow", "1")), api.ErrPaneAnswerInProgress)
		})
	}
}
