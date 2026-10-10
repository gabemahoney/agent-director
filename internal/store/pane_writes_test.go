package store

// pane_writes_test.go — b.146 step 2b's store writes (pane_writes.go): a pane
// answer's intent, sent and release (rule 8, problem 2), record-pane-answer's
// outside record and the PostToolUse close (rule 13), each guarded on the
// request still awaiting an answer (a close between intent and sent wins).
// Seeds: relay_writes_test.go's relay row; closers: relay_close_test.go's.

import (
	"errors"
	"testing"
	"time"
)

// paneSender is the process a test pane answer records as its sender.
var paneSender = ProcessIdentity{PID: 9876, Starttime: "555", PIDNamespace: "pid:[4026531836]"}

// writeIntent records paneSender's intent claiming as on tokenA, stamped at,
// with no check; it must apply. It returns the intent as recorded.
func writeIntent(t *testing.T, s *Store, as string, at time.Time) PaneIntent {
	t.Helper()
	intent, ok, err := s.RecordPaneIntent(relayID, tokenA, as, paneSender, func() time.Time { return at }, DefaultLockWait, nil)
	if err != nil || !ok {
		t.Fatalf("RecordPaneIntent = %v, %v; want written", ok, err)
	}
	return intent
}

// TestPaneAnswerIntentThenSent (rule 8, problem 2): the intent is stamped with
// the clock read after its check, and records the claim, its sender,
// pane_intent_at and hook_gone_at (none before) at that instant; the request
// still awaits an answer; sent matches only the same intent and records
// decision as pane_as, decision_reason pane; then no intent can be written on
// it.
func TestPaneAnswerIntentThenSent(t *testing.T) {
	s, _ := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	at := time.UnixMilli(time.Now().UnixMilli()).UTC()
	clock := at.Add(-time.Minute) // a wait for the lock, then the check, take a minute

	intent, ok, err := s.RecordPaneIntent(relayID, tokenA, "deny", paneSender, func() time.Time { return clock }, DefaultLockWait,
		func(Spawn, []PermissionRow) error { clock = at; return nil })

	if err != nil || !ok || intent.As != "deny" || intent.Sender != paneSender || !intent.At.Equal(at) {
		t.Fatalf("RecordPaneIntent = %+v, %v, %v; want deny by the sender at %v, read after the check", intent, ok, err, at)
	}
	pr := mustRequest(t, s, tokenA)
	if pr.PaneAnswer != PaneAnswerIntent || pr.PaneAs != "deny" || pr.PaneSender != paneSender ||
		!pr.PaneIntentAt.Equal(intent.At) || !pr.HookGoneAt.Equal(intent.At) || !pr.AwaitsAnswer() {
		t.Errorf("after the intent: %+v; want intent, deny, the sender, pane_intent_at and hook_gone_at %v, still awaiting", pr, intent.At)
	}
	other := intent
	other.At = intent.At.Add(time.Millisecond)
	if ok, err := s.RecordPaneSent(relayID, tokenA, other, DefaultLockWait); err != nil || ok {
		t.Errorf("RecordPaneSent with another intent = %v, %v; want nothing written", ok, err)
	}
	if ok, err := s.RecordPaneSent(relayID, tokenA, intent, DefaultLockWait); err != nil || !ok {
		t.Fatalf("RecordPaneSent = %v, %v; want written", ok, err)
	}
	pr = mustRequest(t, s, tokenA)
	if pr.PaneAnswer != PaneAnswerSent || pr.Decision != "deny" || pr.DecisionReason != DecisionReasonPane ||
		pr.DecidedAt.IsZero() || pr.AwaitsAnswer() {
		t.Errorf("after sent: %+v; want sent, decision deny, decision_reason pane, decided_at set, closed", pr)
	}
	if _, ok, err := s.RecordPaneIntent(relayID, tokenA, "allow", paneSender, nil, DefaultLockWait, nil); err != nil || ok {
		t.Errorf("RecordPaneIntent on the closed request = %v, %v; want nothing written", ok, err)
	}
}

