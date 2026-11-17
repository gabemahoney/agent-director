package mixedversion_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

// M3 T1: the two generations really execute their own CLI, MCP, Go Client
// and shipped TS client. Both write; complementary readers cross generations.
func TestMixedVersionCoreABI(t *testing.T) {
	b := builds(t)
	for _, g := range b.generations {
		cmd := exec.Command(g.runtime, "version")
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		var v struct{ Version, Commit string }
		if err = json.Unmarshal(out, &v); err != nil {
			t.Fatal(err)
		}
		exact(t, v, struct{ Version, Commit string }{g.version, g.commit})
	}
	for writer := range b.generations {
		t.Run(b.generations[writer].name, func(t *testing.T) {
			f := newFixture(t)
			wantIdentity := map[string]string{"core_version": "busy-send-core-v1:", "core_declarations_sha256": "45bbc95a6429e2f483f3ab4bc1cc0b8d12d40f607e06b6bf4416d87fff1cb7f7"}
			for _, d := range f.drivers {
				exact(t, value[map[string]string](t, d.call("identity", nil)), wantIdentity)
			}
			surfaces := []string{"cli", "mcp", "go", "ts"}
			for index, surface := range surfaces {
				t.Run(surface, func(t *testing.T) {
					// Each surface case owns its store, session and caller state.
					f := newFixture(t)
					id := f.id
					send := f.caller(t, writer, surface)
					read := f.caller(t, 1-writer, surfaces[(index+1)%len(surfaces)])
					caller, requestID := uuid.NewString(), "whole-ü-漢字"
					params := map[string]any{"instance_id": id, "text": "whole\nü 漢字 🙂", "confirmed_delivery": true, "caller_uuid": caller, "delivery_request_id": requestID}
					before := time.Now().Add(-time.Second)
					got := value[map[string]any](t, send("send", params))
					wait, ok := got["wait"].(map[string]any)
					if !ok {
						t.Fatalf("missing wait: %v", got)
					}
					since, err := time.Parse(time.RFC3339Nano, wait["since"].(string))
					if err != nil || since.Before(before) || since.After(time.Now()) {
						t.Fatalf("invalid admission time: %v %v", since, err)
					}
					want := map[string]any{"caller_uuid": caller, "request_id": requestID, "class": "held", "final": false, "reason": "turn_running", "resend_safe": false, "retry_with": "none", "wait": map[string]any{"reason": "turn_running", "since": wait["since"]}, "forced": false, "instance_id": id, "launch_token": nil, "evidence_kind": nil, "outcome": "not_started"}
					exact(t, got, want)
					lookup := map[string]any{"caller_uuid": caller, "delivery_request_id": requestID}
					exact(t, value[map[string]any](t, read("lookup", lookup)), map[string]any{"lookup": "found", "result": want})
					// A same-pair replay goes through the other generation's interface.
					exact(t, value[map[string]any](t, read("send", params)), want)
					facts := value[apitest.BusySendFacts](t, f.drivers[1-writer].call("facts", id))
					if len(facts.Messages) != 1 {
						t.Fatalf("replay duplicated admission: %+v", facts)
					}
					exact(t, string(facts.Messages[0].Text), params["text"])
					exact(t, value[api.GetDeliveryResult](t, read("lookup", map[string]any{"caller_uuid": caller, "delivery_request_id": "never-admitted"})), api.GetDeliveryResult{Lookup: "unknown"})
				})
			}
		})
	}
}

func admitRequest(f *fixture, text, caller, id string) store.BusySendAdmissionRequest {
	confirmed := true
	return store.BusySendAdmissionRequest{InstanceID: f.id, Text: []byte(text), CallerUUID: caller, DeliveryRequestID: id, Confirmed: &confirmed}
}

func awaitBarrier(t *testing.T, p *process, want string) {
	t.Helper()
	var got reply
	p.read(&got)
	if got.Barrier != want {
		t.Fatalf("barrier %q: %+v", want, got)
	}
}

// M3 T1/T3: the losing writer reaches BEGIN while the winner owns SQLite's
// transaction. The complete pair partition is defined once, in this package.
// A already owns the transaction. B must have received native SQLITE_BUSY
// before A is allowed to commit; only production code retries B's BEGIN.
func releaseContendedWriter(t *testing.T, a, b *process) {
	t.Helper()
	awaitBarrier(t, b, "busy")
	a.write(map[string]bool{"release": true})
	b.write(map[string]bool{"release": true})
}

