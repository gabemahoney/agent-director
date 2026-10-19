package store

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

// Synthetic provenance assertions enter through the ordinary store writer.
func TestRequestEligibilityWritersReadersAndProof(t *testing.T) {
	for _, tc := range []struct {
		name, agent, child string
		explicit           *bool
		legacy, want       bool
	}{
		{name: "legacy claude main", agent: AgentClaude, want: true},
		{name: "legacy codex main", agent: AgentCodex, want: true},
		{name: "explicit false main", agent: AgentClaude, explicit: eligibility(false)},
		{name: "known child overrides explicit true", agent: AgentClaude, child: "child", explicit: eligibility(true)},
		{name: "cursor unknown", agent: AgentCursor},
		{name: "cursor proven main", agent: AgentCursor, explicit: eligibility(true), want: true},
		{name: "cursor known child", agent: AgentCursor, child: "child", explicit: eligibility(true)},
		{name: "legacy insert claude", agent: AgentClaude, legacy: true, want: true},
		{name: "legacy insert cursor", agent: AgentCursor, legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, path := newRelayRow(t, "on", "")
			// Synthetic store binding; controller tests own independent runtime evidence.
			withRaw(t, path, func(db *sql.DB) { mustExec(t, db, `UPDATE spawns SET agent=? WHERE instance_id=?`, tc.agent, relayID) })
			gate := agentGate("PermissionRequest", "")
			if tc.agent == AgentCursor {
				row := mustGetSpawn(t, s, relayID)
				b := CursorRuntime{InstanceID: relayID, Token: row.Identity.Token, Life: row.LifeNumber, GatewayPID: row.Identity.PanePID, GatewayStart: row.Identity.PaneStarttime, RuntimePID: 12345, RuntimeStart: "synthetic", Connection: "synthetic", InputPipe: "pipe:[1]", OutputPipe: "pipe:[2]", InputFD: 11, OutputFD: 12}
				if ok, err := s.RegisterCursorRuntime(row, b); err != nil || !ok {
					t.Fatalf("runtime %v %v", ok, err)
				}
				if ok, err := s.SetCursorRuntimeSession(b, "synthetic-session"); err != nil || !ok {
					t.Fatalf("runtime session %v %v", ok, err)
				}
				b.SessionID = "synthetic-session"
				gate.Cursor = &b
				gate.SessionID = b.SessionID
				if a, err := s.ApplyHookTransition(relayID, gate, StateWorking, false, "test_seed", "", false); err != nil || !a.Applied {
					t.Fatalf("session identity %+v %v", a, err)
				}
			}
			if tc.legacy {
				if a, err := s.UpsertOpenPermissionRequest(relayID, gate, tokenA, "Bash", "{}", 0, ""); err != nil || !a.Applied {
					t.Fatalf("legacy insert %+v %v", a, err)
				}
			} else {
				r := mainReq(tokenA)
				r.AgentID = tc.child
				r.MainTurnEndEligible = tc.explicit
				if _, a, err := s.InsertRelayRequest(relayID, gate, r, 0, DefaultLockWait); err != nil || !a.Applied {
					t.Fatalf("insert %+v %v", a, err)
				}
			}
			pr := mustRequest(t, s, tokenA)
			if pr.MainTurnEndEligible != tc.want {
				t.Fatalf("eligible %v; want %v", pr.MainTurnEndEligible, tc.want)
			}

			beforeSpawn := mustGetSpawn(t, s, relayID)
			var collision error
			if tc.legacy {
				_, collision = s.UpsertOpenPermissionRequest(relayID, gate, tokenA, "changed", "{}", 0, "")
			} else {
				replacement := mainReq(tokenA)
				replacement.AgentID = tc.child
				replacement.MainTurnEndEligible = eligibility(!tc.want)
				_, _, collision = s.InsertRelayRequest(relayID, gate, replacement, 0, DefaultLockWait)
			}
			if !errors.Is(collision, ErrRequestTokenCollision) || !reflect.DeepEqual(pr, mustRequest(t, s, tokenA)) || !reflect.DeepEqual(beforeSpawn, mustGetSpawn(t, s, relayID)) {
				t.Fatalf("collision changed origin/request/state: %v", collision)
			}
			readers := []struct {
				name string
				read func() (PermissionRow, error)
			}{
				{"full", func() (PermissionRow, error) { return s.GetPermissionRequest(relayID, tokenA) }},
				{"bounded", func() (PermissionRow, error) { return s.GetPermissionRequestWithin(relayID, tokenA, DefaultLockWait) }},
				{"token", func() (PermissionRow, error) { return s.GetPermissionRequestByToken(tokenA) }},
			}
			for _, rd := range readers {
				got, err := rd.read()
				if err != nil || !reflect.DeepEqual(got, pr) {
					t.Fatalf("%s %+v %v", rd.name, got, err)
				}
			}
			for _, rd := range []func() ([]PermissionRow, error){func() ([]PermissionRow, error) { return s.OpenPermissionRequestsForSpawn(relayID) }, func() ([]PermissionRow, error) { return s.PermissionRequestsForSpawn(relayID) }, func() ([]PermissionRow, error) { return s.PermissionRequestsForSpawnWithin(relayID, DefaultLockWait) }} {
				rows, err := rd()
				if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], pr) {
					t.Fatalf("list %+v %v", rows, err)
				}
			}
			if ok, err := s.DecideRelayRequest(relayID, tokenA, "deny", "", "", time.Time{}, DefaultLockWait); err != nil || !ok {
				t.Fatalf("decide %v %v", ok, err)
			}
			if _, _, ok, err := s.AckRelayDecision(relayID, tokenA, proofAt, DefaultLockWait, nil); err != nil || !ok {
				t.Fatalf("ack %v %v", ok, err)
			}
			s.Close()
			var err error
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got := mustRequest(t, s, tokenA); got.MainTurnEndEligible != tc.want {
				t.Fatal("decision/ack/reopen conferred eligibility")
			}
			gate.Event = "Stop"
			n, err := s.ProveRequestsGone(relayID, gate, RequestProof{How: ProvenGoneTurnEnd, RecordedBefore: startAfterAll, At: proofAt})
			want := int64(0)
			if tc.want {
				want = 1
			}
			if err != nil || n != want {
				t.Fatalf("turn end %d %v; want %d", n, err, want)
			}
			if !tc.want && !tc.legacy {
				independent := mainReq(tokenB)
				independent.MainTurnEndEligible = eligibility(false)
				if _, a, err := s.InsertRelayRequest(relayID, gate, independent, 0, DefaultLockWait); err != nil || !a.Applied {
					t.Fatalf("independent request %+v %v", a, err)
				}
				tool := "toolu_" + tokenA[:8]
				if tc.agent == AgentCursor {
					if n, err = s.ProveRequestsGone(relayID, gate, RequestProof{How: ProvenGoneToolRan, ToolUseID: tool, At: proofAt}); err != nil || n != 0 {
						t.Fatalf("generic Cursor proof %d %v", n, err)
					}
					check := func(Spawn) error { return nil }
					if err = s.ProveCursorToolRan(*gate.Cursor, tokenA, "other-tool", check); err != nil || mustRequest(t, s, tokenA).ProvenGone() {
						t.Fatalf("unmatched Cursor tool proof %v", err)
					}
					if err = s.ProveCursorToolRan(*gate.Cursor, tokenB, tool, check); err != nil || mustRequest(t, s, tokenA).ProvenGone() || mustRequest(t, s, tokenB).ProvenGone() {
						t.Fatalf("mismatched Cursor token/tool pair %v", err)
					}
					err = s.ProveCursorToolRan(*gate.Cursor, tokenA, tool, check)
				} else {
					n, err = s.ProveRequestsGone(relayID, gate, RequestProof{How: ProvenGoneToolRan, ToolUseID: tool, At: proofAt})
					if n != 1 {
						t.Fatalf("correlated release %d %v", n, err)
					}
				}
				if err != nil || mustRequest(t, s, tokenA).ProvenGoneHow != ProvenGoneToolRan || mustRequest(t, s, tokenA).MainTurnEndEligible != tc.want {
					t.Fatalf("correlated release changed provenance: %v", err)
				}
				if mustRequest(t, s, tokenB).ProvenGone() {
					t.Fatal("matching proof released independent request")
				}
			}
		})
	}
}

