package qualification_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

// A single ASCII-keyed unit journal, parsed by ci's real parser. This is a
// synthetic evidence-shape builder, never a provider recording or live proof.
func unitJournal(t *testing.T, sid, id, role, text string, at time.Time) ([]byte, signals.Transcript) {
	t.Helper()
	message := map[string]any{"role": role, "content": []any{map[string]any{"type": "text", "text": text}}}
	var canonical bytes.Buffer
	e := json.NewEncoder(&canonical)
	e.SetEscapeHTML(false)
	if err := e.Encode(message); err != nil {
		t.Fatal(err)
	}
	hash := runner.Digest(bytes.TrimSuffix(canonical.Bytes(), []byte("\n")))
	var journal bytes.Buffer
	enc := json.NewEncoder(&journal)
	for _, row := range []any{map[string]any{"type": "api-request-blob", "sessionId": sid, "hash": hash, "message": message}, map[string]any{"type": "api-request", "sessionId": sid, "id": id, "timestamp": at, "querySource": "repl_main_thread", "keep": 0, "tail": []string{hash}}} {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := signals.ParseTranscript(journal.Bytes(), sid)
	if err != nil {
		t.Fatal(err)
	}
	return journal.Bytes(), tr
}

func unitEscRecoveries(t *testing.T) (*qualificationfix.Fixture, q.Capture) {
	t.Helper()
	f := qualificationfix.New(t, "esc-gap")
	c := f.Capture()
	c.Manifest.Cases = nil
	c.Commands = map[string]runner.Command{}
	c.Hooks = nil
	c.Owners = nil
	at := func(seconds int64) time.Time { return time.Unix(1800000000+seconds, 0).UTC() }
	iid, sid := "unit-esc-parent", "unit-esc-session"
	old := q.OwnerEvidence{Owner: runner.Owned{InstanceID: iid, SessionID: sid, LaunchToken: "old-launch", Started: at(0), StoppedAt: at(101), Exited: true}, SnapshotRef: "unit/old-store", TranscriptRef: "unit/old-journal"}
	newOwner := q.OwnerEvidence{Owner: runner.Owned{InstanceID: iid, SessionID: sid, LaunchToken: "new-launch", Started: at(110)}, SnapshotRef: "unit/new-store", TranscriptRef: "unit/new-journal"}
	_, typed := unitJournal(t, sid, "typed", "user", "unit fresh typed recovery", at(41))
	old.Transcript = typed
	c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: "E2", InstanceID: iid, Text: "unit fresh typed recovery", Mode: "typed", Started: at(40), Ended: at(40)})
	for i, id := range []string{"E3", "E4"} {
		owner := &old
		issue := int64(46)
		if i == 1 {
			owner = &newOwner
			issue = 112
		}
		text := "unit carried " + id
		body := "[agent-director: message 1 of 1]\n" + text
		owner.Snapshot = runner.Snapshot{InstanceID: iid, Tables: map[string][]runner.Row{"messages": {{"message_id": id, "text": text}}, "members": {{"message_id": id, "handover_id": id, "member_index": 0, "prompt_index": 0}}, "handovers": {{"handover_id": id, "launch_token": owner.Owner.LaunchToken, "boundary": "turn_end"}}, "prompts": {{"handover_id": id, "prompt_index": 0, "message_count": int64(1), "emission": "issued", "transport": "written", "issued_at_ms": at(issue).UnixMilli(), "transport_at_ms": at(issue + 1).UnixMilli(), "admission_kind": "prompt_sighting", "admitted_at_ms": at(issue + 2).UnixMilli(), "order_kind": "ordered_acceptance", "order_at_ms": at(issue + 2).UnixMilli()}}}}
		_, tr := unitJournal(t, sid, id, "user", signals.NativeWake(q.Transform(body, "wait_turn")), at(issue+3))
		owner.Transcript.Requests = append(owner.Transcript.Requests, tr.Requests...)
		c.Manifest.Cases = append(c.Manifest.Cases, runner.Case{ID: id, InstanceID: iid, Text: text, Mode: "wait_turn", Started: at(issue - 10), Ended: at(issue - 9)})
	}
	// Same-conversation resume retains historical requests. Timing ownership
	// must still use the Launch that owned the send, not the newest Launch.
	newOwner.Transcript.Requests = append(append([]signals.Request{}, old.Transcript.Requests...), newOwner.Transcript.Requests...)
	c.Owners = []q.OwnerEvidence{old, newOwner}
	c.Hooks = []q.HookFact{{Receipt: runner.Receipt{Event: "Stop", Received: at(45)}, Payload: runner.Row{"session_id": sid}, Reference: "unit/typed-stop"}}
	c.Commands["unit/pause"] = runner.Command{Name: "esc-keyless-pause", Stdout: "unit/pause", Argv: []string{"ad", "pause", "--instance-id", iid}, Started: at(100), Ended: at(101)}
	c.Commands["unit/resume"] = runner.Command{Name: "esc-keyless-resume", Stdout: "unit/resume", Argv: []string{"ad", "resume", "--instance-id", iid}, Started: at(110), Ended: at(111)}
	for round := 1; round <= 2; round++ {
		escape, placed := int64(10), int64(5)
		held := "E3"
		if round == 2 {
			escape, placed, held = 70, 65, "E4"
		}
		steer := fmt.Sprintf("unit-steer-%d", round)
		body := "[agent-director: message 1 of 1]\nunit complete steer"
		data, _ := unitJournal(t, sid, steer, "system", signals.NativeContextBody(body), at(placed))
		f.PutBytes("raw/"+steer+"-before-Escape.jsonl", data)
		f.Put("raw/"+steer+"-whole-steer.json", map[string]any{"body": body, "prompt": runner.Row{"transport_at_ms": at(placed - 1).UnixMilli()}})
		force := fmt.Sprintf("raw/unit-esc-force-%d.json", round)
		f.Put(force, runner.Row{"force_result": runner.Row{"force_id": "unit-force", "outcome": "refused", "reason": "input_unresolved"}})
		f.Put(fmt.Sprintf("raw/esc-%d-gap.json", round), q.EscObservation{ID: fmt.Sprintf("esc-%d", round), Instance: iid, Steer: steer, Held: held, Escape: at(escape), Delivery: api.DeliveryFacts{AwaitingTurnStart: 1}, Result: api.DeliveryResult{Class: "held", Wait: &api.DeliveryWait{Reason: "readiness_unknown"}}, Notification: q.NotificationWindow{Session: sid, Started: at(escape), Ended: at(escape + 20), Observed: true}, Force: force})
	}
	return f, c
}

