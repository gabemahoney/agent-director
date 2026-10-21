package hook_test

import (
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/contextfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// Interleave at the real store write, after the handler computed its snapshot.
type interleavedClaudeStore struct {
	*store.Store
	beforeWrite func()
	afterWrite  func(bool)
	writes      []bool
}

func (s *interleavedClaudeStore) RecordClaudeCapacity(id string, gate store.HookGate, p store.ClaudeCapacity) (bool, error) {
	if f := s.beforeWrite; f != nil {
		s.beforeWrite = nil
		f()
	}
	applied, err := s.Store.RecordClaudeCapacity(id, gate, p)
	s.writes = append(s.writes, applied)
	if s.afterWrite != nil {
		s.afterWrite(applied)
	}
	return applied, err
}

func claudePairReadings(t *testing.T, st *store.Store, id string) (api.ContextReading, api.ContextReading) {
	t.Helper()
	parent, err := api.Status(st, api.RelayView{}, id)
	if err != nil {
		t.Fatal(err)
	}
	row, err := api.Get(st, api.RelayView{}, id)
	if err != nil || !reflect.DeepEqual(row.Context, parent.Context) {
		t.Fatalf("status/get disagree: %+v %+v %v", parent.Context, row.Context, err)
	}
	children, err := api.ListSubagents(st, id)
	if err != nil || len(children.Subagents) != 1 {
		t.Fatalf("existing child visibility changed: %+v %v", children, err)
	}
	return parent.Context, children.Subagents[0].Context
}

func TestClaudeCapacityConcurrentCallbacksPreserveInvalidation(t *testing.T) {
	for _, name := range []string{"parent window", "parent model", "child window", "provider clear", "initial parent snapshot"} {
		t.Run(name, func(t *testing.T) {
			st, db, id, parent, hc := claudeCallbackRow(t)
			child := contextfix.Must(t, "claude-child")
			seedClaudeCallbackChild(t, db, id, mustGetSpawn(t, st, id).JSONLPath, child, true)
			childHC := hc
			childHC.ContextSource, childHC.Start = "subagentStatusLine", child.ObservedAt
			if name != "initial parent snapshot" {
				ssgHandleWith(t, st, hc, string(parent.Callback), nil)
			}
			if name == "child window" {
				ssgHandleWith(t, st, childHC, string(child.Callback), nil)
			}
			staleHC, stalePayload := childHC, string(child.Callback)
			competingHC, competingPayload := hc, callbackJSON(t, parent.Callback, func(p map[string]any) {
				if name == "parent model" {
					p["model"].(map[string]any)["id"] = "different-resolved-model"
				} else {
					p["context_window"].(map[string]any)["context_window_size"] = 100000
				}
			})
			switch name {
			case "child window":
				staleHC, stalePayload = hc, string(parent.Callback)
				competingHC = childHC
				competingPayload = callbackJSON(t, child.Callback, func(p map[string]any) {
					p["tasks"].([]any)[0].(map[string]any)["contextWindowSize"] = 100000
				})
			case "provider clear":
				staleHC, stalePayload = hc, string(parent.Callback)
				env := hc.Env
				staleHC.Env = func(key string) string {
					if key == "ANTHROPIC_BASE_URL" {
						return "https://synthetic-provider.invalid"
					}
					return env(key)
				}
				competingHC, competingPayload = childHC, string(child.Callback)
			case "initial parent snapshot":
				staleHC, stalePayload = hc, string(parent.Callback)
			}
			var beforeParent, beforeChild api.ContextReading
			interleaved := &interleavedClaudeStore{Store: st}
			interleaved.beforeWrite = func() {
				ssgHandleWith(t, st, competingHC, competingPayload, nil)
				beforeParent, beforeChild = claudePairReadings(t, st, id)
			}
			interleaved.afterWrite = func(applied bool) {
				if !applied {
					gotParent, gotChild := claudePairReadings(t, st, id)
					if !reflect.DeepEqual(gotParent, beforeParent) || !reflect.DeepEqual(gotChild, beforeChild) {
						t.Fatal("stale callback resurrected or overwrote a real public reading")
					}
				}
			}
			ssgHandleWith(t, interleaved, staleHC, stalePayload, nil)
			if !reflect.DeepEqual(interleaved.writes, []bool{false, true}) {
				t.Fatalf("stale write was not rejected and recomputed: %v", interleaved.writes)
			}
			gotParent, gotChild := claudePairReadings(t, st, id)
			wantParent, wantChild := parent, child
			if name != "child window" {
				wantParent.Expected = []byte(contextfix.UnknownJSON)
			}
			if name != "parent window" && name != "parent model" {
				wantChild.Expected = []byte(contextfix.UnknownJSON)
			}
			assertClaudeCallbackContext(t, gotParent, wantParent)
			assertClaudeCallbackContext(t, gotChild, wantChild)
			// Replaying both same-response callbacks must retain the invalidation.
			ssgHandleWith(t, st, hc, string(parent.Callback), nil)
			ssgHandleWith(t, st, childHC, string(child.Callback), nil)
			gotParent, gotChild = claudePairReadings(t, st, id)
			assertClaudeCallbackContext(t, gotParent, wantParent)
			// A first child observation after the initial-parent race is valid.
			if name == "initial parent snapshot" {
				wantChild = child
			}
			assertClaudeCallbackContext(t, gotChild, wantChild)
		})
	}
}

func TestClaudeCapacityRetryCannotCrossLaunch(t *testing.T) {
	st, _, id, parent, hc := claudeCallbackRow(t)
	ssgHandleWith(t, st, hc, string(parent.Callback), nil)
	interleaved := &interleavedClaudeStore{Store: st}
	interleaved.beforeWrite = func() {
		sp := mustGetSpawn(t, st, id)
		gate := store.HookGate{ParentPID: sp.PID, ParentStart: sp.ProcStarttime, SessionID: sp.SessionID}
		if a, err := st.ApplyHookTransition(id, gate, store.StateEnded, false, "SessionEnd", "", false); err != nil || !a.Applied {
			t.Fatalf("end: %+v %v", a, err)
		}
		sp = mustGetSpawn(t, st, id)
		identity := sp.Identity
		identity.Token = "b2b2b2b2b2b2b2b2"
		res, version, err := st.MoveToPending(id, sp.Snapshot, time.Now().UnixMilli(), identity.Token, identity.Socket, "", store.LaunchOwner{})
		if err != nil || res != store.CondApplied {
			t.Fatalf("resume: %v %v", res, err)
		}
		if res, err := st.RecordLaunchIdentity(id, version, identity.Token, identity); err != nil || res != store.CondApplied {
			t.Fatalf("identity: %v %v", res, err)
		}
		if a, err := st.ApplyHookTransition(id, gate, store.StateWaiting, false, "Stop", sp.JSONLPath, true); err != nil || !a.Applied {
			t.Fatalf("new launch waiting: %+v %v", a, err)
		}
	}
	ssgHandleWith(t, interleaved, hc, string(parent.Callback), nil)
	if !reflect.DeepEqual(interleaved.writes, []bool{false}) {
		t.Fatalf("old observation attempted a write into the new launch: %v", interleaved.writes)
	}
	if sp := mustGetSpawn(t, st, id); sp.State != store.StateWaiting || sp.ClaudeCapacity != nil {
		t.Fatalf("retry changed new launch: %+v", sp)
	}
	status, err := api.Status(st, api.RelayView{}, id)
	if err != nil || !reflect.DeepEqual(status.Context, api.ContextReading{}) {
		t.Fatalf("old callback restored public context: %+v %v", status.Context, err)
	}
}

func TestClaudeCapacityResolvedModelPairing(t *testing.T) {
	for _, subject := range []string{"claude-parent", "claude-child"} {
		for _, mode := range []string{"sonnet", "opus", "fable", "other resolved model", "mismatch", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "ANTHROPIC_CUSTOM_MODEL_OPTION", "ANTHROPIC_DEFAULT_FABLE_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES"} {
			t.Run(subject+"/"+mode, func(t *testing.T) {
				st, db, id, capturedParent, hc := claudeCallbackRow(t)
				// Model names and capacities are synthetic changes to real Haiku captures.
				sample := contextfix.ClaudeModelVariant(t, subject, "synthetic-resolved-"+mode, 300000)
				if sample.ChildID == "" {
					if err := os.WriteFile(mustGetSpawn(t, st, id).JSONLPath, sample.Transcript, 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					ssgHandleWith(t, st, hc, string(capturedParent.Callback), nil)
					seedClaudeCallbackChild(t, db, id, mustGetSpawn(t, st, id).JSONLPath, sample, true)
					hc.ContextSource, hc.Start = "subagentStatusLine", sample.ObservedAt
				}
				payload := string(sample.Callback)
				accepted := mode == "sonnet" || mode == "opus" || mode == "fable" || mode == "other resolved model"
				if mode == "mismatch" {
					payload = callbackJSON(t, sample.Callback, func(p map[string]any) {
						if sample.ChildID == "" {
							p["model"].(map[string]any)["id"] = "different-resolved-model"
						} else {
							p["tasks"].([]any)[0].(map[string]any)["model"] = "different-resolved-model"
						}
					})
				} else if !accepted {
					env := hc.Env
					hc.Env = func(key string) string {
						if key == mode {
							return "synthetic-override"
						}
						return env(key)
					}
				}
				ssgHandleWith(t, st, hc, payload, nil)
				want := sample
				if !accepted {
					want.Expected = []byte(contextfix.UnknownJSON)
				}
				if sample.ChildID == "" {
					status, err := api.Status(st, api.RelayView{}, id)
					if err != nil {
						t.Fatal(err)
					}
					assertClaudeCallbackContext(t, status.Context, want)
					row, err := api.Get(st, api.RelayView{}, id)
					if err != nil {
						t.Fatal(err)
					}
					assertClaudeCallbackContext(t, row.Context, want)
				} else {
					_, reading := claudePairReadings(t, st, id)
					assertClaudeCallbackContext(t, reading, want)
				}
			})
		}
	}
}

var _ hook.HookStore = (*interleavedClaudeStore)(nil)
