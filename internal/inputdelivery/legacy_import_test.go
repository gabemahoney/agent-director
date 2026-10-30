package inputdelivery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// legacyEvidence reuses native records for importer-only JSON fixtures.
func legacyEvidence(t *testing.T) (*nativeSession, *Attempt, *LegacyJournal) {
	t.Helper()
	s, a := evidenceSession(t)
	a.Identity = Identity{StoreID: "0123456789abcdef", Token: "fedcba9876543210", InstanceID: "owner", Socket: "/own/socket", Agent: "claude", CWD: "/work", LifeNumber: 3, PaneID: "%1", PID: 123, Starttime: "456"}
	s.manifest.Token, s.manifest.InstanceID, s.manifest.Socket = a.Identity.Token, a.Identity.InstanceID, a.Identity.Socket
	a.NativeSessionID = s.manifest.SessionID
	a.Watermark = map[string]Watermark{s.manifest.Transcript: {PrefixSHA256: Hash(nil)}}
	dir := filepath.Join(t.TempDir(), a.Identity.Token)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(s.manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeEvidence(t, filepath.Join(dir, "launch.json"), data)
	j, err := OpenLegacyJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	return s, a, j
}

func writeLegacyAttempt(t *testing.T, j *LegacyJournal, a *Attempt, history bool) {
	t.Helper()
	name := "attempt.json"
	if history {
		name = "request-" + Hash([]byte(a.RequestID)) + ".json"
	}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	writeEvidence(t, filepath.Join(j.Dir, name), data)
}

func TestLegacyCurrentPrecedenceAndConservativeBlockers(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"current wins", "history retained", "missing owner", "conflicting owner", "corrupt current", "unsupported capability", "unreadable history"} {
		t.Run(mode, func(t *testing.T) {
			s, a, j := legacyEvidence(t)
			a.Phase, a.BytesAttempted = "enter_attempted", len(a.Payload)
			writeLegacyAttempt(t, j, a, false)
			// An interrupted legacy history write cannot supersede current.
			writeEvidence(t, filepath.Join(j.Dir, "request-"+Hash([]byte(a.RequestID))+".json"), []byte("broken stale copy"))
			owner := a.Identity
			ownerPtr := &owner
			switch mode {
			case "history retained":
				old, err := NewAttempt(a.Identity, "older", nil)
				if err != nil {
					t.Fatal(err)
				}
				old.Phase = "not_started"
				writeLegacyAttempt(t, j, old, true)
			case "missing owner":
				ownerPtr = nil
			case "conflicting owner":
				owner.Socket = "/other"
			case "corrupt current":
				writeEvidence(t, filepath.Join(j.Dir, "attempt.json"), []byte("broken"))
			case "unsupported capability":
				s.manifest.Version = "future"
				data, _ := json.Marshal(s.manifest)
				writeEvidence(t, filepath.Join(j.Dir, "launch.json"), data)
			case "unreadable history":
				if err := os.Symlink(s.manifest.Transcript, filepath.Join(j.Dir, "request-other.json")); err != nil {
					t.Fatal(err)
				}
			}
			r, err := j.Read(context.Background(), a.Identity.StoreID, ownerPtr)
			if mode == "unreadable history" {
				if err == nil {
					t.Fatal("unreadable evidence became absence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "current wins" || mode == "history retained" {
				want := 1
				if mode == "history retained" {
					want++
				}
				if r.Launch.Blocker != "" || r.Launch.CurrentRequestID != a.RequestID || len(r.Attempts) != want {
					t.Fatalf("lost ownership: %+v", r)
				}
			} else if r.Launch.Blocker == "" || len(r.Attempts) != 0 {
				t.Fatalf("unsafe import: %+v", r)
			}
			if r.SourceSHA256 != Hash(r.Evidence) || !r.Launch.Confirmation.At.IsZero() {
				t.Fatal("lost source bytes or invented confirmation")
			}
		})
	}
}
