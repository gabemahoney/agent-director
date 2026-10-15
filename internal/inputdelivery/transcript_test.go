package inputdelivery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func evidenceSession(t *testing.T) (*nativeSession, *Attempt) {
	t.Helper()
	dir := t.TempDir()
	s := &nativeSession{dir: dir, manifest: manifest{Agent: "claude", SessionID: uuid.NewString(), CWD: "/work", Version: "2.1.280", Created: time.Now()}}
	s.manifest.Transcript = filepath.Join(dir, "native.jsonl")
	a, err := NewAttempt(Identity{Agent: "claude", CWD: "/work"}, "hello\n🌍", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.EnterAttempted = true
	return s, a
}

func claudeRecord(t *testing.T, s *nativeSession, text string, change func(map[string]any)) []byte {
	t.Helper()
	r := map[string]any{"type": "user", "uuid": uuid.NewString(), "sessionId": s.manifest.SessionID, "cwd": s.manifest.CWD, "version": s.manifest.Version, "isSidechain": false, "message": map[string]any{"role": "user", "content": text}}
	if change != nil {
		change(r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func writeEvidence(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmRequiresNewExactLaunchRecord(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"other session", func(r map[string]any) { r["sessionId"] = uuid.NewString() }},
		{"other cwd", func(r map[string]any) { r["cwd"] = "/other" }},
		{"other version", func(r map[string]any) { r["version"] = "2.1.281" }},
		{"sidechain", func(r map[string]any) { r["isSidechain"] = true }},
		{"invalid uuid", func(r map[string]any) { r["uuid"] = "invalid" }},
		{"event mirror", func(r map[string]any) { r["type"] = "event_msg" }},
		{"different payload", func(r map[string]any) { r["message"] = map[string]any{"role": "user", "content": "hello"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, a := evidenceSession(t)
			writeEvidence(t, s.manifest.Transcript, claudeRecord(t, s, a.Payload, tc.edit))
			proof, err := s.Confirm(a)
			if err != nil || proof != nil {
				t.Fatalf("unrelated evidence confirmed: %+v, %v", proof, err)
			}
		})
	}
}

func TestConfirmWatermarkBoundaries(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"historic only", "new exact", "unfinished historic", "partial new", "two exact records", "truncated", "prefix rewritten", "wrong bound session", "no Enter"} {
		t.Run(mode, func(t *testing.T) {
			s, a := evidenceSession(t)
			old := claudeRecord(t, s, a.Payload, nil)
			if mode == "unfinished historic" {
				old = old[:len(old)/2]
			}
			writeEvidence(t, s.manifest.Transcript, old)
			var err error
			a.Watermark, err = s.Watermark(a.Identity)
			if err != nil {
				t.Fatal(err)
			}
			data := append([]byte(nil), old...)
			switch mode {
			case "new exact", "wrong bound session", "no Enter":
				data = append(data, claudeRecord(t, s, a.Payload, nil)...)
			case "unfinished historic":
				data = claudeRecord(t, s, a.Payload, nil)
				// Complete the same record that was already partial at snapshot.
				data = append(append([]byte(nil), old...), data[len(old):]...)
			case "partial new":
				newRecord := claudeRecord(t, s, a.Payload, nil)
				data = append(data, newRecord[:len(newRecord)-1]...)
			case "two exact records":
				data = append(data, claudeRecord(t, s, a.Payload, nil)...)
				data = append(data, claudeRecord(t, s, a.Payload, nil)...)
			case "truncated":
				data = data[:len(data)-1]
			case "prefix rewritten":
				data[1] = 'X'
			}
			if mode == "wrong bound session" {
				a.NativeSessionID = uuid.NewString()
			}
			if mode == "no Enter" {
				a.EnterAttempted = false
			}
			writeEvidence(t, s.manifest.Transcript, data)
			proof, err := s.Confirm(a)
			wantErr := mode == "two exact records" || mode == "truncated" || mode == "prefix rewritten"
			if (err != nil) != wantErr || (proof != nil) != (mode == "new exact") {
				t.Fatalf("proof=%+v err=%v; mode=%s", proof, err, mode)
			}
			if proof != nil && (proof.Offset != int64(len(old)) || proof.RawSHA256 != Hash(proof.Raw) || proof.RecordID == "" || proof.SessionID != s.manifest.SessionID) {
				t.Fatalf("proof lost record provenance: %+v", proof)
			}
		})
	}
}

func TestCodexSessionRejectsUnrelatedDescriptor(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"current", "older rollout", "other cwd", "other version", "invalid session", "partial metadata"} {
		t.Run(mode, func(t *testing.T) {
			s, a := evidenceSession(t)
			id := uuid.NewString()
			payload := map[string]any{"id": id, "cwd": a.Identity.CWD, "cli_version": "0.159.2", "timestamp": s.manifest.Created.Add(time.Second).Format(time.RFC3339Nano)}
			switch mode {
			case "older rollout":
				payload["timestamp"] = s.manifest.Created.Add(-time.Second).Format(time.RFC3339Nano)
			case "other cwd":
				payload["cwd"] = "/other"
			case "other version":
				payload["cli_version"] = "0.159.3"
			case "invalid session":
				payload["id"] = "invalid"
			}
			b, err := json.Marshal(map[string]any{"type": "session_meta", "payload": payload})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "partial metadata" {
				b = append(b, '\n')
			}
			got, valid := s.codexSession(b, a.Identity)
			if valid != (mode == "current") || valid && got != id {
				t.Fatalf("session=%q valid=%v", got, valid)
			}
		})
	}
}

func TestCodexRecordRequiresUserTextMetadata(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"exact segments", "event mirror", "assistant", "no user metadata", "image", "extra text", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, a := evidenceSession(t)
			a.Identity.Agent = "codex"
			payload := map[string]any{"type": "message", "role": "user", "id": "turn-1", "content": []map[string]string{{"type": "input_text", "text": "hello\n"}, {"type": "input_text", "text": "🌍"}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"user.text"}}}
			r := map[string]any{"type": "response_item", "payload": payload}
			switch mode {
			case "event mirror":
				r["type"] = "event_msg"
			case "assistant":
				payload["role"] = "assistant"
			case "no user metadata":
				delete(payload, "internal_chat_message_metadata_passthrough")
			case "image":
				payload["content"] = []map[string]string{{"type": "input_image", "text": a.Payload}}
			case "extra text":
				payload["content"] = []map[string]string{{"type": "input_text", "text": a.Payload + "x"}}
			}
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "malformed" {
				b = []byte(strings.TrimSuffix(string(b), "}"))
			}
			id, match := s.matchRecord(b, a, "session")
			if match != (mode == "exact segments") || match && id != "turn-1" {
				t.Fatalf("id=%q match=%v", id, match)
			}
		})
	}
}
