package inputdelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// evidenceSession is an uncovered (Codex) launch's native session and an
// attempt whose Enter was pressed. codexRollout gives it a rollout.
func evidenceSession(t *testing.T) (*nativeSession, *Attempt) {
	t.Helper()
	dir := t.TempDir()
	s := &nativeSession{dir: dir, ctx: context.Background(), manifest: manifest{Agent: "codex", CWD: "/work", Version: "0.159.2", Created: time.Now(), KeymapPinned: true}}
	a, err := NewAttempt(Identity{Agent: "codex", CWD: "/work"}, "hello\n🌍", nil)
	if err != nil {
		t.Fatal(err)
	}
	a.EnterAttempted = true
	p := &evidencePersistence{attempts: map[string][]byte{}}
	s.store, s.launch = p, &Launch{EvidenceDir: dir, Revision: 1}
	return s, a
}

// evidencePersistence isolates transcript parsing from storage. SQLite ownership,
// transaction and exact-BLOB coverage lives in store/input_delivery_test.go.
// Parser-only fixtures must reuse it; it does not qualify provider behavior.
type evidencePersistence struct {
	Persistence
	launch   *Launch
	attempts map[string][]byte
}

func (p *evidencePersistence) ReadInputPreparation(_ context.Context, id Identity) (Identity, error) {
	return id, nil
}
func (p *evidencePersistence) CreateInputLaunch(_ context.Context, l Launch) error {
	p.launch = &l
	return nil
}
func (p *evidencePersistence) ReadInputLaunch(context.Context, string) (*Launch, error) {
	return p.launch, nil
}
func (p *evidencePersistence) CheckInputGuard(context.Context, *Launch, Guard) error { return nil }
func (p *evidencePersistence) SaveInputAttempt(_ context.Context, l Launch, _ Guard, a *Attempt) (*Launch, *Attempt, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return nil, nil, err
	}
	if p.attempts == nil {
		p.attempts = map[string][]byte{}
	}
	p.attempts[a.RequestID] = data
	l.CurrentRequestID = a.RequestID
	p.launch = &l
	return &l, a, nil
}
func (p *evidencePersistence) ReadInputAttempt(_ context.Context, _ Launch, id string) (*Attempt, error) {
	data := p.attempts[id]
	if data == nil {
		return nil, nil
	}
	var a Attempt
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}
func reopenEvidence(s *nativeSession) *nativeSession {
	return &nativeSession{dir: s.dir, manifest: s.manifest, ctx: s.ctx, store: s.store, launch: s.launch}
}

// codexRollout gives a a fresh Codex native session: a's rollout path in a
// sessions directory, its session metadata line and a's exact user record
// (recordID). Process ownership is the caller's (codexEvidence on Linux).
func codexRollout(t *testing.T, s *nativeSession, a *Attempt, recordID string) (string, []byte, []byte) {
	t.Helper()
	s.manifest.Created = time.Now().Add(-time.Minute)
	a.Identity.CWD = t.TempDir()
	s.manifest.CWD = a.Identity.CWD
	a.NativeSessionID = uuid.NewString()
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "rollout.jsonl"), codexMetadataRecord(t, a, s.manifest.Created.Add(time.Second)), codexRecord(t, recordID, a.Payload)
}

func codexMetadataRecord(t *testing.T, a *Attempt, created time.Time) []byte {
	t.Helper()
	meta, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": a.NativeSessionID, "cwd": a.Identity.CWD, "cli_version": "0.159.2", "timestamp": created.Format(time.RFC3339Nano)}})
	if err != nil {
		t.Fatal(err)
	}
	return append(meta, '\n')
}

func codexRecord(t *testing.T, recordID, text string) []byte {
	t.Helper()
	record, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "id": recordID, "content": []map[string]string{{"type": "input_text", "text": text}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"user.text"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return append(record, '\n')
}

// codexMark is the watermark Watermark validates for rollout data that starts
// with meta: durable ownership that needs no live descriptor.
func codexMark(data, meta []byte, session string) Watermark {
	return Watermark{Size: int64(len(data)), PrefixSHA256: Hash(data), SessionID: session, MetadataSHA256: Hash(bytes.TrimSuffix(meta, []byte("\n")))}
}

func writeEvidence(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmWatermarkBoundaries(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"historic only", "new exact", "unfinished historic", "partial new", "two exact records", "truncated", "prefix rewritten", "wrong bound session", "no Enter"} {
		t.Run(mode, func(t *testing.T) {
			s, a := evidenceSession(t)
			path, meta, record := codexRollout(t, s, a, "old-turn")
			full := append(append([]byte(nil), meta...), record...)
			old := full
			if mode == "unfinished historic" {
				old = full[:len(meta)+len(record)/2]
			}
			a.Watermark = map[string]Watermark{path: codexMark(old, meta, a.NativeSessionID)}
			data := append([]byte(nil), old...)
			next := codexRecord(t, "new-turn", a.Payload)
			switch mode {
			case "new exact", "wrong bound session", "no Enter":
				data = append(data, next...)
			case "unfinished historic":
				// Complete the same record that was already partial at snapshot.
				data = full
			case "partial new":
				data = append(data, next[:len(next)-1]...)
			case "two exact records":
				data = append(append(data, next...), codexRecord(t, "other-turn", a.Payload)...)
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
			writeEvidence(t, path, data)
			proof, err := s.Confirm(a)
			wantErr := mode == "two exact records" || mode == "truncated" || mode == "prefix rewritten"
			if (err != nil) != wantErr || (proof != nil) != (mode == "new exact") {
				t.Fatalf("proof=%+v err=%v; mode=%s", proof, err, mode)
			}
			if proof != nil && (proof.Path != path || proof.Offset != int64(len(old)) || proof.RawSHA256 != Hash(proof.Raw) || proof.RecordID != "new-turn" || proof.SessionID != a.NativeSessionID) {
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