// TestRecordPaneSentAfterACloseWritesNothing (b.146 rules 8, 12): a close of
// the row's requests between a pane answer's intent and its sent write wins:
// sent writes nothing, and the request keeps the close's deny, closed.
func TestRecordPaneSentAfterACloseWritesNothing(t *testing.T) {
	for _, c := range closers {
		t.Run(c.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			intent := writeIntent(t, s, "allow", time.Now())
			if c.unclosed {
				endRelayRowUnclosed(t, path)
			}
			c.close(t, s)

			if ok, err := s.RecordPaneSent(relayID, tokenA, intent, DefaultLockWait); err != nil || ok {
				t.Errorf("RecordPaneSent after the close = %v, %v; want nothing written", ok, err)
			}
			if pr := mustRequest(t, s, tokenA); pr.PaneAnswer != PaneAnswerIntent || pr.Decision != "deny" ||
				pr.DecisionReason != c.reason || !pr.Closed() {
				t.Errorf("request = %+v; want the close's deny (%s) kept, closed, pane_answer still intent", pr, c.reason)
			}
		})
	}
}

// TestReleasePaneIntent (rule 8, problem 2): the release matches only its own
// intent, clears the sender and pane_intent_at and leaves pane_answer intent;
// a released intent can no longer be recorded sent.
func TestReleasePaneIntent(t *testing.T) {
	s, _ := newRelayRow(t, "on", StateWorking)
	insertRelay(t, s, relayReq(tokenA))
	intent := writeIntent(t, s, "allow", time.Now())
	other := intent
	other.Sender.PID++

	if ok, err := s.ReleasePaneIntent(relayID, tokenA, other, DefaultLockWait); err != nil || ok {
		t.Errorf("ReleasePaneIntent of another sender's intent = %v, %v; want nothing written", ok, err)
	}
	if ok, err := s.ReleasePaneIntent(relayID, tokenA, intent, DefaultLockWait); err != nil || !ok {
		t.Fatalf("ReleasePaneIntent = %v, %v; want written", ok, err)
	}

	pr := mustRequest(t, s, tokenA)
	if pr.PaneAnswer != PaneAnswerIntent || pr.PaneAs != "allow" || pr.PaneSender != (ProcessIdentity{}) || !pr.PaneIntentAt.IsZero() {
		t.Errorf("after the release: %+v; want intent, allow, no sender, no pane_intent_at", pr)
	}
	if ok, err := s.RecordPaneSent(relayID, tokenA, intent, DefaultLockWait); err != nil || ok {
		t.Errorf("RecordPaneSent after the release = %v, %v; want nothing written", ok, err)
	}
}

// TestPaneWriteCheckRefuses (rules 8, 13): a pane write's check sees the row
// and its requests inside the transaction, and its error is returned as it is
// with nothing written.
func TestPaneWriteCheckRefuses(t *testing.T) {
	refused := errors.New("check refused")
	writes := map[string]func(s *Store, check PaneCheck) (bool, error){
		"intent": func(s *Store, check PaneCheck) (bool, error) {
			_, ok, err := s.RecordPaneIntent(relayID, tokenA, "allow", paneSender, nil, DefaultLockWait, check)
			return ok, err
		},
		"outside": func(s *Store, check PaneCheck) (bool, error) {
			return s.RecordPaneOutside(relayID, tokenA, "allow", DefaultLockWait, check)
		},
	}
	for name, write := range writes {
		t.Run(name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			before := requestDump(t, path)
			var seen []string
			ok, err := write(s, func(sp Spawn, rows []PermissionRow) error {
				seen = append(seen, sp.ClaudeInstanceID)
				for _, r := range rows {
					seen = append(seen, r.RequestToken)
				}
				return refused
			})
			if !errors.Is(err, refused) || ok {
				t.Errorf("write = %v, %v; want the check's error", ok, err)
			}
			if len(seen) != 2 || seen[0] != relayID || seen[1] != tokenA {
				t.Errorf("check saw %v; want the row and its one request", seen)
			}
			if after := requestDump(t, path); !mapsEqual(after[0], before[0]) {
				t.Errorf("request changed by a refused write:\n got  %v\n want %v", after[0], before[0])
			}
		})
	}
}

