package store_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// b.ivj D2: a failed Stop publishes waiting and its diagnosis in one guarded write.
func TestCursorFailureReasonStopAtomicAndGuarded(t *testing.T) {
	for _, change := range []string{"current", "token", "life", "connection", "dialog", "write failure"} {
		t.Run(change, func(t *testing.T) {
			e, row, g := rotationStore(t)
			g.Event, g.Examined, g.CursorFailureReason = "Stop", row.Snapshot, "runtime internal error"
			switch change {
			case "token":
				e.exec(t, `UPDATE spawns SET launch_token='2222222222222222'`)
			case "life":
				e.exec(t, `UPDATE spawns SET life_number=life_number+1`)
			case "connection":
				e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id='replacement'`)
			case "dialog":
				e.insert(t, "independent")
			case "write failure":
				e.exec(t, `CREATE TRIGGER fail_reason AFTER UPDATE OF state_evidence ON spawns BEGIN SELECT RAISE(ABORT,'reason write failed'); END`)
			}
			// Even an intermediate UPDATE must never expose waiting without its reason.
			e.exec(t, `CREATE TRIGGER atomic_reason BEFORE UPDATE OF state ON spawns WHEN NEW.state='waiting' AND COALESCE(json_extract(NEW.state_evidence,'$.failure_reason'),'') != 'runtime internal error' BEGIN SELECT RAISE(ABORT,'split state/reason write'); END`)
			before := rvExamine(t, e.f, row.InstanceID)
			_, applied, err := e.f.s.ApplyHookTransitionResult(row.InstanceID, g, store.StateWaiting, false, "Stop", row.JSONLPath, true)
			after := rvExamine(t, e.f, row.InstanceID)
			if change == "current" {
				if err != nil || !applied.Applied || after.State != store.StateWaiting || after.FailureReason(store.SettleRead{}) != g.CursorFailureReason {
					t.Fatalf("failed Stop: %+v %v, row=%+v", applied, err, after)
				}
				c := after.StateConfirmation(store.SettleRead{})
				if c.Status != store.ConfirmationConfirmed || c.Source != store.EvidenceAgentEvent || c.Reason != "" {
					t.Fatalf("diagnosis changed confirmation semantics: %+v", c)
				}
			} else if change == "dialog" {
				// Existing held-dialog soft refresh remains allowed, but cannot publish a failed waiting state.
				if err != nil || !applied.Applied || after.State != store.StateCheckPermission || after.StateEvidence != before.StateEvidence || after.RawFailureReason() != "" {
					t.Fatalf("held Stop replaced dialog evidence: %+v %v row=%+v", applied, err, after)
				}
			} else {
				if applied.Applied || !reflect.DeepEqual(before, after) || (change == "write failure") != (err != nil) {
					t.Fatalf("refused Stop changed row: %+v %v, before=%+v after=%+v", applied, err, before, after)
				}
			}
		})
	}
}

// b.ivj D2: reconciliation may win terminal marking; enriching its reason is metadata only.
func TestCursorFailureReasonMissingAndEnrichment(t *testing.T) {
	for _, change := range []string{"fatal", "enrich", "token", "life", "epoch", "connection"} {
		t.Run(change, func(t *testing.T) {
			e := newCursorTrailEnv(t)
			row := rvExamine(t, e.f, e.b.InstanceID)
			const reason = "Cursor ACP session/prompt failed: runtime internal error"
			if change != "fatal" {
				if _, result, err := e.f.s.MarkCursorMissingIfSameLaunch(row, e.b); err != nil || result != store.CondApplied {
					t.Fatalf("reconciliation: %v %v", result, err)
				}
				row = rvExamine(t, e.f, row.InstanceID)
			}
			switch change {
			case "token":
				e.exec(t, `UPDATE spawns SET launch_token='2222222222222222',state_evidence=json_set(state_evidence,'$.token','2222222222222222')`)
			case "life":
				e.exec(t, `UPDATE spawns SET life_number=life_number+1,state_evidence=json_set(state_evidence,'$.life',life_number+1)`)
			case "epoch":
				e.exec(t, `UPDATE spawns SET permission_epoch=permission_epoch+1,state_evidence=json_set(state_evidence,'$.epoch',permission_epoch+1)`)
			case "connection":
				e.exec(t, `UPDATE cursor_runtimes SET bound_connection_id='replacement'`)
			}
			before := rvExamine(t, e.f, row.InstanceID)
			columns := hgColumns(t, e.f, row.InstanceID)
			familyState, familySettled, familyStamp := familyHistory(t, e.f, row.InstanceID)
			if before.StateConfirmation(store.SettleRead{}).Status != store.ConfirmationConfirmed {
				t.Fatal("current evidence must apply so stale-guard refusals are not vacuous")
			}
			binding, err := e.f.s.ReadCursorRuntime(row.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			prior, result, err := e.f.s.MarkFailedCursorRuntimeMissing(row, e.b, reason)
			after := rvExamine(t, e.f, row.InstanceID)
			if change == "fatal" || change == "enrich" {
				if err != nil || result != store.CondApplied || prior != before.State || after.State != store.StateMissing || after.RawFailureReason() != reason {
					t.Fatalf("failed terminal write: %s %v %v row=%+v", prior, result, err, after)
				}
				if c := after.StateConfirmation(store.SettleRead{}); c.Status != store.ConfirmationConfirmed || c.Source != store.EvidenceProcess || c.Reason != "" {
					t.Fatalf("failure has no process state evidence: %+v", c)
				}
			} else if err != nil || result != store.CondChanged || prior != "" || !reflect.DeepEqual(before, after) {
				t.Fatalf("stale enrichment applied: %s %v %v", prior, result, err)
			}
			if change == "enrich" {
				state, settled, stamp := familyHistory(t, e.f, row.InstanceID)
				if state != familyState || settled != familySettled || stamp != familyStamp {
					t.Fatal("enrichment changed family history")
				}
				// Remove only the optional key before comparing the complete decoded spawn,
				// including state/observation versions, epoch, settle and family columns.
				var evidence map[string]any
				if err := json.Unmarshal([]byte(after.StateEvidence), &evidence); err != nil {
					t.Fatal(err)
				}
				delete(evidence, "failure_reason")
				var old map[string]any
				if err := json.Unmarshal([]byte(before.StateEvidence), &old); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(evidence, old) {
					t.Fatal("enrichment rewrote state confirmation")
				}
				after.StateEvidence = before.StateEvidence
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(columns, hgColumns(t, e.f, row.InstanceID)) {
					t.Fatal("reason enrichment changed lifecycle, timestamps, versions or family")
				}
				current, err := e.f.s.ReadCursorRuntime(row.InstanceID)
				if err != nil || !reflect.DeepEqual(binding, current) {
					t.Fatalf("enrichment changed binding: %+v %v", current, err)
				}
			}
		})
	}
}

// b.ivj D2: reads retain a failure until the next lifecycle write, even one keeping the state.
func TestCursorFailureReasonClearsOnLifecycleAndRotation(t *testing.T) {
	for _, event := range []string{"Stop", "UserPromptSubmit", "rotation"} {
		t.Run(event, func(t *testing.T) {
			e, row, g := rotationStore(t)
			g.Event, g.CursorFailureReason = "Stop", "turn failed"
			if a, err := e.f.s.ApplyHookTransition(row.InstanceID, g, store.StateWaiting, false, "Stop", row.JSONLPath, true); err != nil || !a.Applied {
				t.Fatalf("failed Stop: %+v %v", a, err)
			}
			row = rvExamine(t, e.f, row.InstanceID)
			confirmation := row.StateConfirmation(store.SettleRead{})
			for range 2 {
				read := rvExamine(t, e.f, row.InstanceID)
				if read.RawFailureReason() != "turn failed" || !reflect.DeepEqual(row, read) {
					t.Fatal("read consumed failure reason")
				}
			}
			g.CursorFailureReason = ""
			g.Event = event
			want := store.StateWaiting
			if event == "rotation" {
				g.Event = "SessionStart"
				if ok, err := e.f.s.RotateCursorSession(row, g, "fresh-session", row.JSONLPath, time.Now().Add(time.Second), cursorTrailCheck); err != nil || !ok {
					t.Fatalf("rotation: %v %v", ok, err)
				}
			} else {
				if event == "UserPromptSubmit" {
					want = store.StateWorking
				}
				if a, err := e.f.s.ApplyHookTransition(row.InstanceID, g, want, false, event, row.JSONLPath, true); err != nil || !a.Applied {
					t.Fatalf("next lifecycle: %+v %v", a, err)
				}
			}
			after := rvExamine(t, e.f, row.InstanceID)
			if after.State != want || after.RawFailureReason() != "" || strings.Contains(after.StateEvidence, "failure_reason") {
				t.Fatalf("next lifecycle retained reason: %+v", after)
			}
			if event != "UserPromptSubmit" && after.StateConfirmation(store.SettleRead{}) != confirmation {
				t.Fatal("same-state clear redated confirmation")
			}
		})
	}
}

// b.ivj D2: the diagnosis follows the value reported while either transition settles.
func TestCursorFailureReasonFollowsSettledState(t *testing.T) {
	e, row, g := rotationStore(t)
	e.f.s.SetSettleMs(0)
	for _, step := range []struct{ event, state, reason, oldState, oldReason string }{
		{"Stop", store.StateWaiting, "turn failed", store.StateWorking, ""},
		{"UserPromptSubmit", store.StateWorking, "", store.StateWaiting, "turn failed"},
	} {
		g.Event, g.CursorFailureReason = step.event, step.reason
		if a, err := e.f.s.ApplyHookTransition(row.InstanceID, g, step.state, false, step.event, row.JSONLPath, true); err != nil || !a.Applied {
			t.Fatalf("hook: %+v %v", a, err)
		}
		raw := rvExamine(t, e.f, row.InstanceID)
		before := raw
		for _, age := range []int64{0, settleT} {
			r := store.SettleRead{SettleMs: settleT, NowMs: raw.StateChangedMillis + age}
			read, err := e.f.s.GetSpawnSettled(row.InstanceID, r)
			if err != nil {
				t.Fatal(err)
			}
			wantState, wantReason := step.oldState, step.oldReason
			if age == settleT {
				wantState, wantReason = step.state, step.reason
			}
			if read.ReportedState != wantState || read.FailureReason(r) != wantReason || read.StateConfirmation(r).Status != store.ConfirmationConfirmed {
				t.Fatalf("age=%d state=%s reason=%q confirmation=%+v", age, read.ReportedState, read.FailureReason(r), read.StateConfirmation(r))
			}
		}
		if after := rvExamine(t, e.f, row.InstanceID); !reflect.DeepEqual(before, after) {
			t.Fatal("settled read changed row")
		}
	}
}

// b.ivj D2/A2: optional metadata is tolerant and bounded without changing confirmation's enum.
func TestCursorFailureReasonMetadataValidation(t *testing.T) {
	f, row, _ := cursorRowBinding(t)
	row = stampEvidence(t, f, row.InstanceID)
	wantConfirmation := row.StateConfirmation(store.SettleRead{})
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"string", "runtime failed", "runtime failed"},
		{"null", nil, ""}, {"number", 42, ""}, {"object", map[string]any{"reason": "failed"}, ""},
		{"newline", "failed\nagain", ""}, {"carriage return", "failed\ragain", ""},
		{"nul", "failed\x00again", ""}, {"escape", "failed\x1bagain", ""},
		{"4096 bytes", strings.Repeat("é", 2048), strings.Repeat("é", 2048)},
		{"4097 bytes", strings.Repeat("é", 2048) + "x", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var metadata map[string]any
			if err := json.Unmarshal([]byte(row.StateEvidence), &metadata); err != nil {
				t.Fatal(err)
			}
			metadata["failure_reason"] = tc.value
			encoded, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			sp := row
			sp.StateEvidence = string(encoded)
			if got := sp.RawFailureReason(); got != tc.want {
				t.Fatalf("reason=%q want=%q", got, tc.want)
			}
			if got := sp.StateConfirmation(store.SettleRead{}); got != wantConfirmation {
				t.Fatalf("optional metadata invalidated confirmation: %+v", got)
			}
			for _, agent := range []string{store.AgentClaude, store.AgentCodex} {
				sp.Agent = agent
				if sp.RawFailureReason() != "" {
					t.Fatalf("%s exposed Cursor-only metadata", agent)
				}
			}
		})
	}
}