func TestMixedVersionPairAndRevisionRaces(t *testing.T) {
	for first := range builds(t).generations {
		for _, relation := range []string{"distinct", "repeat", "text-conflict", "target-conflict", "last-reservation"} {
			t.Run(builds(t).generations[first].name+"/"+relation, func(t *testing.T) {
				f := newFixture(t)
				a, b := f.drivers[first], f.drivers[1-first]
				caller := uuid.NewString()
				one := admitRequest(f, "one", caller, "pair")
				two := one
				switch relation {
				case "distinct":
					two.DeliveryRequestID = "different"
					two.Text = []byte("two")
				case "text-conflict":
					two.Text = []byte("different")
				case "target-conflict":
					two.InstanceID = "different-" + uuid.NewString()
				case "last-reservation":
					f.id = "last-" + uuid.NewString()
					one.InstanceID = f.id
					two.InstanceID = f.id
					two.DeliveryRequestID = "other"
					value[string](t, a.call("seed", map[string]any{"ID": f.id, "Session": f.session, "Token": f.token, "State": "working", "Reserved": 999}))
				}
				a.write(map[string]any{"op": "admit", "params": one, "before": "INSERT INTO busy_send_messages"})
				awaitBarrier(t, a, "before")
				b.write(map[string]any{"op": "admit", "params": two, "after_busy": true})
				releaseContendedWriter(t, a, b)
				var ra, rb reply
				a.read(&ra)
				b.read(&rb)
				accepted := value[store.BusySendAdmission](t, ra)
				if accepted.Outcome != "admitted" {
					t.Fatalf("winner: %+v", accepted)
				}
				n := 1
				switch relation {
				case "distinct":
					if got := value[store.BusySendAdmission](t, rb); got.Outcome != "admitted" {
						t.Fatalf("distinct: %+v", got)
					}
					n = 2
				case "repeat":
					got := value[store.BusySendAdmission](t, rb)
					exact(t, got, store.BusySendAdmission{Outcome: store.BusySendReplayed, Result: accepted.Result})
				case "last-reservation":
					if rb.Refusal == nil || rb.Refusal.Reason != "held_count" {
						t.Fatalf("reservation: %+v", rb)
					}
					exact(t, rb.Refusal.Limit, &store.BusySendLimit{Actual: 1001, Allowed: 1000})
				default:
					if rb.Refusal == nil || rb.Refusal.Reason != "id_conflict" {
						t.Fatalf("conflict: %+v", rb)
					}
					exact(t, rb.Refusal.Owner, &accepted.Result)
				}
				facts := value[apitest.BusySendFacts](t, b.call("facts", f.id))
				if len(facts.Messages) != n {
					t.Fatalf("messages=%d want=%d", len(facts.Messages), n)
				}
				for i, m := range facts.Messages {
					if m.Seq != int64(i+1) || !m.Reserved {
						t.Fatalf("sequence/reservation: %+v", m)
					}
				}
				wantReserved := int64(n)
				if relation == "last-reservation" {
					wantReserved = 1000
				}
				if facts.Conversations[0].ReservedCount != wantReserved || facts.Conversations[0].AcceptedSeq != int64(n) {
					t.Fatalf("authority: %+v", facts.Conversations)
				}
			})
		}
	}
}

// The COMMIT really lands before its reply is withheld, then lost. The other
// executable reads it while pending and a literal same-pair retry adds nothing.
func TestMixedVersionUncertainAdmission(t *testing.T) {
	for first := range builds(t).generations {
		t.Run(builds(t).generations[first].name, func(t *testing.T) {
			f := newFixture(t)
			a, b := f.drivers[first], f.drivers[1-first]
			req := admitRequest(f, "retained whole text", uuid.NewString(), "uncertain")
			a.write(map[string]any{"op": "admit", "params": req, "after_commit": true, "lose_reply": true})
			awaitBarrier(t, a, "after_commit")
			pair := store.BusySendPair{CallerUUID: req.CallerUUID, DeliveryRequestID: req.DeliveryRequestID}
			found := value[store.BusySendResultLookup](t, b.call("lookup", pair))
			if found.Lookup != "found" || found.Result == nil {
				t.Fatalf("pending COMMIT readback: %+v", found)
			}
			a.write(map[string]bool{"release": true})
			var lost reply
			a.read(&lost)
			if lost.Error == "" || !lost.UnknownCommit || lost.StoreBusy {
				t.Fatalf("lost reply must retain unknown-admission meaning: %+v", lost)
			}
			exact(t, decode[store.BusySendAdmission](t, lost.Result), store.BusySendAdmission{})
			again := value[store.BusySendAdmission](t, b.call("admit", req))
			exact(t, again, store.BusySendAdmission{Outcome: store.BusySendReplayed, Result: *found.Result})
			facts := value[apitest.BusySendFacts](t, b.call("facts", f.id))
			if len(facts.Messages) != 1 || facts.Conversations[0].AcceptedSeq != 1 {
				t.Fatalf("unknown reply duplicated admission: %+v", facts)
			}
			exact(t, string(facts.Messages[0].Text), string(req.Text))
		})
	}
}

func claimParams(f *fixture, dest store.BusySendDestination) map[string]any {
	return map[string]any{"InstanceID": f.id, "Destination": dest, "Boundary": "turn_end", "IssueBudget": time.Hour, "JobBudget": 2 * time.Hour}
}

func claim(t *testing.T, p *process, params map[string]any) *store.BusySendHandover {
	t.Helper()
	c := value[store.BusySendClaim](t, p.call("claim", params))
	if c.Outcome != "claimed" || c.Handover == nil {
		t.Fatalf("claim: %+v", c)
	}
	return c.Handover
}

