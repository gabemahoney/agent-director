package cursorfix

import (
	"encoding/json"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GatewayHistory contains synthetic AD transcripts, never native Cursor storage.
type GatewayHistory struct {
	Latest, Older           string
	LatestBytes, OlderBytes []byte
}

func NewGatewayHistory(t testing.TB, instance string, life int64, latest, older string) GatewayHistory {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".config", "agent-director", "cursor-transcripts")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(session string) (string, []byte) {
		raw, err := json.Marshal(map[string]any{"sequence": 1, "kind": "launch", "instance_id": instance, "life_number": life, "connection_id": "prior-synthetic-connection", "launch_token": "prior-synthetic-token", "session_id": session, "data": map[string]any{"agent": "cursor"}})
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, '\n')
		path := filepath.Join(dir, strings.ReplaceAll(uuid.NewString(), "-", "")+".jsonl")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return path, raw
	}
	var h GatewayHistory
	h.Latest, h.LatestBytes = write(latest)
	h.Older, h.OlderBytes = write(older)
	return h
}
