//go:build helper

// Package api — helper-build-only seed functions for the ts-helper wrapper CLI.
//
// This file is compiled ONLY when the `helper` build tag is set (e.g.
// `go build -tags helper`). The production agent-director binary and all
// standard `go test ./...` runs never see this file. The symbols here are
// deliberately NOT on the pkg/api.Client surface; they bypass the Client
// altogether and talk directly to internal/store so the ts-helper CLI can
// seed arbitrary DB state without launching a full Client.
package api

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// HelperSeedSpawn inserts a single spawn row and transitions it to state.
//
//   - dbPath: path to the SQLite store file.
//   - id: claude_instance_id; auto-generated UUID if empty.
//   - state: target state (e.g. "waiting", "working", "ended"). Defaults to
//     "waiting" if empty.
//   - cwd: working directory stored on the row; defaults to "/tmp" if empty.
//   - relayMode: relay_mode value ("on"|"off"|""). Defaults to "off" if empty.
//   - sessionID: if non-empty, calls s.SetSessionID after InsertPending so the
//     row has a claude_session_id (required by the resume verb's pre-flight).
//   - createStore: if true the store is created when missing (OpenOrInit);
//     if false the store must already exist (Open).
//
// Returns the claude_instance_id that was written.
func HelperSeedSpawn(dbPath, id, state, cwd, relayMode, sessionID string, createStore bool) (string, error) {
	var (
		s   *store.Store
		err error
	)
	if createStore {
		s, err = store.OpenOrInit(dbPath)
	} else {
		s, err = store.Open(dbPath)
	}
	if err != nil {
		return "", fmt.Errorf("HelperSeedSpawn: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck

	if id == "" {
		id = uuid.NewString()
	}
	if cwd == "" {
		cwd = "/tmp"
	}
	if state == "" {
		state = store.StateWaiting
	}
	if relayMode == "" {
		relayMode = "off"
	}

	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              cwd,
		TmuxSessionName:  "ts-" + id,
		RelayMode:        relayMode,
	}); err != nil {
		return "", fmt.Errorf("HelperSeedSpawn: InsertPending: %w", err)
	}

	if sessionID != "" {
		if err := s.SetSessionID(id, sessionID); err != nil {
			return "", fmt.Errorf("HelperSeedSpawn: SetSessionID %q: %w", sessionID, err)
		}
	}

	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			return "", fmt.Errorf("HelperSeedSpawn: ApplyHookTransition %q: %w", state, err)
		}
	}

	return id, nil
}

// HelperSeedParentChild sets the parent_id on childID to parentID.
// Both rows must already exist in the store at dbPath.
func HelperSeedParentChild(dbPath, parentID, childID string) error {
	s, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("HelperSeedParentChild: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck

	if err := s.SetParentID(childID, parentID); err != nil {
		return fmt.Errorf("HelperSeedParentChild: SetParentID: %w", err)
	}
	return nil
}

// HelperSeedPermissionRequest inserts an open permission request for spawnID
// using toolName. The spawn row must already exist. Returns the request_id
// auto-assigned by SQLite (AUTOINCREMENT).
func HelperSeedPermissionRequest(dbPath, spawnID, toolName string) (int64, error) {
	s, err := store.Open(dbPath)
	if err != nil {
		return 0, fmt.Errorf("HelperSeedPermissionRequest: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck

	requestToken := uuid.NewString()
	if err := s.UpsertOpenPermissionRequest(spawnID, requestToken, toolName, "{}"); err != nil {
		return 0, fmt.Errorf("HelperSeedPermissionRequest: upsert: %w", err)
	}

	row, err := s.GetPermissionRequest(spawnID, requestToken)
	if err != nil {
		return 0, fmt.Errorf("HelperSeedPermissionRequest: get: %w", err)
	}
	return row.RequestID, nil
}

// HelperSeedTemplate writes body to templatesDir/<name>.toml (creating the
// directory if missing). Returns the absolute path of the written file.
func HelperSeedTemplate(templatesDir, name, body string) (string, error) {
	if err := os.MkdirAll(templatesDir, 0o700); err != nil {
		return "", fmt.Errorf("HelperSeedTemplate: mkdir %q: %w", templatesDir, err)
	}
	outPath := filepath.Join(templatesDir, name+".toml")
	if err := os.WriteFile(outPath, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("HelperSeedTemplate: write %q: %w", outPath, err)
	}
	return outPath, nil
}

// HelperInitStore creates a fresh initialized SQLite store at dbPath
// (creating parent directories as needed), then closes it immediately.
// Returns the dbPath so callers can chain: path, err := HelperInitStore(p).
func HelperInitStore(dbPath string) (string, error) {
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		return "", fmt.Errorf("HelperInitStore: %w", err)
	}
	if err := s.Close(); err != nil {
		return "", fmt.Errorf("HelperInitStore: close: %w", err)
	}
	return dbPath, nil
}
