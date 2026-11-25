//go:build linux

package inputdelivery

import (
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/probe"
)

func codexEvidence(t *testing.T, recordID string) (*nativeSession, *Attempt, string, []byte, []byte) {
	t.Helper()
	s, a := evidenceSession(t)
	path, meta, record := codexRollout(t, s, a, recordID)
	a.Identity.PID = os.Getpid()
	start, alive, known := probe.NewProcChecker().StartTime(a.Identity.PID)
	if !alive || !known || start == "" {
		t.Fatal("cannot inspect fixture process identity")
	}
	a.Identity.Starttime = start
	a.Phase, a.BytesAttempted = "enter_attempted", len(a.Payload)
	return s, a, path, meta, record
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
	reopened := reopenEvidence(s)
	if proof, err := reopened.Confirm(a); err != nil || proof == nil || proof.RecordID != "late-turn" {
		t.Fatalf("validated late proof after exit=%+v %v", proof, err)
	}
}

func TestCodexConfirmFirstDescriptorRequiresPostSnapshotRollout(t *testing.T) {
	t.Parallel()
	// b.k7w: Codex 0.159.2 starts the session before SnapshotAt, but writes
	// session_meta (outer timestamp and ordinal 0) only after the first prompt.
	for _, mode := range []string{"older rollout", "older rollout with newer payload", "missing outer timestamp", "invalid outer timestamp", "non-string outer timestamp", "equal timestamp", "missing snapshot", "lazy rollout"} {
		t.Run(mode, func(t *testing.T) {
			s, a, path, _, record := codexEvidence(t, "first-turn")
			started := a.SnapshotAt.Add(-time.Second)
			s.manifest.Created = started.Add(-time.Second)
			var written any = a.SnapshotAt.Add(-time.Millisecond).Format(time.RFC3339Nano)
			switch mode {
			case "older rollout with newer payload":
				started = a.SnapshotAt.Add(time.Millisecond)
			case "missing outer timestamp":
				written = ""
			case "invalid outer timestamp":
				written = "not-a-timestamp"
			case "non-string outer timestamp":
				written = 123
			case "equal timestamp":
				written = a.SnapshotAt.Format(time.RFC3339Nano)
			case "missing snapshot":
				written = a.SnapshotAt.Add(76 * time.Millisecond).Format(time.RFC3339Nano)
				a.SnapshotAt = time.Time{}
			case "lazy rollout":
				written = a.SnapshotAt.Add(76 * time.Millisecond).Format(time.RFC3339Nano)
			}
			meta := codexMetadataRecord(t, a, started, written)
			a.NativeSessionID = ""
			// The file does not exist at the request's snapshot. An older rollout
			// discovered later must still refuse its exact matching user record.
			var err error
			a.Watermark, err = s.Watermark(a.Identity)
			if err != nil {
				t.Fatal(err)
			}
			if len(a.Watermark) != 0 {
				t.Fatalf("unexpected initial watermark: %+v", a.Watermark)
			}
			writeEvidence(t, path, append(meta, record...))
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
				if err != nil || (proof != nil) != (mode == "lazy rollout") {
					t.Fatalf("confirmation %d: proof=%+v err=%v; want proof=%t", i, proof, err, mode == "lazy rollout")
				}
				if proof != nil && (proof.RecordID != "first-turn" || proof.Path != path || proof.SessionID != a.NativeSessionID || proof.Offset != int64(len(meta)) || string(proof.Raw)+"\n" != string(record) || proof.RawSHA256 != Hash(record[:len(record)-1])) {
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
	restarted := reopenEvidence(s)
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
	// b.k7w/TR1: a known rollout never interprets its outer timestamp.
	for _, mode := range []string{"valid timestamp", "non-string outer timestamp"} {
		t.Run(mode, func(t *testing.T) {
			s, a, path, meta, record := codexEvidence(t, "new-turn")
			if mode == "non-string outer timestamp" {
				meta = codexMetadataRecord(t, a, s.manifest.Created.Add(time.Second), 123)
			}
			writeEvidence(t, path, meta)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			a.Watermark, err = s.Watermark(a.Identity)
			if err != nil || a.Watermark[path].SessionID != a.NativeSessionID || a.Watermark[path].Size != int64(len(meta)) {
				t.Fatalf("rollout not watermarked: %+v, %v", a.Watermark, err)
			}
			if p, err := s.Confirm(a); err != nil || p != nil {
				t.Fatalf("empty rollout confirmed: %+v, %v", p, err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			writeEvidence(t, path, append(meta, record...))
			proof, err := s.Confirm(a)
			if err != nil || proof == nil || proof.RecordID != "new-turn" || proof.SessionID != a.NativeSessionID || proof.Path != path || proof.Offset != int64(len(meta)) || string(proof.Raw)+"\n" != string(record) || proof.RawSHA256 != Hash(record[:len(record)-1]) {
				t.Fatalf("lost validated descriptor: %+v, %v", proof, err)
			}
		})
	}
}
