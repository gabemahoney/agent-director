package api_test

import (
	"bytes"
	"testing"

	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestSendKeysPersistedProofReconcilesAndAllowsManualInput(t *testing.T) {
	// Serial: native journal paths use HOME, which this test isolates.
	t.Setenv("HOME", t.TempDir())
	e := newNativeSubmissionEnv(t)
	r := e.seedRow(t, codexSpec(store.StatePending, agentAlive, false))
	payload := "use <>&\n\"quoted\" \\ / café 🌍 ☺"
	raw := []byte(" \t" + `{ "type" : "response_item", "payload": { "type": "message", "role": "user", "id": "native-proof", "content": [{ "type": "input_text", "text": "use <>&\n\"quoted\" \\ \/ café 🌍 \u263a" }], "internal_chat_message_metadata_passthrough": { "content_item_kinds": ["user.text"] } } }` + " \t")
	native := inputdelivery.Native{}
	// Journal I/O and validation stay real; readiness, queue and confirmation are injected.
	e.b.save = func(a *api.InputDeliveryAttempt) error {
		s, err := native.Open(a.Identity)
		if err != nil {
			return err
		}
		defer s.Close()
		return s.Save(a)
	}
	e.b.confirm = func(a *api.InputDeliveryAttempt) (*api.InputDeliveryProof, error) {
		return &api.InputDeliveryProof{SessionID: a.NativeSessionID, RecordID: "native-proof", Raw: raw, RawSHA256: inputdelivery.Hash(raw)}, nil
	}
	p := api.SendKeysParams{InstanceID: r.ID, Text: payload, AllowPending: true, DeliveryRequestID: "original"}
	first, err := e.send(p)
	if err != nil || first.Outcome != "submitted" || first.RecordSHA256 != inputdelivery.Hash(raw) {
		t.Fatalf("initial proof=%+v, err=%v", first, err)
	}
	id := e.b.current.Identity
	e.env.Input = native
	before := len(e.rec.SocketCalls())
	reconciled, err := e.send(p)
	if err != nil || reconciled != first {
		t.Fatalf("reopened reconciliation=%+v, err=%v; want %+v", reconciled, err, first)
	}
	if len(e.rec.SocketCalls()) != before {
		t.Fatal("reopened reconciliation touched tmux")
	}
	if _, err := e.send(api.SendKeysParams{InstanceID: r.ID, Key: "Escape", AllowPending: true}); err != nil {
		t.Fatalf("manual input could not read the stored proof: %v", err)
	}
	before = len(e.rec.SocketCalls())
	reconciled, err = e.send(p)
	if err != nil || reconciled != first || len(e.rec.SocketCalls()) != before {
		t.Fatalf("archived proof after manual input=%+v, err=%v; want %+v without tmux calls", reconciled, err, first)
	}
	s, err := native.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err := s.Load()
	if err != nil || current == nil || !current.ManualInterference || current.Phase != "manual_input" {
		t.Fatalf("manual ownership=%+v, err=%v", current, err)
	}
	archived, err := s.LoadRequest(p.DeliveryRequestID)
	if err != nil || archived == nil || archived.Proof == nil || !bytes.Equal(archived.Proof.Raw, raw) || archived.Proof.RawSHA256 != inputdelivery.Hash(raw) {
		t.Fatalf("archived exact proof=%+v, err=%v", archived, err)
	}
	assertOrdinaryWrites(t, e.killEnv, r, payload)
	escapes := 0
	for _, c := range e.rec.SocketCallsOf(tmux.CallSendKey) {
		if c.Key == "Escape" {
			escapes++
		}
	}
	if escapes != 1 {
		t.Fatalf("manual Escape count=%d; want 1", escapes)
	}
}
