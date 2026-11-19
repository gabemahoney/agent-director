package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// b.o7x: use admitted owners and gated raw transitions, never rebase a request.
func epochRelay(t *testing.T, agent string, child bool) (*Store, string) {
	t.Helper()
	s, path := openTempStore(t)
	if err := insertAgentRow(s, Spawn{InstanceID: relayID, Agent: agent, CWD: "/tmp", TmuxSessionName: "epoch-relay", RelayMode: "on"}); err != nil {
		t.Fatal(err)
	}
	if err := agentHook(s, relayID, StateWorking, false, "UserPromptSubmit"); err != nil {
		t.Fatal(err)
	}
	req := relayReq(tokenA)
	if !child {
		req.AgentID = ""
	}
	insertRelay(t, s, req)
	return s, path
}

func advanceRelayEpoch(t *testing.T, s *Store, bumps int) {
	t.Helper()
	before := mustGetSpawn(t, s, relayID)
	owner := mustRequest(t, s, tokenA)
	for i := 0; i < bumps; i++ {
		state, event := StateWaiting, "Stop"
		if i%2 == 1 {
			state, event = StateAskUser, "PreToolUse"
		}
		if err := agentHook(s, relayID, state, false, event); err != nil {
			t.Fatal(err)
		}
	}
	after := mustGetSpawn(t, s, relayID)
	pr := mustRequest(t, s, tokenA)
	if after.PermissionEpoch != before.PermissionEpoch+int64(bumps) || pr.OwnerPermissionEpoch != owner.OwnerPermissionEpoch || pr.Closed() || pr.ProvenGone() {
		t.Fatalf("expected %d real epoch bumps with immutable open admission: before=%+v after=%+v request=%+v", bumps, before, after, pr)
	}
}

