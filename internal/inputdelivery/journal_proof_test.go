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
	s, a, j := legacyEvidence(t)
	a.Payload = "use <>&\n\"quoted\" \\ / café 🌍 ☺"
	a.PayloadSHA256 = Hash([]byte(a.Payload))
	a.Phase, a.BytesAttempted = "enter_attempted", len(a.Payload)
	// JSONL whitespace, literal HTML, alternate escapes and Unicode are evidence bytes.
	raw := []byte(" \t" + fmt.Sprintf(`{ "type" : "user", "uuid": "cde93519-6972-4b1b-a650-830a0042e876", "sessionId": %q, "cwd": "/work", "version": "2.1.280", "message": { "role": "user", "content": "use <>&\n\"quoted\" \\ \/ café 🌍 \u263a" } }`, s.manifest.SessionID) + " \t")
	writeEvidence(t, s.manifest.Transcript, append(append([]byte(nil), raw...), '\n'))
	proof, err := s.Confirm(a)
	if err != nil || proof == nil || !bytes.Equal(proof.Raw, raw) || proof.RawSHA256 != Hash(raw) {
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
		for _, mode := range []string{"changed bytes", "malformed record with matching hash", "legacy object raw", "invalid base64"} {
			t.Run(fmt.Sprintf("archived=%t/%s", archived, mode), func(t *testing.T) {
				s, a, j := legacyEvidence(t)
				a.Phase, a.BytesAttempted = "submitted", len(a.Payload)
				raw := claudeRecord(t, s, a.Payload, nil)
				recordID, _ := s.matchRecord(raw, a, s.manifest.SessionID)
				a.Proof = &Proof{Path: s.manifest.Transcript, SessionID: s.manifest.SessionID, RecordID: recordID, Raw: raw, RawSHA256: Hash(raw)}
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
				if err != nil || r.Launch.Blocker == "" || len(r.Attempts) != 0 {
					t.Fatalf("altered proof accepted: %+v, err=%v", r, err)
				}
			})
		}
	}
}
