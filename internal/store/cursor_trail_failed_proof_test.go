package store_test

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
)

// b.a65: failed MCP completion proves the dialog gone without inventing an answer.
func TestCursorTrailFailedProofPreservesFinalValuesAndDuplicate(t *testing.T) {
	for _, prior := range []string{"undecided", "delivered allow", "operator deny", "timeout deny", "native deny", "pane intent"} {
		t.Run(prior, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			e.insert(t, "target")
			e.insert(t, "independent")
			switch prior {
			case "delivered allow", "operator deny":
				decision, reason := "allow", ""
				if prior == "operator deny" {
					decision, reason = "deny", "operator"
				}
				if ok, err := e.f.s.DecidePermissionRequest(e.b.InstanceID, "target", decision, reason, store.WriterProcessDecide); err != nil || !ok {
					t.Fatalf("decide: %v %v", ok, err)
				}
				if prior == "delivered allow" {
					if d, ok, err := e.f.s.AckCursorDecision(e.b, "target", false, e.until, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok || d != decision {
						t.Fatalf("ack: %q %v %v", d, ok, err)
					}
				}
			case "timeout deny":
				if d, ok, err := e.f.s.AckCursorDecision(e.b, "target", true, e.until, e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok || d != "deny" {
					t.Fatalf("timeout: %q %v %v", d, ok, err)
				}
			case "native deny":
				if ok, err := e.f.s.ClaimCursorPaneAnswer(e.b, "target", "deny", e.until, store.DefaultLockWait, cursorTrailCheck); err != nil || !ok {
					t.Fatalf("native: %v %v", ok, err)
				}
			case "pane intent":
				e.sent(t, "target", "allow", false)
			}
			before, other := e.read(t, "target"), e.read(t, "independent")
			mark := store.TrailMark(t)
			seen := e.beforeCommit(t, mark)
			if err := e.f.s.ProveCursorToolFailed(e.b, "target", "tool-target", cursorTrailCheck); err != nil {
				t.Fatal(err)
			}
			after := e.assertEvent(t, mark, "target", "update", before.Decision, before.DecisionReason)
			if !*seen || !after.ProvenGone() || after.ProvenGoneHow != "tool_ran" {
				t.Fatalf("failed completion did not commit dialog proof: %+v", after)
			}
			withoutProof := after
			withoutProof.ProvenGoneAt, withoutProof.ProvenGoneHow = before.ProvenGoneAt, before.ProvenGoneHow
			if !reflect.DeepEqual(before, withoutProof) || !reflect.DeepEqual(other, e.read(t, "independent")) {
				t.Fatalf("failed completion changed non-proof evidence: before=%+v after=%+v", before, after)
			}
			if before.Decision == "" {
				db, err := sql.Open("sqlite", e.f.path)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var decisionNull, reasonNull, decidedAtNull bool
				if err := db.QueryRow(`SELECT decision IS NULL, decision_reason IS NULL, decided_at IS NULL FROM permission_requests WHERE instance_id=? AND request_token=?`, e.b.InstanceID, "target").Scan(&decisionNull, &reasonNull, &decidedAtNull); err != nil || !decisionNull || !reasonNull || !decidedAtNull {
					t.Fatalf("failed completion fabricated a decision: nulls=%v/%v/%v err=%v", decisionNull, reasonNull, decidedAtNull, err)
				}
			}
			mark = store.TrailMark(t)
			// The existing guard fails COMMIT if replay actually updates any row.
			e.rejectCommitOnUpdate(t)
			if err := e.f.s.ProveCursorToolFailed(e.b, "target", "tool-target", cursorTrailCheck); err != nil {
				t.Fatalf("duplicate failed proof updated a row: %v", err)
			}
			if events := e.events(t, mark); len(events) != 0 || !reflect.DeepEqual(after, e.read(t, "target")) || !reflect.DeepEqual(other, e.read(t, "independent")) {
				t.Fatalf("duplicate failed proof changed rows or emitted a mutation: %v", events)
			}
		})
	}
}