// M3 T3: the old job is already executing when its captured authority changes.
// Both generations use the production release/claim and owned hook writes.
func TestMixedVersionStaleWork(t *testing.T) {
	for first := range builds(t).generations {
		for _, change := range []string{"owner", "turn-aba", "entity-revision"} {
			t.Run(builds(t).generations[first].name+"/"+change, func(t *testing.T) {
				f := newFixture(t, "waiting")
				a, b := f.drivers[first], f.drivers[1-first]
				value[store.BusySendAdmission](t, a.call("admit", admitRequest(f, "whole pending prompt", uuid.NewString(), "stale")))
				dest := value[store.BusySendDestination](t, a.call("destination", f.id))
				params := claimParams(f, dest)
				h := claim(t, a, params)
				ref := store.BusySendPromptRef{HandoverID: h.HandoverID, Revision: h.Revision}
				if change == "entity-revision" {
					// Same live owner, destination, cursor and dependent facts;
					// only the captured entity revision is wrong (M1 distinction).
					ref.Revision++
				}
				a.write(map[string]any{"op": "issue", "params": map[string]any{"Ref": ref}, "before": "BEGIN IMMEDIATE"})
				awaitBarrier(t, a, "before")
				want := store.BusySendGuardOwner
				var next *store.BusySendHandover
				if change == "owner" {
					r := value[store.BusySendRelease](t, b.call("release", store.BusySendReleaseRequest{HandoverID: h.HandoverID, Revision: h.Revision}))
					if r.Released != 1 || !r.Closed {
						t.Fatalf("release: %+v", r)
					}
					next = claim(t, b, params)
					if next.HandoverID == h.HandoverID || next.HandoverSeq != h.HandoverSeq+1 {
						t.Fatalf("owner did not advance: %+v", next)
					}
					exact(t, next.Prompts[0].MessageIDs, h.Prompts[0].MessageIDs)
				} else if change == "turn-aba" {
					before := value[store.Spawn](t, b.call("row", f.id))
					for _, transition := range []struct{ event, state string }{{"UserPromptSubmit", "working"}, {"Stop", "waiting"}} {
						r := value[store.HookApplied](t, b.call("hook", map[string]string{"ID": f.id, "Event": transition.event, "State": transition.state}))
						if !r.Applied {
							t.Fatalf("owned hook did not apply: %+v", r)
						}
					}
					after := value[store.Spawn](t, b.call("row", f.id))
					if after.State != before.State || after.TurnStarts != before.TurnStarts+1 {
						t.Fatalf("ABA not established: before=%+v after=%+v", before, after)
					}
					want = store.BusySendGuardTurn
				}
				before := value[apitest.BusySendFacts](t, b.call("facts", f.id))
				current := value[store.BusySendHandover](t, b.call("handover", h.HandoverID))
				if change == "entity-revision" {
					exact(t, current, *h)
				}
				a.write(map[string]bool{"release": true})
				var stale reply
				a.read(&stale)
				if stale.Guard == nil || stale.Guard.Failure != want {
					t.Fatalf("stale issue: %+v; want %s", stale, want)
				}
				exact(t, value[apitest.BusySendFacts](t, b.call("facts", f.id)), before)
				exact(t, value[store.BusySendHandover](t, b.call("handover", h.HandoverID)), current)
				if change == "entity-revision" {
					issued := value[store.BusySendIssue](t, b.call("issue", map[string]any{"Ref": store.BusySendPromptRef{HandoverID: current.HandoverID, Revision: current.Revision}}))
					if issued.Revision <= current.Revision {
						t.Fatalf("current reference did not issue: %+v", issued)
					}
				}
				if next != nil {
					late := a.call("release", store.BusySendReleaseRequest{HandoverID: h.HandoverID, Revision: h.Revision})
					if late.Guard == nil || late.Guard.Failure != store.BusySendGuardOwner {
						t.Fatalf("stale release: %+v", late)
					}
					r := value[store.BusySendCompletionResult](t, a.call("evidence", store.BusySendPromptEvidence{HandoverID: h.HandoverID, LaunchID: dest.LaunchID, Order: store.BusySendOrderOrderedAcceptance}))
					if r.Recorded {
						t.Fatal("unissued stale handover accepted evidence")
					}
					exact(t, value[apitest.BusySendFacts](t, b.call("facts", f.id)), before)
					value[store.BusySendIssue](t, b.call("issue", map[string]any{"Ref": store.BusySendPromptRef{HandoverID: next.HandoverID, Revision: next.Revision}}))
				}
			})
		}
	}
}

// M3 T1/T3: an intent's actual commit is pending when the other generation
// appends a Message and ends the frozen force call. No delayed reply grants a
// new issue, and the late append never joins the force's original membership.
func TestMixedVersionFrozenForce(t *testing.T) {
	for first := range builds(t).generations {
		t.Run(builds(t).generations[first].name, func(t *testing.T) {
			f := newFixture(t)
			a, b := f.drivers[first], f.drivers[1-first]
			value[store.BusySendAdmission](t, a.call("admit", admitRequest(f, "before force", uuid.NewString(), "frozen")))
			c := value[store.BusySendForceClaim](t, a.call("force-claim", map[string]any{"ID": f.id, "ForceID": uuid.NewString(), "Owner": uuid.NewString()}))
			if c.Handover == nil || c.Force.Outcome != "running" {
				t.Fatalf("force: %+v", c)
			}
			h := c.Handover
			ref := store.BusySendPromptRef{HandoverID: h.HandoverID, Revision: h.Revision}
			a.write(map[string]any{"op": "issue", "params": map[string]any{"Ref": ref, "Force": c.Ref}, "after_commit": true, "lose_reply": true})
			awaitBarrier(t, a, "after_commit")
			value[store.BusySendAdmission](t, b.call("admit", admitRequest(f, "after freeze", uuid.NewString(), "later")))
			end := value[store.BusySendForceEnd](t, b.call("force-end", map[string]any{"Ref": c.Ref}))
			if !end.Ended || end.Force.Outcome != "pending" {
				t.Fatalf("unknown intent must keep pending force: %+v", end)
			}
			exact(t, end.Force.CarriedPairs, c.Force.CarriedPairs)
			facts := value[apitest.BusySendFacts](t, b.call("facts", f.id))
			if len(facts.Messages) != 2 {
				t.Fatalf("messages: %+v", facts)
			}
			a.write(map[string]bool{"release": true})
			var lost reply
			a.read(&lost)
			if !lost.UnknownCommit || lost.StoreBusy {
				t.Fatalf("intent reply lost its uncertainty: %+v", lost)
			}
			stale := a.call("issue", map[string]any{"Ref": ref, "Force": c.Ref})
			if stale.Guard == nil || stale.Guard.Failure != store.BusySendGuardOwner {
				t.Fatalf("ended force authorized another issue: %+v", stale)
			}
			exact(t, value[apitest.BusySendFacts](t, b.call("facts", f.id)), facts)
		})
	}
}