// b.o7x: each writer must survive both one and several main-state intervals.
func TestChildRelayAcrossEpochs(t *testing.T) {
	for _, agent := range []string{AgentClaude, AgentCodex} {
		for _, bumps := range []int{1, 4} {
			for _, ordering := range []string{"bump then verdict", "verdict then bump", "timeout"} {
				for _, decision := range []string{"allow", "deny"} {
					if ordering == "timeout" && decision == "allow" {
						continue
					}
					t.Run(fmt.Sprintf("%s/%d/%s/%s", agent, bumps, ordering, decision), func(t *testing.T) {
						s, path := epochRelay(t, agent, true)
						owner := mustRequest(t, s, tokenA)
						reason := ""
						if decision == "deny" {
							reason = DecisionReasonOperator
						}
						decide := func() {
							if ok, err := s.DecideRelayRequest(relayID, tokenA, decision, reason, WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
								t.Fatalf("child verdict across epoch: written=%v err=%v", ok, err)
							}
						}
						if ordering == "verdict then bump" {
							decide()
						}
						advanceRelayEpoch(t, s, bumps)
						if ordering == "bump then verdict" {
							decide()
						}
						other, err := Open(path)
						if err != nil {
							t.Fatal(err)
						}
						defer other.Close()
						at := time.UnixMilli(time.Now().UnixMilli())
						origin := DecisionOriginCaller
						if ordering == "timeout" {
							if ok, err := other.DenyRelayTimeout(relayID, tokenA, at, DefaultLockWait, nil); err != nil || !ok {
								t.Fatalf("child timeout across epoch: written=%v err=%v", ok, err)
							}
							origin, reason = DecisionOriginTimeout, DecisionReasonTimeout
						} else {
							pr := mustRequest(t, other, tokenA)
							if pr.DecisionOrigin != origin || pr.Decision != decision || pr.ResponseState != "" || !pr.DeliveredAt.IsZero() || pr.ProvenGone() || pr.Closed() {
								t.Fatalf("verdict prematurely claimed or lost: %+v", pr)
							}
							if ok, err := other.DenyRelayTimeout(relayID, tokenA, at, DefaultLockWait, nil); err != nil || ok {
								t.Fatalf("timeout replaced caller verdict: %v %v", ok, err)
							}
							d, r, ok, err := other.AckRelayDecision(relayID, tokenA, at, DefaultLockWait, nil)
							if err != nil || !ok || d != decision || r != reason {
								t.Fatalf("child ack across epoch: %q %q %v %v", d, r, ok, err)
							}
						}
						pr := mustRequest(t, other, tokenA)
						if pr.Decision != decision || pr.DecisionReason != reason || pr.DecisionOrigin != origin || pr.ResponseState != ResponseRelayClaimed || !pr.DeliveredAt.Equal(at) || pr.ProvenGone() || pr.Closed() ||
							pr.OwnerPermissionEpoch != owner.OwnerPermissionEpoch || pr.OwnerLifeNumber != owner.OwnerLifeNumber || pr.OwnerLaunchToken != owner.OwnerLaunchToken || pr.OwnerSessionID != owner.OwnerSessionID || pr.AgentID != owner.AgentID {
							t.Fatalf("committed child response or immutable owner incorrect: %+v", pr)
						}
						if ok, err := other.DecideRelayRequest(relayID, tokenA, "deny", DecisionReasonOperator, WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || ok {
							t.Fatalf("duplicate verdict: %v %v", ok, err)
						}
						if d, r, ok, err := other.AckRelayDecision(relayID, tokenA, at, DefaultLockWait, nil); err != nil || ok || d != "" || r != "" {
							t.Fatalf("ack replay: %q %q %v %v", d, r, ok, err)
						}
						if ok, err := other.DenyRelayTimeout(relayID, tokenA, at, DefaultLockWait, nil); err != nil || ok {
							t.Fatalf("timeout replay: %v %v", ok, err)
						}
					})
				}
			}
		}
	}
}

// b.o7x: forward epochs grant no authority across any other admission boundary.
func TestChildRelayAdvancedEpochRefusals(t *testing.T) {
	for _, agent := range []string{AgentClaude, AgentCodex} {
		for _, writer := range []string{"decide", "ack", "timeout"} {
			for _, loss := range []struct{ name, sql string }{
				{"main same-second cutoff", ""},
				{"launch", "UPDATE spawns SET launch_token='2222222222222222'"},
				{"life", "UPDATE spawns SET life_number=life_number+1"},
				{"session", "UPDATE spawns SET session_id='replacement'"},
				{"pending", "UPDATE spawns SET state='pending'"},
				{"ended", "UPDATE spawns SET state='ended'"},
				{"missing", "UPDATE spawns SET state='missing'"},
				{"closed", "UPDATE permission_requests SET closed_at=1"},
				{"proven", "UPDATE permission_requests SET proven_gone_at=1,proven_gone_how='agent_gone'"},
				{"pane", "UPDATE permission_requests SET pane_answer='sent'"},
				{"relay claim", "UPDATE permission_requests SET response_state='relay_claimed'"},
				{"native claim", "UPDATE permission_requests SET response_state='native_claimed'"},
				{"missing life", "UPDATE permission_requests SET owner_life_number=NULL"},
				{"missing launch", "UPDATE permission_requests SET owner_launch_token=NULL"},
				{"missing epoch", "UPDATE permission_requests SET owner_permission_epoch=NULL"},
				{"future epoch", "UPDATE permission_requests SET owner_permission_epoch=(SELECT permission_epoch+1 FROM spawns)"},
				{"absent child", "UPDATE permission_requests SET agent_id=NULL"},
				{"empty child", "UPDATE permission_requests SET agent_id=''"},
				{"empty agent", "UPDATE spawns SET agent=''"},
				{"unsupported agent", "UPDATE spawns SET agent='future-agent'"},
			} {
				t.Run(agent+"/"+writer+"/"+loss.name, func(t *testing.T) {
					s, path := epochRelay(t, agent, loss.name != "main same-second cutoff")
					if writer == "ack" {
						if ok, err := s.DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
							t.Fatalf("setup verdict: %v %v", ok, err)
						}
					}
					if loss.name == "main same-second cutoff" {
						pr := mustRequest(t, s, tokenA)
						if n, err := s.ProveRequestsGone(relayID, agentGate("Stop", ""), RequestProof{How: ProvenGoneTurnEnd, RecordedBefore: pr.CreatedAt.Add(time.Millisecond)}); err != nil || n != 0 {
							t.Fatalf("same-second main request should remain unproven: %d %v", n, err)
						}
					}
					advanceRelayEpoch(t, s, 3)
					// Deliberately corrupt/stale persisted authority, after a real bump.
					if loss.sql != "" {
						mustExec(t, s.db, loss.sql)
					}
					before := requestDump(t, path)
					var ok bool
					var err error
					switch writer {
					case "decide":
						ok, err = s.DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, time.Time{}, DefaultLockWait)
					case "ack":
						var d, r string
						d, r, ok, err = s.AckRelayDecision(relayID, tokenA, time.Now(), DefaultLockWait, nil)
						if d != "" || r != "" {
							t.Fatalf("refused ack returned output: %q %q", d, r)
						}
					case "timeout":
						ok, err = s.DenyRelayTimeout(relayID, tokenA, time.Now(), DefaultLockWait, nil)
					}
					if err != nil || ok || !reflect.DeepEqual(before, requestDump(t, path)) {
						t.Fatalf("retired %s changed history: written=%v err=%v", writer, ok, err)
					}
				})
			}
		}
	}
}

