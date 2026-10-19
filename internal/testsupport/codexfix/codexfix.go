// Package codexfix holds test fixtures for Codex's own files and shapes. It is
// a leaf test-support package: it imports no other test-support package
// (storefix imports it, never the reverse), and nothing in the production code
// graph imports it. Its folders are always under t.TempDir(), never ~/.codex.
package codexfix

import (
	"os"
	"path/filepath"
	"testing"
)

// rolloutStamp is the file stamp (and, as 2026/09/01, the date folder) of every
// rollout RolloutPath builds: a fixed day, so a path is the same on every run.
const rolloutStamp = "2026-09-01T00-00-00"

// RolloutPath returns the rollout (transcript) path Codex gives sessionID under
// the Codex folder codexHome: <codexHome>/sessions/2026/09/01/
// rollout-2026-09-01T00-00-00-<sessionID>.jsonl, a file name that carries the
// session id. When present is true the file is created with one placeholder
// line, or the supplied captured content; otherwise only the path is returned,
// as a transcript not on disk yet.
func RolloutPath(t *testing.T, codexHome, sessionID string, present bool, content ...[]byte) string {
	t.Helper()
	p := filepath.Join(codexHome, "sessions", "2026", "09", "01", "rollout-"+rolloutStamp+"-"+sessionID+".jsonl")
	if !present {
		return p
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("codexfix.RolloutPath: mkdir rollout folder: %v", err)
	}
	data := []byte("{}\n")
	if len(content) > 0 {
		data = content[0]
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("codexfix.RolloutPath: write rollout: %v", err)
	}
	return p
}

// TempRolloutPath is RolloutPath under a fresh temporary Codex folder.
func TempRolloutPath(t *testing.T, sessionID string, present bool, content ...[]byte) string {
	t.Helper()
	return RolloutPath(t, t.TempDir(), sessionID, present, content...)
}