// M3 T3: retention competes with the literal retry and append, and later
// completion/evidence cannot restore cleared text or a newer immutable Result.
func TestMixedVersionRetentionRace(t *testing.T) {
	for first := range builds(t).generations {
		for _, operation := range []string{"repeat", "append"} {
			t.Run(builds(t).generations[first].name+"/"+operation, func(t *testing.T) {
				f := newFixture(t, "waiting")
				a, b := f.drivers[first], f.drivers[1-first]
				req := admitRequest(f, "retention body", uuid.NewString(), "owned forever")
				value[store.BusySendAdmission](t, a.call("admit", req))
				dest := value[store.BusySendDestination](t, a.call("destination", f.id))
				h := claim(t, a, claimParams(f, dest))
				is := value[store.BusySendIssue](t, a.call("issue", map[string]any{"Ref": store.BusySendPromptRef{HandoverID: h.HandoverID, Revision: h.Revision}}))
				completion := store.BusySendCompletion{Ref: store.BusySendPromptRef{HandoverID: h.HandoverID, Revision: is.Revision}, Output: store.BusySendOutputComplete, Order: store.BusySendOrderEmissionComplete, UnprovenBoundary: true}
				value[store.BusySendCompletionResult](t, a.call("complete", completion))
				pair := store.BusySendPair{CallerUUID: req.CallerUUID, DeliveryRequestID: req.DeliveryRequestID}
				prior := value[store.BusySendResultLookup](t, b.call("lookup", pair))
				if prior.Result == nil || !prior.Result.Final {
					t.Fatalf("missing final Result: %+v", prior)
				}
				cutoff := time.Now().Add(time.Hour)
				a.write(map[string]any{"op": "expire", "params": map[string]any{"Cutoff": cutoff, "At": cutoff.Add(time.Hour)}, "before": "UPDATE busy_send_messages AS m SET retention"})
				awaitBarrier(t, a, "before")
				next := req
				if operation == "append" {
					next.DeliveryRequestID = "new pair"
					next.Text = []byte("new held body")
				}
				b.write(map[string]any{"op": "admit", "params": next, "after_busy": true})
				releaseContendedWriter(t, a, b)
				var expired, raced reply
				a.read(&expired)
				b.read(&raced)
				if expired.Error != "" {
					t.Fatal(expired.Error)
				}
				if operation == "repeat" {
					if raced.Refusal == nil || raced.Refusal.Reason != "retention_expired" {
						t.Fatalf("expired pair stolen: %+v", raced)
					}
				} else {
					if r := value[store.BusySendAdmission](t, raced); r.Outcome != "admitted" {
						t.Fatalf("append: %+v", r)
					}
				}
				exact(t, value[store.BusySendResultLookup](t, b.call("lookup", pair)), store.BusySendResultLookup{Lookup: store.BusySendLookupRetentionExpired})
				before := value[apitest.BusySendFacts](t, b.call("facts", f.id))
				value[store.BusySendCompletionResult](t, a.call("complete", completion))
				value[store.BusySendCompletionResult](t, a.call("evidence", store.BusySendPromptEvidence{HandoverID: h.HandoverID, LaunchID: dest.LaunchID, Admission: store.BusySendAdmissionPromptSighting}))
				after := value[apitest.BusySendFacts](t, b.call("facts", f.id))
				exact(t, after.Messages, before.Messages)
				if len(after.Messages[0].Text) != 0 {
					t.Fatal("late local value restored expired text")
				}
			})
		}
	}
}

// M3 T3: deletion owns a transaction while a same-pair send is in flight.
// A new incarnation of the same instance ID cannot acquire the old pair.
func TestMixedVersionDeleteReuse(t *testing.T) {
	for first := range builds(t).generations {
		t.Run(builds(t).generations[first].name, func(t *testing.T) {
			f := newFixture(t)
			a, b := f.drivers[first], f.drivers[1-first]
			req := admitRequest(f, "old incarnation", uuid.NewString(), "permanent")
			value[store.BusySendAdmission](t, a.call("admit", req))
			dest := value[store.BusySendDestination](t, a.call("destination", f.id))
			h := claim(t, a, claimParams(f, dest))
			a.write(map[string]any{"op": "delete", "params": f.id, "before": "DELETE FROM spawns"})
			awaitBarrier(t, a, "before")
			b.write(map[string]any{"op": "admit", "params": req, "after_busy": true})
			releaseContendedWriter(t, a, b)
			var deleted, replay reply
			a.read(&deleted)
			b.read(&replay)
			value[store.CondResult](t, deleted)
			owner := value[store.BusySendAdmission](t, replay)
			if owner.Outcome != "replayed" || owner.Result.Class != "not_started" || !owner.Result.Final || owner.Result.Reason != "record_deleted" {
				t.Fatalf("deletion replay: %+v", owner)
			}
			value[string](t, b.call("seed", map[string]any{"ID": f.id, "Session": uuid.NewString(), "Token": uuid.NewString(), "State": "working", "Wake": filepath.Join(f.home, "replacement-wake")}))
			exact(t, value[store.BusySendAdmission](t, b.call("admit", req)), owner)
			conflict := req
			conflict.Text = []byte("replacement text")
			bad := b.call("admit", conflict)
			if bad.Refusal == nil || bad.Refusal.Reason != "id_conflict" {
				t.Fatalf("reuse stole pair: %+v", bad)
			}
			exact(t, bad.Refusal.Owner, &owner.Result)
			fresh := req
			fresh.DeliveryRequestID = "fresh"
			value[store.BusySendAdmission](t, b.call("admit", fresh))
			facts := value[apitest.BusySendFacts](t, b.call("facts", f.id))
			if len(facts.Messages) != 2 {
				t.Fatalf("reuse duplicated old admission: %+v", facts)
			}
			stale := a.call("issue", map[string]any{"Ref": store.BusySendPromptRef{HandoverID: h.HandoverID, Revision: h.Revision}})
			if stale.Guard == nil || stale.Guard.Failure != store.BusySendGuardOwner {
				t.Fatalf("deleted owner issued into replacement: %+v", stale)
			}
			oldEvent := value[store.BusySendClaim](t, a.call("claim", claimParams(f, dest)))
			if oldEvent.Outcome != "ineligible" || oldEvent.Handover != nil {
				t.Fatalf("old event claimed replacement Launch: %+v", oldEvent)
			}
			exact(t, value[apitest.BusySendFacts](t, b.call("facts", f.id)), facts)
			cutoff := time.Now().Add(time.Hour)
			expired := a.call("expire", map[string]any{"Cutoff": cutoff, "At": cutoff.Add(time.Hour)})
			if expired.Error != "" {
				t.Fatal(expired.Error)
			}
			again := b.call("admit", req)
			if again.Refusal == nil || again.Refusal.Reason != "retention_expired" {
				t.Fatalf("expired old pair was reused: %+v", again)
			}
		})
	}
}