func eligibility(v bool) *bool { return &v }

func TestCurrentSchemaContradictoryChildRemainsHeld(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	child := relayReq(tokenA)
	insertRelay(t, s, child)
	unknown := mainReq(tokenB)
	unknown.MainTurnEndEligible = eligibility(false)
	insertRelay(t, s, unknown)
	insertRelay(t, s, mainReq(tokenC))
	addOtherRelayRow(t, s)
	insertOther(t, s, child, 0)
	// Corrupt an already-current store, bypassing migration's child=true refusal.
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `UPDATE permission_requests SET main_turn_end_eligible=1 WHERE instance_id=? AND request_token=?`, relayID, tokenA)
	})
	s.Close()
	if got := readUserVersion(t, path); got != schemaVersion {
		t.Fatalf("corrupted fixture version %d; want already-current schema %d", got, schemaVersion)
	}
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := mustRequest(t, s, tokenA); !got.MainTurnEndEligible || got.AgentID != child.AgentID {
		t.Fatalf("corruption did not survive reopen: %+v", got)
	}
	turnEnd := RequestProof{How: ProvenGoneTurnEnd, RecordedBefore: startAfterAll, At: proofAt}
	if n, err := s.ProveRequestsGone(relayID, agentGate("Stop", ""), turnEnd); err != nil || n != 1 {
		t.Fatalf("main proof %d %v; want only eligible main", n, err)
	}
	if got := proven(t, s, tokenA, tokenB, tokenC); !reflect.DeepEqual(got, map[string]string{tokenA: "", tokenB: "", tokenC: ProvenGoneTurnEnd}) {
		t.Fatalf("main proof released protected requests: %v", got)
	}
	tool := RequestProof{How: ProvenGoneToolRan, ToolUseID: child.ToolUseID, At: proofAt}
	if n, err := s.ProveRequestsGone(relayID, foreignGate("PostToolUse", ""), tool); err != nil || n != 0 {
		t.Fatalf("foreign correlated proof %d %v", n, err)
	}
	if n, err := s.ProveRequestsGone(relayID, agentGate("PostToolUse", ""), tool); err != nil || n != 1 {
		t.Fatalf("owning correlated proof %d %v", n, err)
	}
	if mustRequest(t, s, tokenA).ProvenGoneHow != ProvenGoneToolRan || mustRequest(t, s, tokenB).ProvenGone() || otherRequest(t, s, tokenA).ProvenGone() {
		t.Fatal("correlated proof did not release only its owning request")
	}
	endRelayRow(t, s)
	if got := mustRequest(t, s, tokenB); got.ProvenGoneHow != ProvenGoneAgentGone || got.MainTurnEndEligible {
		t.Fatalf("owning close %+v", got)
	}
	if mustRequest(t, s, tokenA).ProvenGoneHow != ProvenGoneToolRan || otherRequest(t, s, tokenA).ProvenGone() {
		t.Fatal("owning close changed existing proof or crossed spawn")
	}
}

