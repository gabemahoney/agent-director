//go:build linux

package inputdelivery

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/google/uuid"
)

func codexEvidence(t *testing.T, recordID string) (*nativeSession, *Attempt, string, []byte, []byte) {
	t.Helper()
	s, a := evidenceSession(t)
	s.manifest.Created = time.Now().Add(-time.Minute)
	a.Identity.Agent, a.Identity.PID = "codex", os.Getpid()
	start, alive, known := probe.NewProcChecker().StartTime(a.Identity.PID)
	if !alive || !known || start == "" {
		t.Fatal("cannot inspect fixture process identity")
	}
	a.Identity.Starttime = start
	a.Identity.CWD = t.TempDir()
	s.manifest.CWD = a.Identity.CWD
	a.Phase, a.BytesAttempted = "enter_attempted", len(a.Payload)
	a.NativeSessionID = uuid.NewString()
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout.jsonl")
	meta := codexMetadataRecord(t, a, s.manifest.Created.Add(time.Second))
	record, err := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "id": recordID, "content": []map[string]string{{"type": "input_text", "text": a.Payload}}, "internal_chat_message_metadata_passthrough": map[string]any{"content_item_kinds": []string{"user.text"}}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, path, meta, append(record, '\n')
}

func TestCodexDescriptorDiscoveryRejectsWrongProcessIdentity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing starttime", "reused PID", "real owner"} {
		t.Run(mode, func(t *testing.T) {
			s, a, path, meta, _ := codexEvidence(t, "turn")
			writeEvidence(t, path, meta)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if mode == "missing starttime" {
				a.Identity.Starttime = ""
			} else if mode == "reused PID" {
				a.Identity.Starttime += "0"
			}
			marks, err := s.Watermark(a.Identity)
			if mode == "real owner" {
				if err != nil || marks[path].SessionID != a.NativeSessionID {
					t.Fatalf("real owner lost rollout: %+v %v", marks, err)
				}
			} else if err == nil || len(marks) != 0 {
				t.Fatalf("unowned descriptor accepted: %+v %v", marks, err)
			}
		})
	}
}

func TestCodexValidatedRolloutConfirmsAfterProcessExit(t *testing.T) {
	t.Parallel()
	s, a, path, meta, record := codexEvidence(t, "late-turn")
	writeEvidence(t, path, meta)
	child := exec.Command("sh", "-c", "exec 3<\"$1\"; printf ready; read release", "rollout-owner", path)
	in, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); child.Wait() })
	ready := make([]byte, 5)
	if _, err := io.ReadFull(out, ready); err != nil {
		t.Fatal(err)
	}
	a.Identity.PID = child.Process.Pid
	start, alive, known := probe.NewProcChecker().StartTime(a.Identity.PID)
	if !alive || !known {
		t.Fatal("cannot inspect rollout owner")
	}
	a.Identity.Starttime = start
	a.Watermark, err = s.Watermark(a.Identity)
	if err != nil || a.Watermark[path].SessionID != a.NativeSessionID {
		t.Fatalf("rollout not owned: %+v %v", a.Watermark, err)
	}
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Write([]byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	writeEvidence(t, path, append(meta, record...))
	reopened := &nativeSession{dir: s.dir, manifest: s.manifest}
	if proof, err := reopened.Confirm(a); err != nil || proof == nil || proof.RecordID != "late-turn" {
		t.Fatalf("validated late proof after exit=%+v %v", proof, err)
	}
}

func codexMetadataRecord(t *testing.T, a *Attempt, created time.Time) []byte {
	t.Helper()
	meta, err := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": a.NativeSessionID, "cwd": a.Identity.CWD, "cli_version": "0.159.2", "timestamp": created.Format(time.RFC3339Nano)}})
	if err != nil {
		t.Fatal(err)
	}
	return append(meta, '\n')
}