// Complete closed-enum catalogue, plus the extensible reason catalogues. Only
// these internal records use SQL fixture writes. The other generation consumes
// them through production record readers or the production decision they drive.
func TestMixedVersionPermanentEnums(t *testing.T) {
	type column struct{ table, key, id, name, codes string }
	columns := []column{
		{"spawns", "instance_id", "rec", "busy_send", "not_covered covered"},
		{"busy_conversations", "conversation_id", "rec-conv-2", "publication", "candidate published retired abandoned"},
		{"busy_conversations", "conversation_id", "rec-conv-2", "busy_send", "not_covered covered"},
		{"busy_conversations", "conversation_id", "rec-conv-2", "default_send_mode", "wait_turn steer_turn"},
		{"busy_conversations", "conversation_id", "rec-conv-1", "retired_reason", "conversation_replaced record_deleted"},
		{"busy_send_launches", "launch_id", "rec-launch-2", "busy_send", "not_covered covered"},
		{"busy_send_launches", "launch_id", "rec-launch-2", "kind", "spawn reuse resume migrated"},
		{"busy_send_launches", "launch_id", "rec-launch-2", "phase", "candidate uncertain failed current superseded"},
		{"busy_send_launches", "launch_id", "rec-launch-2", "route_kind", "claude_hooks codex_app_server"},
		{"busy_send_launches", "launch_id", "rec-launch-2", "override_send_mode", "wait_turn steer_turn"},
		{"busy_send_launches", "launch_id", "rec-launch-1", "end_cause", "process_exited launch_published creation_failed record_deleted candidate_replaced agent_missing"},
		{"busy_send_launch_processes", "launch_id", "rec-launch-1", "role", "agent app_server tui helper"},
		{"busy_send_messages", "message_id", "rec-msg-2", "send_mode", "wait_turn steer_turn"},
		{"busy_send_messages", "message_id", "rec-msg-2", "place", "held claimed issued closed"},
		{"busy_send_messages", "message_id", "rec-msg-2", "retention", "retained expired"},
		{"busy_send_handovers", "handover_id", "rec-handover-2", "route_kind", "claude_hooks codex_app_server"},
		{"busy_send_handovers", "handover_id", "rec-handover-2", "boundary", "turn_end tool_boundary force"},
		{"busy_send_handovers", "handover_id", "rec-handover-2", "state", "open closed"},
		{"busy_send_prompts", "handover_id", "rec-handover-2", "emission", "none intent issued released"},
		{"busy_send_prompts", "handover_id", "rec-handover-2", "transport", "written partial uncertain unwritten"},
		{"busy_send_prompts", "handover_id", "rec-handover-2", "order_kind", "ordered_acceptance exact_receipt emission_complete"},
		{"busy_send_prompts", "handover_id", "rec-handover-2", "admission_kind", "prompt_sighting rpc_success prompt_response native_signal transcript_match exact_receipt"},
		{"busy_send_prompts", "handover_id", "rec-handover-2", "evidence_kind", "codex_item_started codex_thread_items"},
		{"busy_send_prompts", "handover_id", "rec-handover-2", "negative_kind", "not_submitted no_admission"},
		{"busy_send_forces", "force_id", "rec-force-1", "retention", "retained expired"},
		{"busy_send_pause_claims", "claim_id", "rec-pause-2", "signal_kind", "sigterm server_shutdown"},
		{"busy_send_pause_claims", "claim_id", "rec-pause-2", "state", "standing released ended"},
		{"busy_send_pause_claims", "claim_id", "rec-pause-2", "call_outcome", "paused refused exit_uncertain failed"},
		{"busy_send_pause_claims", "claim_id", "rec-pause-2", "closed_by", "refused_before_signal abandoned_before_signal launch_end launch_published"},
		{"busy_send_pause_claims", "claim_id", "rec-pause-0", "call_cause", "future_pause_cause"},
	}
	for writer := range builds(t).generations {
		for _, col := range columns {
			for _, code := range strings.Fields(col.codes) {
				t.Run(builds(t).generations[writer].name+"/"+col.table+"/"+col.name+"/"+code, func(t *testing.T) {
					_, helpers, _ := recordFixture(t, writer)
					fields := map[string]any{col.name: code}
					put := func(k string, v any) { fields[k] = v }
					switch col.table + "." + col.name {
					case "busy_conversations.publication":
						put("published_at_ms", nil)
						put("retired_at_ms", nil)
						put("retired_reason", nil)
						if code == "published" || code == "retired" {
							put("published_at_ms", 1000)
						}
						if code == "retired" {
							put("retired_at_ms", 4000)
							put("retired_reason", "record_deleted")
						}
					case "busy_conversations.retired_reason":
						put("replaced_by", nil)
					case "busy_send_launches.phase":
						if code == "failed" {
							put("ended_at_ms", 4000)
							put("end_cause", "creation_failed")
						}
						if code == "superseded" {
							put("ended_at_ms", 4000)
							put("end_cause", "launch_published")
						}
					case "busy_send_launches.end_cause":
						if code == "creation_failed" {
							put("phase", "failed")
						}
					case "busy_send_messages.place":
						if code == "held" || code == "closed" {
							put("handover_id", nil)
						}
					case "busy_send_messages.retention":
						if code == "expired" {
							for _, k := range []string{"text", "text_sha256", "result_class", "result_reason", "wait_reason", "wait_since_ms", "evidence_kind", "record_id", "record_sha256"} {
								put(k, nil)
							}
							put("result_final", 1)
							put("final_at_ms", 4000)
							put("expired_at_ms", 5000)
						}
					case "busy_send_handovers.boundary":
						if code != "force" {
							put("forced", 0)
							put("force_id", nil)
						}
					case "busy_send_handovers.state":
						if code == "closed" {
							put("closed_at_ms", 4000)
						}
					case "busy_send_prompts.emission":
						put("intent_at_ms", nil)
						put("issued_at_ms", nil)
						put("released_at_ms", nil)
						if code == "intent" || code == "issued" {
							put("intent_at_ms", 3090)
						}
						if code == "issued" {
							put("issued_at_ms", 3095)
						}
						if code == "released" {
							put("released_at_ms", 4000)
						}
					case "busy_send_prompts.order_kind":
						put("order_at_ms", 3100)
					case "busy_send_prompts.admission_kind":
						put("admitted_at_ms", 3100)
					case "busy_send_prompts.evidence_kind":
						put("receipt_at_ms", 3100)
					case "busy_send_prompts.negative_kind":
						put("negative_at_ms", 3100)
					case "busy_send_forces.retention":
						if code == "expired" {
							put("expired_at_ms", 5000)
						}
					case "busy_send_pause_claims.state":
						if code == "released" {
							put("closed_by", "abandoned_before_signal")
							put("closed_at_ms", 3450)
						}
						if code == "ended" {
							put("closed_by", "launch_end")
							put("closed_at_ms", 3450)
						}
					case "busy_send_pause_claims.call_outcome":
						put("signal_intent_at_ms", 3410)
						put("call_outcome_at_ms", 3420)
						if code == "refused" {
							put("state", "released")
							put("closed_by", "refused_before_signal")
							put("closed_at_ms", 3420)
						}
					case "busy_send_pause_claims.closed_by":
						put("closed_at_ms", 3450)
						put("state", "ended")
						if code == "refused_before_signal" || code == "abandoned_before_signal" {
							put("state", "released")
						}
						if code == "refused_before_signal" {
							put("call_outcome", "refused")
							put("call_outcome_at_ms", 3450)
						}
					}
					fixtureSet(t, helpers[writer], col.table, col.key, col.id, fields)
					// One decoded projection per production reader. Companion
					// fields are independently expected, not copied from SQL.
					reader := ""
					var want any
					published := map[string]any{"found": true, "conversation": "rec-conv-2", "launch": "rec-launch-2",
						"coverage": "covered", "launch_coverage": "covered", "send_mode": "wait_turn", "route_kind": "claude_hooks"}
					switch col.table {
					case "spawns":
						reader, want = "spawn", map[string]any{"busy_send": code}
					case "busy_conversations", "busy_send_launches":
						reader, want = "published", published
						switch col.name {
						case "publication", "phase":
							if code != "published" && code != "current" {
								want = map[string]any{"found": false}
							}
						case "busy_send":
							fixtureSet(t, helpers[writer], "spawns", "instance_id", "rec", map[string]any{"busy_send": code})
							published["launch_coverage"] = code
							if col.table == "busy_conversations" {
								fixtureSet(t, helpers[writer], "busy_send_launches", "launch_id", "rec-launch-2", map[string]any{"busy_send": code})
								published["coverage"] = code
							}
						case "default_send_mode":
							published["send_mode"] = code
						case "route_kind", "kind":
							r := map[string]any{"kind": "resume", "route_kind": "claude_hooks"}
							r[col.name] = code
							reader, want = "route", r
						case "retired_reason":
							fixtureSet(t, helpers[writer], "busy_send_messages", "message_id", "rec-msg-1", map[string]any{"conversation_id": "rec-conv-1"})
							r := map[string]any{"deleted": false, "outcome": "", "reason": "", "conversation": ""}
							if code == "record_deleted" {
								r = map[string]any{"deleted": true, "outcome": "refused", "reason": "record_deleted", "conversation": "rec-conv-1"}
							}
							reader, want = "deleted-force", r
						case "end_cause":
							ids := []string{"rec-launch-1", "rec-launch-2"}
							if code == "creation_failed" || code == "candidate_replaced" {
								ids = []string{"rec-launch-2"}
							}
							reader, want = "exit-scope", ids
						case "override_send_mode":
							// Resume publication consumes the candidate override.
							// The old ended Launch remains current until that write.
							fixtureSet(t, helpers[writer], "busy_send_launches", "launch_id", "rec-launch-2", map[string]any{"phase": "candidate"})
							fixtureSet(t, helpers[writer], "busy_send_launches", "launch_id", "rec-launch-1", map[string]any{"phase": "current"})
							published["send_mode"] = code
							reader = "publish"
						default:
							t.Fatalf("no production consumer for %+v", col)
						}
					case "busy_send_launch_processes":
						reader, want = "processes", []map[string]any{{"Role": code, "PID": 100, "StartTime": "s1", "PIDNamespace": "", "Endpoint": ""}}
					case "busy_send_messages":
						reader, want = "message", map[string]any{"lookup": "found", "send_mode": "steer_turn"}
						if col.name == "send_mode" {
							want = map[string]any{"lookup": "found", "send_mode": code}
						}
						if col.name == "retention" && code == "expired" {
							want = map[string]any{"lookup": "retention_expired"}
						}
						if col.name == "place" {
							fixtureSet(t, helpers[writer], "busy_send_messages", "message_id", "rec-msg-4", fields)
							ids := []string{"rec-msg-1"}
							if code == "held" {
								ids = append(ids, "rec-msg-2", "rec-msg-4")
							}
							reader, want = "custody", map[string]any{"held": ids, "claimed": code == "claimed", "issued": code == "issued"}
						}
					case "busy_send_handovers":
						r := map[string]any{"route_kind": "claude_hooks", "boundary": "force", "open": true}
						if col.name == "state" {
							r["open"] = code == "open"
						} else {
							r[col.name] = code
						}
						reader, want = "handover", r
					case "busy_send_prompts":
						r := map[string]any{"emission": "issued", "transport": "uncertain", "order_kind": "", "admission_kind": "", "evidence_kind": "", "negative_kind": ""}
						r[col.name] = code
						reader, want = "prompt", r
					case "busy_send_forces":
						reader, want = "force", map[string]any{"found": true, "retention": code}
					case "busy_send_pause_claims":
						r := map[string]any{"found": true, "signal_kind": "sigterm", "state": "standing", "call_outcome": "", "call_cause": "", "closed_by": ""}
						if col.id == "rec-pause-0" {
							r["state"], r["call_outcome"], r["closed_by"] = "released", "refused", "refused_before_signal"
						}
						r[col.name] = code
						if col.name == "state" {
							if code == "released" {
								r["closed_by"] = "abandoned_before_signal"
							}
							if code == "ended" {
								r["closed_by"] = "launch_end"
							}
						}
						if col.name == "call_outcome" && code == "refused" {
							r["state"], r["closed_by"] = "released", "refused_before_signal"
						}
						if col.name == "closed_by" {
							r["state"] = "ended"
							if code == "refused_before_signal" || code == "abandoned_before_signal" {
								r["state"] = "released"
							}
							if code == "refused_before_signal" {
								r["call_outcome"] = "refused"
							}
						}
						reader, want = "pause", r
					default:
						t.Fatalf("no production reader for %+v", col)
					}
					exact(t, value[any](t, helpers[1-writer].call("record-read", map[string]string{"Reader": reader, "ID": col.id})), want)
				})
			}
		}
	}
}