func TestExplicitFalseUnknownClosePreservesOtherHolds(t *testing.T) {
	s, path := newRelayRow(t, "on", StateWorking)
	a := mainReq(tokenA)
	a.MainTurnEndEligible = eligibility(false)
	insertRelay(t, s, a)
	b := mainReq(tokenB)
	b.MainTurnEndEligible = eligibility(false)
	insertRelay(t, s, b)
	addOtherRelayRow(t, s)
	insertOther(t, s, a, 0)
	s.Close()
	var err error
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.ProveRequestsGone(relayID, foreignGate("Stop", ""), RequestProof{How: ProvenGoneTurnEnd, RecordedBefore: startAfterAll, At: proofAt}); err != nil || n != 0 {
		t.Fatalf("foreign proof %d %v", n, err)
	}
	if n, err := s.ProveRequestsGone(relayID, agentGate("Stop", ""), RequestProof{How: ProvenGoneTurnEnd, RecordedBefore: startAfterAll, At: proofAt}); err != nil || n != 0 {
		t.Fatalf("main proof %d %v", n, err)
	}
	if n, err := s.ProveRequestsGone(relayID, agentGate("PostToolUse", ""), RequestProof{How: ProvenGoneToolRan, ToolUseID: a.ToolUseID, At: proofAt}); err != nil || n != 1 {
		t.Fatalf("correlated proof %d %v", n, err)
	}
	if mustRequest(t, s, tokenB).ProvenGone() || otherRequest(t, s, tokenA).ProvenGone() {
		t.Fatal("correlated proof crossed request/spawn")
	}
	endRelayRow(t, s)
	if got := mustRequest(t, s, tokenB); got.ProvenGoneHow != ProvenGoneAgentGone || got.MainTurnEndEligible {
		t.Fatalf("owning close %+v", got)
	}
	if otherRequest(t, s, tokenA).ProvenGone() {
		t.Fatal("owning close crossed spawn")
	}
}