func TestCodexConfirmFirstDescriptorRequiresPostSnapshotSession(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"older session", "equal timestamp", "missing snapshot", "fresh session"} {
		t.Run(mode, func(t *testing.T) {
			s, a, path, meta, record := codexEvidence(t, "first-turn")
			switch mode {
			case "equal timestamp":
				meta = codexMetadataRecord(t, a, a.SnapshotAt)
			case "missing snapshot":
				a.SnapshotAt = time.Time{}
			case "fresh session":
				meta = codexMetadataRecord(t, a, a.SnapshotAt.Add(time.Millisecond))
			}
			a.NativeSessionID = ""
			writeEvidence(t, path, append(meta, record...))
			// The rollout is not in the owned FD set at the request's snapshot.
			var err error
			a.Watermark, err = s.Watermark(a.Identity)
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Watermark) != 0 {
				t.Fatalf("unexpected initial watermark: %+v", a.Watermark)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			for i := 0; i < 3; i++ {
				if err := s.Save(a); err != nil {
					t.Fatal(err)
				}
				a, err = s.Load()
				if err != nil {
					t.Fatal(err)
				}
				proof, err := s.Confirm(a)
				if err != nil || (proof != nil) != (mode == "fresh session") {
					t.Fatalf("confirmation %d: proof=%+v err=%v", i, proof, err)
				}
				if proof != nil && (proof.RecordID != "first-turn" || proof.Path != path || proof.Offset != int64(len(meta)) || proof.RawSHA256 != Hash(proof.Raw)) {
					t.Fatalf("first rollout lost exact provenance: %+v", proof)
				}
			}
		})
	}
}

func TestCodexWatermarkRetainsPriorAttemptAfterWriterRestart(t *testing.T) {
	t.Parallel()
	s, a, path, meta, record := codexEvidence(t, "prior-turn")
	writeEvidence(t, path, meta)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	a.Watermark, err = s.Watermark(a.Identity)
	if err != nil {
		t.Fatal(err)
	}
	writeEvidence(t, path, append(meta, record...))
	proof, err := s.Confirm(a)
	if err != nil || proof == nil {
		t.Fatalf("prior attempt did not confirm: %+v, %v", proof, err)
	}
	a.Phase, a.Proof = "submitted", proof
	if err := s.Save(a); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// A new writer has only the journal and no live descriptor association.
	restarted := &nativeSession{dir: s.dir, manifest: s.manifest}
	marks, err := restarted.Watermark(a.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if mark, ok := marks[path]; !ok || mark.Size != int64(len(meta)+len(record)) || mark.SessionID != a.NativeSessionID {
		t.Fatalf("lost prior attempt's rollout after restart: %+v", marks)
	}
	next, err := NewAttempt(a.Identity, a.Payload, marks)
	if err != nil {
		t.Fatal(err)
	}
	next.Phase, next.BytesAttempted, next.EnterAttempted = "enter_attempted", len(next.Payload), true
	if err := restarted.Save(next); err != nil {
		t.Fatal(err)
	}
	if proof, err := restarted.Confirm(next); err != nil || proof != nil {
		t.Fatalf("prior turn confirmed next request: %+v, %v", proof, err)
	}
	writeEvidence(t, path, append(append(meta, record...), record...))
	proof, err = restarted.Confirm(next)
	if err != nil || proof == nil || proof.Offset != int64(len(meta)+len(record)) || proof.SessionID != a.NativeSessionID {
		t.Fatalf("new turn after restart did not confirm: %+v, %v", proof, err)
	}
}

func TestCodexConfirmRetainsValidatedDescriptor(t *testing.T) {
	t.Parallel()
	s, a, path, meta, record := codexEvidence(t, "new-turn")
	writeEvidence(t, path, meta)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a.Watermark, err = s.Watermark(a.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := s.Confirm(a); err != nil || p != nil {
		t.Fatalf("empty rollout confirmed: %+v, %v", p, err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	writeEvidence(t, path, append(meta, record...))
	proof, err := s.Confirm(a)
	if err != nil || proof == nil || proof.RecordID != "new-turn" || proof.SessionID != a.NativeSessionID {
		t.Fatalf("lost validated descriptor: %+v, %v", proof, err)
	}
}
