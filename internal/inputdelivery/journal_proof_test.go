package inputdelivery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestJournalPreservesExactTranscriptProofAfterReopen(t *testing.T) {
	t.Parallel()
	s, a := evidenceSession(t)
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
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	reopened := &nativeSession{dir: s.dir, manifest: s.manifest}
	loaded, err := reopened.Load()
	if err != nil || !reflect.DeepEqual(loaded, a) {
		t.Fatalf("reopened proof=%+v, err=%v; want %+v", loaded, err, a)
	}
	// A later owner forces LoadRequest to read the archived request file.
	next, err := NewAttempt(a.Identity, "next ordinary prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Save(next); err != nil {
		t.Fatal(err)
	}
	reopened = &nativeSession{dir: s.dir, manifest: s.manifest}
	archived, err := reopened.LoadRequest(a.RequestID)
	if err != nil || !reflect.DeepEqual(archived, a) {
		t.Fatalf("archived proof=%+v, err=%v; want %+v", archived, err, a)
	}
}

func TestJournalRejectsAlteredRawProofOnCurrentAndArchivedRequests(t *testing.T) {
	t.Parallel()
	for _, archived := range []bool{false, true} {
		for _, mode := range []string{"changed bytes", "malformed record with matching hash", "legacy object raw", "invalid base64"} {
			t.Run(fmt.Sprintf("archived=%t/%s", archived, mode), func(t *testing.T) {
				s, a := evidenceSession(t)
				a.Phase, a.BytesAttempted = "submitted", len(a.Payload)
				raw := claudeRecord(t, s, a.Payload, nil)
				a.Proof = &Proof{SessionID: s.manifest.SessionID, Raw: raw, RawSHA256: Hash(raw)}
				if err := s.Save(a); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(s.dir, "attempt.json")
				load := s.Load
				if archived {
					next, err := NewAttempt(a.Identity, "next ordinary prompt", nil)
					if err != nil {
						t.Fatal(err)
					}
					if err := s.Save(next); err != nil {
						t.Fatal(err)
					}
					path = filepath.Join(s.dir, "request-"+Hash([]byte(a.RequestID))+".json")
					load = func() (*Attempt, error) { return s.LoadRequest(a.RequestID) }
				}
				data, err := os.ReadFile(path)
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
				if got, err := load(); err == nil || got != nil {
					t.Fatalf("altered proof accepted: %+v, err=%v", got, err)
				}
			})
		}
	}
}