func TestQualificationIndependentEscRecoveries(t *testing.T) {
	for _, kind := range []string{"both-scoped-recoveries", "missing-typed", "missing-resume", "missing-placement", "placement-after-Escape"} {
		t.Run(kind, func(t *testing.T) {
			f, c := unitEscRecoveries(t)
			switch kind {
			case "missing-typed":
				c.Manifest.Cases = c.Manifest.Cases[1:]
			case "missing-resume":
				delete(c.Commands, "unit/resume")
			case "missing-placement":
				delete(f.Manifest.Artifacts, "raw/unit-steer-1-before-Escape.jsonl")
			case "placement-after-Escape":
				data, _ := unitJournal(t, "unit-esc-session", "late-steer", "system", signals.NativeContextBody("[agent-director: message 1 of 1]\nunit complete steer"), time.Unix(1800000011, 0).UTC())
				f.PutBytes("raw/unit-steer-1-before-Escape.jsonl", data)
			}
			rows := q.CheckEscRecovery(c)
			for _, id := range []string{"esc-typed-recovery", "esc-resume-recovery", "esc-input-placement", "esc-notification"} {
				want := "pass"
				if kind == "missing-typed" && id == "esc-typed-recovery" || kind == "missing-resume" && id == "esc-resume-recovery" || kind == "missing-placement" && id == "esc-input-placement" {
					want = "not_completed"
				}
				if kind == "placement-after-Escape" && id == "esc-input-placement" {
					want = "fail"
				}
				if got := namedResult(t, rows, id); got.Status != want {
					t.Fatalf("%s: %+v; want %s", kind, got, want)
				}
			}
		})
	}
}

func TestQualificationTimingLaunchOwnership(t *testing.T) {
	_, c := unitEscRecoveries(t)
	for _, timing := range q.ExtractTimings(c) {
		if timing.Case == "E2" {
			if timing.Launch != "old-launch" {
				t.Fatalf("typed send attributed to later resumed Launch: %+v", timing)
			}
			return
		}
	}
	t.Fatal("owned typed timing disappeared")
}