// Public Result shapes use independent JSON expectations, including omissions,
// nulls, both receipt kinds, a partial transport and an unknown extensible code.
// The table is shared across complementary surfaces, never replayed in Docker.
func TestMixedVersionResultShapes(t *testing.T) {
	type shapeCase struct {
		name, class, reason, wait, outcome, evidence string
		final, bound, forced, partial, earlier       bool
	}
	shapes := []shapeCase{
		{"held", "held", "turn_running", "turn_running", "not_started", "", false, false, false, false, false},
		{"queued", "queued", "earlier_message", "earlier_message", "submission_unconfirmed", "", false, true, true, false, true},
		{"partial", "sent_unconfirmed", "handover_in_progress", "handover_in_progress", "partial_input_unconfirmed", "", false, true, false, true, false},
		{"final-unproven", "sent_unconfirmed", "handed_over_not_proven", "", "submission_unconfirmed", "", true, true, true, false, false},
		{"receipt-item", "delivered_confirmed", "exact_receipt", "", "submitted", "codex_item_started", true, true, false, false, false},
		{"receipt-items", "delivered_confirmed", "exact_receipt", "", "submitted", "codex_thread_items", true, true, true, false, false},
		{"not-started", "not_started", "record_deleted", "", "not_started", "", true, false, false, false, false},
		{"future-reason", "held", "future_result", "future_wait", "not_started", "", false, false, false, false, false},
	}
	// These are the remaining public stable codes; they are projected once
	// here, not repeated in the private enum readback table.
	for _, reason := range strings.Fields("dialog compaction agent_starting launch_ended route_unavailable readiness_unknown input_unresolved awaiting_proof") {
		shapes = append(shapes, shapeCase{name: "wait-" + reason, class: "held", reason: reason, wait: reason, outcome: "not_started"})
	}
	for _, reason := range strings.Fields("confirmation_off wait_turn_raced launch_ended admission_unknown conversation_replaced") {
		c := shapeCase{name: "final-" + reason, class: "sent_unconfirmed", reason: reason, final: true, bound: true, outcome: "submission_unconfirmed"}
		if reason == "conversation_replaced" {
			c.class, c.outcome, c.bound = "not_started", "not_started", false
		}
		shapes = append(shapes, c)
	}
	for writer := range builds(t).generations {
		for index, shape := range shapes {
			t.Run(builds(t).generations[writer].name+"/"+shape.name, func(t *testing.T) {
				f, helpers, ids := recordFixture(t, writer)
				fields := map[string]any{"result_class": shape.class, "result_reason": shape.reason, "result_final": shape.final, "final_at_ms": nil, "forced": shape.forced, "wait_reason": nil, "wait_since_ms": nil, "wait_earlier_message_id": nil, "bound_launch_token": nil, "bound_session_id": nil, "evidence_kind": nil, "record_id": nil, "record_sha256": nil}
				want := map[string]any{"caller_uuid": ids["caller"], "request_id": "req-2", "class": shape.class, "final": shape.final, "reason": shape.reason, "resend_safe": shape.class == "not_started", "retry_with": "none", "wait": nil, "forced": shape.forced, "instance_id": "rec", "launch_token": nil, "evidence_kind": nil, "outcome": shape.outcome}
				if shape.final {
					fields["final_at_ms"] = 4000
				}
				if shape.class == "not_started" {
					want["retry_with"] = "new_id"
				}
				if shape.wait != "" {
					fields["wait_reason"] = shape.wait
					fields["wait_since_ms"] = 3100
					want["wait"] = map[string]any{"reason": shape.wait, "since": "1970-01-01T00:00:03.1Z"}
				}
				if shape.earlier {
					fields["wait_earlier_message_id"] = "rec-msg-1"
					want["wait"].(map[string]any)["earlier"] = map[string]any{"caller_uuid": ids["caller"], "request_id": "req-1"}
				}
				if shape.bound {
					fields["bound_launch_token"] = ids["token"]
					fields["bound_session_id"] = "sess-2"
					want["launch_token"] = ids["token"]
					want["session_id"] = "sess-2"
				}
				if shape.evidence != "" {
					fields["evidence_kind"] = shape.evidence
					fields["record_id"] = "receipt-2"
					fields["record_sha256"] = strings.Repeat("b", 64)
					want["evidence_kind"] = shape.evidence
					want["record_id"] = "receipt-2"
					want["record_sha256"] = strings.Repeat("b", 64)
				}
				fixtureSet(t, helpers[writer], "busy_send_messages", "message_id", "rec-msg-2", fields)
				if shape.partial {
					fixtureSet(t, helpers[writer], "busy_send_prompts", "handover_id", "rec-handover-2", map[string]any{"transport": "partial"})
				}
				surface := []string{"cli", "mcp", "go", "ts"}[index%4]
				read := f.caller(t, 1-writer, surface)
				exact(t, value[map[string]any](t, read("lookup", map[string]any{"caller_uuid": ids["caller"], "delivery_request_id": "req-2"})), map[string]any{"lookup": "found", "result": want})
				// An existing tombstone retains ownership without inventing a Result.
				exact(t, value[map[string]any](t, read("lookup", map[string]any{"caller_uuid": ids["caller"], "delivery_request_id": "req-3"})), map[string]any{"lookup": "retention_expired", "result": nil})
			})
		}
	}
}