// b.o7x: main proofs (and a child's own proof) still defeat a later epoch.
func TestRelayEpochTurnEndProofRefusals(t *testing.T) {
	for _, event := range []string{"Stop", "Notification", "Interrupt", "SubagentStop"} {
		for _, writer := range []string{"decide", "ack", "timeout"} {
			t.Run(event+"/"+writer, func(t *testing.T) {
				agent := AgentClaude
				if event == "Interrupt" {
					agent = AgentCodex
				}
				s, path := epochRelay(t, agent, event == "SubagentStop")
				child := ""
				if event == "SubagentStop" {
					child = relayReq(tokenA).AgentID
				}
				if writer == "ack" {
					if ok, err := s.DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
						t.Fatalf("setup verdict: %v %v", ok, err)
					}
				}
				proof := turnEndProof()
				proof.AgentID = child
				if n, err := s.ProveRequestsGone(relayID, agentGate(event, ""), proof); err != nil || n != 1 {
					t.Fatalf("turn proof: %d %v", n, err)
				}
				if err := agentHook(s, relayID, StateWaiting, false, event); err != nil {
					t.Fatal(err)
				}
				if mustGetSpawn(t, s, relayID).PermissionEpoch <= mustRequest(t, s, tokenA).OwnerPermissionEpoch {
					t.Fatal("proof scenario did not advance the epoch")
				}
				before := requestDump(t, path)
				var ok bool
				var err error
				switch writer {
				case "decide":
					ok, err = s.DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, time.Time{}, DefaultLockWait)
				case "ack":
					_, _, ok, err = s.AckRelayDecision(relayID, tokenA, time.Now(), DefaultLockWait, nil)
				case "timeout":
					ok, err = s.DenyRelayTimeout(relayID, tokenA, time.Now(), DefaultLockWait, nil)
				}
				if err != nil || ok || !reflect.DeepEqual(before, requestDump(t, path)) {
					t.Fatalf("proof allowed a new %s: %v %v", writer, ok, err)
				}
			})
		}
	}
}

// b.o7x: the exception never bypasses the hook's response precondition.
func TestChildRelayEpochPrecondition(t *testing.T) {
	for _, writer := range []string{"ack", "timeout"} {
		t.Run(writer, func(t *testing.T) {
			s, path := epochRelay(t, AgentClaude, true)
			if writer == "ack" {
				if ok, err := s.DecideRelayRequest(relayID, tokenA, "allow", "", WriterProcessDecide, time.Time{}, DefaultLockWait); err != nil || !ok {
					t.Fatalf("setup verdict: %v %v", ok, err)
				}
			}
			advanceRelayEpoch(t, s, 2)
			before := requestDump(t, path)
			injected := errors.New("response precondition failed")
			check := func() error { return injected }
			var ok bool
			var err error
			if writer == "ack" {
				_, _, ok, err = s.AckRelayDecision(relayID, tokenA, time.Now(), DefaultLockWait, check)
			} else {
				ok, err = s.DenyRelayTimeout(relayID, tokenA, time.Now(), DefaultLockWait, check)
			}
			if !errors.Is(err, injected) || ok || !reflect.DeepEqual(before, requestDump(t, path)) {
				t.Fatalf("precondition failed to roll back %s: %v %v", writer, ok, err)
			}
		})
	}
}
