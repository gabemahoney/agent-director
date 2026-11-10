package inputdelivery

import (
	"path/filepath"
	"testing"
)

func TestJournalRejectsCorruptIntent(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"payload changed", "negative bytes", "excess bytes", "unknown phase", "submitted without proof", "proof hash changed", "proof wrong payload"} {
		t.Run(mode, func(t *testing.T) {
			s, a := evidenceSession(t)
			switch mode {
			case "payload changed":
				a.Payload += "x"
			case "negative bytes":
				a.BytesAttempted = -1
			case "excess bytes":
				a.BytesAttempted = len(a.Payload) + 1
			case "unknown phase":
				a.Phase = "ready"
			case "submitted without proof":
				a.Phase = "submitted"
			case "proof hash changed", "proof wrong payload":
				a.Phase = "submitted"
				text := a.Payload
				if mode == "proof wrong payload" {
					text += "x"
				}
				raw := claudeRecord(t, s, text, nil)
				a.Proof = &Proof{SessionID: s.manifest.SessionID, Raw: raw, RawSHA256: Hash(raw)}
				if mode == "proof hash changed" {
					a.Proof.RawSHA256 = "wrong"
				}
			}
			if err := s.validateAttempt(a); err == nil {
				t.Fatalf("corrupt intent accepted: %+v", a)
			}
		})
	}
}

func TestReadyRequiresFreshSupportedLaunch(t *testing.T) {
	t.Parallel()
	for _, agent := range []string{"claude", "codex"} {
		modes := []string{"ready", "missing marker", "missing file", "unsafe launch", "unsupported version", "other token", "other socket", "other cwd", "other instance"}
		if agent == "codex" {
			modes = append(modes, "unpinned keymap")
		}
		for _, mode := range modes {
			t.Run(agent+"/"+mode, func(t *testing.T) {
				s, a := evidenceSession(t)
				id := a.Identity
				id.Agent, id.Token, id.Socket, id.InstanceID = agent, "0123456789abcdef", "/own/socket", "own"
				s.manifest.Agent, s.manifest.Token, s.manifest.Socket, s.manifest.InstanceID, s.manifest.Safe = agent, id.Token, id.Socket, id.InstanceID, true
				s.manifest.KeymapPinned = true
				file, marker := "claude-debug.log", "Terminal capabilities:"
				if agent == "codex" {
					file, marker, s.manifest.Version = "codex-tui.log", "tui startup initial frame scheduled", "0.159.2"
				}
				switch mode {
				case "unpinned keymap":
					s.manifest.KeymapPinned = false
				case "missing marker":
					marker = "SessionStart"
				case "unsafe launch":
					s.manifest.Safe = false
				case "unsupported version":
					s.manifest.Version = ""
				case "other token":
					id.Token = "fedcba9876543210"
				case "other socket":
					id.Socket = "/other"
				case "other cwd":
					id.CWD = "/other"
				case "other instance":
					id.InstanceID = "other"
				}
				if mode != "missing file" {
					writeEvidence(t, filepath.Join(s.dir, file), []byte(marker))
				}
				got, err := s.Ready(id)
				wantErr := mode != "ready" && mode != "missing marker" && mode != "missing file"
				if got != (mode == "ready") || (err != nil) != wantErr {
					t.Fatalf("ready=%v err=%v", got, err)
				}
			})
		}
	}
}

func TestNativeLockRejectsConcurrentWriter(t *testing.T) {
	// Serial: launchDir uses HOME, which this test isolates.
	t.Setenv("HOME", t.TempDir())
	id := Identity{StoreID: "0123456789abcdef", Token: "fedcba9876543210"}
	first, err := (Native{Store: &evidencePersistence{}}).Open(id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Native{Store: &evidencePersistence{}}).Open(id)
	if err == nil {
		second.Close()
		t.Error("concurrent writer acquired the launch lock")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := (Native{Store: &evidencePersistence{}}).Open(id)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	// A lost descriptor makes cleanup fail. Releasing its OS lock cannot
	// make this abandoned session eligible to authorize another write.
	native := third.(*nativeSession)
	if err := native.lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err = third.Close(); err == nil {
		t.Fatal("failed lock cleanup reported success")
	}
	if err = native.Check(); err == nil {
		t.Fatal("failed cleanup regained authority")
	}
}