// TestRecordPaneOutsideClaims (rule 13): the claim is stored as pane_as and as
// the decision (NULL for unknown), decision_reason pane_outside; a request no
// longer awaiting an answer is left alone.
func TestRecordPaneOutsideClaims(t *testing.T) {
	for claim, decision := range map[string]string{"allow": "allow", "deny": "deny", "unknown": ""} {
		t.Run(claim, func(t *testing.T) {
			s, _ := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))

			if ok, err := s.RecordPaneOutside(relayID, tokenA, claim, DefaultLockWait, nil); err != nil || !ok {
				t.Fatalf("RecordPaneOutside = %v, %v; want written", ok, err)
			}

			pr := mustRequest(t, s, tokenA)
			if pr.PaneAnswer != PaneAnswerOutside || pr.PaneAs != claim || pr.Decision != decision ||
				pr.DecisionReason != DecisionReasonPaneOutside || pr.AwaitsAnswer() {
				t.Errorf("request = %+v; want outside, pane_as %s, decision %q, pane_outside, closed", pr, claim, decision)
			}
			if ok, err := s.RecordPaneOutside(relayID, tokenA, "deny", DefaultLockWait, nil); err != nil || ok {
				t.Errorf("a second RecordPaneOutside = %v, %v; want nothing written", ok, err)
			}
		})
	}
}

// TestCloseToolRanRequests (rule 13): the PostToolUse close closes the
// request carrying the tool_use_id only when gone judges its hook gone, it
// still awaits an answer and the hook's gate holds; it keeps an earlier
// hook_gone_at.
func TestCloseToolRanRequests(t *testing.T) {
	gone := func(PermissionRow) bool { return true }
	at := time.UnixMilli(time.Now().UnixMilli())
	cases := []struct {
		name      string
		arrange   func(t *testing.T, s *Store)
		toolUseID string
		gate      HookGate
		gone      func(PermissionRow) bool
		want      bool
	}{
		{"fallen back", nil, "toolu_" + tokenA[:8], agentGate("PostToolUse", ""), gone, true},
		{"hook_gone_at already recorded", func(t *testing.T, s *Store) {
			if _, err := s.RecordHookGone(at.Add(-time.Minute), DefaultLockWait, mustRequest(t, s, tokenA).RequestID); err != nil {
				t.Fatalf("RecordHookGone: %v", err)
			}
		}, "toolu_" + tokenA[:8], agentGate("PostToolUse", ""), gone, true},
		{"its hook may still answer", nil, "toolu_" + tokenA[:8], agentGate("PostToolUse", ""), func(PermissionRow) bool { return false }, false},
		{"no judge", nil, "toolu_" + tokenA[:8], agentGate("PostToolUse", ""), nil, false},
		{"another tool_use_id", nil, "toolu_other", agentGate("PostToolUse", ""), gone, false},
		{"no tool_use_id", nil, "", agentGate("PostToolUse", ""), gone, false},
		{"another process's hook", nil, "toolu_" + tokenA[:8], foreignGate("PostToolUse", ""), gone, false},
		{"acked", func(t *testing.T, s *Store) {
			decideA(t, s, "deny")
			if _, _, ok, err := s.AckRelayDecision(relayID, tokenA, time.Now(), DefaultLockWait, nil); err != nil || !ok {
				t.Fatalf("ack = %v, %v", ok, err)
			}
		}, "toolu_" + tokenA[:8], agentGate("PostToolUse", ""), gone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newRelayRow(t, "on", StateWorking)
			insertRelay(t, s, relayReq(tokenA))
			if tc.arrange != nil {
				tc.arrange(t, s)
			}
			before := mustRequest(t, s, tokenA)

			closed, err := s.CloseToolRanRequests(relayID, tc.gate, tc.toolUseID, at, tc.gone)

			pr := mustRequest(t, s, tokenA)
			if !tc.want {
				if err != nil || len(closed) != 0 || pr.PaneAnswer != before.PaneAnswer || pr.Decision != before.Decision {
					t.Errorf("close = %v, %v; request %+v; want nothing closed", closed, err, pr)
				}
				return
			}
			wantGone := at
			if !before.HookGoneAt.IsZero() {
				wantGone = before.HookGoneAt
			}
			if err != nil || len(closed) != 1 || closed[0] != tokenA {
				t.Fatalf("close = %v, %v; want [%s]", closed, err, tokenA)
			}
			if pr.PaneAnswer != PaneAnswerToolRan || pr.Decision != "allow" || pr.DecisionReason != DecisionReasonToolRan ||
				!pr.HookGoneAt.Equal(wantGone) || pr.AwaitsAnswer() {
				t.Errorf("request = %+v; want tool_ran, allow, tool_ran, hook_gone_at %v, closed", pr, wantGone)
			}
		})
	}
}
