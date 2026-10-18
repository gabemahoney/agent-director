package api_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestSendKeysDefaultNativeBackendPreservesExistingOwnership(t *testing.T) {
	// Serial: native launch paths use HOME, which each case isolates.
	for _, mode := range []string{"absent", "corrupt manifest", "corrupt journal", "partial input", "submitted proof"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			e := newNativeSubmissionEnv(t)
			r := e.seedRow(t, codexSpec(store.StateWaiting, agentAlive, false))
			identity := r.Spawn.Identity
			id := api.InputDeliveryIdentity{StoreID: e.store.StoreID(), Token: identity.Token, InstanceID: r.ID, Socket: identity.Socket, PaneID: identity.PaneID, PID: identity.PanePID, Starttime: identity.PaneStarttime, Agent: r.Spawn.Agent, CWD: r.Spawn.CWD}
			dir := filepath.Join(home, ".config", "agent-director", "input", id.StoreID, id.Token)
			e.env.Input = nil
			params := api.SendKeysParams{InstanceID: r.ID, Text: skpText, DeliveryRequestID: "original"}
			var owner *api.InputDeliveryAttempt
			if mode != "absent" {
				var err error
				owner, err = inputdelivery.NewAttempt(id, params.Text, nil)
				if err != nil {
					t.Fatal(err)
				}
				owner.RequestID, owner.Phase, owner.BytesAttempted = params.DeliveryRequestID, "text_attempted", len(params.Text)
				if mode == "submitted proof" {
					owner.Phase, owner.EnterAttempted = "submitted", true
					raw := []byte(`{"type":"response_item","payload":{"type":"message","role":"user","id":"native-proof","content":[{"type":"input_text","text":"` + params.Text + `"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text"]}}}`)
					owner.Proof = &api.InputDeliveryProof{SessionID: r.Spawn.SessionID, RecordID: "native-proof", Raw: raw, RawSHA256: inputdelivery.Hash(raw)}
				}
				s, err := (inputdelivery.Native{}).Open(id)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Save(owner); err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				corrupt := ""
				if mode == "corrupt manifest" {
					corrupt = "launch.json"
				} else if mode == "corrupt journal" {
					corrupt = "attempt.json"
				}
				if corrupt != "" {
					if err := os.WriteFile(filepath.Join(dir, corrupt), []byte("not JSON"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before, readErr := os.ReadFile(filepath.Join(dir, "attempt.json"))
			if mode != "absent" && readErr != nil {
				t.Fatal(readErr)
			}
			result, err := e.send(params)
			switch mode {
			case "absent":
				if err != nil || result != (api.SendKeysResult{}) {
					t.Fatalf("compatibility input result=%+v err=%v", result, err)
				}
				e.assertDelivered(t, r.Socket, identity.PaneID, params.Text)
				if _, err := os.Stat(filepath.Join(home, ".config", "agent-director", "input")); !os.IsNotExist(err) {
					t.Fatalf("default compatibility input allocated native state: %v", err)
				}
				return
			case "partial input":
				assertDeliveryError(t, err, "partial_input_unconfirmed", len(params.Text), false)
			case "submitted proof":
				if err != nil || result.Outcome != "submitted" || result.RequestID != params.DeliveryRequestID || result.RecordSHA256 != owner.Proof.RawSHA256 {
					t.Fatalf("persisted proof result=%+v err=%v", result, err)
				}
			default:
				assertDeliveryError(t, err, "not_started", 0, false)
			}
			e.assertNoTmuxCall(t)
			after, err := os.ReadFile(filepath.Join(dir, "attempt.json"))
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("default native backend replaced existing ownership: %v", err)
			}
		})
	}
}
