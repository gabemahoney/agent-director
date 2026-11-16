package inputdelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
)

func TestLegacyPreservesExactTranscriptProof(t *testing.T) {
	t.Parallel()
	s, a, j, path, meta := legacyEvidence(t)
	a.Payload = "use <>&\n\"quoted\" \\ / café 🌍 ☺"
	a.PayloadSHA256 = Hash([]byte(a.Payload))
	a.Phase, a.BytesAttempted = "enter_attempted", len(a.Payload)
	// JSONL whitespace, literal HTML, alternate escapes and Unicode are evidence bytes.
	raw := []byte(" \t" + `{ "type" : "response_item", "payload": { "type": "message", "role": "user", "id": "turn-1", "content": [ { "type": "input_text", "text": "use <>&\n\"quoted\" \\ \/ café 🌍 \u263a" } ], "internal_chat_message_metadata_passthrough": { "content_item_kinds": [ "user.text" ] } } }` + " \t")
	writeEvidence(t, path, append(append(append([]byte(nil), meta...), raw...), '\n'))
	proof, err := s.Confirm(a)
	if err != nil || proof == nil || !bytes.Equal(proof.Raw, raw) || proof.RawSHA256 != Hash(raw) || proof.RecordID != "turn-1" {
		t.Fatalf("exact native proof=%+v, err=%v; want raw=%q", proof, err, raw)
	}
	a.Proof, a.Phase = proof, "submitted"
	writeLegacyAttempt(t, j, a, false)
	r, err := j.Read(context.Background(), a.Identity.StoreID, &a.Identity)
	if err != nil || r.Launch.Blocker != "" || len(r.Attempts) != 1 || !bytes.Equal(r.Attempts[0].Proof.Raw, raw) {
		t.Fatalf("import=%+v err=%v", r, err)
	}
	if !r.Attempts[0].Confirmation.At.IsZero() || !r.Launch.Confirmation.At.IsZero() {
		t.Fatal("import invented observation time")
	}
}

func TestLegacyRejectsAlteredRawProofOnCurrentAndArchivedRequests(t *testing.T) {
	t.Parallel()
	for _, archived := range []bool{false, true} {
		for _, mode := range []string{"unaltered", "changed bytes", "malformed record with matching hash", "legacy object raw", "invalid base64"} {
			t.Run(fmt.Sprintf("archived=%t/%s", archived, mode), func(t *testing.T) {
				_, a, j, rollout, meta := legacyEvidence(t)
				a.Phase, a.BytesAttempted = "submitted", len(a.Payload)
				raw := bytes.TrimSuffix(codexRecord(t, "turn", a.Payload), []byte("\n"))
				a.Proof = &Proof{Path: rollout, SessionID: a.NativeSessionID, RecordID: "turn", Offset: int64(len(meta)), Raw: raw, RawSHA256: Hash(raw)}
				path := filepath.Join(j.Dir, "attempt.json")
				if archived {
					path = filepath.Join(j.Dir, "request-"+Hash([]byte(a.RequestID))+".json")
					next, err := NewAttempt(a.Identity, "next prompt", nil)
					if err != nil {
						t.Fatal(err)
					}
					writeLegacyAttempt(t, j, next, false)
				}
				data, err := json.Marshal(a)
				if err != nil {
					t.Fatal(err)
				}
				var journal map[string]any
				if err := json.Unmarshal(data, &journal); err != nil {
					t.Fatal(err)
				}
				proof := journal["proof"].(map[string]any)
				switch mode {
				case "changed bytes":
					proof["Raw"] = append([]byte(" "), raw...)
				case "malformed record with matching hash":
					proof["Raw"] = []byte("not JSON")
					proof["RawSHA256"] = Hash([]byte("not JSON"))
				case "legacy object raw":
					proof["Raw"] = json.RawMessage(raw)
				case "invalid base64":
					proof["Raw"] = "%%%"
				}
				data, err = json.Marshal(journal)
				if err != nil {
					t.Fatal(err)
				}
				writeEvidence(t, path, data)
				r, err := j.Read(context.Background(), a.Identity.StoreID, &a.Identity)
				if mode == "unaltered" {
					// The control: only the alteration refuses this proof.
					var proved *Attempt
					for _, imported := range r.Attempts {
						if imported.RequestID == a.RequestID {
							proved = imported
						}
					}
					if err != nil || r.Launch.Blocker != "" || proved == nil || proved.Proof == nil || !bytes.Equal(proved.Proof.Raw, raw) {
						t.Fatalf("exact proof refused: %+v, err=%v", r, err)
					}
				} else if err != nil || r.Launch.Blocker == "" || len(r.Attempts) != 0 {
					t.Fatalf("altered proof accepted: %+v, err=%v", r, err)
				}
			})
		}
	}
}