func TestMixedVersionForceResultShapes(t *testing.T) {
	cases := []struct{ outcome, reason string }{{"running", ""}, {"pending", "awaiting_admission"}, {"handed_over", "admitted"}, {"refused", "no_held_messages"}, {"failed", "admission_unknown"}}
	for _, reason := range strings.Fields("transcript_match handover_in_progress earlier_message input_unresolved agent_starting launch_ended route_unavailable not_submitted not_issued agent_not_covered record_deleted compaction turn_running dialog future_force") {
		outcome := "refused"
		if reason == "transcript_match" {
			outcome = "handed_over"
		}
		cases = append(cases, struct{ outcome, reason string }{outcome, reason})
	}
	for writer := range builds(t).generations {
		for index, shape := range cases {
			outcome := shape.outcome
			t.Run(builds(t).generations[writer].name+"/"+outcome+"/"+shape.reason, func(t *testing.T) {
				f, helpers, ids := recordFixture(t, writer)
				forceID := uuid.NewString()
				value[string](t, helpers[writer].call("fixture-force-id", map[string]string{"ID": forceID}))
				// One member's Result expired while its permanent pair survived.
				fixtureSet(t, helpers[writer], "busy_send_messages", "message_id", "rec-msg-2", map[string]any{"retention": "expired", "text": nil, "text_sha256": nil, "result_class": nil, "result_reason": nil, "evidence_kind": nil, "record_id": nil, "record_sha256": nil, "wait_reason": nil, "wait_since_ms": nil, "result_final": 1, "final_at_ms": 4000, "expired_at_ms": 5000})
				fixtureSet(t, helpers[writer], "busy_send_messages", "message_id", "rec-msg-4", map[string]any{"result_class": "not_started", "result_reason": "record_deleted", "result_final": 1, "final_at_ms": 4000, "wait_reason": nil, "wait_since_ms": nil})
				fields := map[string]any{"outcome": outcome, "reason": shape.reason, "ended_at_ms": 3300, "call_ended_at_ms": 3300}
				var ended any = "1970-01-01T00:00:03.3Z"
				if outcome == "running" || outcome == "pending" {
					fields["ended_at_ms"] = nil
					ended = nil
				}
				if outcome == "running" {
					fields["call_ended_at_ms"] = nil
					fields["reason"] = ""
				}
				fixtureSet(t, helpers[writer], "busy_send_forces", "force_id", forceID, fields)
				result := map[string]any{"caller_uuid": ids["caller"], "request_id": "req-4", "class": "not_started", "final": true, "reason": "record_deleted", "resend_safe": true, "retry_with": "new_id", "wait": nil, "forced": true, "instance_id": "rec", "launch_token": nil, "evidence_kind": nil, "outcome": "not_started"}
				want := map[string]any{"force_id": forceID, "outcome": outcome, "reason": fields["reason"], "started_at": "1970-01-01T00:00:03.07Z", "ended_at": ended, "carried_pairs": []any{map[string]any{"caller_uuid": ids["caller"], "request_id": "req-2"}, map[string]any{"caller_uuid": ids["caller"], "request_id": "req-4"}}, "carried_results": []any{result}}
				read := f.caller(t, 1-writer, []string{"cli", "mcp", "go", "ts"}[index%4])
				exact(t, value[map[string]any](t, read("force-lookup", map[string]any{"force_id": forceID})), map[string]any{"lookup": "found", "force_result": want})
				exact(t, value[map[string]any](t, read("force-lookup", map[string]any{"force_id": uuid.NewString()})), map[string]any{"lookup": "unknown", "force_result": nil})
				if ended != nil {
					fixtureSet(t, helpers[writer], "busy_send_forces", "force_id", forceID, map[string]any{"retention": "expired", "expired_at_ms": 6000})
					exact(t, value[map[string]any](t, read("force-lookup", map[string]any{"force_id": forceID})), map[string]any{"lookup": "retention_expired", "force_result": nil})
				}
			})
		}
	}
}

func TestMixedVersionUnconfirmedOmissions(t *testing.T) {
	for writer := range builds(t).generations {
		t.Run(builds(t).generations[writer].name, func(t *testing.T) {
			f := newFixture(t)
			send := f.caller(t, writer, []string{"cli", "ts"}[writer])
			read := f.caller(t, 1-writer, []string{"mcp", "go"}[writer])
			first := value[map[string]any](t, send("send", map[string]any{"instance_id": f.id, "text": "unconfirmed first", "confirmed_delivery": false}))
			wait, ok := first["wait"].(map[string]any)
			if !ok {
				t.Fatalf("wait missing: %v", first)
			}
			want := map[string]any{"class": "held", "final": false, "reason": "turn_running", "resend_safe": false, "retry_with": "none", "wait": map[string]any{"reason": "turn_running", "since": wait["since"]}, "forced": false, "instance_id": f.id, "launch_token": nil, "evidence_kind": nil, "outcome": "not_started"}
			exact(t, first, want)
			caller := uuid.NewString()
			second := value[map[string]any](t, read("send", map[string]any{"instance_id": f.id, "text": "confirmed second", "confirmed_delivery": true, "caller_uuid": caller, "delivery_request_id": "second"}))
			w, ok := second["wait"].(map[string]any)
			if !ok {
				t.Fatalf("wait missing: %v", second)
			}
			want["caller_uuid"], want["request_id"], want["reason"] = caller, "second", "earlier_message"
			want["wait"] = map[string]any{"reason": "earlier_message", "since": w["since"]}
			exact(t, second, want)
		})
	}
}
